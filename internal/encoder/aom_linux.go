package encoder

// Software AV1 with libaom in its realtime mode (the one WebRTC uses):
// speed 10, no lag, no frame dropping, CBR with a 100ms buffer, and
// keyframes only when asked for (or every KeyintSeconds). Each Encode
// returns one temporal unit in low-overhead OBU format; libaom puts a
// sequence header OBU in every keyframe.

/*
#cgo pkg-config: aom
#include <stdlib.h>
#include <string.h>
#include <aom/aom_encoder.h>
#include <aom/aomcx.h>

typedef struct {
	aom_codec_ctx_t ctx;
	aom_codec_enc_cfg_t cfg;
	aom_image_t img;
	uint8_t *out;
	size_t out_len, out_cap;
	int key;
} aenc;

static void aenc_rate(aom_codec_enc_cfg_t *c, int kbps) {
	c->rc_target_bitrate = kbps;
	c->rc_buf_sz = 100; // ms
	c->rc_buf_initial_sz = 80;
	c->rc_buf_optimal_sz = 90;
}

static aenc *aenc_open(int w, int h, int fps, int kbps, int threads, int speed,
		uint8_t *y, int ys, uint8_t *u, uint8_t *v, int cs, const char **err) {
	aom_codec_iface_t *iface = aom_codec_av1_cx();
	aenc *e = calloc(1, sizeof *e);
	aom_codec_enc_cfg_t *c = &e->cfg;
	if (aom_codec_enc_config_default(iface, c, AOM_USAGE_REALTIME) != AOM_CODEC_OK) {
		*err = "aom_codec_enc_config_default failed";
		free(e);
		return NULL;
	}
	c->g_usage = AOM_USAGE_REALTIME;
	c->g_w = w;
	c->g_h = h;
	// Constant frame rate time base (pts counts frames); see vpx_linux.go.
	c->g_timebase.num = 1;
	c->g_timebase.den = fps;
	c->g_threads = threads;
	c->g_lag_in_frames = 0;
	c->g_pass = AOM_RC_ONE_PASS;
	c->g_profile = 0;
	c->rc_end_usage = AOM_CBR;
	c->rc_dropframe_thresh = 0; // never drop: one in, one out
	c->rc_resize_mode = 0;
	c->rc_superres_mode = 0;
	c->rc_min_quantizer = 4;
	c->rc_max_quantizer = 56;
	c->rc_undershoot_pct = 50;
	c->rc_overshoot_pct = 50;
	aenc_rate(c, kbps);
	c->kf_mode = AOM_KF_DISABLED;
	if (aom_codec_enc_init(&e->ctx, iface, c, 0) != AOM_CODEC_OK) {
		*err = aom_codec_error_detail(&e->ctx) ? aom_codec_error_detail(&e->ctx) : aom_codec_error(&e->ctx);
		free(e);
		return NULL;
	}
	aom_codec_ctx_t *x = &e->ctx;
	aom_codec_control(x, AOME_SET_CPUUSED, speed);
	aom_codec_control(x, AV1E_SET_ROW_MT, 1);
	aom_codec_control(x, AV1E_SET_TILE_COLUMNS, 2); // log2; clamped to what the width allows
	aom_codec_control(x, AV1E_SET_AQ_MODE, 3);      // cyclic refresh
	aom_codec_control(x, AV1E_SET_TUNE_CONTENT, AOM_CONTENT_SCREEN);
	aom_codec_control(x, AV1E_SET_NOISE_SENSITIVITY, 0);
	aom_codec_control(x, AV1E_SET_ENABLE_ORDER_HINT, 0);
	aom_codec_control(x, AV1E_SET_ENABLE_TPL_MODEL, 0);
	aom_codec_control(x, AV1E_SET_DELTAQ_MODE, 0);
	aom_codec_control(x, AV1E_SET_ENABLE_GLOBAL_MOTION, 0);
	aom_codec_control(x, AV1E_SET_ENABLE_WARPED_MOTION, 0);
	aom_codec_control(x, AV1E_SET_ENABLE_OBMC, 0);
	aom_codec_control(x, AV1E_SET_ENABLE_RESTORATION, 0u);
	aom_codec_control(x, AV1E_SET_COEFF_COST_UPD_FREQ, 3);
	aom_codec_control(x, AV1E_SET_MODE_COST_UPD_FREQ, 3);
	aom_codec_control(x, AV1E_SET_MV_COST_UPD_FREQ, 3);
	aom_codec_control(x, AV1E_SET_DV_COST_UPD_FREQ, 3);
#ifdef AOM_CTRL_AV1E_SET_POSTENCODE_DROP_RTC // libaom 3.9+
	aom_codec_control(x, AV1E_SET_POSTENCODE_DROP_RTC, 0);
#endif
	aom_codec_control(x, AOME_SET_MAX_INTRA_BITRATE_PCT, 300u);
	// BT.709 limited range, matching the I420 conversion.
	aom_codec_control(x, AV1E_SET_COLOR_PRIMARIES, AOM_CICP_CP_BT_709);
	aom_codec_control(x, AV1E_SET_TRANSFER_CHARACTERISTICS, AOM_CICP_TC_BT_709);
	aom_codec_control(x, AV1E_SET_MATRIX_COEFFICIENTS, AOM_CICP_MC_BT_709);
	aom_codec_control(x, AV1E_SET_COLOR_RANGE, AOM_CR_STUDIO_RANGE);
	aom_img_wrap(&e->img, AOM_IMG_FMT_I420, w, h, 1, y);
	e->img.planes[AOM_PLANE_Y] = y;
	e->img.planes[AOM_PLANE_U] = u;
	e->img.planes[AOM_PLANE_V] = v;
	e->img.stride[AOM_PLANE_Y] = ys;
	e->img.stride[AOM_PLANE_U] = cs;
	e->img.stride[AOM_PLANE_V] = cs;
	return e;
}

static void aenc_close(aenc *e) {
	aom_codec_destroy(&e->ctx);
	free(e->out);
	free(e);
}

static int aenc_encode(aenc *e, int64_t pts, int force_kf) {
	aom_enc_frame_flags_t flags = force_kf ? AOM_EFLAG_FORCE_KF : 0;
	if (aom_codec_encode(&e->ctx, &e->img, pts, 1, flags) != AOM_CODEC_OK) return -1;
	e->out_len = 0;
	e->key = 0;
	aom_codec_iter_t it = NULL;
	const aom_codec_cx_pkt_t *pkt;
	while ((pkt = aom_codec_get_cx_data(&e->ctx, &it))) {
		if (pkt->kind != AOM_CODEC_CX_FRAME_PKT) continue;
		size_t n = pkt->data.frame.sz;
		if (e->out_len + n > e->out_cap) {
			e->out_cap = (e->out_len + n) * 2;
			e->out = realloc(e->out, e->out_cap);
		}
		memcpy(e->out + e->out_len, pkt->data.frame.buf, n);
		e->out_len += n;
		if (pkt->data.frame.flags & AOM_FRAME_IS_KEY) e->key = 1;
	}
	return 0;
}

static int aenc_set_bitrate(aenc *e, int kbps) {
	aenc_rate(&e->cfg, kbps);
	return aom_codec_enc_config_set(&e->ctx, &e->cfg) == AOM_CODEC_OK ? 0 : -1;
}

static const char *aenc_error(aenc *e) { return aom_codec_error(&e->ctx); }
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
	Register(Backend{Name: "aom-av1", Codec: AV1, New: func(w, h int, o Options) (Encoder, error) { return newAOM(w, h, o) }})
}

type aomEncoder struct {
	e      *C.aenc
	pic    *I420
	w, h   int
	keyint int64
	frames int64 // frames encoded, which is also the pts
	lastKF int64
	buf    []byte
}

func newAOM(w, h int, o Options) (*aomEncoder, error) {
	w, h, o, err := normalize(w, h, o)
	if err != nil {
		return nil, err
	}
	pic := NewI420(w, h)
	var cerr *C.char
	e := C.aenc_open(C.int(w), C.int(h), C.int(o.FPS), C.int(o.BitrateKbps), C.int(min(runtime.NumCPU(), 8)), 10,
		(*C.uint8_t)(unsafe.Pointer(&pic.Y[0])), C.int(pic.YStride),
		(*C.uint8_t)(unsafe.Pointer(&pic.U[0])), (*C.uint8_t)(unsafe.Pointer(&pic.V[0])), C.int(pic.CStride), &cerr)
	if e == nil {
		pic.Free()
		return nil, fmt.Errorf("encoder: libaom init: %s", C.GoString(cerr))
	}
	return &aomEncoder{e: e, pic: pic, w: w, h: h, keyint: int64(o.FPS * o.KeyintSeconds)}, nil
}

func (e *aomEncoder) Codec() Codec     { return AV1 }
func (e *aomEncoder) Size() (int, int) { return e.w, e.h }

func (e *aomEncoder) SetBitrate(kbps int) error {
	if C.aenc_set_bitrate(e.e, C.int(kbps)) < 0 {
		return fmt.Errorf("encoder: libaom reconfigure: %s", C.GoString(C.aenc_error(e.e)))
	}
	return nil
}

// Encode returns one AV1 temporal unit, valid until the next call.
func (e *aomEncoder) Encode(f *capture.Frame, _ int64, keyframe bool) ([]byte, bool, error) {
	if f.Width < e.w || f.Height < e.h {
		return nil, false, errors.New("encoder: frame smaller than encoder")
	}
	e.pic.Convert(f)
	if e.frames == 0 || e.frames-e.lastKF >= e.keyint {
		keyframe = true
	}
	if C.aenc_encode(e.e, C.int64_t(e.frames), cbool(keyframe)) < 0 {
		return nil, false, fmt.Errorf("encoder: libaom encode: %s", C.GoString(C.aenc_error(e.e)))
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

func (e *aomEncoder) Close() {
	if e.e != nil {
		C.aenc_close(e.e)
		e.e = nil
		e.pic.Free()
	}
}
