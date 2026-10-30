// Package encoder compresses BGRA frames to H.264 with libx264, tuned for
// interactive latency: no B-frames, no lookahead, sliced threads, and
// one-frame-in/one-frame-out operation.
package encoder

/*
#cgo pkg-config: x264
#cgo CFLAGS: -O3
#cgo windows LDFLAGS: -static
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <x264.h>

// BT.709 limited range, 8-bit fixed point. Chroma is the 2x2 average.
// Written as simple per-row loops so the compiler auto-vectorizes them.
static void bgra_to_i420(const uint8_t *restrict src, int stride, int w, int h,
		uint8_t *restrict y, int ys, uint8_t *restrict u, int us, uint8_t *restrict v, int vs) {
	for (int j = 0; j < h; j++) {
		const uint8_t *s = src + (size_t)j * stride;
		uint8_t *d = y + (size_t)j * ys;
		for (int i = 0; i < w; i++)
			d[i] = (uint8_t)(((47 * s[4 * i + 2] + 157 * s[4 * i + 1] + 16 * s[4 * i] + 128) >> 8) + 16);
	}
	for (int j = 0; j < h; j += 2) {
		const uint8_t *s0 = src + (size_t)j * stride, *s1 = s0 + stride;
		uint8_t *du = u + (size_t)(j / 2) * us, *dv = v + (size_t)(j / 2) * vs;
		for (int i = 0; i < w / 2; i++) {
			int b = s0[8 * i] + s0[8 * i + 4] + s1[8 * i] + s1[8 * i + 4];
			int g = s0[8 * i + 1] + s0[8 * i + 5] + s1[8 * i + 1] + s1[8 * i + 5];
			int r = s0[8 * i + 2] + s0[8 * i + 6] + s1[8 * i + 2] + s1[8 * i + 6];
			du[i] = (uint8_t)(((-26 * r - 86 * g + 112 * b + 512) >> 10) + 128);
			dv[i] = (uint8_t)(((112 * r - 102 * g - 10 * b + 512) >> 10) + 128);
		}
	}
}

// A 100ms VBV buffer bounds how big a burst (e.g. a keyframe) can be, and
// so how long it occupies the link, at some cost to keyframe quality.
static int vbv_buffer(int kbps) { return kbps / 10; }

typedef struct {
	x264_t *h;
	x264_picture_t in, out;
	int64_t pts;
} enc;

static enc *enc_open(int w, int h, int fps, int kbps, int keyint, int threads) {
	x264_param_t p;
	if (x264_param_default_preset(&p, "ultrafast", "zerolatency") < 0) return NULL;
	p.i_width = w;
	p.i_height = h;
	p.i_csp = X264_CSP_I420;
	p.i_fps_num = fps;
	p.i_fps_den = 1;
	p.i_timebase_num = 1;
	p.i_timebase_den = 1000;
	// VFR rate control would hold each frame until the next one arrives
	// to learn its duration: a frame of latency, or worse, since frames are
	// only produced when the screen changes. CFR just under-credits VBV
	// refill across idle gaps, which errs on the safe side.
	p.b_vfr_input = 0;
	p.i_threads = threads;
	p.i_keyint_max = keyint;
	p.b_repeat_headers = 1;
	p.b_annexb = 1;
	p.i_log_level = X264_LOG_WARNING;
	// Quality-targeted with a VBV cap: static desktops cost almost
	// nothing, while bursts are bounded so they don't queue on the wire.
	p.rc.i_rc_method = X264_RC_CRF;
	p.rc.f_rf_constant = 23;
	p.rc.i_vbv_max_bitrate = kbps;
	p.rc.i_vbv_buffer_size = vbv_buffer(kbps);
	p.vui.b_fullrange = 0;
	p.vui.i_colorprim = 1;
	p.vui.i_transfer = 1;
	p.vui.i_colmatrix = 1;
	if (x264_param_apply_profile(&p, "baseline") < 0) return NULL;
	enc *e = calloc(1, sizeof(enc));
	e->h = x264_encoder_open(&p);
	if (!e->h) { free(e); return NULL; }
	if (x264_picture_alloc(&e->in, X264_CSP_I420, w, h) < 0) { x264_encoder_close(e->h); free(e); return NULL; }
	return e;
}

static void enc_close(enc *e) {
	x264_picture_clean(&e->in);
	x264_encoder_close(e->h);
	free(e);
}

// Converts rows [j0, j1) of src; j0 and j1 must be even.
static void enc_convert(enc *e, const uint8_t *src, int stride, int w, int j0, int j1) {
	x264_image_t *im = &e->in.img;
	bgra_to_i420(src + (size_t)j0 * stride, stride, w, j1 - j0,
		im->plane[0] + (size_t)j0 * im->i_stride[0], im->i_stride[0],
		im->plane[1] + (size_t)(j0 / 2) * im->i_stride[1], im->i_stride[1],
		im->plane[2] + (size_t)(j0 / 2) * im->i_stride[2], im->i_stride[2]);
}

static uint8_t *enc_plane(enc *e, int i, int *stride) { *stride = e->in.img.i_stride[i]; return e->in.img.plane[i]; }

// Returns encoded byte count (Annex B, all NALs contiguous) or <0 on error.
static int enc_encode(enc *e, int64_t pts_ms, int idr, uint8_t **out) {
	x264_nal_t *nals;
	int n;
	e->in.i_pts = pts_ms;
	e->in.i_type = idr ? X264_TYPE_IDR : X264_TYPE_AUTO;
	int size = x264_encoder_encode(e->h, &nals, &n, &e->in, &e->out);
	if (size > 0) *out = nals[0].p_payload;
	return size;
}

static int enc_set_bitrate(enc *e, int kbps) {
	x264_param_t p;
	x264_encoder_parameters(e->h, &p);
	p.rc.i_vbv_max_bitrate = kbps;
	p.rc.i_vbv_buffer_size = vbv_buffer(kbps);
	return x264_encoder_reconfig(e->h, &p);
}

static int enc_last_idr(enc *e) { return e->out.b_keyframe; }
*/
import "C"

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"

	"github.com/kevmo314/urdp/internal/capture"
)

