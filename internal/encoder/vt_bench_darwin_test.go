package encoder

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/kevmo314/urdp/internal/capture"
)

// screenFrame draws a document-like screen scrolled by off rows: white
// background, lines of dark "words" (rectangles of varying width).
func screenFrame(f *capture.Frame, off int) {
	for y := 0; y < f.Height; y++ {
		row := f.Pix[y*f.Stride : y*f.Stride+f.Width*4]
		line, inLine := (y+off)/22, (y+off)%22
		for x := 0; x < f.Width; x++ {
			v := byte(250)
			if inLine >= 4 && inLine < 16 && x > 40 && x < f.Width-40 {
				// Words: widths and gaps derived from the line and position.
				w := (x - 40) / 9
				if (w*7+line*13)%11 > 2 && (x-40)%9 < 7 && (w*3+line*5)%17 != 0 {
					v = byte(30 + (line*29+w*7)%60)
				}
			}
			row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] = v, v, v, 255
		}
	}
}

// TestVideoToolboxRateBench (URDP_VT_BENCH=1) measures encode time, frame
// sizes (keyframes included: a mid-stream one is forced, as a viewer's loss
// recovery would), drops and achieved bitrate on scrolling screen content.
func TestVideoToolboxRateBench(t *testing.T) {
	if os.Getenv("URDP_VT_BENCH") == "" {
		t.Skip("set URDP_VT_BENCH=1")
	}
	vtAvailable(t, H264)
	mode := "real-time"
	const w, h, fps, n = 1920, 1080, 60, 600
	f := &capture.Frame{Width: w, Height: h, Stride: w * 4, Pix: make([]byte, w*h*4)}
	for _, kbps := range []int{2000, 8000, 20000} {
		enc, err := newVT(H264, w, h, Options{FPS: fps, BitrateKbps: kbps, KeyintSeconds: 60})
		if err != nil {
			t.Fatal(err)
		}
		var times []time.Duration
		var sizes []int
		key0, keyMid, drops, total := 0, 0, 0, 0
		for i := 0; i < n; i++ {
			screenFrame(f, i*4)
			t0 := time.Now()
			data, isKey, err := enc.Encode(f, int64(i*1000/fps), i == 0 || i == n/2)
			times = append(times, time.Since(t0))
			if err != nil {
				t.Fatal(err)
			}
			if len(data) == 0 {
				drops++
				continue
			}
			sizes = append(sizes, len(data))
			total += len(data)
			if isKey && i == 0 {
				key0 = len(data)
			}
			if isKey && i == n/2 {
				keyMid = len(data)
			}
		}
		enc.Close()
		slices.Sort(times)
		slices.Sort(sizes)
		budget := kbps * 1000 / 8 / fps
		got := float64(total*8) / (float64(n) / fps) / 1000
		t.Logf("%-11s %5d kbps: encode p50 %5.2fms p95 %5.2fms | keyframes %6d / %6d B (%4.1fx / %4.1fx per-frame budget %d B) | max P %6d B | dropped %3d/%d | achieved %5.0f kbps (%3.0f%%)",
			mode, kbps, ms(times[n/2]), ms(times[n*95/100]), key0, keyMid, float64(key0)/float64(budget), float64(keyMid)/float64(budget), budget,
			maxNonKey(sizes, key0, keyMid), drops, n, got, 100*got/float64(kbps))
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func maxNonKey(sorted []int, keys ...int) int {
	for i := len(sorted) - 1; i >= 0; i-- {
		if !slices.Contains(keys, sorted[i]) {
			return sorted[i]
		}
	}
	return 0
}
