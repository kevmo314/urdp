package encoder

import (
	"testing"

	"github.com/kevmo314/urdp/internal/capture"
)

// nalTypes splits an Annex B stream and returns its NAL unit types.
func nalTypes(b []byte) []int {
	var out []int
	for i := 0; i+3 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			out = append(out, int(b[i+3]&0x1f))
			i += 3
		}
	}
	return out
}

func TestEncodeKeyframes(t *testing.T) {
	src := capture.NewTestSource(320, 240)
	enc, err := New(320, 240, Options{FPS: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	for i := range 10 {
		f, _, _ := src.Capture(0)
		data, idr, err := enc.Encode(f, int64(i*33), i == 0 || i == 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) == 0 {
			t.Fatalf("frame %d: no output; zerolatency should not delay frames", i)
		}
		types := nalTypes(data)
		hasIDR := contains(types, 5)
		if want := i == 0 || i == 5; idr != want || hasIDR != want {
			t.Errorf("frame %d: idr=%v hasIDR=%v nals=%v, want %v", i, idr, hasIDR, types, want)
		}
		if i == 0 && (!contains(types, 7) || !contains(types, 8)) {
			t.Errorf("first frame missing SPS/PPS: %v", types)
		}
	}
}

func TestOddDimensions(t *testing.T) {
	enc, err := New(321, 241, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	if w, h := enc.Size(); w != 320 || h != 240 {
		t.Fatalf("size = %dx%d, want 320x240", w, h)
	}
	f, _, _ := capture.NewTestSource(321, 241).Capture(0)
	if _, _, err := enc.Encode(f, 0, true); err != nil {
		t.Fatal(err)
	}
}

func contains(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestConvertColors(t *testing.T) {
	enc, err := New(64, 64, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	// Top half white, bottom half black, as BGRA.
	f := &capture.Frame{Pix: make([]byte, 64*64*4), Stride: 64 * 4, Width: 64, Height: 64}
	for i := 0; i < 32*64*4; i++ {
		f.Pix[i] = 255
	}
	enc.convert(f)
	y, ys := enc.plane(0)
	u, us := enc.plane(1)
	if y[0] != 235 || y[63*ys] != 16 {
		t.Errorf("luma white=%d black=%d, want 235/16", y[0], y[63*ys])
	}
	if u[0] != 128 || u[31*us] != 128 {
		t.Errorf("chroma white=%d black=%d, want 128", u[0], u[31*int(us)])
	}
}
