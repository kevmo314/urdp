package server

import (
	"encoding/binary"
	"errors"
	"strings"
)

// RED (RFC 2198) for Opus: every audio packet also carries the previous
// redDepth frames, so the receiver rebuilds a lost packet from the next one
// that arrives. Opus's own in-band FEC only exists in its speech (SILK)
// mode, not the low-delay music mode used here.

const (
	// redPayloadType is what Chrome offers for red/48000/2.
	redPayloadType  = 63
	opusPayloadType = 111
	// redDepth earlier 10ms frames ride along: 20ms of loss is covered.
	redDepth = 2
)

// redEncoder builds RED payloads from consecutive Opus frames.
type redEncoder struct {
	history [][]byte // oldest first, at most redDepth
}

// encode returns the RED payload for frame (whose RTP timestamps advance
// by step per frame) and remembers frame for the following packets.
func (r *redEncoder) encode(frame []byte, step uint32) []byte {
	out := make([]byte, 0, 4*len(r.history)+1+len(frame)*(len(r.history)+1))
	for i, old := range r.history {
		// F=1, PT, 14-bit timestamp offset, 10-bit block length.
		offset := step * uint32(len(r.history)-i)
		hdr := uint32(1)<<31 | uint32(opusPayloadType)<<24 | (offset&0x3fff)<<10 | uint32(len(old)&0x3ff)
		out = binary.BigEndian.AppendUint32(out, hdr)
	}
	out = append(out, opusPayloadType) // F=0: the primary block
	for _, old := range r.history {
		out = append(out, old...)
	}
	out = append(out, frame...)
	// Blocks over 1023 bytes can't be described; drop history instead.
	if len(frame) > 0x3ff {
		r.history = r.history[:0]
		return append([]byte{opusPayloadType}, frame...)
	}
	r.history = append(r.history, append([]byte(nil), frame...))
	if len(r.history) > redDepth {
		r.history = r.history[1:]
	}
	return out
}

// redPrimary extracts the primary (newest) block of a RED payload.
func redPrimary(p []byte) ([]byte, error) {
	skip := 0
	for {
		if len(p) < 1 {
			return nil, errors.New("red: truncated header")
		}
		if p[0]&0x80 == 0 {
			p = p[1:]
			break
		}
		if len(p) < 4 {
			return nil, errors.New("red: truncated header")
		}
		skip += int(binary.BigEndian.Uint32(p) & 0x3ff)
		p = p[4:]
	}
	if skip > len(p) {
		return nil, errors.New("red: block lengths exceed payload")
	}
	return p[skip:], nil
}

// offersRED reports whether an offer's audio sections include red/48000.
func offersRED(sdp string) bool {
	inAudio := false
	for line := range strings.SplitSeq(sdp, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "m=") {
			inAudio = strings.HasPrefix(line, "m=audio")
		}
		if inAudio && strings.HasPrefix(line, "a=rtpmap:") && strings.Contains(strings.ToLower(line), " red/48000") {
			return true
		}
	}
	return false
}
