package server

import (
	"log"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/kevmo314/urdp/internal/audio"
)

// startAudio captures system audio for the lifetime of the process and
// writes Opus packets to a track shared by all viewers.
func startAudio(open func() (audio.Source, error), bitrate int) (*webrtc.TrackLocalStaticSample, error) {
	src, err := open()
	if err != nil {
		return nil, err
	}
	enc, err := audio.NewOpusEncoder(bitrate)
	if err != nil {
		src.Close()
		return nil, err
	}
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: audio.SampleRate, Channels: audio.Channels},
		"audio", "urdp-audio")
	if err != nil {
		src.Close()
		enc.Close()
		return nil, err
	}
	go func() {
		defer src.Close()
		defer enc.Close()
		pcm := make([]int16, audio.FrameSamples*audio.Channels)
		pkt := make([]byte, 1500)
		for {
			if err := src.Read(pcm); err != nil {
				log.Printf("audio: %v", err)
				return
			}
			n, err := enc.Encode(pcm, pkt)
			if err != nil {
				log.Printf("audio: %v", err)
				continue
			}
			_ = track.WriteSample(media.Sample{Data: pkt[:n], Duration: audio.FrameMs * time.Millisecond})
		}
	}()
	return track, nil
}
