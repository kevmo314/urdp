// Package capture grabs screen or window pixels as BGRA frames.
package capture

import (
	"errors"
	"fmt"
	"image"
	"strconv"
	"strings"
	"time"
)

// Frame is a BGRA (little-endian 0xAARRGGBB) image. Pix is only valid until the
// next call to Capture on the same Source.
type Frame struct {
	Pix    []byte
	Stride int
	Width  int
	Height int
	// Dirty lists the regions that changed since the previous frame from
	// the same source. nil means the whole frame may have changed.
	Dirty []image.Rectangle
	// GPU optionally carries a platform-specific handle to the frame still
	// in GPU memory (e.g. a D3D11 texture or CVPixelBuffer). Hardware
	// encoders that understand it can encode without a CPU readback; Pix
	// may then be nil, so encoders that don't must not be paired with a
	// source that omits Pix.
	GPU any
}

// Source produces frames from the desktop or a single window.
type Source interface {
	// Capture returns the latest frame and whether it changed since the
	// previous call. Sources that are notified of screen updates block up
	// to wait for new content so frames are grabbed the moment they are
	// presented; polling sources return immediately.
	Capture(wait time.Duration) (f *Frame, changed bool, err error)
	// Bounds is the screen-space rectangle covered by the last frame, used
	// to map normalized input coordinates to absolute screen positions.
	Bounds() image.Rectangle
	// WindowID is the captured window, or 0 for the full desktop.
	WindowID() uint64
	Close() error
}

// Window describes a capturable top-level window.
type Window struct {
	ID    uint64 `json:"id"`
	Title string `json:"title"`
	// App names the owning application (e.g. "chrome.exe", "firefox"),
	// used to group windows.
	App    string `json:"app"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Display is one monitor, in virtual-desktop coordinates.
type Display struct {
	Index   int    `json:"index"` // 1-based; opened as "display<Index>"
	Name    string `json:"name"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Primary bool   `json:"primary"`
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
// virtual desktop across all monitors, "display<N>" one monitor (see
// Displays), "test" a synthetic animated pattern; anything else is parsed
// as a window id.
func Open(id string) (Source, error) {
	switch id {
	case "", "desktop":
		return openDesktop()
	case "test":
		return NewTestSource(1280, 720), nil
	}
	if n, ok := strings.CutPrefix(id, "display"); ok {
		i, err := strconv.Atoi(n)
		if err != nil || i < 1 {
			return nil, fmt.Errorf("invalid display %q", id)
		}
		return openDisplay(i)
	}
	wid, err := ParseID(id)
	if err != nil {
		return nil, fmt.Errorf("invalid source %q: %w", id, err)
	}
	return openWindow(wid)
}

// Displays enumerates monitors.
func Displays() ([]Display, error) { return listDisplays() }

// List enumerates capturable windows.
func List() ([]Window, error) { return listWindows() }

// Icon returns the window's icon, scaled to roughly size x size pixels when
// the platform offers a choice. It returns ErrNotFound if the window has
// none.
func Icon(id uint64, size int) (image.Image, error) { return windowIcon(id, size) }
