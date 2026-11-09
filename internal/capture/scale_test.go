package capture

import (
	"testing"
	"time"
)

func TestDownscale(t *testing.T) {
	// 8x4 frame with a stride wider than the pixels: left half 0, right
	// half 200 in every channel, one 100 pixel in the top-left 2x2 block.
	f := &Frame{Width: 8, Height: 4, Stride: 40, Pix: make([]byte, 40*4), Time: time.Unix(5, 0)}
	for y := 0; y < 4; y++ {
		for x := 4; x < 8; x++ {
			for c := 0; c < 4; c++ {
				f.Pix[y*40+x*4+c] = 200
			}
		}
	}
	for c := 0; c < 4; c++ {
		f.Pix[c] = 100
	}
	var d Frame
	Downscale(f, 2, &d)
	if d.Width != 4 || d.Height != 2 || d.Stride != 16 || !d.Time.Equal(f.Time) || d.Dirty != nil {
		t.Fatalf("frame %dx%d stride %d time %v dirty %v", d.Width, d.Height, d.Stride, d.Time, d.Dirty)
	}
	want := []byte{25, 0, 200, 200, 0, 0, 200, 200} // per pixel, channel 0
	for i, px := range []int{0, 1, 2, 3, 4, 5, 6, 7} {
		if got := d.Pix[px*4]; got != want[i] {
			t.Errorf("pixel %d = %d, want %d", px, got, want[i])
		}
	}
	Downscale(f, 4, &d)
	if d.Width != 2 || d.Height != 1 || d.Pix[0] != 6 || d.Pix[4] != 200 {
		t.Errorf("1/4: %dx%d %v", d.Width, d.Height, d.Pix[:8])
	}
}

func BenchmarkDownscale(b *testing.B) {
	f := &Frame{Width: 3440, Height: 1440, Stride: 3440 * 4, Pix: make([]byte, 3440*1440*4)}
	var d Frame
	for b.Loop() {
		Downscale(f, 2, &d)
	}
}
