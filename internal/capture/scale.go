package capture

import (
	"runtime"
	"sync"
)

// Downscale box-filters a CPU frame to 1/n of its width and height (n = 2
// or 4), into dst's buffer, which it reuses. The result has no dirty
// rectangles (the whole frame counts as changed) and keeps f's Time.
func Downscale(f *Frame, n int, dst *Frame) {
	w, h := f.Width/n, f.Height/n
	stride := w * 4
	if cap(dst.Pix) < stride*h {
		dst.Pix = make([]byte, stride*h)
	}
	*dst = Frame{Pix: dst.Pix[:stride*h], Stride: stride, Width: w, Height: h, Time: f.Time}
	// Rows in parallel: a 3440x1440 frame is 5M pixels to read.
	workers := min(runtime.GOMAXPROCS(0), 8, max(1, h/16))
	var wg sync.WaitGroup
	for k := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			area := uint32(n * n)
			for y := h * k / workers; y < h*(k+1)/workers; y++ {
				out := dst.Pix[y*stride : y*stride+stride]
				for x := 0; x < w; x++ {
					var b, g, r, a uint32
					for dy := 0; dy < n; dy++ {
						row := f.Pix[(y*n+dy)*f.Stride+x*n*4:]
						for dx := 0; dx < n*4; dx += 4 {
							b += uint32(row[dx])
							g += uint32(row[dx+1])
							r += uint32(row[dx+2])
							a += uint32(row[dx+3])
						}
					}
					o := out[x*4 : x*4+4]
					o[0], o[1], o[2], o[3] = byte(b/area), byte(g/area), byte(r/area), byte(a/area)
				}
			}
		}()
	}
	wg.Wait()
}
