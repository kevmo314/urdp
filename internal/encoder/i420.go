package encoder

/*
#cgo CFLAGS: -O3
#include <stdint.h>
#include <stddef.h>
#include <stdlib.h>

// BT.709 limited range BGRA -> I420; the same math as bgra_to_nv12.
static void i420_rect(const uint8_t *restrict src, int stride, int x, int y, int w, int h,
		uint8_t *restrict yp, int ys, uint8_t *restrict up, uint8_t *restrict vp, int cs) {
	src += (size_t)y * stride + (size_t)x * 4;
	yp += (size_t)y * ys + x;
	up += (size_t)(y / 2) * cs + x / 2;
	vp += (size_t)(y / 2) * cs + x / 2;
	for (int j = 0; j < h; j++) {
		const uint8_t *s = src + (size_t)j * stride;
		uint8_t *d = yp + (size_t)j * ys;
		for (int i = 0; i < w; i++)
			d[i] = (uint8_t)(((47 * s[4 * i + 2] + 157 * s[4 * i + 1] + 16 * s[4 * i] + 128) >> 8) + 16);
	}
	for (int j = 0; j < h; j += 2) {
		const uint8_t *s0 = src + (size_t)j * stride, *s1 = s0 + stride;
		uint8_t *du = up + (size_t)(j / 2) * cs, *dv = vp + (size_t)(j / 2) * cs;
		for (int i = 0; i < w / 2; i++) {
			int b = s0[8 * i] + s0[8 * i + 4] + s1[8 * i] + s1[8 * i + 4];
			int g = s0[8 * i + 1] + s0[8 * i + 5] + s1[8 * i + 1] + s1[8 * i + 5];
			int r = s0[8 * i + 2] + s0[8 * i + 6] + s1[8 * i + 2] + s1[8 * i + 6];
			du[i] = (uint8_t)(((-26 * r - 86 * g + 112 * b + 512) >> 10) + 128);
			dv[i] = (uint8_t)(((112 * r - 102 * g - 10 * b + 512) >> 10) + 128);
		}
	}
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

// I420 is a planar 4:2:0 picture for the software encoders that take it
// (libvpx, libaom). It lives in C memory so encoders can reference it
// directly, and persists between frames so only dirty regions need
// converting. Call Free when done.
type I420 struct {
	Width, Height int
	Y, U, V       []byte
	YStride       int
	CStride       int
	primed        bool
	mem           unsafe.Pointer
}

// NewI420 allocates a picture; w and h must be even. Strides are padded
// to 64 bytes for aligned SIMD loads in the encoders.
func NewI420(w, h int) *I420 {
	ys := (w + 63) &^ 63
	cs := (w/2 + 63) &^ 63
	ySize, cSize := ys*h, cs*h/2
	mem := C.calloc(C.size_t(ySize+2*cSize), 1)
	buf := unsafe.Slice((*byte)(mem), ySize+2*cSize)
	return &I420{
		Width: w, Height: h, YStride: ys, CStride: cs, mem: mem,
		Y: buf[:ySize], U: buf[ySize : ySize+cSize], V: buf[ySize+cSize:],
	}
}

// Free releases the picture's memory.
func (p *I420) Free() {
	if p.mem != nil {
		C.free(p.mem)
		p.mem, p.Y, p.U, p.V = nil, nil, nil, nil
	}
}

// Convert updates the picture from f: just its dirty rectangles when
// possible, otherwise the whole frame split across cores.
func (p *I420) Convert(f *capture.Frame) {
	bounds := image.Rect(0, 0, p.Width, p.Height)
	src := (*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(f.Pix)))
	rect := func(r image.Rectangle) {
		C.i420_rect(src, C.int(f.Stride), C.int(r.Min.X), C.int(r.Min.Y), C.int(r.Dx()), C.int(r.Dy()),
			(*C.uint8_t)(unsafe.Pointer(&p.Y[0])), C.int(p.YStride),
			(*C.uint8_t)(unsafe.Pointer(&p.U[0])), (*C.uint8_t)(unsafe.Pointer(&p.V[0])), C.int(p.CStride))
	}
	if p.primed && f.Dirty != nil {
		area := 0
		for _, r := range f.Dirty {
			area += r.Intersect(bounds).Dx() * r.Intersect(bounds).Dy()
		}
		if area <= p.Width*p.Height/2 {
			for _, r := range f.Dirty {
				// 4:2:0 chroma covers 2x2 blocks, so widen to even coordinates.
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
