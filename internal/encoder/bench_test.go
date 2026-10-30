package encoder

import (
	"testing"

	"github.com/kevmo314/urdp/internal/capture"
)

func BenchmarkEncode1080p(b *testing.B) {
	src := capture.NewTestSource(1920, 1080)
	f, _, _ := src.Capture(0)
	enc, err := New(1920, 1080, Options{FPS: 60})
	if err != nil {
		b.Fatal(err)
	}
	defer enc.Close()
	b.ResetTimer()
	for i := range b.N {
		if _, _, err := enc.Encode(f, int64(i), false); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkConvert1080p(b *testing.B) {
	src := capture.NewTestSource(1920, 1080)
	f, _, _ := src.Capture(0)
	enc, err := New(1920, 1080, Options{FPS: 60})
	if err != nil {
		b.Fatal(err)
	}
	defer enc.Close()
	b.ResetTimer()
	for range b.N {
		enc.convert(f)
	}
}
