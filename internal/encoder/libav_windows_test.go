//go:build libav

package encoder

import (
	"os"
	"sort"
	"testing"
	"time"

	"github.com/kevmo314/urdp/internal/capture"
	"github.com/kevmo314/urdp/internal/encoder/d3dtest"
)

// bars draws four vertical bars: red, green, blue, white (BGRA).
func bars(w, h int) []byte {
	colors := [4][4]byte{{0, 0, 255, 255}, {0, 255, 0, 255}, {255, 0, 0, 255}, {255, 255, 255, 255}}
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			copy(pix[(y*w+x)*4:], colors[x*4/w][:])
		}
	}
	return pix
}

// TestLibavD3D11 encodes a texture on the GPU with the first available
// hardware encoder (or URDP_TEST_ENCODER), checks it is taken without a
// CPU copy and that every frame comes straight back, and writes the stream
// to URDP_TEST_DUMP for checking the colors with a decoder.
func TestLibavD3D11(t *testing.T) {
	name := os.Getenv("URDP_TEST_ENCODER")
	if name == "" {
		t.Skip("set URDP_TEST_ENCODER (e.g. h264_amf); this uses the GPU's hardware encoder")
	}
	const w, h = 1920, 1080
	pix := bars(w, h)
	tex, err := d3dtest.New(w, h, pix)
	if err != nil {
		t.Skip(err)
	}
	defer tex.Close()
	var codec Codec
	var opts []string
	for _, e := range libavEncoders {
		if e.name == name {
			codec, opts = e.codec, e.opts
		}
	}
	for _, mode := range []string{"gpu", "cpu"} {
		t.Run(mode, func(t *testing.T) {
			o := Options{FPS: 60, BitrateKbps: 8000}
			if mode == "gpu" {
				o.GPUDevice = capture.D3D11Device(tex.Device)
			}
			enc, err := newLibav(name, codec, opts, w, h, o)
			if err != nil {
				t.Fatal(err)
			}
			defer enc.Close()
			if got := enc.(*libavEncoder).AcceptsGPUFrames(); got != (mode == "gpu") {
				t.Fatalf("AcceptsGPUFrames = %v", got)
			}
			var out *os.File
			if dump := os.Getenv("URDP_TEST_DUMP"); dump != "" {
				out, _ = os.Create(dump + "." + mode)
				defer out.Close()
			}
			var times []time.Duration
			for i := 0; i < 120; i++ {
				f := &capture.Frame{Width: w, Height: h}
				if mode == "gpu" {
					f.GPU = capture.D3D11Texture{Device: tex.Device, Texture: tex.Texture}
				} else {
					f.Pix, f.Stride = pix, w*4
				}
				t0 := time.Now()
				data, key, err := enc.Encode(f, int64(i*16), i%60 == 0)
				times = append(times, time.Since(t0))
				if err != nil {
					t.Fatalf("frame %d: %v", i, err)
				}
				if len(data) == 0 {
					t.Fatalf("frame %d: no output", i)
				}
				if i%60 == 0 && !key {
					t.Errorf("frame %d: keyframe requested, got none", i)
				}
				if out != nil {
					out.Write(data)
				}
			}
			sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
			t.Logf("%s %s: encode p50 %v p95 %v max %v", name, mode, times[len(times)/2], times[len(times)*95/100], times[len(times)-1])
		})
	}
}
