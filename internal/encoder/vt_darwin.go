package encoder

// Apple VideoToolbox hardware H.264/HEVC. Frames from ScreenCaptureKit
// arrive as IOSurface-backed NV12 CVPixelBuffers (capture.DarwinPixelBuffer)
// and go to the encoder without touching the CPU; BGRA frames (stitched
// desktops, synthetic sources) are converted to NV12 and uploaded.

/*
#cgo CFLAGS: -mmacosx-version-min=13.0
#cgo darwin CFLAGS: -x objective-c -fobjc-arc
#cgo darwin LDFLAGS: -mmacosx-version-min=13.0 -framework Foundation -framework VideoToolbox -framework CoreMedia -framework CoreVideo
#include <stdlib.h>
#include "vt_darwin.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/kevmo314/urdp/internal/capture"
)

func init() {
	for _, c := range []Codec{H264, H265} {
		hevc := c == H265
		if C.ur_vt_supported(cbool(hevc)) == 0 {
			continue
		}
		name := "vt-h264"
		if hevc {
			name = "vt-hevc"
		}
		Register(Backend{Name: name, Codec: c, Hardware: true, New: func(w, h int, o Options) (Encoder, error) { return newVT(c, w, h, o) }})
	}
}

type vtEncoder struct {
	native *C.ur_vt
	pixel  unsafe.Pointer // NV12 upload buffer for CPU frames
	w, h   int
	codec  Codec
	cpu    *NV12
	buf    []byte
	// pendingKey carries a keyframe request over a frame VT dropped.
	pendingKey bool
}

func newVT(codec Codec, w, h int, o Options) (*vtEncoder, error) {
	w, h, o, err := normalize(w, h, o)
	if err != nil {
		return nil, err
	}
	var status C.int
	p := C.ur_vt_open(C.int(w), C.int(h), C.int(o.FPS), C.int(o.BitrateKbps), C.int(o.FPS*o.KeyintSeconds), C.int(o.KeyintSeconds), cbool(codec == H265), &status)
	if p == nil {
		return nil, fmt.Errorf("VideoToolbox open: OSStatus %d", status)
	}
	return &vtEncoder{native: p, w: w, h: h, codec: codec}, nil
}

// AcceptsGPUFrames tells capture it may hand over CVPixelBuffers in place
// of CPU pixels. BGRA CPU frames still work.
func (e *vtEncoder) AcceptsGPUFrames() bool { return true }
func (e *vtEncoder) Size() (int, int)       { return e.w, e.h }
func (e *vtEncoder) Codec() Codec           { return e.codec }

func (e *vtEncoder) SetBitrate(kbps int) error {
	if e.native == nil {
		return errors.New("VideoToolbox closed")
	}
	if kbps <= 0 {
		return errors.New("VideoToolbox bitrate must be positive")
	}
	if status := C.ur_vt_bitrate(e.native, C.int(kbps)); status != 0 {
		return fmt.Errorf("VideoToolbox bitrate: OSStatus %d", status)
	}
	return nil
}

// property reads back a session property (see ur_vt_property), for tests.
func (e *vtEncoder) property(name string) (int64, error) {
	cs := C.CString(name)
	defer C.free(unsafe.Pointer(cs))
	var v C.int64_t
	if status := C.ur_vt_property(e.native, cs, &v); status != 0 {
		return 0, fmt.Errorf("%s: OSStatus %d", name, status)
	}
	return int64(v), nil
}

func (e *vtEncoder) Encode(f *capture.Frame, ptsMs int64, keyframe bool) ([]byte, bool, error) {
	if e.native == nil {
		return nil, false, errors.New("VideoToolbox closed")
	}
	var pixel unsafe.Pointer
	if gpu, ok := f.GPU.(capture.DarwinPixelBuffer); ok && gpu.Pointer != nil {
		if f.Width != e.w || f.Height != e.h {
			return nil, false, fmt.Errorf("VideoToolbox: %dx%d pixel buffer for a %dx%d encoder", f.Width, f.Height, e.w, e.h)
		}
		pixel = gpu.Pointer
	} else {
		if f.Width < e.w || f.Height < e.h || f.Stride < e.w*4 || len(f.Pix) < f.Stride*(e.h-1)+e.w*4 {
			return nil, false, errors.New("VideoToolbox: invalid BGRA frame")
		}
		if e.pixel == nil {
			if e.pixel = newPixelBuffer(e.w, e.h); e.pixel == nil {
				return nil, false, errors.New("VideoToolbox: pixel buffer allocation failed")
			}
			e.cpu = NewNV12(e.w, e.h)
		}
		e.cpu.Convert(f)
		if err := uploadNV12(e.pixel, e.cpu); err != nil {
			return nil, false, err
		}
		pixel = e.pixel
	}
	keyframe = keyframe || e.pendingKey
	var data unsafe.Pointer
	var size C.size_t
	var key C.int
	if status := C.ur_vt_encode(e.native, pixel, C.int64_t(ptsMs), cbool(keyframe), &data, &size, &key); status != 0 {
		e.pendingKey = keyframe
		return nil, false, fmt.Errorf("VideoToolbox encode: OSStatus %d", status)
	}
	if size == 0 {
		// Dropped by the encoder; retry the keyframe on the next frame.
		e.pendingKey = keyframe
		return nil, false, nil
	}
	e.pendingKey = keyframe && key == 0
	e.buf = append(e.buf[:0], unsafe.Slice((*byte)(data), int(size))...)
	return e.buf, key != 0, nil
}

func (e *vtEncoder) Close() {
	C.ur_vt_close(e.native)
	e.native = nil
	releasePixelBuffer(e.pixel)
	e.pixel = nil
}

// newPixelBuffer allocates an IOSurface-backed NV12 CVPixelBuffer, the
// format ScreenCaptureKit delivers in GPU mode, or returns nil.
func newPixelBuffer(w, h int) unsafe.Pointer { return C.ur_vt_pixel(C.int(w), C.int(h)) }

func releasePixelBuffer(p unsafe.Pointer) { C.ur_vt_release(p) }

// uploadNV12 copies p's planes into the pixel buffer px of the same size.
func uploadNV12(px unsafe.Pointer, p *NV12) error {
	if status := C.ur_vt_upload(px, (*C.uint8_t)(unsafe.Pointer(&p.Y[0])), C.int(p.YStride), (*C.uint8_t)(unsafe.Pointer(&p.UV[0])), C.int(p.UVStride)); status != 0 {
		return fmt.Errorf("VideoToolbox upload: CVReturn %d", status)
	}
	return nil
}
