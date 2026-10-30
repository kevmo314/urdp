// Package encoder compresses captured frames for streaming. Each codec can
// have several backends, hardware ones preferred over software; New picks
// the best that works on this host.
package encoder

import (
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"

	"github.com/kevmo314/urdp/internal/capture"
)

// Codec identifies a video codec by its WebRTC name.
type Codec string

const (
	H264 Codec = "H264"
	H265 Codec = "H265"
	AV1  Codec = "AV1"
	VP8  Codec = "VP8"
	VP9  Codec = "VP9"
)

// ParseCodec accepts names like "h264", "hevc", "av1".
func ParseCodec(s string) (Codec, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "H264", "AVC":
		return H264, nil
	case "H265", "HEVC":
		return H265, nil
	case "AV1":
		return AV1, nil
	case "VP8":
		return VP8, nil
	case "VP9":
		return VP9, nil
	}
	return "", fmt.Errorf("unknown codec %q", s)
}

// Options configures an encoder.
type Options struct {
	FPS         int
	BitrateKbps int // rate ceiling (VBV/HRD max)
	// KeyintSeconds is the periodic keyframe interval; keyframes are also
	// produced on demand when the receiver reports loss (PLI).
	KeyintSeconds int
	// Backend restricts the choice to the named backend ("x264", ...);
	// "software" or "hardware" restrict by kind; "" picks automatically.
	Backend string
}

// Encoder compresses one stream. Not safe for concurrent use.
type Encoder interface {
	// Encode compresses f, forcing a keyframe if asked, and returns the
	// bitstream for one frame, valid until the next call: Annex B NAL units
	// for H.264/H.265, a temporal unit of OBUs (low-overhead format, no
	// temporal delimiter needed) for AV1, a frame for VP8/VP9. A nil result
	// with no error means no output for this frame. Frames must not be
	// delayed: one in, one out.
	Encode(f *capture.Frame, ptsMs int64, keyframe bool) (data []byte, isKey bool, err error)
	// SetBitrate changes the rate ceiling on the fly (congestion control).
	SetBitrate(kbps int) error
	Size() (w, h int)
	Codec() Codec
	Close()
}

// Backend is one way of encoding a codec.
type Backend struct {
	Name     string
	Codec    Codec
	Hardware bool
	// New opens an encoder; it should fail fast if the hardware or driver
	// is missing so the next backend can be tried.
	New func(w, h int, o Options) (Encoder, error)
}

var (
	mu       sync.Mutex
	backends []Backend
	// broken remembers hardware backends that failed to open, so each
	// new pipeline doesn't pay for probing them again.
	broken = map[string]bool{}
)

// Register adds a backend. Platform files call it from init.
func Register(b Backend) {
	mu.Lock()
	defer mu.Unlock()
	backends = append(backends, b)
}

// Backends lists the registered backends for c, hardware first.
func Backends(c Codec) []Backend {
	mu.Lock()
	defer mu.Unlock()
	var out []Backend
	for _, b := range backends {
		if b.Codec == c {
			out = append(out, b)
		}
	}
	slices.SortStableFunc(out, func(a, b Backend) int {
		switch {
		case a.Hardware && !b.Hardware:
			return -1
		case !a.Hardware && b.Hardware:
			return 1
		}
		return 0
	})
	return out
}

// Codecs lists codecs with at least one backend allowed by o.Backend.
func Codecs(o Options) []Codec {
	var out []Codec
	for _, c := range []Codec{AV1, H265, H264, VP9, VP8} {
		for _, b := range Backends(c) {
			if allowed(b, o.Backend) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

func allowed(b Backend, want string) bool {
	switch want {
	case "":
		return true
	case "hardware":
		return b.Hardware
	case "software":
		return !b.Hardware
	}
	return b.Name == want
}

// New opens the best available encoder for c: hardware backends first,
// falling back to software.
func New(c Codec, w, h int, o Options) (Encoder, error) {
	var errs []error
	for _, b := range Backends(c) {
		if !allowed(b, o.Backend) {
			continue
		}
		mu.Lock()
		skip := broken[b.Name]
		mu.Unlock()
		if skip {
			continue
		}
		e, err := b.New(w, h, o)
		if err == nil {
			return e, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", b.Name, err))
		if b.Hardware {
			log.Printf("encoder: %s unavailable, falling back: %v", b.Name, err)
			mu.Lock()
			broken[b.Name] = true
			mu.Unlock()
		}
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("encoder: no %s backend available", c)
	}
	return nil, errors.Join(errs...)
}

// normalize applies defaults and rounds dimensions down to even values,
// as 4:2:0 chroma subsampling requires.
func normalize(w, h int, o Options) (int, int, Options, error) {
	w, h = w&^1, h&^1
	if w < 16 || h < 16 {
		return 0, 0, o, errors.New("encoder: frame too small")
	}
	if o.FPS <= 0 {
		o.FPS = 60
	}
	if o.BitrateKbps <= 0 {
		o.BitrateKbps = 10000
	}
	if o.KeyintSeconds <= 0 {
		o.KeyintSeconds = 5
	}
	return w, h, o, nil
}
