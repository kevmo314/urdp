package server

import (
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor/pkg/cc"
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

	viewersMu sync.Mutex
	viewers   map[*session]*viewerRate
}

// minBitrate is the floor congestion control may push the encoder to.
const minBitrate = 500_000

type viewerRate struct {
	bwe cc.BandwidthEstimator
	rc  *rateControl
}

func (p *pipeline) addViewer(s *session, bwe cc.BandwidthEstimator) {
	p.viewersMu.Lock()
	defer p.viewersMu.Unlock()
	p.viewers[s] = &viewerRate{bwe: bwe, rc: newRateControl(minBitrate, p.opts.BitrateKbps*1000)}
}

func (p *pipeline) removeViewer(s *session) {
	p.viewersMu.Lock()
	defer p.viewersMu.Unlock()
	delete(p.viewers, s)
}

// viewerStats reports each viewer's congestion-control state.
func (p *pipeline) viewerStats() []map[string]any {
	p.viewersMu.Lock()
	defer p.viewersMu.Unlock()
	out := []map[string]any{}
	for _, v := range p.viewers {
		st := v.bwe.GetStats()
		st["targetBitrate"] = v.bwe.GetTargetBitrate()
		st["cap"] = v.rc.cap
		out = append(out, st)
	}
	return out
}

// targetKbps is the bitrate the slowest viewer can currently take, given
// the rate recently sent; all viewers share one encoder.
func (p *pipeline) targetKbps(sentBps int) int {
	p.viewersMu.Lock()
	defer p.viewersMu.Unlock()
	target := p.opts.BitrateKbps * 1000
	for _, v := range p.viewers {
		st := v.bwe.GetStats()
		loss, _ := st["averageLoss"].(float64)
		target = min(target, v.rc.update(v.bwe.GetTargetBitrate(), sentBps, st["usage"] == "overuse", loss))
	}
	return target / 1000
}

type pipelineStats struct {
	Width, Height int
	CaptureMs     float64
	EncodeMs      float64
	Bytes         int
	BitrateKbps   int
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
		viewers: map[*session]*viewerRate{},
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
	timer := time.NewTimer(0)
	defer timer.Stop()
	start := time.Now()
	last := start
	kbps := p.opts.BitrateKbps
	var nextRate time.Time
	var sentBytes int // since nextRate was last set

	for {
		// Pace to the frame-rate cap. Sources that block in Capture until
		// the screen updates hand frames over the moment they're presented,
		// so this only throttles, it never adds a fixed polling delay.
		select {
		case <-p.stop:
			return
		case <-timer.C:
		}
		t0 := time.Now()
		timer.Reset(interval)

		f, changed, err := p.src.Capture(interval)
		if errors.Is(err, capture.ErrNotFound) {
			log.Printf("source %s: window gone", p.id)
			return
		}
		if err != nil {
			log.Printf("source %s: capture: %v", p.id, err)
			continue
		}
		// Nothing new on screen: send nothing rather than re-encode an
		// identical picture, unless a viewer needs a keyframe.
		if !changed && !p.keyframe.Load() {
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
			kbps = p.opts.BitrateKbps
		}
		if t1.After(nextRate) {
			const window = 250 * time.Millisecond
			sent := int(float64(sentBytes*8) / window.Seconds())
			nextRate, sentBytes = t1.Add(window), 0
			// Only reconfigure on meaningful changes; it isn't free.
			if k := p.targetKbps(sent); k < kbps*9/10 || k > kbps*11/10 {
				if err := enc.SetBitrate(k); err == nil {
					kbps = k
				}
			}
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
		sentBytes += len(data)
		if err := p.track.WriteSample(media.Sample{Data: data, Duration: t0.Sub(last)}); err != nil {
			log.Printf("source %s: write: %v", p.id, err)
		}
		last = t0

		ew, eh := enc.Size()
		p.statsMu.Lock()
		p.stats = pipelineStats{
			Width: ew, Height: eh,
			CaptureMs:   float64(t1.Sub(t0).Microseconds()) / 1000,
			EncodeMs:    float64(t2.Sub(t1).Microseconds()) / 1000,
			Bytes:       len(data),
			BitrateKbps: kbps,
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
