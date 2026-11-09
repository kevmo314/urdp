package server

import (
	"errors"
	"log"
	"math"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/kevmo314/urdp/internal/capture"
	"github.com/kevmo314/urdp/internal/encoder"
)

// pipeline captures and encodes one source and fans the result out to
// every viewer through a shared track.
type pipeline struct {
	id    string
	src   capture.Source
	codec encoder.Codec
	track *webrtc.TrackLocalStaticRTP
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
	for s, v := range p.viewers {
		st := v.bwe.GetStats()
		st["targetBitrate"] = v.bwe.GetTargetBitrate()
		st["cap"] = v.rc.cap
		s.reportMu.Lock()
		if s.report != nil {
			st["viewer"] = s.report
		}
		s.reportMu.Unlock()
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
	SendMs        float64
	Bytes         int
	BitrateKbps   int
	// Per second, over the last second: what happened to each capture.
	Rates frameCounts
}

// frameCounts tallies the fate of each pass through the pipeline loop.
type frameCounts struct {
	Captures, Unchanged, CaptureErrors, EncodeErrors, Empty, Sent int
	// Spread of the time between sent frames' timestamps, in ms.
	SendIntervalMs, SendIntervalStdMs float64
	sum, sq                           float64
}

func newPipeline(id string, c encoder.Codec, src capture.Source, opts encoder.Options) (*pipeline, error) {
	v := lookupCodec(c)
	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: v.mime, ClockRate: 90000, SDPFmtpLine: v.fmtp},
		"video", "urdp-"+id)
	if err != nil {
		return nil, err
	}
	p := &pipeline{
		id: id, codec: c, src: src, track: track, opts: opts,
		done: make(chan struct{}), stop: make(chan struct{}),
		viewers: map[*session]*viewerRate{},
	}
	go p.run()
	return p, nil
}

