package vdev

/*
#cgo windows LDFLAGS: -lole32 -lsetupapi -ladvapi32
#include <stdlib.h>
#include "native_windows.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

const cableHelp = "install VB-Audio Virtual Cable (https://vb-audio.com/Cable/), then select CABLE Output in your app; URDP_MIC_DEVICE may specify an exact render endpoint name"

func hresultError(operation string, hr C.long) error {
	return fmt.Errorf("%s: HRESULT 0x%08x", operation, uint32(hr))
}

type micRequest struct {
	pcm    []int16
	result chan error
}
type windowsMicrophone struct {
	requests   chan micRequest
	stop, done chan struct{}
	once       sync.Once
}

func openMicrophone() (Microphone, error) {
	m := &windowsMicrophone{requests: make(chan micRequest), stop: make(chan struct{}), done: make(chan struct{})}
	ready := make(chan error, 1)
	go m.run(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return m, nil
}
func (m *windowsMicrophone) run(ready chan<- error) {
	// WASAPI lifetime stays on one COM apartment, including release.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(m.done)
	name := C.CString(os.Getenv("URDP_MIC_DEVICE"))
	var native *C.mic_native
	hr := C.mic_open(&native, name)
	C.free(unsafe.Pointer(name))
	if hr < 0 {
		err := hresultError("virtual microphone", hr)
		if uint32(hr) == 0x80070490 {
			err = fmt.Errorf("%w: %v; %s", ErrUnsupported, err, cableHelp)
		}
		ready <- err
		return
	}
	defer C.mic_close(native)
	ready <- nil
	for {
		select {
		case <-m.stop:
			return
		case req := <-m.requests:
			hr := C.mic_write(native, (*C.int16_t)(unsafe.Pointer(&req.pcm[0])), C.uint(len(req.pcm)/2))
			var err error
			if hr < 0 {
				err = hresultError("virtual microphone write", hr)
			}
			req.result <- err
		}
	}
}
func (m *windowsMicrophone) Write(pcm []int16) error {
	if len(pcm)%2 != 0 {
		return errors.New("virtual microphone: expected whole stereo frames")
	}
	if len(pcm) == 0 {
		return nil
	}
	// Bound the native request even for oversized caller buffers.
	if len(pcm) > 1920 {
		pcm = pcm[len(pcm)-1920:]
	}
	req := micRequest{pcm: pcm, result: make(chan error, 1)}
	select {
	case <-m.done:
		return errors.New("virtual microphone closed")
	case <-m.stop:
		return errors.New("virtual microphone closed")
	default:
	}
	select {
	case <-m.done:
		return errors.New("virtual microphone closed")
	case <-m.stop:
		return errors.New("virtual microphone closed")
	case m.requests <- req:
		return <-req.result
	default:
		return nil // Drop concurrent/backed-up audio; never queue latency.
	}
}
func (m *windowsMicrophone) Close() error {
	m.once.Do(func() { close(m.stop) })
	<-m.done
	return nil
}

type windowsCamera struct {
	mu     sync.Mutex
	native *C.camera_native
}

func openCamera() (Camera, error) {
	var native *C.camera_native
	if code := C.camera_open(&native); code != 0 {
		if code == 183 {
			return nil, errors.New("virtual camera: OBS queue is already in use; stop OBS Virtual Camera and close its consumers before retrying")
		}
		if code == 2 || code == 3 {
			return nil, fmt.Errorf("%w: install/register OBS Studio Virtual Camera (https://obsproject.com/); if OBS is installed, run data/obs-plugins/win-dshow/virtualcam-install.bat as Administrator, and leave OBS Virtual Camera stopped", ErrUnsupported)
		}
		return nil, fmt.Errorf("virtual camera: open OBS queue (Windows error %d)", uint32(code))
	}
	return &windowsCamera{native: native}, nil
}
func validateImage(img *Image) error {
	if img == nil || img.Width <= 0 || img.Height <= 0 || img.Width > 16384 || img.Height > 16384 {
		return errors.New("virtual camera: invalid image dimensions")
	}
	cw, ch := (img.Width+1)/2, (img.Height+1)/2
	valid := func(p []byte, stride, width, height int) bool {
		return stride >= width && stride <= math.MaxInt32/height && len(p) >= stride*(height-1)+width
	}
	if !valid(img.Y, img.YStride, img.Width, img.Height) || !valid(img.U, img.UStride, cw, ch) || !valid(img.V, img.VStride, cw, ch) {
		return errors.New("virtual camera: invalid I420 planes or strides")
	}
	return nil
}
func (c *windowsCamera) WriteFrame(img *Image) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.native == nil {
		return errors.New("virtual camera closed")
	}
	if err := validateImage(img); err != nil {
		return err
	}
	C.camera_write(c.native, (*C.uint8_t)(unsafe.Pointer(&img.Y[0])), (*C.uint8_t)(unsafe.Pointer(&img.U[0])), (*C.uint8_t)(unsafe.Pointer(&img.V[0])),
		C.int(img.Width), C.int(img.Height), C.int(img.YStride), C.int(img.UStride), C.int(img.VStride))
	return nil
}
func (c *windowsCamera) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.native != nil {
		C.camera_close(c.native)
		c.native = nil
	}
	return nil
}

type windowsGamepad struct {
	mu     sync.Mutex
	native *C.pad_native
}

func openGamepad() (Gamepad, error) {
	var native *C.pad_native
	if code := C.pad_open(&native); code != 0 {
		if code == 1168 {
			return nil, fmt.Errorf("%w: install ViGEmBus (https://github.com/nefarius/ViGEmBus/releases)", ErrUnsupported)
		}
		return nil, fmt.Errorf("ViGEmBus: open (Windows error %d); install or repair ViGEmBus (https://github.com/nefarius/ViGEmBus/releases)", uint32(code))
	}
	return &windowsGamepad{native: native}, nil
}
func clamp(value, low, high float64) float64 {
	if math.IsNaN(value) {
		return 0
	}
	return max(low, min(high, value))
}
func stick(value float64) C.int16_t {
	value = clamp(value, -1, 1)
	if value < 0 {
		return C.int16_t(math.Round(value * 32768))
	}
	return C.int16_t(math.Round(value * 32767))
}
func (g *windowsGamepad) Update(state GamepadState) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.native == nil {
		return errors.New("virtual gamepad closed")
	}
	masks := [17]uint16{0x1000, 0x2000, 0x4000, 0x8000, 0x0100, 0x0200, 0, 0, 0x0020, 0x0010, 0x0040, 0x0080, 0x0001, 0x0002, 0x0004, 0x0008, 0x0400}
	var buttons uint16
	for i, value := range state.Buttons {
		if value > 0.5 {
			buttons |= masks[i]
		}
	}
	trigger := func(v float64) C.uint8_t { return C.uint8_t(math.Round(clamp(v, 0, 1) * 255)) }
	code := C.pad_update(g.native, C.uint16_t(buttons), trigger(state.Buttons[6]), trigger(state.Buttons[7]),
		stick(state.Axes[0]), stick(-state.Axes[1]), stick(state.Axes[2]), stick(-state.Axes[3]))
	if code != 0 {
		return fmt.Errorf("ViGEmBus: update (Windows error %d)", uint32(code))
	}
	return nil
}
func (g *windowsGamepad) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.native == nil {
		return nil
	}
	code := C.pad_close(g.native)
	g.native = nil
	if code != 0 {
		return fmt.Errorf("ViGEmBus: unplug (Windows error %d)", uint32(code))
	}
	return nil
}
