package encoder

// Software VP8 and VP9 with libvpx in realtime mode: no lag, no frame
// dropping, error resilient, CBR with a 100ms buffer, and keyframes only
// when asked for (or every KeyintSeconds), so each Encode call returns
// exactly the frame it was given.

/*
#cgo pkg-config: vpx
#include <stdlib.h>
#include <string.h>
#include <vpx/vpx_encoder.h>
#include <vpx/vp8cx.h>

typedef struct {
	vpx_codec_ctx_t ctx;
	vpx_codec_enc_cfg_t cfg;
	vpx_image_t img;
	int vp9;
	uint8_t *out;
	size_t out_len, out_cap;
	int key;
} venc;

static int venc_ctl(venc *e, int id, int v) { return vpx_codec_control_(&e->ctx, id, v); }

static void venc_rate(vpx_codec_enc_cfg_t *c, int kbps) {
	c->rc_target_bitrate = kbps;
	// The buffer model is in ms: about 100ms of data, which bounds how
	// long a burst (e.g. a keyframe) occupies the link.
	c->rc_buf_sz = 100;
	c->rc_buf_initial_sz = 80;
	c->rc_buf_optimal_sz = 90;
}

// Returns NULL and sets *err on failure.
static venc *venc_open(int vp9, int w, int h, int fps, int kbps, int threads,
		uint8_t *y, int ys, uint8_t *u, uint8_t *v, int cs, const char **err) {
	vpx_codec_iface_t *iface = vp9 ? vpx_codec_vp9_cx() : vpx_codec_vp8_cx();
	venc *e = calloc(1, sizeof *e);
	e->vp9 = vp9;
	vpx_codec_enc_cfg_t *c = &e->cfg;
	if (vpx_codec_enc_config_default(iface, c, 0) != VPX_CODEC_OK) {
		*err = "vpx_codec_enc_config_default failed";
		free(e);
		return NULL;
	}
	c->g_w = w;
	c->g_h = h;
	// Constant frame rate time base: pts counts frames, so idle gaps don't
	// make the rate control think the frame rate dropped and hand the next
	// frame a huge budget (the same choice as x264's b_vfr_input = 0).
	c->g_timebase.num = 1;
	c->g_timebase.den = fps;
	c->g_threads = threads;
	c->g_lag_in_frames = 0;
	c->g_pass = VPX_RC_ONE_PASS;
	c->g_error_resilient = VPX_ERROR_RESILIENT_DEFAULT;
	c->rc_end_usage = VPX_CBR;
	c->rc_dropframe_thresh = 0; // never drop: one in, one out
	c->rc_resize_allowed = 0;
	c->rc_min_quantizer = 2;
	c->rc_max_quantizer = 56;
	c->rc_undershoot_pct = 50;
	c->rc_overshoot_pct = 50;
	venc_rate(c, kbps);
	c->kf_mode = VPX_KF_DISABLED;
	if (vpx_codec_enc_init(&e->ctx, iface, c, 0) != VPX_CODEC_OK) {
		*err = vpx_codec_error_detail(&e->ctx) ? vpx_codec_error_detail(&e->ctx) : vpx_codec_error(&e->ctx);
		free(e);
		return NULL;
	}
	if (vp9) {
		venc_ctl(e, VP8E_SET_CPUUSED, 9);
		venc_ctl(e, VP9E_SET_ROW_MT, 1);
		// log2 of the tile column count; libvpx clamps it to what the
		// width allows (4 columns at 1080p).
		venc_ctl(e, VP9E_SET_TILE_COLUMNS, 2);
		venc_ctl(e, VP9E_SET_FRAME_PARALLEL_DECODING, 1);
		venc_ctl(e, VP9E_SET_AQ_MODE, 3); // cyclic refresh, as WebRTC uses
		venc_ctl(e, VP9E_SET_TUNE_CONTENT, VP9E_CONTENT_SCREEN);
		venc_ctl(e, VP9E_SET_NOISE_SENSITIVITY, 0);
	} else {
		venc_ctl(e, VP8E_SET_CPUUSED, 16);
		venc_ctl(e, VP8E_SET_NOISE_SENSITIVITY, 0);
		venc_ctl(e, VP8E_SET_TOKEN_PARTITIONS, VP8_FOUR_TOKENPARTITION);
		venc_ctl(e, VP8E_SET_STATIC_THRESHOLD, 1);
		// Screen content mode keeps CBR from spending the whole budget
		// refining a picture that barely changed.
		venc_ctl(e, VP8E_SET_SCREEN_CONTENT_MODE, 1);
	}
	// Cap keyframes at a few frames' worth of the target rate.
	venc_ctl(e, VP8E_SET_MAX_INTRA_BITRATE_PCT, 300);
	vpx_img_wrap(&e->img, VPX_IMG_FMT_I420, w, h, 1, y);
	e->img.planes[VPX_PLANE_Y] = y;
	e->img.planes[VPX_PLANE_U] = u;
	e->img.planes[VPX_PLANE_V] = v;
	e->img.stride[VPX_PLANE_Y] = ys;
	e->img.stride[VPX_PLANE_U] = cs;
	e->img.stride[VPX_PLANE_V] = cs;
	return e;
}

static void venc_close(venc *e) {
	vpx_codec_destroy(&e->ctx);
	free(e->out);
	free(e);
}

// Encodes the wrapped picture; the output is in e->out / e->out_len.
static int venc_encode(venc *e, int64_t pts, int force_kf) {
	vpx_enc_frame_flags_t flags = force_kf ? VPX_EFLAG_FORCE_KF : 0;
	if (vpx_codec_encode(&e->ctx, &e->img, pts, 1, flags, VPX_DL_REALTIME) != VPX_CODEC_OK) return -1;
	e->out_len = 0;
	e->key = 0;
	vpx_codec_iter_t it = NULL;
	const vpx_codec_cx_pkt_t *pkt;
	while ((pkt = vpx_codec_get_cx_data(&e->ctx, &it))) {
		if (pkt->kind != VPX_CODEC_CX_FRAME_PKT) continue;
		size_t n = pkt->data.frame.sz;
		if (e->out_len + n > e->out_cap) {
			e->out_cap = (e->out_len + n) * 2;
			e->out = realloc(e->out, e->out_cap);
		}
		memcpy(e->out + e->out_len, pkt->data.frame.buf, n);
		e->out_len += n;
		if (pkt->data.frame.flags & VPX_FRAME_IS_KEY) e->key = 1;
	}
	return 0;
}

static int venc_set_bitrate(venc *e, int kbps) {
	venc_rate(&e->cfg, kbps);
	return vpx_codec_enc_config_set(&e->ctx, &e->cfg) == VPX_CODEC_OK ? 0 : -1;
}

static const char *venc_error(venc *e) { return vpx_codec_error(&e->ctx); }
*/
import "C"

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"github.com/kevmo314/urdp/internal/capture"
)

