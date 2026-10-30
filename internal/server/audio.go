package server

import (
	"log"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/kevmo314/urdp/internal/audio"
)

// startAudio captures system audio for the lifetime of the process and
// writes Opus packets to a track shared by all viewers. If the device
// fails (e.g. the default output changes) it is reopened.
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
		defer enc.Close()
		pcm := make([]int16, audio.FrameSamples*audio.Channels)
		pkt := make([]byte, 1500)
		for {
			err := pump(src, enc, track, pcm, pkt)
			src.Close()
			log.Printf("audio: %v; reopening", err)
			for {
				time.Sleep(time.Second)
				if src, err = open(); err == nil {
					break
				}
			}
		}
	}()
	return track, nil
}

// pump encodes and sends audio until src fails.
func pump(src audio.Source, enc *audio.OpusEncoder, track *webrtc.TrackLocalStaticSample, pcm []int16, pkt []byte) error {
	for {
		if err := src.Read(pcm); err != nil {
			return err
		}
		n, err := enc.Encode(pcm, pkt)
		if err != nil {
			return err
		}
		_ = track.WriteSample(media.Sample{Data: pkt[:n], Duration: audio.FrameMs * time.Millisecond})
	}
}