// Options configures the encoder.
type Options struct {
	FPS         int
	BitrateKbps int // VBV ceiling
	// KeyintSeconds is the periodic keyframe interval; keyframes are also
	// produced on demand when the receiver reports loss (PLI).
	KeyintSeconds int
}

// Encoder is a single-stream H.264 encoder. Not safe for concurrent use.
type Encoder struct {
	e    *C.enc
	w, h int
	opts Options
	buf  []byte
}

// New creates an encoder for w x h frames. Dimensions are rounded down
// to even values as required by 4:2:0 chroma subsampling.
func New(w, h int, o Options) (*Encoder, error) {
	w, h = w&^1, h&^1
	if w < 16 || h < 16 {
		return nil, errors.New("encoder: frame too small")
	}
	if o.FPS <= 0 {
		o.FPS = 60
	}
	if o.BitrateKbps <= 0 {
		o.BitrateKbps = 10000
	}
	if o.KeyintSeconds <= 0 {
		o.KeyintSeconds = 5
	}
	threads := min(runtime.NumCPU(), 8)
	e := C.enc_open(C.int(w), C.int(h), C.int(o.FPS), C.int(o.BitrateKbps), C.int(o.FPS*o.KeyintSeconds), C.int(threads))
	if e == nil {
		return nil, errors.New("encoder: x264_encoder_open failed")
	}
	return &Encoder{e: e, w: w, h: h, opts: o}, nil
}

// SetBitrate changes the VBV ceiling on the fly, e.g. in response to
// congestion control.
func (e *Encoder) SetBitrate(kbps int) error {
	if C.enc_set_bitrate(e.e, C.int(kbps)) < 0 {
		return errors.New("encoder: x264_encoder_reconfig failed")
	}
	return nil
}

// Size is the encoded frame size.
func (e *Encoder) Size() (int, int) { return e.w, e.h }

// Encode converts and compresses f, returning Annex B NAL units that
// remain valid until the next call. keyframe forces an IDR frame. A nil
// result with no error means the encoder produced no output for f.
func (e *Encoder) Encode(f *capture.Frame, ptsMs int64, keyframe bool) ([]byte, bool, error) {
	if f.Width < e.w || f.Height < e.h {
		return nil, false, errors.New("encoder: frame smaller than encoder")
	}
	e.convert(f)
	var out *C.uint8_t
	n := C.enc_encode(e.e, C.int64_t(ptsMs), cbool(keyframe), &out)
	if n < 0 {
		return nil, false, errors.New("encoder: x264_encoder_encode failed")
	}
	if n == 0 {
		return nil, false, nil
	}
	e.buf = append(e.buf[:0], unsafe.Slice((*byte)(unsafe.Pointer(out)), int(n))...)
	return e.buf, C.enc_last_idr(e.e) != 0, nil
}

// convert fills the encoder's input picture from f, splitting the rows
// across goroutines; it's memory bound, so a few bands are enough.
func (e *Encoder) convert(f *capture.Frame) {
	src := (*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(f.Pix)))
	bands := min(4, runtime.NumCPU())
	step := (e.h/bands + 1) &^ 1
	var wg sync.WaitGroup
	for j := 0; j < e.h; j += step {
		wg.Add(1)
		go func(j0, j1 int) {
			defer wg.Done()
			C.enc_convert(e.e, src, C.int(f.Stride), C.int(e.w), C.int(j0), C.int(j1))
		}(j, min(j+step, e.h))
	}
	wg.Wait()
}

// plane exposes input picture plane i (0=Y, 1=U, 2=V) for tests.
func (e *Encoder) plane(i int) ([]byte, int) {
	var stride C.int
	p := C.enc_plane(e.e, C.int(i), &stride)
	rows := e.h
	if i > 0 {
		rows /= 2
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(p)), int(stride)*rows), int(stride)
}

func (e *Encoder) Close() {
	if e.e != nil {
		C.enc_close(e.e)
		e.e = nil
	}
}

func cbool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}
