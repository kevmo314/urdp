package capture

import (
	"image"
	"os"
	"testing"
	"time"
)

type fixedDarwinDisplay struct {
	bounds image.Rectangle
	frame  Frame
}

func (s *fixedDarwinDisplay) Capture(time.Duration) (*Frame, bool, error) {
	return &s.frame, false, nil
}
func (s *fixedDarwinDisplay) Bounds() image.Rectangle { return s.bounds }
func (s *fixedDarwinDisplay) WindowID() uint64        { return 0 }
func (s *fixedDarwinDisplay) Close() error            { return nil }

func TestDarwinStitchMixedScales(t *testing.T) {
	// A Retina display at negative coordinates and a 1x display separated by
	// a point-sized gap. Input coordinates must match the composed geometry.
	left := &fixedDarwinDisplay{bounds: image.Rect(-2, -1, 0, 1), frame: Frame{Width: 4, Height: 4, Stride: 16, Pix: make([]byte, 64)}}
	right := &fixedDarwinDisplay{bounds: image.Rect(1, 0, 3, 1), frame: Frame{Width: 2, Height: 1, Stride: 8, Pix: []byte{50, 0, 0, 255, 60, 0, 0, 255}}}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			left.frame.Pix[y*16+x*4] = byte(y*10 + x)
		}
	}
	s := &darwinDesktop{sources: []Source{left, right}, bounds: image.Rect(-2, -1, 3, 1), first: true, frame: Frame{Width: 5, Height: 2, Stride: 20, Pix: make([]byte, 40)}}
	f, changed, err := s.Capture(0)
	if err != nil || !changed {
		t.Fatalf("initial capture: changed=%v err=%v", changed, err)
	}
	want := []byte{0, 2, 0, 0, 0, 20, 22, 0, 50, 60}
	for i, v := range want {
		if f.Pix[i*4] != v {
			t.Errorf("pixel %d = %d, want %d", i, f.Pix[i*4], v)
		}
	}
	start := time.Now()
	_, changed, err = s.Capture(15 * time.Millisecond)
	if err != nil || changed {
		t.Fatalf("idle capture: changed=%v err=%v", changed, err)
	}
	if time.Since(start) < 14*time.Millisecond {
		t.Fatal("idle capture did not wait")
	}
}

// Opt in on a GUI Mac with Screen Recording permission. CI stays headless.
func TestDarwinScreenCapture(t *testing.T) {
	if os.Getenv("URDP_TEST_SCREEN") == "" {
		t.Skip("set URDP_TEST_SCREEN=1 on a GUI Mac with Screen Recording permission")
	}
	ds, err := Displays()
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) == 0 {
		t.Fatal("no displays")
	}
	for _, source := range []string{"desktop", "display1"} {
		s, err := Open(source)
		if err != nil {
			t.Fatal(err)
		}
		f, changed, err := s.Capture(100 * time.Millisecond)
		if err != nil || !changed || f == nil {
			s.Close()
			t.Fatalf("first capture %s: %v, %v", source, changed, err)
		}
		if f.Width < s.Bounds().Dx() || f.Height < s.Bounds().Dy() || len(f.Pix) < f.Stride*f.Height {
			t.Fatal("invalid frame geometry")
		}
		start := time.Now()
		for i := 0; i < 100; i++ {
			if _, _, err = s.Capture(0); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("%s: %dx%d pixels; bounds %v; Capture(0) average %v", source, f.Width, f.Height, s.Bounds(), time.Since(start)/100)
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
