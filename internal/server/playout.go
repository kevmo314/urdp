package server

import (
	"slices"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

// PlayoutDelayURI is the abs-playout-delay header extension. Sending
// min=max=0 tells Chrome to skip its smoothing buffer and render frames
// as soon as they are decoded.
const PlayoutDelayURI = "http://www.webrtc.org/experiments/rtp-hdrext/playout-delay"

// playoutDelayFactory stamps a zero playout delay on every outgoing packet
// of streams that negotiated the extension.
type playoutDelayFactory struct{}

func (playoutDelayFactory) NewInterceptor(string) (interceptor.Interceptor, error) {
	return &playoutDelay{}, nil
}

type playoutDelay struct{ interceptor.NoOp }

func (p *playoutDelay) BindLocalStream(info *interceptor.StreamInfo, w interceptor.RTPWriter) interceptor.RTPWriter {
	id := 0
	for _, e := range info.RTPHeaderExtensions {
		if e.URI == PlayoutDelayURI {
			id = e.ID
		}
	}
	zero := []byte{0, 0, 0}
	return interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, a interceptor.Attributes) (int, error) {
		// A track's header is shared by all its viewers, and interceptors
		// further down (e.g. transport-cc) set extensions in place with
		// per-viewer ids. Give each viewer its own copy. This interceptor is
		// registered last, so it runs first.
		hdr := *h
		hdr.Extensions = slices.Clone(h.Extensions)
		if id != 0 {
			if err := hdr.SetExtension(uint8(id), zero); err != nil {
				return 0, err
			}
		}
		return w.Write(&hdr, payload, a)
	})
}
