// Package capture grabs screen or window pixels as BGRA frames.
package capture

import (
	"errors"
	"fmt"
	"image"
	"strconv"
	"strings"
)

// Frame is a BGRA (little-endian 0xAARRGGBB) image. Pix is only valid until the
// next call to Capture on the same Source.
type Frame struct {
	Pix    []byte
	Stride int
	Width  int
	Height int
}

// Source produces frames from the desktop or a single window.
type Source interface {
	// Capture returns the latest frame. It may return the previously
	// captured frame if nothing has changed.
	Capture() (*Frame, error)
	// Bounds is the screen-space rectangle covered by the last frame, used
	// to map normalized input coordinates to absolute screen positions.
	Bounds() image.Rectangle
	// WindowID is the captured window, or 0 for the full desktop.
	WindowID() uint64
	Close() error
}

// Window describes a capturable top-level window.
type Window struct {
	ID     uint64 `json:"id"`
	Title  string `json:"title"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// ErrNotFound is returned when the requested window does not exist.
var ErrNotFound = errors.New("window not found")

// ParseID parses a window id as decimal or 0x-prefixed hex.
func ParseID(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		return strconv.ParseUint(s[2:], 16, 64)
	}
	return strconv.ParseUint(s, 10, 64)
}

// FormatID renders a window id the way ParseID accepts it.
func FormatID(id uint64) string { return fmt.Sprintf("0x%x", id) }

// Open opens a capture source. id "" (or "desktop") captures the whole
// desktop, "test" yields a synthetic animated pattern, anything else is
// parsed as a window id.
func Open(id string) (Source, error) {
	switch id {
	case "", "desktop":
		return openDesktop()
	case "test":
		return NewTestSource(1280, 720), nil
	}
	wid, err := ParseID(id)
	if err != nil {
		return nil, fmt.Errorf("invalid source %q: %w", id, err)
	}
	return openWindow(wid)
}

// List enumerates capturable windows.
func List() ([]Window, error) { return listWindows() }
