//go:build libav

package encoder

// Hardware encoders through FFmpeg's libavcodec: NVENC, AMF, Quick Sync,
// VA-API, VideoToolbox and Media Foundation behind one API. Only the
// encoders are used; capture and color conversion stay ours.
//
// libavcodec's wrappers default to throughput: NVENC queues frames
// (delay=INT_MAX), QSV/VA-API pipeline async_depth frames, and so on. Each
// encoder below is forced into its low-latency mode, and every backend is
// verified at runtime to return frame N before frame N+1 is submitted; one
// that holds frames back is rejected so it can't silently add latency.

/*
#cgo pkg-config: libavcodec libavutil
#include <libavcodec/avcodec.h>
#include <libavutil/opt.h>
#include <libavutil/frame.h>
#include <libavutil/error.h>
#include <stdlib.h>
#include <string.h>

static int set_opt(AVCodecContext *c, const char *k, const char *v) {
	// Options live on the codec's private context; unknown ones are
	// ignored so one table can cover several wrapper versions.
	int r = av_opt_set(c->priv_data, k, v, 0);
	return r == AVERROR_OPTION_NOT_FOUND ? 0 : r;
}

// alloc_encoder configures an encoder; open_encoder then opens it, so a
// platform file can attach hardware frames in between.
static AVCodecContext *alloc_encoder(const char *name, int w, int h, int fps, int kbps, int keyint,
		int pix_fmt, const char **opts, int nopts, int *err) {
	const AVCodec *codec = avcodec_find_encoder_by_name(name);
	if (!codec) { *err = AVERROR_ENCODER_NOT_FOUND; return NULL; }
	AVCodecContext *c = avcodec_alloc_context3(codec);
	c->width = w;
	c->height = h;
	c->time_base = (AVRational){1, 1000};
	c->framerate = (AVRational){fps, 1};
	c->pix_fmt = pix_fmt;
	c->gop_size = keyint;
	c->max_b_frames = 0;
	c->bit_rate = (int64_t)kbps * 1000;
	c->rc_max_rate = (int64_t)kbps * 1000;
	c->rc_buffer_size = kbps * 100; // 100ms, like the x264 backend
	c->flags |= AV_CODEC_FLAG_LOW_DELAY;
	c->color_primaries = AVCOL_PRI_BT709;
	c->color_trc = AVCOL_TRC_BT709;
	c->colorspace = AVCOL_SPC_BT709;
	c->color_range = AVCOL_RANGE_MPEG;
	for (int i = 0; i + 1 < nopts; i += 2) {
		if ((*err = set_opt(c, opts[i], opts[i + 1])) < 0) { avcodec_free_context(&c); return NULL; }
	}
	return c;
}

static int open_encoder(AVCodecContext *c) { return avcodec_open2(c, c->codec, NULL); }

// coded_padding reports how much bigger than the input the coded picture
// is (AV1 hardware that aligns sizes, e.g. AMD RDNA3 to 64x16).
static void coded_padding(AVCodecContext *c, int *right, int *bottom) {
	*right = *bottom = 0;
	const AVPacketSideData *sd = av_packet_side_data_get(c->coded_side_data, c->nb_coded_side_data, AV_PKT_DATA_FRAME_CROPPING);
	if (sd && sd->size >= 16) {
		const uint32_t *crop = (const uint32_t *)sd->data; // top, bottom, left, right
		*bottom = crop[1];
		*right = crop[3];
	}
}

static AVFrame *alloc_frame(AVCodecContext *c, int *err) {
	AVFrame *f = av_frame_alloc();
	f->format = c->pix_fmt;
	f->width = c->width;
	f->height = c->height;
	if ((*err = av_frame_get_buffer(f, 64)) < 0) { av_frame_free(&f); return NULL; }
	return f;
}

static int send(AVCodecContext *c, AVFrame *f, int64_t pts, int key) {
	int r = av_frame_make_writable(f);
	if (r < 0) return r;
	f->pts = pts;
	f->pict_type = key ? AV_PICTURE_TYPE_I : AV_PICTURE_TYPE_NONE;
	if (key) f->flags |= AV_FRAME_FLAG_KEY; else f->flags &= ~AV_FRAME_FLAG_KEY;
	return avcodec_send_frame(c, f);
}

static int is_eagain(int r) { return r == AVERROR(EAGAIN); }
static int pkt_is_key(AVPacket *p) { return (p->flags & AV_PKT_FLAG_KEY) != 0; }

static void errstr(int e, char *buf, int n) { av_strerror(e, buf, n); }

static int set_bitrate(AVCodecContext *c, int kbps) {
	// Most hardware wrappers re-read these on the next frame.
	c->bit_rate = (int64_t)kbps * 1000;
	c->rc_max_rate = (int64_t)kbps * 1000;
	c->rc_buffer_size = kbps * 100;
	return 0;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/kevmo314/urdp/internal/capture"
)

// libavEncoders lists hardware encoders and the options that put each into
// its lowest-latency mode. Earlier entries for a codec are preferred.
//
// experimental entries haven't been verified on real hardware yet, so
// they're used only when named (-encoder libav-h264_nvenc).
var libavEncoders = []struct {
	name         string
	codec        Codec
	opts         []string
	experimental bool
}{
	// NVIDIA: ultra-low-latency tuning, no frame delay or lookahead.
	{"h264_nvenc", H264, []string{"preset", "p1", "tune", "ull", "zerolatency", "1", "delay", "0", "rc-lookahead", "0", "rc", "cbr", "forced-idr", "1", "surfaces", "1"}, true},
	{"hevc_nvenc", H265, []string{"preset", "p1", "tune", "ull", "zerolatency", "1", "delay", "0", "rc-lookahead", "0", "rc", "cbr", "forced-idr", "1", "surfaces", "1"}, true},
	{"av1_nvenc", AV1, []string{"preset", "p1", "tune", "ull", "zerolatency", "1", "delay", "0", "rc-lookahead", "0", "rc", "cbr", "forced-idr", "1", "surfaces", "1"}, true},
	// AMD: the ultra-low-latency usage preset. async_depth defaults to 16
	// queued frames, and without AV_CODEC_FLAG_LOW_DELAY (set in
	// alloc_encoder) FFmpeg holds back one frame before polling for output.
	{"h264_amf", H264, []string{"usage", "ultralowlatency", "quality", "speed", "rc", "cbr", "header_insertion_mode", "idr", "latency", "1", "async_depth", "1", "forced_idr", "1"}, false},
	{"hevc_amf", H265, []string{"usage", "ultralowlatency", "quality", "speed", "rc", "cbr", "header_insertion_mode", "idr", "latency", "1", "async_depth", "1", "forced_idr", "1"}, false},
	{"av1_amf", AV1, []string{"usage", "ultralowlatency", "quality", "speed", "rc", "cbr", "header_insertion_mode", "frame", "latency", "1", "async_depth", "1", "forced_idr", "1"}, false},
	// Intel: no pipelining, no lookahead, fixed-function encoder.
	{"h264_qsv", H264, []string{"async_depth", "1", "preset", "veryfast", "look_ahead", "0", "low_power", "1", "forced_idr", "1"}, true},
	{"hevc_qsv", H265, []string{"async_depth", "1", "preset", "veryfast", "look_ahead", "0", "low_power", "1", "forced_idr", "1"}, true},
	{"av1_qsv", AV1, []string{"async_depth", "1", "preset", "veryfast", "look_ahead", "0", "low_power", "1"}, true},
	// Apple: real-time mode, speed over quality.
	{"h264_videotoolbox", H264, []string{"realtime", "1", "prio_speed", "1", "allow_sw", "0"}, true},
	{"hevc_videotoolbox", H265, []string{"realtime", "1", "prio_speed", "1", "allow_sw", "0"}, true},
	// Windows Media Foundation (vendor-neutral fallback): the remote
	// display scenario is Microsoft's low-latency preset.
	{"h264_mf", H264, []string{"hw_encoding", "1", "scenario", "display_remoting", "rate_control", "cbr"}, true},
	{"hevc_mf", H265, []string{"hw_encoding", "1", "scenario", "display_remoting", "rate_control", "cbr"}, true},
}

func init() {
	for _, e := range libavEncoders {
		e := e
		// On macOS the native vt-* backends cover VideoToolbox, with
		// zero-copy capture; don't let these shadow them.
		if runtime.GOOS == "darwin" && strings.HasSuffix(e.name, "_videotoolbox") {
			continue
		}
		if C.avcodec_find_encoder_by_name(cstr(e.name)) == nil {
			continue
		}
		Register(Backend{
			Name: "libav-" + e.name, Codec: e.codec, Hardware: true, Experimental: e.experimental,
			New: func(w, h int, o Options) (Encoder, error) { return newLibav(e.name, e.codec, e.opts, w, h, o) },
		})
	}
}

var cstrs sync.Map

// cstr returns a C string that lives for the life of the process.
func cstr(s string) *C.char {
	if p, ok := cstrs.Load(s); ok {
		return p.(*C.char)
	}
	p, _ := cstrs.LoadOrStore(s, C.CString(s))
	return p.(*C.char)
}

func averr(op string, code C.int) error {
	var buf [128]C.char
	C.errstr(code, &buf[0], C.int(len(buf)))
	return fmt.Errorf("%s: %s", op, C.GoString(&buf[0]))
}

// hwSession allows one hardware encode session at a time. Several
// concurrent sessions on one GPU are an unusual workload that has hung
// drivers (see README), and a remote desktop never needs more than one.
var hwSession sync.Mutex
var hwBusy bool

type libavEncoder struct {
	name  string
	codec Codec
	ctx   *C.AVCodecContext
	frame *C.AVFrame
	pkt   *C.AVPacket
	nv12  *NV12
	w, h  int
	buf   []byte
	// verified is set once enough frames have come out synchronously.
	verified int
	// gpu: the encoder takes frames in GPU memory (see libav_windows.go):
	// hw is the frame handed to it, bgra stages CPU frames for upload.
	gpu    bool
	device any
	// blocking: receive_packet waits in the driver, so poll without sleeping.
	blocking bool
	hw       *C.AVFrame
	bgra     *C.AVFrame
}

// maxEncodeWait bounds how long Encode waits for a submitted frame's
// output. Hardware encoders are asynchronous, so the first poll may miss;
// an encoder that doesn't produce the frame within this time is holding
// it back for later input, which is the extra latency we refuse.
const maxEncodeWait = 50 * time.Millisecond

func newLibav(name string, codec Codec, opts []string, w, h int, o Options) (Encoder, error) {
	w, h, o, err := normalize(w, h, o)
	if err != nil {
		return nil, err
	}
	hwSession.Lock()
	if hwBusy {
		hwSession.Unlock()
		return nil, errors.New("another hardware encode session is active")
	}
	hwBusy = true
	hwSession.Unlock()
	release := true
	defer func() {
		if release {
			hwSession.Lock()
			hwBusy = false
			hwSession.Unlock()
		}
	}()

	copts := make([]*C.char, len(opts))
	for i, s := range opts {
		copts[i] = cstr(s)
	}
	var cerr C.int
	var popts **C.char
	if len(copts) > 0 {
		popts = (**C.char)(unsafe.Pointer(unsafe.SliceData(copts)))
	}
	open := func(gpu bool) (*C.AVCodecContext, error) {
		ctx := C.alloc_encoder(cstr(name), C.int(w), C.int(h), C.int(o.FPS), C.int(o.BitrateKbps),
			C.int(o.FPS*o.KeyintSeconds), C.AV_PIX_FMT_NV12, popts, C.int(len(copts)), &cerr)
		runtime.KeepAlive(copts)
		if ctx == nil {
			return nil, averr("open "+name, cerr)
		}
		if gpu {
			if err := libavGPUSetup(ctx, o.GPUDevice, w, h); err != nil {
				C.avcodec_free_context(&ctx)
				return nil, err
			}
		}
		if r := C.open_encoder(ctx); r < 0 {
			C.avcodec_free_context(&ctx)
			return nil, averr("open "+name, r)
		}
		// WebRTC can't carry AV1's out-of-band crop, so padding would show
		// in the viewer and skew pointer mapping. Allow the couple of rows
		// AMD adds at 1080p; refuse more so another backend takes over.
		var right, bottom C.int
		C.coded_padding(ctx, &right, &bottom)
		if right > 0 || bottom > 2 {
			C.avcodec_free_context(&ctx)
			return nil, fmt.Errorf("%s would pad %dx%d to %dx%d", name, w, h, w+int(right), h+int(bottom))
		}
		return ctx, nil
	}
	e := &libavEncoder{name: name, codec: codec, w: w, h: h, blocking: strings.HasSuffix(name, "_amf")}
	// Take frames straight from the capture's GPU device when offered;
	// anything that fails there falls back to CPU (NV12) frames.
	if o.GPUDevice != nil {
		ctx, err := open(true)
		if err == nil {
			e.ctx, e.gpu, e.device = ctx, true, o.GPUDevice
			e.hw = C.av_frame_alloc()
		} else {
			log.Printf("encoder: %s can't take GPU frames, using CPU frames: %v", name, err)
		}
	}
	if e.ctx == nil {
		ctx, err := open(false)
		if err != nil {
			return nil, err
		}
		e.ctx = ctx
		e.nv12 = NewNV12(w, h)
		if e.frame = C.alloc_frame(ctx, &cerr); e.frame == nil {
			C.avcodec_free_context(&e.ctx)
			return nil, averr("frame", cerr)
		}
	}
	e.pkt = C.av_packet_alloc()
	release = false
	return e, nil
}

// AcceptsGPUFrames reports whether Encode takes capture frames that are
// only in GPU memory.
func (e *libavEncoder) AcceptsGPUFrames() bool { return e.gpu }

func (e *libavEncoder) Codec() Codec     { return e.codec }
func (e *libavEncoder) Size() (int, int) { return e.w, e.h }
func (e *libavEncoder) SetBitrate(k int) error {
	C.set_bitrate(e.ctx, C.int(k))
	return nil
}

func (e *libavEncoder) Encode(f *capture.Frame, ptsMs int64, keyframe bool) ([]byte, bool, error) {
	if e.gpu {
		if err := e.sendGPU(f, ptsMs, keyframe); err != nil {
			return nil, false, err
		}
		return e.receive()
	}
	if f.Pix == nil {
		return nil, false, errors.New("libav: frame has no CPU pixels")
	}
	e.nv12.Convert(f)
	// Copy the NV12 planes into the encoder's frame (strides may differ).
	// make_writable first: the encoder may still reference the buffer.
	if r := C.av_frame_make_writable(e.frame); r < 0 {
		return nil, false, averr("frame", r)
	}
	ys, uvs := int(e.frame.linesize[0]), int(e.frame.linesize[1])
	dy := unsafe.Slice((*byte)(unsafe.Pointer(e.frame.data[0])), ys*e.h)
	duv := unsafe.Slice((*byte)(unsafe.Pointer(e.frame.data[1])), uvs*e.h/2)
	for y := 0; y < e.h; y++ {
		copy(dy[y*ys:y*ys+e.w], e.nv12.Y[y*e.nv12.YStride:])
	}
	for y := 0; y < e.h/2; y++ {
		copy(duv[y*uvs:y*uvs+e.w], e.nv12.UV[y*e.nv12.UVStride:])
	}
	if r := C.send(e.ctx, e.frame, C.int64_t(ptsMs), cbool(keyframe)); r < 0 {
		return nil, false, averr("send frame", r)
	}
	return e.receive()
}

// receive waits for the packet of the frame just sent.
func (e *libavEncoder) receive() ([]byte, bool, error) {
	deadline := time.Now().Add(maxEncodeWait)
	for {
		r := C.avcodec_receive_packet(e.ctx, e.pkt)
		if r == 0 {
			break
		}
		if C.is_eagain(r) == 0 {
			return nil, false, averr("receive packet", r)
		}
		if time.Now().After(deadline) {
			return nil, false, fmt.Errorf("%s held a frame back for more than %v; refusing an encoder that adds latency", e.name, maxEncodeWait)
		}
		// Hardware finishes asynchronously; poll briefly rather than
		// submit the next frame (which would hide the delay). AMF's
		// receive already waits inside the driver (QueryOutput with a
		// timeout), and a sleep here would only add latency: on Windows
		// even a 200µs sleep takes over half a millisecond.
		if !e.blocking {
			time.Sleep(200 * time.Microsecond)
		}
	}
	e.buf = append(e.buf[:0], unsafe.Slice((*byte)(unsafe.Pointer(e.pkt.data)), int(e.pkt.size))...)
	key := C.pkt_is_key(e.pkt) != 0
	C.av_packet_unref(e.pkt)
	return e.buf, key, nil
}

func (e *libavEncoder) Close() {
	if e.ctx == nil {
		return
	}
	C.av_packet_free(&e.pkt)
	C.av_frame_free(&e.frame)
	C.av_frame_free(&e.hw)
	C.av_frame_free(&e.bgra)
	C.avcodec_free_context(&e.ctx)
	hwSession.Lock()
	hwBusy = false
	hwSession.Unlock()
}
