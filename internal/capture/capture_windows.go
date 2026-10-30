package capture

/*
#cgo windows LDFLAGS: -ld3d11 -ldxgi -ldxguid -ldwmapi -lgdi32 -luser32
#include "native_windows.h"
*/
import "C"

import (
	"fmt"
	"image"
	"sync"
	"time"
	"unsafe"
)

var dpiOnce sync.Once

type windowsSource struct {
	mu       sync.Mutex
	native   *C.capture_native
	id       uint64
	bounds   image.Rectangle
	frame    Frame
	hasFrame bool
}

func captureError(op string, hr C.long) error {
	if uint32(hr) == 0x80070578 {
		return ErrNotFound
	}
	return fmt.Errorf("%s: HRESULT 0x%08x", op, uint32(hr))
}
func openDesktop() (Source, error) { return openWindows(0) }
func openWindow(id uint64) (Source, error) {
	if id == 0 {
		return nil, ErrNotFound
	}
	return openWindows(id)
}
func openWindows(id uint64) (Source, error) {
	dpiOnce.Do(func() { C.capture_dpi() })
	s := &windowsSource{id: id}
	if hr := C.capture_open(C.uint64_t(id), &s.native); hr < 0 {
		return nil, captureError("open capture", hr)
	}
	if err := s.resize(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
func (s *windowsSource) resize() error {
	var r C.capture_rect
	if hr := C.capture_bounds(s.native, &r); hr < 0 {
		return captureError("capture bounds", hr)
	}
	s.bounds = image.Rect(int(r.x), int(r.y), int(r.x+r.w), int(r.y+r.h))
	w, h := int(r.w), int(r.h)
	if w != s.frame.Width || h != s.frame.Height {
		s.hasFrame = false
		s.frame = Frame{Pix: make([]byte, w*h*4), Stride: w * 4, Width: w, Height: h}
	}
	return nil
}
func (s *windowsSource) Capture(wait time.Duration) (*Frame, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.native == nil {
		return nil, false, fmt.Errorf("capture closed")
	}
	if err := s.resize(); err != nil {
		return nil, false, err
	}
	timeout := C.uint(wait.Milliseconds())
	// A new duplication may take a few refreshes to produce its first frame.
	for attempt := 0; attempt < 64; attempt++ {
		hr := C.capture_frame(s.native, (*C.uchar)(unsafe.Pointer(&s.frame.Pix[0])), timeout)
		if hr < 0 {
			return nil, false, captureError("capture frame", hr)
		}
		changed := hr == 0
		if changed {
			s.hasFrame = true
		}
		// Recreation can change dimensions without producing pixels yet.
		if err := s.resize(); err != nil {
			return nil, false, err
		}
		if s.hasFrame {
			return &s.frame, changed, nil
		}
		timeout = 16
	}
	return nil, false, fmt.Errorf("desktop duplication has not produced a frame")
}
func (s *windowsSource) Bounds() image.Rectangle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bounds
}
func (s *windowsSource) WindowID() uint64 { return s.id }
func (s *windowsSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	C.capture_close(s.native)
	s.native = nil
	return nil
}
func listWindows() ([]Window, error) {
	dpiOnce.Do(func() { C.capture_dpi() })
	var items *C.capture_window
	var count C.int
	if hr := C.capture_list(&items, &count); hr < 0 {
		return nil, captureError("list windows", hr)
	}
	defer C.capture_free(unsafe.Pointer(items))
	result := make([]Window, 0, int(count))
	for _, w := range unsafe.Slice(items, int(count)) {
		result = append(result, Window{ID: uint64(w.id), Title: C.GoString(&w.title[0]), Width: int(w.w), Height: int(w.h)})
	}
	return result, nil
}
