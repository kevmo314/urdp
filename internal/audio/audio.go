// Package audio captures the host's system audio output (loopback) as PCM.
package audio

const (
	SampleRate = 48000
	Channels   = 2
	// FrameMs is the packet duration. 10ms Opus frames keep latency low
	// at a small bitrate cost compared to the usual 20ms.
	FrameMs = 10
	// FrameSamples is the per-channel sample count of one frame.
	FrameSamples = SampleRate * FrameMs / 1000
)

// Source yields interleaved signed 16-bit stereo PCM at 48kHz.
type Source interface {
	// Read blocks until len(buf) samples (FrameSamples*Channels) are
	// available and copies them into buf.
	Read(buf []int16) error
	Close() error
}

// Open opens the default system output's loopback/monitor stream.
func Open() (Source, error) { return openLoopback() }
