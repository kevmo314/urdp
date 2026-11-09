package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/kevmo314/urdp/internal/encoder"
	"github.com/pion/webrtc/v4"
)

// Set URDP_TEST_SERVER_URL to exercise a separately running Mac server.
// Otherwise this starts the production HTTP handler with a synthetic source.
func TestVideoToolboxWebRTC(t *testing.T) {
	for _, codec := range []encoder.Codec{encoder.H264, encoder.H265} {
		t.Run(string(codec), func(t *testing.T) {
			backend := "vt-h264"
			if codec == encoder.H265 {
				backend = "vt-hevc"
			}
			found := false
			for _, b := range encoder.Backends(codec) {
				if b.Name == backend {
					found = true
				}
			}
			if !found {
				t.Skip("VideoToolbox hardware unavailable")
			}
			url := os.Getenv("URDP_TEST_SERVER_URL")
			if url == "" {
				_, ts := newTestServer(t, &fakeInjector{}, func(c *Config) { c.Encoder.Backend = backend })
				url = ts.URL
			}
			m := &webrtc.MediaEngine{}
			v := lookupCodec(codec)
			if err := m.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: v.mime, ClockRate: 90000, SDPFmtpLine: v.fmtp}, PayloadType: v.pt}, webrtc.RTPCodecTypeVideo); err != nil {
				t.Fatal(err)
			}
			pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(m)).NewPeerConnection(webrtc.Configuration{})
			if err != nil {
				t.Fatal(err)
			}
			defer pc.Close()
			if _, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
				t.Fatal(err)
			}
			if _, err = pc.CreateDataChannel("input", nil); err != nil {
				t.Fatal(err)
			}
			received := make(chan string, 1)
			pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
				packets, frames := 0, 0
				var previous uint32
				for {
					pkt, _, err := tr.ReadRTP()
					if err != nil {
						return
					}
					packets++
					if pkt.Marker {
						if frames > 0 && pkt.Timestamp == previous {
							received <- "duplicate frame timestamp"
							return
						}
						previous = pkt.Timestamp
						frames++
						if frames == 5 {
							received <- fmt.Sprintf("%s: %d packets, %d frames", tr.Codec().MimeType, packets, frames)
							return
						}
					}
				}
			})
			offer, err := pc.CreateOffer(nil)
			if err != nil {
				t.Fatal(err)
			}
			gathered := webrtc.GatheringCompletePromise(pc)
			if err = pc.SetLocalDescription(offer); err != nil {
				t.Fatal(err)
			}
			<-gathered
			body, err := json.Marshal(pc.LocalDescription())
			if err != nil {
				t.Fatal(err)
			}
			res, err := http.Post(url+"/api/offer?source=test&codec="+string(codec), "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != 200 {
				t.Fatalf("offer: %s", res.Status)
			}
			var answer webrtc.SessionDescription
			if err = json.NewDecoder(res.Body).Decode(&answer); err != nil {
				t.Fatal(err)
			}
			if err = pc.SetRemoteDescription(answer); err != nil {
				t.Fatal(err)
			}
			select {
			case message := <-received:
				if message == "duplicate frame timestamp" {
					t.Fatal(message)
				}
				t.Log(message)
			case <-time.After(15 * time.Second):
				t.Fatal("no video packets")
			}
		})
	}
}
