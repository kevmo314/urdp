package vpx

/*
#cgo pkg-config: vpx
#include <stdlib.h>
#include <vpx/vpx_decoder.h>
#include <vpx/vp8dx.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"github.com/kevmo314/urdp/internal/vdev"
)

type windowsDecoder struct {
	mu  sync.Mutex
	ctx *C.vpx_codec_ctx_t
}

func newDecoder() (Decoder, error) {
	ctx := (*C.vpx_codec_ctx_t)(C.calloc(1, C.size_t(C.sizeof_vpx_codec_ctx_t)))
	if ctx == nil {
		return nil, errors.New("vpx: allocation failed")
	}
	// No postprocessing, one decoding thread, and no decode deadline.
	cfg := C.vpx_codec_dec_cfg_t{threads: 1}
	if rc := C.vpx_codec_dec_init_ver(ctx, C.vpx_codec_vp8_dx(), &cfg, 0, C.VPX_DECODER_ABI_VERSION); rc != C.VPX_CODEC_OK {
		C.free(unsafe.Pointer(ctx))
		return nil, fmt.Errorf("vpx: initialize: %s", C.GoString(C.vpx_codec_err_to_string(rc)))
	}
	return &windowsDecoder{ctx: ctx}, nil
}

func (d *windowsDecoder) Decode(frame []byte) (*vdev.Image, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx == nil {
		return nil, errors.New("vpx: decoder closed")
	}
	if len(frame) == 0 {
		return nil, errors.New("vpx: empty frame")
	}
	if uint64(len(frame)) > uint64(^uint32(0)) {
		return nil, errors.New("vpx: frame too large")
	}
	if rc := C.vpx_codec_decode(d.ctx, (*C.uint8_t)(unsafe.Pointer(&frame[0])), C.uint(len(frame)), nil, 0); rc != C.VPX_CODEC_OK {
		return nil, fmt.Errorf("vpx: decode: %s", C.GoString(C.vpx_codec_error(d.ctx)))
	}
	var iter C.vpx_codec_iter_t
	img := C.vpx_codec_get_frame(d.ctx, &iter)
	if img == nil {
		return nil, nil
	}
	if img.fmt != C.VPX_IMG_FMT_I420 {
		return nil, errors.New("vpx: unexpected image format")
	}
	h := int(img.d_h)
	plane := func(n, width, height int) []byte {
		return unsafe.Slice((*byte)(unsafe.Pointer(img.planes[n])), int(img.stride[n])*(height-1)+width)
	}
	// Expose native storage only; it remains valid until the next Decode or Close.
	return &vdev.Image{Width: int(img.d_w), Height: h,
		Y: plane(0, int(img.d_w), h), U: plane(1, (int(img.d_w)+1)/2, (h+1)/2), V: plane(2, (int(img.d_w)+1)/2, (h+1)/2),
		YStride: int(img.stride[0]), UStride: int(img.stride[1]), VStride: int(img.stride[2])}, nil
}

func (d *windowsDecoder) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx != nil {
		C.vpx_codec_destroy(d.ctx)
		C.free(unsafe.Pointer(d.ctx))
		d.ctx = nil
	}
}
