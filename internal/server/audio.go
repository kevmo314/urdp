package server

import (
	"log"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/kevmo314/urdp/internal/audio"
)

// audioTracks carries the host's audio to every viewer, as plain Opus and
// as RED (Opus with redundancy) for browsers that take it.
type audioTracks struct {
	opus, red *webrtc.TrackLocalStaticRTP
}

// startAudio captures system audio for the lifetime of the process and
// writes Opus packets to tracks shared by all viewers. If the device
// fails (e.g. the default output changes) it is reopened.
func startAudio(open func() (audio.Source, error), bitrate int) (*audioTracks, error) {
	src, err := open()
	if err != nil {
		return nil, err
	}
	enc, err := audio.NewOpusEncoder(bitrate)
	if err != nil {
		src.Close()
		return nil, err
	}
	tracks := &audioTracks{}
	tracks.opus, err = webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: audio.SampleRate, Channels: audio.Channels},
		"audio", "urdp-audio")
	if err == nil {
		tracks.red, err = webrtc.NewTrackLocalStaticRTP(redCapability, "audio", "urdp-audio")
	}
	if err != nil {
		src.Close()
		enc.Close()
		return nil, err
	}
	go func() {
		defer enc.Close()
		pcm := make([]int16, audio.FrameSamples*audio.Channels)
		pkt := make([]byte, 1500)
		var out audioOut
		out.seqOpus, out.seqRED, out.ts = uint16(rand.Uint32()), uint16(rand.Uint32()), rand.Uint32()
		for {
			err := pump(src, enc, tracks, &out, pcm, pkt)
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
	return tracks, nil
}

// redCapability is RED wrapping Opus (payload type 111).
var redCapability = webrtc.RTPCodecCapability{MimeType: "audio/red", ClockRate: audio.SampleRate, Channels: audio.Channels, SDPFmtpLine: "111/111"}

// audioOut is the RTP state of the audio tracks.
type audioOut struct {
	seqOpus, seqRED uint16
	ts              uint32
	red             redEncoder
}

// audioSendStats tracks how evenly 10ms packets leave, over the last second:
// the receiver's jitter buffer grows to cover irregular sending.
var audioSendStats struct {
	sync.Mutex
	last            time.Time
	windowStart     time.Time
	packets, bursts int // bursts: packets sent <2ms after the previous
	maxGap          time.Duration
	report          map[string]any
}

func noteAudioSend(now time.Time) {
	st := &audioSendStats
	st.Lock()
	defer st.Unlock()
	if !st.last.IsZero() {
		gap := now.Sub(st.last)
		st.maxGap = max(st.maxGap, gap)
		if gap < 2*time.Millisecond {
			st.bursts++
		}
	}
	st.last = now
	st.packets++
	if now.Sub(st.windowStart) >= time.Second {
		st.report = map[string]any{"packetsPerSec": st.packets, "bursts": st.bursts, "maxGapMs": st.maxGap.Milliseconds()}
		st.windowStart, st.packets, st.bursts, st.maxGap = now, 0, 0, 0
	}
}

// AudioSendStats is the last second's audio send regularity.
func AudioSendStats() map[string]any {
	audioSendStats.Lock()
	defer audioSendStats.Unlock()
	return audioSendStats.report
}

// pump encodes and sends audio until src fails.
func pump(src audio.Source, enc *audio.OpusEncoder, tracks *audioTracks, out *audioOut, pcm []int16, pkt []byte) error {
	for {
		if err := src.Read(pcm); err != nil {
			return err
		}
		noteAudioSend(time.Now())
		n, err := enc.Encode(pcm, pkt)
		if err != nil {
			return err
		}
		frame := pkt[:n]
		_ = tracks.opus.WriteRTP(&rtp.Packet{
			Header:  rtp.Header{Version: 2, PayloadType: opusPayloadType, SequenceNumber: out.seqOpus, Timestamp: out.ts},
			Payload: frame,
		})
		_ = tracks.red.WriteRTP(&rtp.Packet{
			Header:  rtp.Header{Version: 2, PayloadType: redPayloadType, SequenceNumber: out.seqRED, Timestamp: out.ts},
			Payload: out.red.encode(frame, audio.FrameSamples),
		})
		out.seqOpus++
		out.seqRED++
		out.ts += audio.FrameSamples
	}
}
