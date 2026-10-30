// Package server serves the web client and negotiates WebRTC sessions
// that stream a capture source and accept input.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"

	"github.com/kevmo314/urdp/internal/audio"
	"github.com/kevmo314/urdp/internal/capture"
	"github.com/kevmo314/urdp/internal/encoder"
	"github.com/kevmo314/urdp/internal/input"
)

type Config struct {
	WebFS   fs.FS
	Encoder encoder.Options
	// AudioKbps is the Opus bitrate; 0 disables audio.
	AudioKbps int
	// ICEServers are passed to the peer connection (e.g. STUN for NAT).
	ICEServers []webrtc.ICEServer
	// NAT1To1IPs advertises these IPs as host candidates, for hosts
	// behind 1:1 NAT (e.g. WSL2, containers).
	NAT1To1IPs []string

	// Hooks for tests and alternative platforms; nil means the defaults.
	OpenSource  func(id string) (capture.Source, error)
	ListWindows func() ([]capture.Window, error)
	NewInjector func() (input.Injector, error)
	OpenAudio   func() (audio.Source, error)
}

type Server struct {
	cfg Config
	api *webrtc.API
	mux *http.ServeMux

	mu        sync.Mutex
	pipelines map[string]*pipeline

	injOnce sync.Once
	inj     input.Injector

	audioOnce  sync.Once
	audioTrack *webrtc.TrackLocalStaticSample
}

func New(cfg Config) (*Server, error) {
	if cfg.OpenSource == nil {
		cfg.OpenSource = capture.Open
	}
	if cfg.ListWindows == nil {
		cfg.ListWindows = capture.List
	}
	if cfg.NewInjector == nil {
		cfg.NewInjector = input.New
	}
	if cfg.OpenAudio == nil {
		cfg.OpenAudio = audio.Open
	}
	if cfg.Encoder.FPS <= 0 {
		cfg.Encoder.FPS = 60
	}

	api, err := newAPI(cfg)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, api: api, mux: http.NewServeMux(), pipelines: map[string]*pipeline{}}

	s.mux.HandleFunc("GET /api/windows", s.handleWindows)
	s.mux.HandleFunc("POST /api/offer", s.handleOffer)
	s.mux.HandleFunc("GET /api/stats", s.handleStats)
	if cfg.WebFS != nil {
		static := http.FileServerFS(cfg.WebFS)
		s.mux.Handle("GET /assets/", static)
		// Every other path is a source id rendered by the same page:
		// "/" is the desktop, "/0x1a00007" a window.
		s.mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, cfg.WebFS, "index.html")
		})
	}
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func newAPI(cfg Config) (*webrtc.API, error) {
	m := &webrtc.MediaEngine{}
	fb := []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "ccm", Parameter: "fir"}, {Type: "goog-remb"}, {Type: "transport-cc"}}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
			SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
			RTCPFeedback: fb,
		},
		PayloadType: 102,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
			SDPFmtpLine: "minptime=10;useinbandfec=1;stereo=1",
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	if err := m.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: PlayoutDelayURI}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}

	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, ir); err != nil {
		return nil, err
	}
	ir.Add(playoutDelayFactory{})

	se := webrtc.SettingEngine{}
	// Lets a browser on the same machine connect over loopback, which also
	// works across WSL2/VM boundaries where host-IP UDP is firewalled.
	se.SetIncludeLoopbackCandidate(true)
	if len(cfg.NAT1To1IPs) > 0 {
		se.SetNAT1To1IPs(cfg.NAT1To1IPs, webrtc.ICECandidateTypeHost)
	}
	return webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir), webrtc.WithSettingEngine(se)), nil
}

func (s *Server) handleWindows(w http.ResponseWriter, r *http.Request) {
	wins, err := s.cfg.ListWindows()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type win struct {
		capture.Window
		Path string `json:"path"`
	}
	out := make([]win, 0, len(wins))
	for _, x := range wins {
		out = append(out, win{x, "/" + capture.FormatID(x.ID)})
	}
	writeJSON(w, out)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	out := map[string]any{}
	for id, p := range s.pipelines {
		out[id] = map[string]any{"viewers": p.refs, "stats": p.Stats()}
	}
	s.mu.Unlock()
	writeJSON(w, out)
}

