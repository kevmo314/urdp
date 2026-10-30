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
	next := time.Now()
	for {
		select {
		case <-s.stop:
			return
		case req := <-s.requests:
			duration := time.Duration(len(req.buf)/Channels) * time.Second / SampleRate
			now := time.Now()
			if now.Sub(next) > duration {
				next = now
			}
			next = next.Add(duration)
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
				wait := time.Until(next)
				if wait <= 0 {
					clear(req.buf[offset:])
					break
				}
				if offset < len(req.buf) {
					wait = min(wait, time.Millisecond)
				}
				timer := time.NewTimer(wait)
				select {
				case <-timer.C:
				case <-s.stop:
					timer.Stop()
					req.result <- errors.New("audio closed")
					return
				}
				if offset == len(req.buf) {
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