func (p *pipeline) run() {
	defer close(p.done)
	defer p.src.Close()

	var enc encoder.Encoder
	var lastOpenErr string
	defer func() {
		if enc != nil {
			enc.Close()
		}
	}()

	interval := time.Second / time.Duration(p.opts.FPS)
	timer := time.NewTimer(0)
	defer timer.Stop()
	start := time.Now()
	kbps := p.opts.BitrateKbps
	lastRate := start
	sentBytes := 0 // since lastRate

	// Packetize here rather than via TrackLocalStaticSample so each frame
	// carries its own capture time as RTP timestamp. (The sample track
	// advances the timestamp after a frame, which shifts idle gaps onto
	// the wrong frame and inflates the receiver's jitter estimate.)
	packetizer := rtp.NewPacketizer(1200, 0, 0, lookupCodec(p.codec).payloader(), rtp.NewRandomSequencer(), 90000)
	tsBase := rand.Uint32()
	var counts, rates frameCounts
	var lastStamp time.Time
	countStart := time.Now()
	tally := func() {
		if time.Since(countStart) >= time.Second {
			if n := float64(counts.Sent - 1); n > 0 {
				mean := counts.sum / n
				counts.SendIntervalMs = mean
				counts.SendIntervalStdMs = math.Sqrt(max(0, counts.sq/n-mean*mean))
			}
			rates, counts, countStart = counts, frameCounts{}, time.Now()
			p.statsMu.Lock()
			p.stats.Rates = rates
			p.statsMu.Unlock()
		}
	}

	for {
		select {
		case <-p.stop:
			return
		case <-timer.C:
		}

		// Sources that block in Capture until the screen updates hand
		// frames over the moment they're presented; the timer below only
		// enforces the frame-rate cap after a frame has been produced.
		tally()
		captureStart := time.Now()
		f, changed, err := p.src.Capture(interval)
		t0 := time.Now()
		counts.Captures++
		if errors.Is(err, capture.ErrNotFound) {
			log.Printf("source %s: window gone", p.id)
			return
		}
		if err != nil {
			log.Printf("source %s: capture: %v", p.id, err)
			counts.CaptureErrors++
			timer.Reset(interval)
			continue
		}
		// Nothing new on screen: send nothing rather than re-encode an
		// identical picture, unless a viewer needs a keyframe.
		if !changed && !p.keyframe.Load() {
			counts.Unchanged++
			timer.Reset(0)
			continue
		}
		// Space frames at least one interval apart, measured from when this
		// frame was grabbed: the capture start, unless Capture spent a
		// while waiting for the screen to change (allow a quarter interval
		// for the grab itself).
		grabbed := captureStart
		if g := t0.Add(-interval / 4); g.After(grabbed) {
			grabbed = g
		}
		timer.Reset(interval - time.Since(grabbed))
		t1 := time.Now()

		if enc != nil {
			if ew, eh := enc.Size(); ew != f.Width&^1 || eh != f.Height&^1 {
				enc.Close()
				enc = nil
			}
		}
		if enc == nil {
			opts := p.opts
			opts.BitrateKbps = kbps // keep the congestion-controlled cap
			// Offer the capture's GPU device so a hardware encoder can take
			// frames without a round trip through the CPU.
			if d, ok := p.src.(interface{ GPUDevice() any }); ok {
				opts.GPUDevice = d.GPUDevice()
			}
			if enc, err = encoder.New(p.codec, f.Width, f.Height, opts); err != nil {
				// Usually permanent (no backend for this codec at this
				// size): say so once and retry slowly, e.g. after a resize.
				if msg := err.Error(); msg != lastOpenErr {
					log.Printf("source %s: %v", p.id, err)
					lastOpenErr = msg
				}
				enc = nil
				timer.Reset(time.Second)
				continue
			}
			lastOpenErr = ""
			// Let capture hand over GPU frames (zero-copy) only once the
			// backend that will take them is open, so a software fallback
			// keeps getting CPU pixels. The current frame stays valid.
			if src, ok := p.src.(interface{ SetGPUOutput(bool) error }); ok {
				// On failure capture keeps its old format: GPU-capable
				// encoders take CPU frames too, and GPU-only frames are
				// skipped below for the rest.
				if err := src.SetGPUOutput(acceptsGPU(enc)); err != nil {
					log.Printf("source %s: capture format: %v", p.id, err)
				}
			}
			p.keyframe.Store(true)
		}
		// Right after switching back from GPU output the source may still
		// hold a GPU-only frame; wait for the next one.
		if f.Pix == nil && !acceptsGPU(enc) {
			continue
		}
		if elapsed := t1.Sub(lastRate); elapsed >= 250*time.Millisecond {
			sent := int(float64(sentBytes*8) / elapsed.Seconds())
			lastRate, sentBytes = t1, 0
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
			counts.EncodeErrors++
			if errors.Is(err, encoder.ErrReopen) {
				enc.Close()
				enc = nil
			}
			continue
		}
		t2 := time.Now()
		if len(data) == 0 {
			counts.Empty++
			continue
		}
		counts.Sent++
		sentBytes += len(data)
		// Stamp the frame with when it was on screen when the source says
		// (the grab can lag that by up to an interval of pacing), kept
		// increasing.
		stamp := t0
		if !f.Time.IsZero() && f.Time.Before(t0) && t0.Sub(f.Time) < time.Second {
			stamp = f.Time
		}
		if !stamp.After(lastStamp) {
			stamp = lastStamp.Add(time.Millisecond)
		}
		if !lastStamp.IsZero() && counts.Sent > 1 {
			d := float64(stamp.Sub(lastStamp).Microseconds()) / 1000
			counts.sum += d
			counts.sq += d * d
		}
		lastStamp = stamp
		ts := tsBase + uint32(stamp.Sub(start)*90000/time.Second)
		for _, pkt := range packetizer.Packetize(data, 0) {
			pkt.Timestamp = ts
			// An error means some viewer failed; keep going so the others
			// get the whole frame.
			_ = p.track.WriteRTP(pkt)
		}

		t3 := time.Now()
		ew, eh := enc.Size()
		p.statsMu.Lock()
		p.stats = pipelineStats{
			Width: ew, Height: eh, Rates: rates,
			SendMs:      float64(t3.Sub(t2).Microseconds()) / 1000,
			CaptureMs:   float64(t0.Sub(captureStart).Microseconds()) / 1000, // includes waiting for an update
			EncodeMs:    float64(t2.Sub(t1).Microseconds()) / 1000,
			Bytes:       len(data),
			BitrateKbps: kbps,
		}
		p.statsMu.Unlock()
	}
}

// acceptsGPU reports whether enc encodes capture.Frame.GPU handles directly.
func acceptsGPU(enc encoder.Encoder) bool {
	g, ok := enc.(interface{ AcceptsGPUFrames() bool })
	return ok && g.AcceptsGPUFrames()
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
