package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"

	"github.com/kevmo314/urdp/internal/audio"
	"github.com/kevmo314/urdp/internal/capture"
	"github.com/kevmo314/urdp/internal/encoder"
	"github.com/kevmo314/urdp/internal/input"
)

type fakeInjector struct {
	mu    sync.Mutex
	moves []image.Point
	keys  []input.Key
}

func (f *fakeInjector) MouseMove(x, y int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moves = append(f.moves, image.Pt(x, y))
	return nil
}
func (f *fakeInjector) MouseButton(int, bool) error { return nil }
func (f *fakeInjector) Wheel(int, int) error        { return nil }
func (f *fakeInjector) Key(k input.Key, down bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, k)
	return nil
}
func (f *fakeInjector) Focus(uint64) error { return nil }
func (f *fakeInjector) Close() error       { return nil }

// toneSource yields a real-time paced 440Hz tone.
type toneSource struct{ n int }

func (s *toneSource) Read(buf []int16) error {
	time.Sleep(audio.FrameMs * time.Millisecond)
	for i := 0; i < len(buf); i += audio.Channels {
		v := int16(8000 * math.Sin(2*math.Pi*440*float64(s.n)/audio.SampleRate))
		buf[i], buf[i+1] = v, v
		s.n++
	}
	return nil
}
func (s *toneSource) Close() error { return nil }

func newTestServer(t *testing.T, inj *fakeInjector, opts ...func(*Config)) (*Server, *httptest.Server) {
	t.Helper()
	cfg := Config{
		Encoder:    encoder.Options{FPS: 30},
		OpenSource: func(string) (capture.Source, error) { return capture.NewTestSource(320, 240), nil },
		ListWindows: func() ([]capture.Window, error) {
			return []capture.Window{{ID: 0x1234, Title: "x", Width: 1, Height: 1}}, nil
		},
		NewInjector: func() (input.Injector, error) { return inj, nil },
		OpenAudio:   func() (audio.Source, error) { return nil, errors.New("no audio in tests") },
	}
	for _, o := range opts {
		o(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return s, ts
}

func TestStreamAndInput(t *testing.T) {
	inj := &fakeInjector{}
	s, ts := newTestServer(t, inj)

	// Mirror Chrome, which offers the playout-delay extension.
	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: PlayoutDelayURI}, webrtc.RTPCodecTypeVideo); err != nil {
		t.Fatal(err)
	}
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(m)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	dc, err := pc.CreateDataChannel("input", nil)
	if err != nil {
		t.Fatal(err)
	}

	gotIDR := make(chan struct{})
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		var h264 codecs.H264Packet
		var frame []byte
		for {
			pkt, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			b, err := h264.Unmarshal(pkt.Payload)
			if err != nil {
				continue
			}
			frame = append(frame, b...)
			if pkt.Marker {
				if bytes.Contains(frame, []byte{0, 0, 1, 0x65}) || bytes.Contains(frame, []byte{0, 0, 0, 1, 0x65}) {
					close(gotIDR)
					return
				}
				frame = frame[:0]
			}
		}
	})
	dcOpen := make(chan struct{})
	dc.OnOpen(func() { close(dcOpen) })

	offer, _ := pc.CreateOffer(nil)
	done := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-done
	body, _ := json.Marshal(pc.LocalDescription())
	res, err := http.Post(ts.URL+"/api/offer?source=test", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("offer: %s", res.Status)
	}
	var answer webrtc.SessionDescription
	if err := json.NewDecoder(res.Body).Decode(&answer); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains([]byte(answer.SDP), []byte(PlayoutDelayURI)) {
		t.Error("answer does not negotiate playout-delay extension")
	}
	if err := pc.SetRemoteDescription(answer); err != nil {
		t.Fatal(err)
	}

	select {
	case <-gotIDR:
	case <-time.After(10 * time.Second):
		t.Fatal("no IDR frame received")
	}
	select {
	case <-dcOpen:
	case <-time.After(5 * time.Second):
		t.Fatal("data channel did not open")
	}
	dc.SendText(`{"t":"m","x":1,"y":1}`)
	dc.SendText(`{"t":"kd","c":"KeyB"}`)
	deadline := time.Now().Add(5 * time.Second)
	for {
		inj.mu.Lock()
		n, k := len(inj.moves), len(inj.keys)
		var last image.Point
		if n > 0 {
			last = inj.moves[n-1]
		}
		inj.mu.Unlock()
		if n > 0 && k > 0 {
			if last != image.Pt(319, 239) {
				t.Errorf("move = %v, want (319,239)", last)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("input not delivered")
		}
		time.Sleep(10 * time.Millisecond)
	}

	s.mu.Lock()
	viewers := s.pipelines["test"].refs
	s.mu.Unlock()
	if viewers != 1 {
		t.Errorf("viewers = %d, want 1", viewers)
	}
	pc.Close()
	deadline = time.Now().Add(10 * time.Second)
	for {
		s.mu.Lock()
		n := len(s.pipelines)
		s.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pipeline not released after viewer left")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestWindowsAPI(t *testing.T) {
	_, ts := newTestServer(t, &fakeInjector{})
	res, err := http.Get(ts.URL + "/api/windows")
	if err != nil {
		t.Fatal(err)
	}
	var wins []struct {
		ID   uint64 `json:"id"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(res.Body).Decode(&wins); err != nil {
		t.Fatal(err)
	}
	if len(wins) != 1 || wins[0].Path != "/0x1234" {
		t.Fatalf("windows = %+v", wins)
	}
}

func TestAudio(t *testing.T) {
	_, ts := newTestServer(t, &fakeInjector{}, func(c *Config) {
		c.AudioKbps = 64
		c.OpenAudio = func() (audio.Source, error) { return &toneSource{}, nil }
	})
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	for _, k := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		if _, err := pc.AddTransceiverFromKind(k, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			t.Fatal(err)
		}
	}
	got := make(chan int, 1)
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if tr.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		n := 0
		for {
			pkt, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			// A 440Hz tone needs real payload, not Opus DTX/silence frames.
			if len(pkt.Payload) > 20 {
				n++
			}
			if n == 50 {
				got <- int(tr.Codec().ClockRate)
				return
			}
		}
	})
	offer, _ := pc.CreateOffer(nil)
	done := webrtc.GatheringCompletePromise(pc)
	pc.SetLocalDescription(offer)
	<-done
	body, _ := json.Marshal(pc.LocalDescription())
	res, err := http.Post(ts.URL+"/api/offer", "application/json", bytes.NewReader(body))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("offer: %v %v", err, res.Status)
	}
	var answer webrtc.SessionDescription
	json.NewDecoder(res.Body).Decode(&answer)
	if err := pc.SetRemoteDescription(answer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("no audio received")
	}
}
