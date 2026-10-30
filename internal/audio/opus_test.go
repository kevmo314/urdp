package audio

import (
	"math"
	"testing"
)

func TestOpusRoundTrip(t *testing.T) {
	enc, err := NewOpusEncoder(64000)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	dec, err := NewOpusDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	in := make([]int16, FrameSamples*Channels)
	out := make([]int16, 5760*Channels)
	pkt := make([]byte, 1500)
	var energy float64
	for f := 0; f < 20; f++ {
		for i := 0; i < FrameSamples; i++ {
			v := int16(10000 * math.Sin(2*math.Pi*440*float64(f*FrameSamples+i)/SampleRate))
			in[2*i], in[2*i+1] = v, v
		}
		n, err := enc.Encode(in, pkt)
		if err != nil {
			t.Fatal(err)
		}
		got, err := dec.Decode(pkt[:n], out)
		if err != nil {
			t.Fatal(err)
		}
		if got != FrameSamples {
			t.Fatalf("decoded %d samples, want %d", got, FrameSamples)
		}
		if f >= 10 {
			for _, s := range out[:got*Channels] {
				energy += float64(s) * float64(s)
			}
		}
	}
	if rms := math.Sqrt(energy / float64(10*FrameSamples*Channels)); rms < 3000 {
		t.Errorf("decoded RMS %.0f, want a ~7000 tone", rms)
	}
	if _, err := dec.Decode(nil, out); err != nil {
		t.Errorf("packet loss concealment: %v", err)
	}
}