func init() {
	Register(Backend{Name: "vpx-vp9", Codec: VP9, New: func(w, h int, o Options) (Encoder, error) { return newVPX(VP9, w, h, o) }})
	Register(Backend{Name: "vpx-vp8", Codec: VP8, New: func(w, h int, o Options) (Encoder, error) { return newVPX(VP8, w, h, o) }})
}

type vpxEncoder struct {
	e      *C.venc
	codec  Codec
	pic    *I420
	w, h   int
	keyint int64
	frames int64 // frames encoded, which is also the pts
	lastKF int64
	buf    []byte
}

func newVPX(c Codec, w, h int, o Options) (*vpxEncoder, error) {
	w, h, o, err := normalize(w, h, o)
	if err != nil {
		return nil, err
	}
	pic := NewI420(w, h)
	threads := min(runtime.NumCPU(), 8)
	vp9 := C.int(0)
	if c == VP9 {
		vp9 = 1
	}
	var cerr *C.char
	e := C.venc_open(vp9, C.int(w), C.int(h), C.int(o.FPS), C.int(o.BitrateKbps), C.int(threads),
		(*C.uint8_t)(unsafe.Pointer(&pic.Y[0])), C.int(pic.YStride),
		(*C.uint8_t)(unsafe.Pointer(&pic.U[0])), (*C.uint8_t)(unsafe.Pointer(&pic.V[0])), C.int(pic.CStride), &cerr)
	if e == nil {
		pic.Free()
		return nil, fmt.Errorf("encoder: libvpx %s init: %s", c, C.GoString(cerr))
	}
	return &vpxEncoder{e: e, codec: c, pic: pic, w: w, h: h, keyint: int64(o.FPS * o.KeyintSeconds)}, nil
}

func (e *vpxEncoder) Codec() Codec     { return e.codec }
func (e *vpxEncoder) Size() (int, int) { return e.w, e.h }

func (e *vpxEncoder) SetBitrate(kbps int) error {
	if C.venc_set_bitrate(e.e, C.int(kbps)) < 0 {
		return fmt.Errorf("encoder: libvpx reconfigure: %s", C.GoString(C.venc_error(e.e)))
	}
	return nil
}

// Encode returns one VP8/VP9 frame, valid until the next call.
func (e *vpxEncoder) Encode(f *capture.Frame, _ int64, keyframe bool) ([]byte, bool, error) {
	if f.Width < e.w || f.Height < e.h {
		return nil, false, errors.New("encoder: frame smaller than encoder")
	}
	e.pic.Convert(f)
	// The encoder never places keyframes itself; this is the periodic one.
	if e.frames == 0 || e.frames-e.lastKF >= e.keyint {
		keyframe = true
	}
	if C.venc_encode(e.e, C.int64_t(e.frames), cbool(keyframe)) < 0 {
		return nil, false, fmt.Errorf("encoder: libvpx encode: %s", C.GoString(C.venc_error(e.e)))
	}
	e.frames++
	if e.e.out_len == 0 {
		return nil, false, nil
	}
	isKey := e.e.key != 0
	if isKey {
		e.lastKF = e.frames - 1
	}
	e.buf = append(e.buf[:0], unsafe.Slice((*byte)(unsafe.Pointer(e.e.out)), int(e.e.out_len))...)
	return e.buf, isKey, nil
}

func (e *vpxEncoder) Close() {
	if e.e != nil {
		C.venc_close(e.e)
		e.e = nil
		e.pic.Free()
	}
}
