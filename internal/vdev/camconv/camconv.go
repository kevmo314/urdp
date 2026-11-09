// Package camconv exposes the C pixel conversion used by the Windows urdp
// Camera (convert.h) to Go so it can be tested on any platform. The camera
// itself includes convert.h directly; nothing in urdp imports this package.
package camconv

/*
#include "convert.h"
*/
import "C"

import "unsafe"

// I420ToNV12 converts planar I420 to NV12, padding odd sizes to even ones.
// It returns the NV12 bytes and their even dimensions.
func I420ToNV12(y, u, v []byte, width, height, ys, us, vs int) ([]byte, int, int) {
	w, h := (width+1)&^1, (height+1)&^1
	dst := make([]byte, w*h*3/2)
	C.urdp_i420_to_nv12((*C.uint8_t)(unsafe.Pointer(&dst[0])), (*C.uint8_t)(unsafe.Pointer(&y[0])),
		(*C.uint8_t)(unsafe.Pointer(&u[0])), (*C.uint8_t)(unsafe.Pointer(&v[0])),
		C.int(width), C.int(height), C.int(ys), C.int(us), C.int(vs))
	return dst, w, h
}

// NV12Fit scales an NV12 image into a dw x dh one, letterboxing as needed.
func NV12Fit(src []byte, sw, sh, dw, dh int) []byte {
	dst := make([]byte, dw*dh*3/2)
	C.urdp_nv12_fit((*C.uint8_t)(unsafe.Pointer(&dst[0])), C.uint32_t(dw), C.uint32_t(dh),
		(*C.uint8_t)(unsafe.Pointer(&src[0])), C.uint32_t(sw), C.uint32_t(sh))
	return dst
}

// NV12Black returns a black w x h NV12 image.
func NV12Black(w, h int) []byte {
	dst := make([]byte, w*h*3/2)
	C.urdp_nv12_black((*C.uint8_t)(unsafe.Pointer(&dst[0])), C.uint32_t(w), C.uint32_t(h))
	return dst
}
