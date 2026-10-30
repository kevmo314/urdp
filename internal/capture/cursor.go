package capture

import (
	"bytes"
	"image"
	"image/png"
)

// Cursor describes a host pointer shape, independent of its position.
// Serial identifies the shape for the lifetime of a watcher. PNG uses host pixels
// and is only needed when CSS is empty. HotX/HotY locate the click within PNG.
type Cursor struct {
	Serial     uint64
	CSS        string
	PNG        []byte
	HotX, HotY int
	Hidden     bool
}

func cursorPNG(im image.Image) ([]byte, error) {
	var b bytes.Buffer
	err := png.Encode(&b, im)
	return b.Bytes(), err
}
