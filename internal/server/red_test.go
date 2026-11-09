package server

import (
	"bytes"
	"testing"
)

func TestRED(t *testing.T) {
	var r redEncoder
	frames := [][]byte{{1, 1}, {2, 2, 2}, {3}, {4, 4, 4, 4}}
	var payloads [][]byte
	for _, f := range frames {
		payloads = append(payloads, r.encode(f, 480))
	}
	// The first packet has no history; the fourth carries frames 2 and 3.
	if !bytes.Equal(payloads[0], []byte{111, 1, 1}) {
		t.Fatalf("first: %v", payloads[0])
	}
	p := payloads[3]
	// Two 4-byte headers: offsets 960 and 480, lengths 3 and 1.
	want := []byte{
		0x80 | 111, byte(960 >> 6), byte(960<<2&0xff) | 0, 3,
		0x80 | 111, byte(480 >> 6), byte(480<<2&0xff) | 0, 1,
		111, 2, 2, 2, 3, 4, 4, 4, 4,
	}
	if !bytes.Equal(p, want) {
		t.Fatalf("red payload\n got %v\nwant %v", p, want)
	}
	for i, pl := range payloads {
		got, err := redPrimary(pl)
		if err != nil || !bytes.Equal(got, frames[i]) {
			t.Errorf("primary %d = %v, %v", i, got, err)
		}
	}
	if _, err := redPrimary([]byte{0x80 | 111, 0, 0}); err == nil {
		t.Error("truncated header accepted")
	}
}

func TestOffersRED(t *testing.T) {
	offer := "v=0\r\nm=video 9 UDP/TLS/RTP/SAVPF 96\r\na=rtpmap:96 H264/90000\r\nm=audio 9 UDP/TLS/RTP/SAVPF 111 63\r\na=rtpmap:111 opus/48000/2\r\na=rtpmap:63 red/48000/2\r\n"
	if !offersRED(offer) {
		t.Error("red not found")
	}
	if offersRED("m=audio 9 x 111\r\na=rtpmap:111 opus/48000/2\r\nm=video 9 x 63\r\na=rtpmap:63 red/90000\r\n") {
		t.Error("video red counted")
	}
}
