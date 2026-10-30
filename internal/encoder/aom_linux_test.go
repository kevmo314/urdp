package encoder

import (
	"testing"

	"github.com/pion/rtp/codecs"
)

// obuTypes lists the OBU types in a low-overhead bitstream, failing if
// any OBU lacks a size field (pion's payloader needs them to split OBUs).
func obuTypes(t *testing.T, b []byte) []int {
	t.Helper()
	var types []int
	for len(b) > 0 {
		h := b[0]
		if h&0x80 != 0 || h&0x02 == 0 {
			t.Fatalf("bad OBU header %#x", h)
		}
		n := 1
		if h&0x04 != 0 {
			n++ // extension header
		}
		size, shift := 0, 0
		for {
			if n >= len(b) {
				t.Fatal("truncated leb128")
			}
			c := b[n]
			n++
			size |= int(c&0x7f) << shift
			shift += 7
			if c&0x80 == 0 {
				break
			}
		}
		if n+size > len(b) {
			t.Fatalf("OBU size %d overruns %d bytes", size, len(b)-n)
		}
		types = append(types, int(h>>3)&0xf)
		b = b[n+size:]
	}
	return types
}

const (
	obuSequenceHeader     = 1
	obuTemporalDelimiter  = 2
	obuFrame              = 6
	obuFrameHeader        = 3
	av1AggregationNewSeqN = 0x08
)

func TestAOMEncode(t *testing.T) {
	const w, h = 320, 240
	enc, err := newAOM(w, h, Options{FPS: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	frames := encodeSequence(t, enc, w, h, 20, 0, 10)

	var rtpFrames []encodedFrame
	pay := &codecs.AV1Payloader{}
	for i, f := range frames {
		types := obuTypes(t, f.data)
		if got := contains(types, obuSequenceHeader); got != f.key {
			t.Errorf("frame %d (key=%v): OBUs %v", i, f.key, types)
		}
		if !contains(types, obuFrame) && !contains(types, obuFrameHeader) {
			t.Errorf("frame %d: no frame OBU in %v", i, types)
		}
		// Round trip through the RTP packetizer the server uses: the
		// first packet of a keyframe must flag a new coded video sequence.
		pkts := pay.Payload(1200, f.data)
		if len(pkts) == 0 {
			t.Fatalf("frame %d: payloader produced nothing", i)
		}
		if got := pkts[0][0]&av1AggregationNewSeqN != 0; got != f.key {
			t.Errorf("frame %d: N bit %v, keyframe %v", i, got, f.key)
		}
		depack := &codecs.AV1Depacketizer{}
		tu := []byte{obuTemporalDelimiter<<3 | 0x02, 0}
		for _, p := range pkts {
			obus, err := depack.Unmarshal(p)
			if err != nil {
				t.Fatalf("frame %d: depacketize: %v", i, err)
			}
			tu = append(tu, obus...)
		}
		rtpFrames = append(rtpFrames, encodedFrame{tu, f.key})
	}
	checkDecodes(t, "AV01", w, h, frames)
	checkDecodes(t, "AV01", w, h, rtpFrames)

	if err := enc.SetBitrate(2000); err != nil {
		t.Fatal(err)
	}
	encodeSequence(t, enc, w, h, 2, 0)
}
