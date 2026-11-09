package capture

/*
#cgo pkg-config: x11 xfixes
#include <X11/Xlib.h>
#include <X11/extensions/Xfixes.h>
#include <poll.h>
#include <errno.h>
static int cursor_wait(Display *d, int cancel, int event, unsigned long *serial, Atom *name) {
 for (;;) {
  while (XPending(d)) {
   XEvent e;
   XNextEvent(d, &e);
   if (e.type == event + XFixesCursorNotify) {
    XFixesCursorNotifyEvent *cursor = (XFixesCursorNotifyEvent *)&e;
    *serial = cursor->cursor_serial;
    *name = cursor->cursor_name;
    return 1;
   }
  }
  struct pollfd fds[2] = {{ConnectionNumber(d), POLLIN, 0}, {cancel, POLLIN, 0}};
  if (poll(fds, 2, -1) < 0) { if (errno == EINTR) continue; return -1; }
  if (fds[1].revents) return 0;
  if (fds[0].revents & (POLLERR | POLLHUP | POLLNVAL)) return -1;
 }
}
*/
import "C"

import (
	"context"
	"errors"
	"image"
	"os"
	"unsafe"

	"github.com/kevmo314/urdp/internal/xlib"
)

// WatchCursor watches XFixes shape notifications without polling pointer position.
func WatchCursor(ctx context.Context, fn func(Cursor)) error {
	xlib.Init()
	d := C.XOpenDisplay(nil)
	if d == nil {
		return errors.New("cursor: cannot open X display")
	}
	defer C.XCloseDisplay(d)
	var major, minor C.int
	major = 2
	if C.XFixesQueryVersion(d, &major, &minor) == 0 || major < 2 {
		return errors.New("cursor: XFixes 2 required")
	}
	var event, xerr C.int
	if C.XFixesQueryExtension(d, &event, &xerr) == 0 {
		return errors.New("cursor: XFixes unavailable")
	}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	defer w.Close()
	stop := context.AfterFunc(ctx, func() { _, _ = w.Write([]byte{1}) })
	defer stop()
	C.XFixesSelectCursorInput(d, C.XDefaultRootWindow(d), C.XFixesDisplayCursorNotifyMask)
	C.XFlush(d)
	var previous Cursor
	first := true
	var serial C.ulong
	var atom C.Atom
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// XACE may deny reads after the creating client exits (xsetroot).
		// Notifications still supply the name and serial in that case.
		// This fails for a cursor whose creator has freed it and exited
		// (xsetroot); named cursors still come through by name.
		im := C.XFixesGetCursorImage(d)
		c := Cursor{Serial: uint64(serial)}
		if im != nil {
			c.Serial, c.HotX, c.HotY = uint64(im.cursor_serial), int(im.xhot), int(im.yhot)
			atom = im.atom
		}
		if atom != 0 {
			name := C.XGetAtomName(d, atom)
			if name != nil {
				c.CSS = xCursorCSS[C.GoString(name)]
				C.XFree(unsafe.Pointer(name))
			}
		}
		if im != nil && c.CSS == "" {
			rgba := image.NewRGBA(image.Rect(0, 0, int(im.width), int(im.height)))
			// Xlib exposes CARD32 pixels as unsigned long, including on LP64 hosts.
			pixels := unsafe.Slice(im.pixels, int(im.width)*int(im.height))
			c.Hidden = true
			for i, pixel := range pixels {
				p := uint32(pixel)
				rgba.Pix[4*i], rgba.Pix[4*i+1], rgba.Pix[4*i+2], rgba.Pix[4*i+3] = byte(p>>16), byte(p>>8), byte(p), byte(p>>24)
				if p>>24 != 0 {
					c.Hidden = false
				}
			}
			c.PNG, err = cursorPNG(rgba)
		}
		if im != nil {
			C.XFree(unsafe.Pointer(im))
		}
		if err != nil {
			return err
		}
		if (c.CSS != "" || len(c.PNG) != 0) && (first || c.Serial != previous.Serial || c.Hidden != previous.Hidden) {
			fn(c)
			previous = c
			first = false
		}
		switch C.cursor_wait(d, C.int(r.Fd()), event, &serial, &atom) {
		case 0:
			return ctx.Err()
		case -1:
			return errors.New("cursor: X display disconnected")
		}
	}
}

var xCursorCSS = map[string]string{
	"left_ptr": "default", "default": "default", "arrow": "default", "top_left_arrow": "default",
	"xterm": "text", "text": "text", "vertical-text": "vertical-text",
	"hand1": "pointer", "hand2": "pointer", "pointer": "pointer",
	"watch": "wait", "wait": "wait", "left_ptr_watch": "progress", "progress": "progress",
	"cross": "crosshair", "crosshair": "crosshair", "cross_reverse": "crosshair", "tcross": "crosshair",
	"fleur": "move", "move": "move", "all-scroll": "all-scroll", "question_arrow": "help", "help": "help",
	"not-allowed": "not-allowed", "crossed_circle": "not-allowed", "forbidden": "not-allowed", "no-drop": "no-drop",
	"sb_h_double_arrow": "ew-resize", "h_double_arrow": "ew-resize", "ew-resize": "ew-resize",
	"sb_v_double_arrow": "ns-resize", "v_double_arrow": "ns-resize", "ns-resize": "ns-resize",
	"bottom_right_corner": "nwse-resize", "top_left_corner": "nwse-resize", "nwse-resize": "nwse-resize",
	"bottom_left_corner": "nesw-resize", "top_right_corner": "nesw-resize", "nesw-resize": "nesw-resize",
	"left_side": "w-resize", "right_side": "e-resize", "top_side": "n-resize", "bottom_side": "s-resize",
	"col-resize": "col-resize", "row-resize": "row-resize", "openhand": "grab", "grab": "grab",
	"closedhand": "grabbing", "grabbing": "grabbing", "copy": "copy", "alias": "alias", "zoom-in": "zoom-in", "zoom-out": "zoom-out",
}
