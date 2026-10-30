package encoder

import (
	"testing"

	"github.com/kevmo314/urdp/internal/capture"
)

// benchMotion encodes 1080p content that changes every frame with the
// named backend; ns/op is the per-frame encode latency (one in, one out).
func benchMotion(b *testing.B, backend string, c Codec) {
	src := capture.NewTestSource(1920, 1080)
	enc, err := New(c, 1920, 1080, Options{FPS: 60, Backend: backend})
	if err != nil {
		b.Skip(err)
	}
	defer enc.Close()
	size := 0
	for i := range b.N {
		b.StopTimer()
		f, _, _ := src.Capture(0)
		b.StartTimer()
		data, _, err := enc.Encode(f, int64(i*16), i == 0)
		if err != nil {
			b.Fatal(err)
		}
		size += len(data)
	}
	b.ReportMetric(float64(size)/float64(b.N), "B/frame")
}

func BenchmarkVP9Motion1080p(b *testing.B) { benchMotion(b, "vpx-vp9", VP9) }
func BenchmarkVP8Motion1080p(b *testing.B) { benchMotion(b, "vpx-vp8", VP8) }
func BenchmarkAV1Motion1080p(b *testing.B) { benchMotion(b, "aom-av1", AV1) }
