package server

import (
	"slices"
	"strings"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"

	"github.com/kevmo314/urdp/internal/encoder"
)

// videoCodec describes how a codec is negotiated and packetized.
type videoCodec struct {
	codec     encoder.Codec
	mime      string
	pt        webrtc.PayloadType
	fmtp      string
	payloader func() rtp.Payloader
}

var videoCodecs = []videoCodec{
	{encoder.H264, webrtc.MimeTypeH264, 102, "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
		func() rtp.Payloader { return &codecs.H264Payloader{} }},
	{encoder.H265, webrtc.MimeTypeH265, 116, "",
		func() rtp.Payloader { return &codecs.H265Payloader{} }},
	{encoder.AV1, webrtc.MimeTypeAV1, 45, "",
		func() rtp.Payloader { return &codecs.AV1Payloader{} }},
	{encoder.VP9, webrtc.MimeTypeVP9, 98, "profile-id=0",
		func() rtp.Payloader { return &codecs.VP9Payloader{} }},
	// VP8 is also how browsers send the viewer's webcam.
	{encoder.VP8, webrtc.MimeTypeVP8, 96, "",
		func() rtp.Payloader { return &codecs.VP8Payloader{EnablePictureID: true} }},
}

func lookupCodec(c encoder.Codec) videoCodec {
	for _, v := range videoCodecs {
		if v.codec == c {
			return v
		}
	}
	return videoCodecs[0]
}

// DefaultCodecs is the server's preference when the viewer doesn't ask for
// a codec: H.264 first, as every browser decodes it in hardware with the
// least latency.
var DefaultCodecs = []encoder.Codec{encoder.H264, encoder.H265, encoder.AV1, encoder.VP9, encoder.VP8}

// offeredVideoCodecs returns the codec names in the offer's first video
// section (the screen), e.g. "H264", "AV1".
func offeredVideoCodecs(sdp string) []string {
	var names []string
	inVideo := false
	for line := range strings.SplitSeq(sdp, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "m=") {
			if inVideo {
				break // only the first video section
			}
			inVideo = strings.HasPrefix(line, "m=video")
			continue
		}
		if !inVideo || !strings.HasPrefix(line, "a=rtpmap:") {
			continue
		}
		// a=rtpmap:96 VP8/90000
		if _, enc, ok := strings.Cut(line, " "); ok {
			name, _, _ := strings.Cut(enc, "/")
			names = append(names, strings.ToUpper(name))
		}
	}
	return names
}

// chooseCodec picks the codec for a viewer: the one they asked for if
// possible, else the first of the server's preferences that both the
// browser offers and this host can encode.
func (s *Server) chooseCodec(offer string, requested string) encoder.Codec {
	offered := offeredVideoCodecs(offer)
	available := encoder.Codecs(s.cfg.Encoder)
	prefs := s.cfg.Codecs
	if c, err := encoder.ParseCodec(requested); err == nil {
		prefs = append([]encoder.Codec{c}, prefs...)
	}
	for _, c := range prefs {
		if slices.Contains(available, c) && (len(offered) == 0 || slices.Contains(offered, string(c))) {
			return c
		}
	}
	return encoder.H264
}
