package server

import (
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
	p  *pipeline
	pc *webrtc.PeerConnection

	mu   sync.Mutex
	disp *input.Dispatcher
	ctl  *webrtc.DataChannel

	// USB passthrough (usb.go); usb is nil unless running with -usb.
	usb        usbip.Attacher
	usbBridges []*usbip.Bridge
}

func (s *session) onDataChannel(dc *webrtc.DataChannel) {
	if dc.Label() == "usb" {
		s.onUSB(dc)
		return
	}
	if dc.Label() == "input" {
		s.mu.Lock()
		s.ctl = dc
		s.mu.Unlock()
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
	dc.OnClose(s.release)
}

// release lifts any keys or buttons this viewer still holds.
func (s *session) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disp != nil {
		s.disp.Release()
	}
}

func (s *session) send(msg string) {
	s.mu.Lock()
	dc := s.ctl
	s.mu.Unlock()
	if dc != nil {
		_ = dc.SendText(msg)
	}
}

func isKeyframeRequest(p rtcp.Packet) bool {
	switch p.(type) {
	case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
		return true
	}
	return false
}
