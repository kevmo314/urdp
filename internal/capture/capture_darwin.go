package capture

/*
#cgo CFLAGS: -mmacosx-version-min=13.0
#cgo darwin LDFLAGS: -mmacosx-version-min=13.0 -framework AppKit -framework ScreenCaptureKit -framework CoreMedia -framework CoreVideo -framework CoreGraphics
#cgo darwin CFLAGS: -x objective-c -fobjc-arc
#include <stdlib.h>
#include <stdint.h>
uint64_t ur_generation(void);
void ur_wait(uint64_t, double);
char *ur_list(int, char **);
void *ur_open(uint32_t, int, char **);
int ur_frame(void *, double, void **, int *, int *, int *, double *, char **);
void ur_close(void *);
int ur_icon(uint32_t, int, unsigned char *);
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"sync"
	"time"
	"unsafe"
)

type darwinSource struct {
	mu     sync.Mutex
	native unsafe.Pointer
	id     uint64
	bounds image.Rectangle
	frame  Frame
	first  bool
}

func nativeError(p *C.char) error {
	if p == nil {
		return errors.New("ScreenCaptureKit failed")
	}
	defer C.free(unsafe.Pointer(p))
	if C.GoString(p) == "window not found" {
		return ErrNotFound
	}
	return errors.New(C.GoString(p))
}
func enumeration(displays bool, out any) error {
	var e *C.char
	n := C.int(0)
	if displays {
		n = 1
	}
	p := C.ur_list(n, &e)
	if p == nil {
		return nativeError(e)
	}
	defer C.free(unsafe.Pointer(p))
	return json.Unmarshal([]byte(C.GoString(p)), out)
}
func listWindows() (out []Window, err error)   { err = enumeration(false, &out); return }
func listDisplays() (out []Display, err error) { err = enumeration(true, &out); return }
func openWindow(id uint64) (Source, error) {
	if id == 0 || id > 1<<32-1 {
		return nil, ErrNotFound
	}
	return openDarwin(id, 0)
}
func openDisplay(i int) (Source, error) { return openDarwin(0, i) }
func openDarwin(id uint64, display int) (Source, error) {
	var e *C.char
	p := C.ur_open(C.uint32_t(id), C.int(display), &e)
	if p == nil {
		return nil, nativeError(e)
	}
	s := &darwinSource{native: p, id: id}
	// Prime the source so Bounds and the first frame are available immediately.
	if _, _, err := s.Capture(3 * time.Second); err != nil {
		s.Close()
		return nil, err
	}
	s.first = true
	return s, nil
}
func (s *darwinSource) Capture(wait time.Duration) (*Frame, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.native == nil {
		return nil, false, errors.New("capture closed")
	}
	var p unsafe.Pointer
	var w, h, stride C.int
	var r [4]C.double
	var e *C.char
	n := C.ur_frame(s.native, C.double(max(0, wait.Seconds())), &p, &w, &h, &stride, &r[0], &e)
	if n == -2 {
		return nil, false, ErrNotFound
	}
	if n < 0 {
		return nil, false, nativeError(e)
	}
	s.bounds = image.Rect(int(r[0]), int(r[1]), int(r[0]+r[2]), int(r[1]+r[3]))
	if p == nil {
		return nil, false, errors.New("ScreenCaptureKit has not delivered a frame")
	}
	s.frame = Frame{Pix: unsafe.Slice((*byte)(p), int(stride)*int(h)), Stride: int(stride), Width: int(w), Height: int(h)}
	changed := n > 0 || s.first
	s.first = false
	return &s.frame, changed, nil
}
func (s *darwinSource) Bounds() image.Rectangle { s.mu.Lock(); defer s.mu.Unlock(); return s.bounds }
func (s *darwinSource) WindowID() uint64        { return s.id }
func (s *darwinSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	C.ur_close(s.native)
	s.native = nil
	return nil
}
func windowIcon(id uint64, size int) (image.Image, error) {
	if id > 1<<32-1 {
		return nil, ErrNotFound
	}
	if size <= 0 {
		size = 32
	}
	if size > 1024 {
		return nil, fmt.Errorf("icon size exceeds 1024 pixels")
	}
	im := image.NewRGBA(image.Rect(0, 0, size, size))
	if C.ur_icon(C.uint32_t(id), C.int(size), (*C.uchar)(unsafe.Pointer(&im.Pix[0]))) == 0 {
		return nil, ErrNotFound
	}
	return im, nil
}

// A single display stays zero-copy at native resolution. Multiple displays are
// stitched at one pixel per global point, including gaps and negative origins.
// This deliberately downsamples Retina displays to align mixed-scale monitors.
type darwinDesktop struct {
	mu      sync.Mutex
	sources []Source
	bounds  image.Rectangle
	frame   Frame
	first   bool
}

func openDesktop() (Source, error) {
	ds, err := listDisplays()
	if err != nil {
		return nil, err
	}
	if len(ds) == 0 {
		return nil, errors.New("no displays")
	}
	if len(ds) == 1 {
		return openDisplay(1)
	}
	s := &darwinDesktop{first: true}
	for _, d := range ds {
		src, e := openDisplay(d.Index)
		if e != nil {
			s.Close()
			return nil, e
		}
		s.sources = append(s.sources, src)
		s.bounds = s.bounds.Union(src.Bounds())
	}
	s.frame = Frame{Width: s.bounds.Dx(), Height: s.bounds.Dy(), Stride: s.bounds.Dx() * 4}
	s.frame.Pix = make([]byte, s.frame.Stride*s.frame.Height)
	return s, nil
}
func (s *darwinDesktop) Capture(wait time.Duration) (*Frame, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sources) == 0 {
		return nil, false, errors.New("capture closed")
	}
	deadline := time.Now().Add(wait)
	changed := s.first
	for {
		generation := C.ur_generation()
		for _, src := range s.sources {
			f, c, e := src.Capture(0)
			if e != nil {
				return nil, false, e
			}
			if !c && !s.first {
				continue
			}
			changed = true
			r := src.Bounds().Sub(s.bounds.Min)
			for y := 0; y < r.Dy(); y++ {
				sy := y * f.Height / r.Dy()
				for x := 0; x < r.Dx(); x++ {
					sx := x * f.Width / r.Dx()
					dst := (r.Min.Y+y)*s.frame.Stride + (r.Min.X+x)*4
					off := sy*f.Stride + sx*4
					copy(s.frame.Pix[dst:dst+4], f.Pix[off:off+4])
				}
			}
		}
		if changed || !time.Now().Before(deadline) {
			s.first = false
			return &s.frame, changed, nil
		}
		C.ur_wait(generation, C.double(time.Until(deadline).Seconds()))
	}
}
func (s *darwinDesktop) Bounds() image.Rectangle { return s.bounds }
func (s *darwinDesktop) WindowID() uint64        { return 0 }
func (s *darwinDesktop) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, src := range s.sources {
		src.Close()
	}
	s.sources = nil
	return nil
}
