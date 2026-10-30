package encoder

/*
#cgo CFLAGS: -O3
#include <stdint.h>
#include <stddef.h>

// BT.709 limited range BGRA -> NV12 (Y plane + interleaved UV), the input
// format most hardware encoders take. Same math as bgra_to_i420.
static void bgra_to_nv12(const uint8_t *restrict src, int stride, int w, int h,
		uint8_t *restrict y, int ys, uint8_t *restrict uv, int uvs) {
	for (int j = 0; j < h; j++) {
		const uint8_t *s = src + (size_t)j * stride;
		uint8_t *d = y + (size_t)j * ys;
		for (int i = 0; i < w; i++)
			d[i] = (uint8_t)(((47 * s[4 * i + 2] + 157 * s[4 * i + 1] + 16 * s[4 * i] + 128) >> 8) + 16);
	}
	for (int j = 0; j < h; j += 2) {
		const uint8_t *s0 = src + (size_t)j * stride, *s1 = s0 + stride;
		uint8_t *d = uv + (size_t)(j / 2) * uvs;
		for (int i = 0; i < w / 2; i++) {
			int b = s0[8 * i] + s0[8 * i + 4] + s1[8 * i] + s1[8 * i + 4];
			int g = s0[8 * i + 1] + s0[8 * i + 5] + s1[8 * i + 1] + s1[8 * i + 5];
			int r = s0[8 * i + 2] + s0[8 * i + 6] + s1[8 * i + 2] + s1[8 * i + 6];
			d[2 * i] = (uint8_t)(((-26 * r - 86 * g + 112 * b + 512) >> 10) + 128);
			d[2 * i + 1] = (uint8_t)(((112 * r - 102 * g - 10 * b + 512) >> 10) + 128);
		}
	}
}

static void nv12_rect(const uint8_t *src, int stride, int x, int y, int w, int h,
		uint8_t *yp, int ys, uint8_t *uvp, int uvs) {
	bgra_to_nv12(src + (size_t)y * stride + (size_t)x * 4, stride, w, h,
		yp + (size_t)y * ys + x, ys, uvp + (size_t)(y / 2) * uvs + x, uvs);
}
*/
import "C"

import (
	"image"
	"runtime"
	"sync"
	"unsafe"

	"github.com/kevmo314/urdp/internal/capture"
)

// NV12 holds a frame converted for hardware encoders: a Y plane followed by
// interleaved UV at half resolution. It persists between frames so only
// dirty regions need converting.
type NV12 struct {
	Width, Height int
	Y, UV         []byte
	YStride       int
	UVStride      int
	primed        bool
}

// NewNV12 allocates an NV12 picture; w and h must be even. Strides are
// padded to 64 bytes, which hardware upload paths tend to like.
func NewNV12(w, h int) *NV12 {
	stride := (w + 63) &^ 63
	buf := make([]byte, stride*h+stride*h/2)
	return &NV12{Width: w, Height: h, Y: buf[:stride*h], UV: buf[stride*h:], YStride: stride, UVStride: stride}
}

// Convert updates the picture from f: just its dirty rectangles when
// possible, otherwise the whole frame split across cores.
func (p *NV12) Convert(f *capture.Frame) {
	bounds := image.Rect(0, 0, p.Width, p.Height)
	src := (*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(f.Pix)))
	yp := (*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(p.Y)))
	uvp := (*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(p.UV)))
	rect := func(r image.Rectangle) {
		C.nv12_rect(src, C.int(f.Stride), C.int(r.Min.X), C.int(r.Min.Y), C.int(r.Dx()), C.int(r.Dy()),
			yp, C.int(p.YStride), uvp, C.int(p.UVStride))
	}
	if p.primed && f.Dirty != nil {
		area := 0
		for _, r := range f.Dirty {
			area += r.Intersect(bounds).Dx() * r.Intersect(bounds).Dy()
		}
		if area <= p.Width*p.Height/2 {
			for _, r := range f.Dirty {
				r = image.Rect(r.Min.X&^1, r.Min.Y&^1, (r.Max.X+1)&^1, (r.Max.Y+1)&^1).Intersect(bounds)
				if !r.Empty() {
					rect(r)
				}
			}
			return
		}
	}
	bands := min(4, runtime.NumCPU())
	step := (p.Height/bands + 1) &^ 1
	var wg sync.WaitGroup
	for y := 0; y < p.Height; y += step {
		wg.Add(1)
		go func(r image.Rectangle) {
			defer wg.Done()
			rect(r)
		}(image.Rect(0, y, p.Width, min(y+step, p.Height)))
	}
	wg.Wait()
	p.primed = true
}
