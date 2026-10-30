package server

// USB passthrough (experimental, off unless urdp runs with -usb): each
// "usb" data channel carries one WebUSB device from the viewer's browser,
// which internal/usbip attaches to this host as a real USB device.

import (
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/kevmo314/urdp/internal/usbip"
)

var usbBusSeq atomic.Uint32

func (s *session) onUSB(dc *webrtc.DataChannel) {
	if s.usb == nil {
		dc.OnOpen(func() {
			usbip.Reject(dc.Send, "USB passthrough is not enabled on this server (start urdp with -usb on a Linux or Windows host)")
			time.AfterFunc(time.Second, func() { _ = dc.Close() })
		})
		return
	}
	b := usbip.NewBridge(s.usb, fmt.Sprintf("1-%d", usbBusSeq.Add(1)), dc.Send, func() { _ = dc.Close() })
	b.Logf = log.Printf
	s.mu.Lock()
	s.usbBridges = append(s.usbBridges, b)
	s.mu.Unlock()
	dc.OnOpen(b.Open)
	dc.OnMessage(func(m webrtc.DataChannelMessage) { b.HandleMessage(m.Data) })
	dc.OnClose(b.Close)
}

// closeUSB detaches every device this viewer attached.
func (s *session) closeUSB() {
	s.mu.Lock()
	bs := s.usbBridges
	s.usbBridges = nil
	s.mu.Unlock()
	for _, b := range bs {
		b.Close()
	}
}
