package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kevmo314/urdp/internal/capture"
	"github.com/pion/webrtc/v4"
)

func cursorClient(t *testing.T, url string) <-chan map[string]any {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	_, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	if err != nil {
		t.Fatal(err)
	}
	dc, err := pc.CreateDataChannel("input", nil)
	if err != nil {
		t.Fatal(err)
	}
	messages := make(chan map[string]any, 32)
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		var v map[string]any
		if json.Unmarshal(m.Data, &v) == nil {
			messages <- v
		}
	})
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	body, _ := json.Marshal(pc.LocalDescription())
	res, err := http.Post(url+"/api/offer?source=test", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
	var answer webrtc.SessionDescription
	if err := json.NewDecoder(res.Body).Decode(&answer); err != nil {
		t.Fatal(err)
	}
	if err := pc.SetRemoteDescription(answer); err != nil {
		t.Fatal(err)
	}
	return messages
}

func expectCursor(t *testing.T, messages <-chan map[string]any, want string) {
	t.Helper()
	var expected map[string]any
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-messages:
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(expected)
		if !bytes.Equal(a, b) {
			t.Fatalf("cursor: got %s, want %s", a, b)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no cursor received")
	}
}

func TestCursorBroadcastAndSessionCache(t *testing.T) {
	updates := make(chan capture.Cursor)
	done := make(chan struct{})
	defer close(done)
	var starts atomic.Int32
	_, ts := newTestServer(t, &fakeInjector{}, func(cfg *Config) {
		cfg.WatchCursor = func(ctx context.Context, fn func(capture.Cursor)) error {
			starts.Add(1)
			fn(capture.Cursor{Serial: 1, CSS: "default"})
			for {
				select {
				case c := <-updates:
					fn(c)
				case <-done:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	})
	if starts.Load() != 0 {
		t.Fatal("watcher started without viewers")
	}
	a := cursorClient(t, ts.URL)
	expectCursor(t, a, `{"t":"cursor","id":1,"css":"default"}`)
	// The bytes need only test JSON/base64 transport here, not PNG encoding.
	custom := capture.Cursor{Serial: 2, PNG: []byte{1, 2, 3}, HotX: 3, HotY: 4}
	updates <- custom
	expectCursor(t, a, `{"t":"cursor","id":2,"png":"AQID","x":3,"y":4}`)
	b := cursorClient(t, ts.URL)
	expectCursor(t, b, `{"t":"cursor","id":2,"png":"AQID","x":3,"y":4}`)
	updates <- capture.Cursor{Hidden: true}
	for _, ch := range []<-chan map[string]any{a, b} {
		expectCursor(t, ch, `{"t":"cursor","hidden":true}`)
	}
	c := cursorClient(t, ts.URL)
	expectCursor(t, c, `{"t":"cursor","hidden":true}`)
	updates <- custom
	for _, ch := range []<-chan map[string]any{a, b} {
		expectCursor(t, ch, `{"t":"cursor","id":2}`)
	}
	expectCursor(t, c, `{"t":"cursor","id":2,"png":"AQID","x":3,"y":4}`)
	updates <- capture.Cursor{Serial: 1, CSS: "default"}
	expectCursor(t, a, `{"t":"cursor","id":1}`)
	expectCursor(t, b, `{"t":"cursor","id":1,"css":"default"}`)
	expectCursor(t, c, `{"t":"cursor","id":1,"css":"default"}`)
	if starts.Load() != 1 {
		t.Fatalf("watcher started %d times", starts.Load())
	}
}
