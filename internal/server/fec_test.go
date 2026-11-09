package server

import (
	"testing"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

type packetLog struct{ pkts []rtp.Header }

func (c *packetLog) Write(h *rtp.Header, _ []byte, _ interceptor.Attributes) (int, error) {
	c.pkts = append(c.pkts, h.Clone())
	return 0, nil
}

func TestFrameFEC(t *testing.T) {
	const media, repair = 1111, 2222
	f, _ := frameFECFactory{}.NewInterceptor("")
	out := &packetLog{}
	w := f.BindLocalStream(&interceptor.StreamInfo{
		SSRC: media, MimeType: "video/VP9", PayloadTypeForwardErrorCorrection: FlexFECPayloadType, SSRCForwardErrorCorrection: repair,
	}, out)
	seq := uint16(65530) // exercise wraparound
	send := func(n int, ts uint32) {
		for i := range n {
			w.Write(&rtp.Header{Version: 2, SSRC: media, SequenceNumber: seq, Timestamp: ts, Marker: i == n-1, PayloadType: 102}, make([]byte, 1000), nil)
			seq++
		}
	}
	count := func() (m, r int) {
		for _, h := range out.pkts {
			if h.SSRC == repair {
				r++
			} else {
				m++
			}
		}
		out.pkts = nil
		return
	}

	// A small frame gets its repair packet as soon as the frame ends.
	send(3, 100)
	if m, r := count(); m != 3 || r != 1 {
		t.Fatalf("3-packet frame: %d media, %d repair; want 3, 1", m, r)
	}
	// 20 packets at 10% overhead -> 2 repair packets, after the marker.
	send(20, 200)
	if m, r := count(); m != 20 || r != 2 {
		t.Fatalf("20-packet frame: %d media, %d repair; want 20, 2", m, r)
	}
	// A 100-packet keyframe is protected in groups of at most 48, so repair
	// starts flowing before the frame ends: 48 + 48 + 4 -> 5 + 5 + 1.
	send(100, 300)
	if m, r := count(); m != 100 || r != 11 {
		t.Fatalf("100-packet frame: %d media, %d repair; want 100, 11", m, r)
	}
	// A retransmission of an old packet passes through but isn't protected
	// again.
	w.Write(&rtp.Header{Version: 2, SSRC: media, SequenceNumber: seq - 5, Marker: true, PayloadType: 102}, make([]byte, 1000), nil)
	if m, r := count(); m != 1 || r != 0 {
		t.Fatalf("retransmission: %d media, %d repair; want 1, 0", m, r)
	}
	// Loss raises the overhead: at 10% loss, 20 packets get ceil(20*0.3)=6.
	f.(*frameFEC).loss[media].loss = 0.10
	send(20, 400)
	if _, r := count(); r != 6 {
		t.Fatalf("at 10%% loss: %d repair packets, want 6", r)
	}
}

func TestFECCount(t *testing.T) {
	for _, c := range []struct {
		n    int
		loss float64
		want uint32
	}{{1, 0, 1}, {10, 0, 1}, {48, 0, 5}, {48, 0.05, 10}, {48, 0.5, 24}} {
		if got := fecCount(c.n, c.loss); got != c.want {
			t.Errorf("fecCount(%d, %g) = %d, want %d", c.n, c.loss, got, c.want)
		}
	}
}

func TestFECSkippedForH264(t *testing.T) {
	f, _ := frameFECFactory{}.NewInterceptor("")
	out := &packetLog{}
	w := f.BindLocalStream(&interceptor.StreamInfo{
		SSRC: 1, MimeType: "video/H264", PayloadTypeForwardErrorCorrection: FlexFECPayloadType, SSRCForwardErrorCorrection: 2,
	}, out)
	for i := range 5 {
		w.Write(&rtp.Header{Version: 2, SSRC: 1, SequenceNumber: uint16(i), Marker: i == 4}, make([]byte, 100), nil)
	}
	if len(out.pkts) != 5 {
		t.Errorf("H.264: %d packets out, want 5 (no repair)", len(out.pkts))
	}
}
