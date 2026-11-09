package server

import (
	"math"
	"strings"
	"sync"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/flexfec"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

// FlexFECPayloadType matches what Chrome offers for flexfec-03.
const FlexFECPayloadType = 49

const (
	// maxFECGroup bounds how many media packets one set of repair packets
	// covers. Smaller groups repair sooner within large keyframes; flexfec-03
	// masks allow up to 110.
	maxFECGroup = 48
	// Repair overhead adapts to the viewer's reported loss: fecBase with no
	// loss, rising by fecLossGain per unit of loss, capped at fecMax.
	fecBase     = 0.10
	fecLossGain = 2.0
	fecMax      = 0.50
)

// frameFEC generates FlexFEC-03 repair packets (interleaved XOR, via
// pion's encoder) for each viewer's video stream. Unlike pion's stock
// interceptor, which waits for a fixed packet count and so may hold a
// frame's repair packets until the next frame (indefinitely on an idle
// screen), groups end at every frame boundary.
type frameFECFactory struct{}

func (frameFECFactory) NewInterceptor(string) (interceptor.Interceptor, error) {
	return &frameFEC{loss: map[uint32]*lossEstimate{}}, nil
}

type frameFEC struct {
	interceptor.NoOp
	mu   sync.Mutex
	loss map[uint32]*lossEstimate // by media SSRC
}

// lossEstimate smooths the fraction lost from RTCP receiver reports.
type lossEstimate struct {
	mu   sync.Mutex
	loss float64
}

func (l *lossEstimate) update(fraction uint8) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.loss = 0.7*l.loss + 0.3*float64(fraction)/256
}

func (l *lossEstimate) get() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loss
}

// fecCount returns how many repair packets protect n media packets at the
// given loss rate: at least one.
func fecCount(n int, loss float64) uint32 {
	rate := min(fecBase+fecLossGain*loss, fecMax)
	// The epsilon keeps float error (20*0.3 = 6.0000000000000001) from
	// rounding up a whole extra packet.
	return uint32(max(1, int(math.Ceil(float64(n)*rate-1e-9))))
}

func (f *frameFEC) BindRTCPReader(reader interceptor.RTCPReader) interceptor.RTCPReader {
	return interceptor.RTCPReaderFunc(func(b []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) {
		n, attr, err := reader.Read(b, a)
		if err != nil {
			return n, attr, err
		}
		if pkts, perr := rtcp.Unmarshal(b[:n]); perr == nil {
			for _, p := range pkts {
				rr, ok := p.(*rtcp.ReceiverReport)
				if !ok {
					continue
				}
				f.mu.Lock()
				for _, r := range rr.Reports {
					if l := f.loss[r.SSRC]; l != nil {
						l.update(r.FractionLost)
					}
				}
				f.mu.Unlock()
			}
		}
		return n, attr, err
	})
}

func (f *frameFEC) BindLocalStream(info *interceptor.StreamInfo, writer interceptor.RTPWriter) interceptor.RTPWriter {
	if info.PayloadTypeForwardErrorCorrection == 0 || info.SSRCForwardErrorCorrection == 0 || !fecUsable(info.MimeType) {
		return writer
	}
	media := info.SSRC
	loss := &lossEstimate{}
	f.mu.Lock()
	f.loss[media] = loss
	f.mu.Unlock()

	enc := flexfec.NewFlexEncoder03(info.PayloadTypeForwardErrorCorrection, info.SSRCForwardErrorCorrection)
	var (
		mu      sync.Mutex
		group   []rtp.Packet
		nextSeq uint16
		started bool
	)
	return interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, a interceptor.Attributes) (int, error) {
		n, err := writer.Write(h, payload, a)
		if h.SSRC != media {
			return n, err
		}
		mu.Lock()
		// Only protect the live sequence; NACK retransmissions of older
		// packets come through here too and must not join a group.
		if started && h.SequenceNumber != nextSeq {
			if seqBefore(h.SequenceNumber, nextSeq) {
				mu.Unlock()
				return n, err
			}
			group = group[:0] // a gap: start over
		}
		started = true
		group = append(group, rtp.Packet{Header: h.Clone(), Payload: append([]byte(nil), payload...)})
		nextSeq = h.SequenceNumber + 1
		var repair []rtp.Packet
		if h.Marker || len(group) == maxFECGroup {
			repair = enc.EncodeFec(group, fecCount(len(group), loss.get()))
			group = group[:0]
		}
		mu.Unlock()
		for i := range repair {
			if _, werr := writer.Write(&repair[i].Header, repair[i].Payload, a); werr != nil && err == nil {
				err = werr
			}
		}
		return n, err
	})
}

func (f *frameFEC) UnbindLocalStream(info *interceptor.StreamInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.loss, info.SSRC)
}

// fecUsable reports whether Chrome recovers lost packets with FlexFEC for
// this codec. It ignores FEC for H.264 and H.265 (measured: no recovery),
// so sending it there would only waste bandwidth.
func fecUsable(mime string) bool {
	switch strings.ToLower(mime) {
	case "video/vp8", "video/vp9", "video/av1":
		return true
	}
	return false
}

// seqBefore reports whether a precedes b, accounting for wraparound.
func seqBefore(a, b uint16) bool { return int16(a-b) < 0 }
