package vpx

/*
#cgo pkg-config: vpx
#include <vpx/vpx_decoder.h>
#include <vpx/vp8dx.h>
#include <stdlib.h>

static vpx_codec_ctx_t *vp8_open(int *err) {
	vpx_codec_ctx_t *c = calloc(1, sizeof *c);
	if (!c) { *err = VPX_CODEC_MEM_ERROR; return NULL; }
	vpx_codec_dec_cfg_t cfg = { .threads = 1 };
	*err = vpx_codec_dec_init(c, vpx_codec_vp8_dx(), &cfg, 0);
	if (*err != VPX_CODEC_OK) { free(c); return NULL; }
	return c;
}

static vpx_image_t *vp8_decode(vpx_codec_ctx_t *c, const uint8_t *data, unsigned len, int *err) {
	*err = vpx_codec_decode(c, data, len, NULL, 0);
	if (*err != VPX_CODEC_OK) return NULL;
	vpx_codec_iter_t it = NULL;
	vpx_image_t *img, *last = NULL;
	while ((img = vpx_codec_get_frame(c, &it))) last = img;
	return last;
}

static void vp8_close(vpx_codec_ctx_t *c) {
	vpx_codec_destroy(c);
	free(c);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/kevmo314/urdp/internal/vdev"
)

type decoder struct {
	c   *C.vpx_codec_ctx_t
	img vdev.Image
}

func newDecoder() (Decoder, error) {
	var cerr C.int
	c := C.vp8_open(&cerr)
	if c == nil {
		return nil, fmt.Errorf("vpx: init: %s", C.GoString(C.vpx_codec_err_to_string(C.vpx_codec_err_t(cerr))))
	}
	return &decoder{c: c}, nil
}

func (d *decoder) Decode(frame []byte) (*vdev.Image, error) {
	if d.c == nil {
		return nil, errors.New("vpx: decoder closed")
	}
	if len(frame) == 0 {
		return nil, nil
	}
	var cerr C.int
	// libvpx doesn't retain the input, so Go memory is fine here.
	img := C.vp8_decode(d.c, (*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(frame))), C.uint(len(frame)), &cerr)
	if cerr != C.VPX_CODEC_OK {
		msg := C.GoString(C.vpx_codec_error(d.c))
		if detail := C.vpx_codec_error_detail(d.c); detail != nil {
			msg += ": " + C.GoString(detail)
		}
		return nil, fmt.Errorf("vpx: decode: %s", msg)
	}
	if img == nil {
		return nil, nil
	}
	if img.fmt != C.VPX_IMG_FMT_I420 {
		return nil, fmt.Errorf("vpx: unexpected image format %d", img.fmt)
	}
	w, h := int(img.d_w), int(img.d_h)
	ch := (h + 1) / 2
	plane := func(i, rows int) ([]byte, int) {
		stride := int(img.stride[i])
		return unsafe.Slice((*byte)(unsafe.Pointer(img.planes[i])), stride*rows), stride
	}
	d.img.Width, d.img.Height = w, h
	d.img.Y, d.img.YStride = plane(0, h)
	d.img.U, d.img.UStride = plane(1, ch)
	d.img.V, d.img.VStride = plane(2, ch)
	return &d.img, nil
}

func (d *decoder) Close() {
	if d.c != nil {
		C.vp8_close(d.c)
		d.c = nil
	}
}
