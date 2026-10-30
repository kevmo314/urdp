package audio

/*
#cgo darwin CFLAGS: -x objective-c -fobjc-arc -mmacosx-version-min=13.0
#cgo darwin LDFLAGS: -mmacosx-version-min=13.0 -framework Foundation -framework ScreenCaptureKit -framework CoreMedia -framework CoreAudio -framework CoreGraphics
#include <stdint.h>
#include <stdlib.h>
void *ur_audio_open(char **);
int ur_audio_read(void *, int16_t *, int, char **);
void ur_audio_close(void *);
*/
import "C"
import (
	"errors"
	"sync"
	"time"
	"unsafe"
)

type darwinAudio struct {
	readMu sync.Mutex
	mu     sync.Mutex
	native unsafe.Pointer
	next   time.Time
	stop   chan struct{}
	once   sync.Once
}

func audioDarwinError(p *C.char) error {
	if p == nil {
		return errors.New("ScreenCaptureKit audio failed")
	}
	defer C.free(unsafe.Pointer(p))
	return errors.New(C.GoString(p))
}
func openLoopback() (Source, error) {
	var e *C.char
	p := C.ur_audio_open(&e)
	if p == nil {
		return nil, audioDarwinError(e)
	}
	return &darwinAudio{native: p, next: time.Now(), stop: make(chan struct{})}, nil
}
func (s *darwinAudio) Read(buf []int16) error {
	if len(buf) == 0 {
		return nil
	}
	if len(buf)%Channels != 0 {
		return errors.New("audio buffer must contain whole stereo frames")
	}
	s.readMu.Lock()
	defer s.readMu.Unlock()
	duration := time.Duration(len(buf)/Channels) * time.Second / SampleRate
	now := time.Now()
	if now.Sub(s.next) > duration {
		s.next = now
	}
	s.next = s.next.Add(duration)
	timer := time.NewTimer(max(0, time.Until(s.next)))
	defer timer.Stop()
	select {
	case <-s.stop:
		return errors.New("audio closed")
	case <-timer.C:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.native == nil {
		return errors.New("audio closed")
	}
	var e *C.char
	if C.ur_audio_read(s.native, (*C.int16_t)(unsafe.Pointer(&buf[0])), C.int(len(buf)), &e) < 0 {
		return audioDarwinError(e)
	}
	return nil
}
func (s *darwinAudio) Close() error {
	s.once.Do(func() {
		close(s.stop)
		s.mu.Lock()
		defer s.mu.Unlock()
		C.ur_audio_close(s.native)
		s.native = nil
	})
	return nil
}
