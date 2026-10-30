// Package vpx decodes the VP8 video the browser sends from its webcam.
package vpx

import "github.com/kevmo314/urdp/internal/vdev"

// Decoder turns complete VP8 frames into I420 images.
type Decoder interface {
	// Decode returns the decoded picture, valid until the next call, or
	// nil if the frame produced no output.
	Decode(frame []byte) (*vdev.Image, error)
	Close()
}

// NewDecoder creates a VP8 decoder.
func NewDecoder() (Decoder, error) { return newDecoder() }
