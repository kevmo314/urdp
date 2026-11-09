package capture

/*
#include <stdint.h>
void capture_dpi(void);
int capture_cursor(uint64_t *, int *, int *, int *, int *, int *, int *);
int capture_cursor_render(uint64_t, int, int, unsigned char *);
*/
import "C"

import (
	"context"
	"errors"
	"image"
	"time"
	"unsafe"
)

// renderCursor draws a cursor handle as a w×h PNG.
func renderCursor(handle uint64, w, h int) ([]byte, error) {
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	if C.capture_cursor_render(C.uint64_t(handle), C.int(w), C.int(h), (*C.uchar)(unsafe.Pointer(&im.Pix[0]))) == 0 {
		return nil, errors.New("cursor: render failed")
	}
	return cursorPNG(im)
}

// WatchCursor reports the initial cursor and subsequent shape/visibility changes
// until ctx is canceled. Pointer motion alone does not trigger a callback.
func WatchCursor(ctx context.Context, fn func(Cursor)) error {
	dpiOnce.Do(func() { C.capture_dpi() })
	ticker := time.NewTicker(time.Second / 60)
	defer ticker.Stop()
	names := []string{"default", "text", "wait", "crosshair", "n-resize", "nwse-resize", "nesw-resize", "ew-resize", "ns-resize", "move", "not-allowed", "pointer", "progress", "help"}
	var previous uint64
	var hidden bool
	first := true
	for {
		var handle C.uint64_t
		var showing, kind, w, h, x, y C.int
		if C.capture_cursor(&handle, &showing, &kind, &w, &h, &x, &y) != 0 && (first || uint64(handle) != previous || (showing == 0) != hidden) {
			c := Cursor{Serial: uint64(handle), Hidden: showing == 0, HotX: int(x), HotY: int(y)}
			if !c.Hidden {
				if kind >= 0 {
					c.CSS = names[int(kind)]
				} else {
					data, err := renderCursor(uint64(handle), int(w), int(h))
					if err != nil {
						goto wait
					}
					c.PNG = data
				}
			}
			fn(c)
			previous, hidden, first = c.Serial, c.Hidden, false
		}
	wait:
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
