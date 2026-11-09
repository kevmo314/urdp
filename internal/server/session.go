package server

import (
	"encoding/json"
	"log"
	"sync"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"

	"github.com/kevmo314/urdp/internal/input"
	"github.com/kevmo314/urdp/internal/usbip"
)

// session is one viewer. Input arrives on two data channels: "move"
// (unordered, no retransmits, so a lost pointer position is simply
// superseded by the next) and "input" (reliable, for buttons and keys).
type session struct {
	reportMu sync.Mutex
	report   json.RawMessage

	server       *Server
	cursorIDs    map[uint64]bool
	cursorClosed bool // protected by server.cursorMu

	p  *pipeline
	pc *webrtc.PeerConnection

	mu   sync.Mutex
	disp *input.Dispatcher
	ctl  *webrtc.DataChannel
	pads *gamepads

	// USB passthrough (usb.go); usb is nil unless running with -usb.
	usb        usbip.Attacher
	usbBridges []*usbip.Bridge
}

func (s *session) onDataChannel(dc *webrtc.DataChannel) {
	if dc.Label() == "gamepad" {
		dc.OnMessage(func(m webrtc.DataChannelMessage) { s.pads.handle(m.Data) })
		return
	}
	if dc.Label() == "usb" {
		s.onUSB(dc)
		return
	}
	if dc.Label() == "stats" {
		// The viewer's own timing (jitter buffers, playout delay), shown
		// in /api/stats for diagnosing latency it alone can measure.
		dc.OnMessage(func(m webrtc.DataChannelMessage) {
			if len(m.Data) <= 4096 && json.Valid(m.Data) {
				s.reportMu.Lock()
				s.report = append(json.RawMessage(nil), m.Data...)
				s.reportMu.Unlock()
			}
		})
		return
	}
	if dc.Label() == "input" {
		s.mu.Lock()
		s.ctl = dc
		s.mu.Unlock()
		dc.OnOpen(func() {
			if s.server != nil {
				s.server.addCursorSession(s)
			}
		})
		dc.OnClose(func() {
			if s.server != nil {
				s.server.removeCursorSession(s)
			}
			s.release()
		})
	}
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.disp == nil {
			return
		}
		if err := s.disp.Handle(m.Data); err != nil {
			log.Printf("input: %v", err)
		}
	})
	if dc.Label() != "input" {
		dc.OnClose(s.release)
	}
}

// release lifts any keys or buttons this viewer still holds.
func (s *session) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disp != nil {
		s.disp.Release()
	}
}

// notify tells the viewer why one of their devices isn't being used.
func (s *session) notify(dev, msg string) {
	b, _ := json.Marshal(map[string]string{"t": "notice", "dev": dev, "msg": msg})
	s.send(string(b))
}

func (s *session) send(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.sendLocked(msg)
}

func (s *session) sendLocked(msg string) error {
	if s.ctl == nil {
		return webrtc.ErrDataChannelNotOpen
	}
	return s.ctl.SendText(msg)
}

func isKeyframeRequest(p rtcp.Packet) bool {
	switch p.(type) {
	case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
		return true
	}
	return false
}