// acquire returns the running pipeline for id, starting one if needed.
func (s *Server) acquire(id string) (*pipeline, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.pipelines[id]; ok {
		select {
		case <-p.done:
			delete(s.pipelines, id) // source died; start fresh
		default:
			p.refs++
			p.keyframe.Store(true) // new viewer needs an IDR to start decoding
			return p, nil
		}
	}
	src, err := s.cfg.OpenSource(id)
	if err != nil {
		return nil, err
	}
	p, err := newPipeline(id, src, s.cfg.Encoder)
	if err != nil {
		src.Close()
		return nil, err
	}
	p.refs = 1
	s.pipelines[id] = p
	return p, nil
}

func (s *Server) release(p *pipeline) {
	s.mu.Lock()
	p.refs--
	last := p.refs == 0
	if last && s.pipelines[p.id] == p {
		delete(s.pipelines, p.id)
	}
	s.mu.Unlock()
	if last {
		p.Close()
	}
}

func (s *Server) injector() input.Injector {
	s.injOnce.Do(func() {
		inj, err := s.cfg.NewInjector()
		if err != nil {
			log.Printf("input disabled: %v", err)
			return
		}
		s.inj = inj
	})
	return s.inj
}

func (s *Server) audio() *webrtc.TrackLocalStaticSample {
	s.audioOnce.Do(func() {
		if s.cfg.AudioKbps <= 0 {
			return
		}
		t, err := startAudio(s.cfg.OpenAudio, s.cfg.AudioKbps*1000)
		if err != nil {
			log.Printf("audio disabled: %v", err)
			return
		}
		s.audioTrack = t
	})
	return s.audioTrack
}

func (s *Server) handleOffer(w http.ResponseWriter, r *http.Request) {
	var offer webrtc.SessionDescription
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&offer); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id := strings.Trim(r.URL.Query().Get("source"), "/")
	answer, err := s.negotiate(id, offer)
	if errors.Is(err, capture.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, answer)
}

// negotiate builds a peer connection for source id and returns the
// complete answer (non-trickle: candidates are embedded in the SDP).
func (s *Server) negotiate(id string, offer webrtc.SessionDescription) (*webrtc.SessionDescription, error) {
	p, err := s.acquire(id)
	if err != nil {
		return nil, fmt.Errorf("open source %q: %w", id, err)
	}
	pc, err := s.api.NewPeerConnection(webrtc.Configuration{ICEServers: s.cfg.ICEServers})
	if err != nil {
		s.release(p)
		return nil, err
	}
	var closeOnce sync.Once
	cleanup := func() {
		closeOnce.Do(func() {
			s.release(p)
			_ = pc.Close()
		})
	}
	fail := func(err error) (*webrtc.SessionDescription, error) { cleanup(); return nil, err }

	sender, err := pc.AddTrack(p.track)
	if err != nil {
		return fail(err)
	}
	go readRTCP(sender, p)
	if at := s.audio(); at != nil {
		as, err := pc.AddTrack(at)
		if err != nil {
			return fail(err)
		}
		go readRTCP(as, nil)
	}

	sess := &session{p: p, pc: pc}
	if inj := s.injector(); inj != nil {
		sess.disp = &input.Dispatcher{Inj: inj, Bounds: p.src.Bounds, WindowID: p.src.WindowID()}
	}
	pc.OnDataChannel(sess.onDataChannel)
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		log.Printf("peer %p source %q: %s", pc, id, st)
		switch st {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			sess.release()
			cleanup()
		}
	})
	go func() {
		// Tear down viewers when the source disappears (window closed).
		<-p.done
		sess.send(`{"t":"ended"}`)
		time.Sleep(100 * time.Millisecond)
		cleanup()
	}()

	if err := pc.SetRemoteDescription(offer); err != nil {
		return fail(err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return fail(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		return fail(err)
	}
	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
		return fail(errors.New("ICE gathering timed out"))
	}
	return pc.LocalDescription(), nil
}

// readRTCP drains RTCP (required for interceptors to run) and turns
// keyframe requests into IDR frames.
func readRTCP(sender *webrtc.RTPSender, p *pipeline) {
	for {
		pkts, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}
		if p == nil {
			continue
		}
		for _, pkt := range pkts {
			if isKeyframeRequest(pkt) {
				p.keyframe.Store(true)
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
