package server

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"

	"github.com/kevmo314/urdp/internal/audio"
	"github.com/kevmo314/urdp/internal/vdev"
	"github.com/kevmo314/urdp/internal/vpx"
)

// The viewer's own devices arrive as tracks the browser sends (microphone,
// webcam) and as gamepad state on the "gamepad" data channel. Each is
// replayed into a virtual device on the host.

// receiveMicrophone decodes the viewer's microphone into a virtual mic.
func (s *Server) receiveMicrophone(t *webrtc.TrackRemote, notify func(dev, msg string)) {
	mic, err := s.cfg.OpenMicrophone()
	if err != nil {
		log.Printf("viewer microphone ignored: %v", err)
		notify("mic", "The host can't use your microphone: "+err.Error())
		drain(t)
		return
	}
	defer mic.Close()
	dec, err := audio.NewOpusDecoder()
	if err != nil {
		log.Printf("viewer microphone: %v", err)
		return
	}
	defer dec.Close()

	pcm := make([]int16, 5760*audio.Channels) // max Opus frame (120ms)
	var lastSeq uint16
	first := true
	for {
		pkt, _, err := t.ReadRTP()
		if err != nil {
			return
		}
		// Conceal a single lost packet; longer gaps are just skipped so
		// we never play stale audio.
		if !first && pkt.SequenceNumber == lastSeq+2 {
			if n, err := dec.Decode(nil, pcm); err == nil {
				_ = mic.Write(pcm[:n*audio.Channels])
			}
		}
		first, lastSeq = false, pkt.SequenceNumber
		n, err := dec.Decode(pkt.Payload, pcm)
		if err != nil {
			continue
		}
		if err := mic.Write(pcm[:n*audio.Channels]); err != nil {
			log.Printf("viewer microphone: %v", err)
			return
		}
	}
}

// receiveCamera decodes the viewer's VP8 webcam into a virtual camera.
func (s *Server) receiveCamera(pc *webrtc.PeerConnection, t *webrtc.TrackRemote, notify func(dev, msg string)) {
	cam, err := s.cfg.OpenCamera()
	if err != nil {
		log.Printf("viewer camera ignored: %v", err)
		notify("cam", "The host can't use your camera: "+err.Error())
		drain(t)
		return
	}
	defer cam.Close()
	dec, err := vpx.NewDecoder()
	if err != nil {
		log.Printf("viewer camera: %v", err)
		drain(t)
		return
	}
	defer dec.Close()

	pli := func() {
		_ = pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(t.SSRC())}})
	}
	pli()
	// Assemble frames from packets. A short reorder window keeps latency
	// low; frames with gaps are dropped and a keyframe requested.
	sb := samplebuilder.New(16, &codecs.VP8Packet{}, t.Codec().ClockRate)
	lastPLI := time.Now()
	for {
		pkt, _, err := t.ReadRTP()
		if err != nil {
			return
		}
		sb.Push(pkt)
		for sample := sb.Pop(); sample != nil; sample = sb.Pop() {
			if sample.PrevDroppedPackets > 0 && time.Since(lastPLI) > 500*time.Millisecond {
				pli()
				lastPLI = time.Now()
			}
			img, err := dec.Decode(sample.Data)
			if err != nil {
				if time.Since(lastPLI) > 500*time.Millisecond {
					pli()
					lastPLI = time.Now()
				}
				continue
			}
			if img == nil {
				continue
			}
			if err := cam.WriteFrame(img); err != nil {
				log.Printf("viewer camera: %v", err)
				return
			}
		}
	}
}

// drain discards a track's packets so its receive buffers don't fill.
func drain(t *webrtc.TrackRemote) {
	buf := make([]byte, 1500)
	for {
		if _, _, err := t.Read(buf); err != nil {
			return
		}
	}
}

// gamepadMsg is the "gamepad" data channel wire format, one per change:
//
//	{"i":0,"b":[...17 button values...],"a":[lx,ly,rx,ry]}
//	{"i":0,"gone":true}
type gamepadMsg struct {
	Index   int       `json:"i"`
	Buttons []float64 `json:"b"`
	Axes    []float64 `json:"a"`
	Gone    bool      `json:"gone"`
}

// gamepads holds one viewer's virtual controllers, keyed by the browser's
// gamepad index.
type gamepads struct {
	open   func() (vdev.Gamepad, error)
	notify func(dev, msg string)

	mu     sync.Mutex
	pads   map[int]vdev.Gamepad
	failed bool
}

const maxGamepads = 4

func (g *gamepads) handle(data []byte) {
	var m gamepadMsg
	if err := json.Unmarshal(data, &m); err != nil || m.Index < 0 || m.Index >= maxGamepads {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pads == nil {
		g.pads = map[int]vdev.Gamepad{}
	}
	pad := g.pads[m.Index]
	if m.Gone {
		if pad != nil {
			pad.Close()
			delete(g.pads, m.Index)
		}
		return
	}
	if pad == nil {
		if g.failed {
			return
		}
		var err error
		if pad, err = g.open(); err != nil {
			log.Printf("viewer gamepad ignored: %v", err)
			if g.notify != nil {
				g.notify("gamepad", "The host can't use your gamepad: "+err.Error())
			}
			g.failed = true // don't retry (and log) on every update
			return
		}
		g.pads[m.Index] = pad
	}
	var st vdev.GamepadState
	copy(st.Buttons[:], m.Buttons)
	copy(st.Axes[:], m.Axes)
	if err := pad.Update(st); err != nil {
		log.Printf("viewer gamepad: %v", err)
	}
}

func (g *gamepads) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, p := range g.pads {
		p.Close()
		delete(g.pads, i)
	}
}
