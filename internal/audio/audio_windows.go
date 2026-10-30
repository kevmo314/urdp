package audio

/*
#cgo windows LDFLAGS: -lole32
#include "native_windows.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"
)

type audioRequest struct {
	buf    []int16
	result chan error
}
type windowsAudio struct {
	requests chan audioRequest
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
}

func audioError(hr C.long) error { return fmt.Errorf("WASAPI: HRESULT 0x%08x", uint32(hr)) }
func openLoopback() (Source, error) {
	s := &windowsAudio{requests: make(chan audioRequest), stop: make(chan struct{}), done: make(chan struct{})}
	ready := make(chan error, 1)
	go s.run(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return s, nil
}

// All COM calls, including shutdown, run on the same locked OS thread.
func (s *windowsAudio) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(s.done)
	var native *C.audio_native
	if hr := C.audio_open(&native); hr < 0 {
		ready <- audioError(hr)
		return
	}
	defer C.audio_close(native)
	ready <- nil
	const packetJitter = 2 * time.Millisecond
	var lastData time.Time
	for {
		select {
		case <-s.stop:
			return
		case req := <-s.requests:
			duration := time.Duration(len(req.buf)/Channels) * time.Second / SampleRate
			// A late engine packet must get a chance to arrive before we synthesize
			// silence. A wall-clock schedule can put that packet behind padding.
			deadline := time.Now().Add(duration)
			// Allow small engine/event scheduling jitter after an active packet.
			// Subsequent silent reads still wait one frame period.
			if activeDeadline := lastData.Add(duration + packetJitter); activeDeadline.After(deadline) {
				deadline = activeDeadline
			}
			offset := 0
			var err error
			for {
				var written C.uint
				hr := C.audio_poll(native, (*C.int16_t)(unsafe.Pointer(&req.buf[offset])), C.uint(len(req.buf)-offset), &written)
				if hr < 0 {
					err = audioError(hr)
					break
				}
				offset += int(written)
				if written > 0 {
					lastData = time.Now()
					deadline = lastData.Add(duration + packetJitter)
				}
				if offset == len(req.buf) {
					break
				}
				wait := time.Until(deadline)
				if wait <= 0 {
					// The final poll above drains real samples before padding.
					clear(req.buf[offset:])
					break
				}
				select {
				case <-s.stop:
					req.result <- errors.New("audio closed")
					return
				default:
				}
				// Bound the native wait so Close remains responsive, including
				// when the render engine produces no loopback events in silence.
				ms := (min(wait, 10*time.Millisecond) + time.Millisecond - 1) / time.Millisecond
				if hr := C.audio_wait(native, C.uint(ms)); hr < 0 {
					err = audioError(hr)
					break
				}
			}
			req.result <- err
		}
	}
}
func (s *windowsAudio) Read(buf []int16) error {
	if len(buf) == 0 {
		return nil
	}
	if len(buf)%Channels != 0 {
		return errors.New("audio buffer must contain whole stereo frames")
	}
	req := audioRequest{buf: buf, result: make(chan error, 1)}
	select {
	case <-s.done:
		return errors.New("audio closed")
	case <-s.stop:
		return errors.New("audio closed")
	case s.requests <- req:
	}
	return <-req.result
}
func (s *windowsAudio) Close() error {
	s.once.Do(func() { close(s.stop) })
	<-s.done
	return nil
}
