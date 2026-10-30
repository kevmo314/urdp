package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/kevmo314/urdp/internal/usbip"
)

type pipeAttacher struct{ host chan net.Conn }

type pipeAttachment struct{ net.Conn }

func (pipeAttachment) String() string { return "test port" }

func (p *pipeAttacher) Attach(*usbip.Device) (usbip.Attachment, error) {
	a, b := net.Pipe()
	p.host <- b
	return pipeAttachment{a}, nil
}

// usbClient connects a viewer that opens a "usb" channel and returns the
// channel and its reassembled messages.
func usbClient(t *testing.T, ts string) (*webrtc.PeerConnection, *webrtc.DataChannel, chan []byte) {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	dc, err := pc.CreateDataChannel("usb", nil)
	if err != nil {
		t.Fatal(err)
	}
	msgs := make(chan []byte, 16)
	var f usbip.Framer
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		_ = f.Feed(m.Data, func(b []byte) { msgs <- b })
	})
	offer, _ := pc.CreateOffer(nil)
	done := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-done
	body, _ := json.Marshal(pc.LocalDescription())
	res, err := http.Post(ts+"/api/offer?source=test", "application/json", bytes.NewReader(body))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("offer: %v %v", err, res)
	}
	var answer webrtc.SessionDescription
	if err := json.NewDecoder(res.Body).Decode(&answer); err != nil {
		t.Fatal(err)
	}
	if err := pc.SetRemoteDescription(answer); err != nil {
		t.Fatal(err)
	}
	return pc, dc, msgs
}

func nextMsg(t *testing.T, msgs chan []byte) []byte {
	t.Helper()
	select {
	case m := <-msgs:
		return m
	case <-time.After(10 * time.Second):
		t.Fatal("no message on usb channel")
	}
	return nil
}

func TestUSBDisabled(t *testing.T) {
	_, ts := newTestServer(t, &fakeInjector{})
	_, _, msgs := usbClient(t, ts.URL)
	m := nextMsg(t, msgs)
	if m[0] != usbip.MsgStatus || m[1] != usbip.StatusError || !bytes.Contains(m, []byte("-usb")) {
		t.Fatalf("got %q", m)
	}
}

func TestUSBAttachDetach(t *testing.T) {
	att := &pipeAttacher{host: make(chan net.Conn, 1)}
	_, ts := newTestServer(t, &fakeInjector{}, func(c *Config) { c.USB = att })
	pc, dc, msgs := usbClient(t, ts.URL)
	if m := nextMsg(t, msgs); m[0] != usbip.MsgStatus || m[1] != usbip.StatusReady {
		t.Fatalf("got %q", m)
	}
	hello := usbip.Hello{
		ActiveConfig: 1,
		Device:       []byte{18, 1, 0, 2, 0xff, 0, 0, 64, 0x09, 0x12, 0x01, 0x00, 0, 1, 0, 0, 0, 1},
		Configs:      [][]byte{{9, 2, 18, 0, 1, 1, 0, 0x80, 50, 9, 4, 0, 0, 0, 0xff, 0, 0, 0}},
	}
	for _, c := range usbip.Frame(hello.Marshal()) {
		if err := dc.Send(c); err != nil {
			t.Fatal(err)
		}
	}
	var host net.Conn
	select {
	case host = <-att.host:
	case <-time.After(10 * time.Second):
		t.Fatal("not attached")
	}
	if m := nextMsg(t, msgs); m[1] != usbip.StatusAttached || string(m[2:]) != "test port" {
		t.Fatalf("got %q", m)
	}
	// The viewer leaving detaches the device.
	pc.Close()
	errc := make(chan error, 1)
	go func() { _, err := io.ReadAll(host); errc <- err }()
	select {
	case <-errc:
	case <-time.After(15 * time.Second):
		t.Fatal("device not detached after the viewer left")
	}
}
