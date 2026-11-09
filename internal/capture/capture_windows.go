package capture

/*
#cgo windows LDFLAGS: -ld3d11 -ldxgi -ldxguid -ldwmapi -lgdi32 -luser32 -lruntimeobject -lole32 -static-libgcc -static-libstdc++
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
	mu     sync.Mutex
	native *C.capture_native
	id     uint64
	bounds image.Rectangle
	frame  Frame
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
	return nil
}
func (s *windowsSource) Capture(wait time.Duration) (*Frame, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.native == nil {
		return nil, false, fmt.Errorf("capture closed")
	}
	timeout := C.uint(0)
	if wait > 0 {
		// Round up sub-millisecond waits and avoid the Windows INFINITE value.
		ms := wait / time.Millisecond
		if wait%time.Millisecond != 0 {
			ms++
		}
		if ms > 0xfffffffe {
			ms = 0xfffffffe
		}
		timeout = C.uint(ms)
	}
	var image C.capture_image
	hr := C.capture_frame(s.native, &image, timeout)
	if hr < 0 {
		return nil, false, captureError("capture frame", hr)
	}
	if err := s.resize(); err != nil {
		return nil, false, err
	}
	w, h, stride := int(image.w), int(image.h), int(image.stride)
	switch {
	case image.texture != nil:
		s.frame = Frame{Width: w, Height: h, GPU: D3D11Texture{Device: image.device, Texture: image.texture, Mip: int(image.mip)}}
	case image.pixels != nil:
		s.frame = Frame{
			Pix:    unsafe.Slice((*byte)(unsafe.Pointer(image.pixels)), (h-1)*stride+w*4),
			Stride: stride, Width: w, Height: h,
		}
	default:
		return nil, false, fmt.Errorf("capture has not produced a frame")
	}
	if image.present > 0 {
		// Convert from the QueryPerformanceCounter clock to Go's.
		if age := time.Duration(C.capture_now_100ns()-image.present) * 100; age >= 0 && age < time.Second {
			s.frame.Time = time.Now().Add(-age)
		}
	}
	return &s.frame, hr == 0, nil
}

// D3D11Texture is a Windows frame left in GPU memory: an ID3D11Texture2D
// (DXGI_FORMAT_B8G8R8A8_UNORM, at least Width x Height) on an
// ID3D11Device. It is owned by the source and overwritten by the next
// Capture.
type D3D11Texture struct {
	Device, Texture unsafe.Pointer
	// Mip is the mip level holding the frame when it's scaled down.
	Mip int
}

// SetScale makes GPU frames 1/scale of the size in each dimension (1, 2
// or 4), scaled on the GPU. It reports whether GPU frames will be scaled;
// CPU frames always come at full size.
func (s *windowsSource) SetScale(scale int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.native != nil && C.capture_set_scale(s.native, C.int(scale)) != 0
}

// SetGPUOutput switches between CPU pixels and D3D11Texture frames. Even
// with it on, some frames (rotated monitors, fallbacks) come as CPU pixels.
func (s *windowsSource) SetGPUOutput(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.native == nil {
		return fmt.Errorf("capture closed")
	}
	if got := C.capture_set_gpu(s.native, cbool(on)) != 0; got != on {
		return fmt.Errorf("GPU frames unavailable (monitors on several adapters?)")
	}
	return nil
}

// GPUDevice is the ID3D11Device that GPU frames will be on, for opening an
// encoder that can take them.
func (s *windowsSource) GPUDevice() any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.native == nil {
		return nil
	}
	if d := C.capture_device(s.native); d != nil {
		return D3D11Device(d)
	}
	return nil
}

// D3D11Device is an ID3D11Device pointer.
type D3D11Device unsafe.Pointer

func cbool(b bool) C.int {
	if b {
		return 1
	}
	return 0
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
		result = append(result, Window{
			ID: uint64(w.id), Title: C.GoString(&w.title[0]), App: C.GoString(&w.app[0]),
			Width: int(w.w), Height: int(w.h),
		})
	}
	return result, nil
}

func windowIcon(id uint64, size int) (image.Image, error) {
	dpiOnce.Do(func() { C.capture_dpi() })
	if size <= 0 {
		size = 32
	}
	// Bound allocations and the dimensions passed to GDI for caller-supplied sizes.
	if size > 1024 {
		return nil, fmt.Errorf("icon size exceeds 1024 pixels")
	}
	icon := image.NewRGBA(image.Rect(0, 0, size, size))
	if hr := C.capture_icon(C.uint64_t(id), C.int(size), (*C.uchar)(unsafe.Pointer(&icon.Pix[0]))); hr < 0 {
		return nil, captureError("window icon", hr)
	}
	return icon, nil
}
