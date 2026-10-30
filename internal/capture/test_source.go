package capture

import (
	"image"
	"sync"
	"time"
)

// TestSource renders a moving gradient with a sweeping bar. It needs no
// display server, so it backs unit tests and headless demos.
type TestSource struct {
	mu    sync.Mutex
	frame Frame
	start time.Time
	n     int
}

func NewTestSource(w, h int) *TestSource {
	return &TestSource{
		frame: Frame{Pix: make([]byte, w*h*4), Stride: w * 4, Width: w, Height: h},
		start: time.Now(),
	}
}

func (t *TestSource) Capture() (*Frame, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := &t.frame
	t.n++
	bar := (t.n * 8) % f.Width
	for y := 0; y < f.Height; y++ {
		row := f.Pix[y*f.Stride:]
		for x := 0; x < f.Width; x++ {
			p := row[x*4 : x*4+4]
			if x >= bar && x < bar+16 {
				p[0], p[1], p[2] = 255, 255, 255
			} else {
				p[0] = byte(x * 255 / f.Width)
				p[1] = byte(y * 255 / f.Height)
				p[2] = byte(t.n)
			}
			p[3] = 255
		}
	}
	return f, nil
}

func (t *TestSource) Bounds() image.Rectangle {
	return image.Rect(0, 0, t.frame.Width, t.frame.Height)
}

func (t *TestSource) WindowID() uint64 { return 0 }
func (t *TestSource) Close() error     { return nil }
