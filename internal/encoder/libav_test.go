//go:build libav

package encoder

import (
	"strings"
	"testing"

	"github.com/kevmo314/urdp/internal/capture"
)

// The libav plumbing is exercised with FFmpeg's software libx264, since CI
// has no hardware encoder.
func TestLibavLowLatency(t *testing.T) {
	enc, err := newLibav("libx264", H264, []string{"preset", "ultrafast", "tune", "zerolatency"}, 320, 240, Options{FPS: 30})
	if err != nil {
		t.Skipf("libx264 via libavcodec unavailable: %v", err)
	}
	defer enc.Close()
	src := capture.NewTestSource(320, 240)
	for i := range 10 {
		f, _, _ := src.Capture(0)
		data, key, err := enc.Encode(f, int64(i*33), i == 0 || i == 6)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if len(data) == 0 {
			t.Fatalf("frame %d: no output", i)
		}
		if want := i == 0 || i == 6; key != want {
			t.Errorf("frame %d: key=%v, want %v", i, key, want)
		}
	}
}

// An encoder that holds frames back (here: x264's default lookahead) must
// be rejected rather than silently adding latency.
func TestLibavRejectsBufferingEncoder(t *testing.T) {
	enc, err := newLibav("libx264", H264, []string{"preset", "medium"}, 320, 240, Options{FPS: 30})
	if err != nil {
		t.Skipf("libx264 via libavcodec unavailable: %v", err)
	}
	defer enc.Close()
	f, _, _ := capture.NewTestSource(320, 240).Capture(0)
	_, _, err = enc.Encode(f, 0, true)
	if err == nil || !strings.Contains(err.Error(), "held a frame back") {
		t.Fatalf("buffering encoder accepted: %v", err)
	}
}

func TestLibavOneSession(t *testing.T) {
	a, err := newLibav("libx264", H264, []string{"tune", "zerolatency"}, 320, 240, Options{})
	if err != nil {
		t.Skip(err)
	}
	if _, err := newLibav("libx264", H264, []string{"tune", "zerolatency"}, 320, 240, Options{}); err == nil {
		t.Error("a second concurrent hardware session was allowed")
	}
	a.Close()
	b, err := newLibav("libx264", H264, []string{"tune", "zerolatency"}, 320, 240, Options{})
	if err != nil {
		t.Errorf("session not released on Close: %v", err)
	} else {
		b.Close()
	}
}
