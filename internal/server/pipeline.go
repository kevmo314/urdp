package server

import (
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/kevmo314/urdp/internal/capture"
	"github.com/kevmo314/urdp/internal/encoder"
)

// pipeline captures and encodes one source and fans the result out to
// every viewer through a shared track.
type pipeline struct {
	id    string
	src   capture.Source
	track *webrtc.TrackLocalStaticSample
	opts  encoder.Options

	keyframe atomic.Bool
	done     chan struct{} // closed when the capture loop exits
	stop     chan struct{}
	refs     int // guarded by Server.mu

	statsMu sync.Mutex
	stats   pipelineStats
}

type pipelineStats struct {
	Width, Height int
	CaptureMs     float64
	EncodeMs      float64
	Bytes         int
}

func newPipeline(id string, src capture.Source, opts encoder.Options) (*pipeline, error) {
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
		"video", "urdp-"+id)
	if err != nil {
		return nil, err
	}
	p := &pipeline{
		id: id, src: src, track: track, opts: opts,
		done: make(chan struct{}), stop: make(chan struct{}),
	}
	go p.run()
	return p, nil
}

func (p *pipeline) run() {
	defer close(p.done)
	defer p.src.Close()

	var enc *encoder.Encoder
	defer func() {
		if enc != nil {
			enc.Close()
		}
	}()

	interval := time.Second / time.Duration(p.opts.FPS)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	start := time.Now()
	last := start

	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
		}

		t0 := time.Now()
		f, err := p.src.Capture()
		if errors.Is(err, capture.ErrNotFound) {
			log.Printf("source %s: window gone", p.id)
			return
		}
		if err != nil {
			log.Printf("source %s: capture: %v", p.id, err)
			continue
		}
		t1 := time.Now()

		if enc != nil {
			if ew, eh := enc.Size(); ew != f.Width&^1 || eh != f.Height&^1 {
				enc.Close()
				enc = nil
			}
		}
		if enc == nil {
			if enc, err = encoder.New(f.Width, f.Height, p.opts); err != nil {
				log.Printf("source %s: %v", p.id, err)
				enc = nil
				continue
			}
			p.keyframe.Store(true)
		}

		data, _, err := enc.Encode(f, t0.Sub(start).Milliseconds(), p.keyframe.Swap(false))
		if err != nil {
			log.Printf("source %s: encode: %v", p.id, err)
			continue
		}
		t2 := time.Now()
		if len(data) == 0 {
			continue
		}
		if err := p.track.WriteSample(media.Sample{Data: data, Duration: t0.Sub(last)}); err != nil {
			log.Printf("source %s: write: %v", p.id, err)
		}
		last = t0

		ew, eh := enc.Size()
		p.statsMu.Lock()
		p.stats = pipelineStats{
			Width: ew, Height: eh,
			CaptureMs: float64(t1.Sub(t0).Microseconds()) / 1000,
			EncodeMs:  float64(t2.Sub(t1).Microseconds()) / 1000,
			Bytes:     len(data),
		}
		p.statsMu.Unlock()
	}
}

func (p *pipeline) Stats() pipelineStats {
	p.statsMu.Lock()
	defer p.statsMu.Unlock()
	return p.stats
}

func (p *pipeline) Close() {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	<-p.done
}
