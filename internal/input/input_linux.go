package input

/*
#cgo pkg-config: x11 xtst
#include <X11/Xlib.h>
#include <X11/Xatom.h>
#include <X11/extensions/XTest.h>
#include <string.h>

static int ignore_errors(Display *d, XErrorEvent *e) { return 0; }

static Display *xt_open(void) {
	XInitThreads();
	XSetErrorHandler(ignore_errors);
	Display *d = XOpenDisplay(NULL);
	if (!d) return NULL;
	int ev, er, maj, min;
	if (!XTestQueryExtension(d, &ev, &er, &maj, &min)) { XCloseDisplay(d); return NULL; }
	return d;
}

static void xt_move(Display *d, int x, int y) { XTestFakeMotionEvent(d, -1, x, y, CurrentTime); XFlush(d); }
static void xt_button(Display *d, unsigned int b, int down) { XTestFakeButtonEvent(d, b, down, CurrentTime); XFlush(d); }

static int xt_key(Display *d, unsigned long sym, int down) {
	KeyCode kc = XKeysymToKeycode(d, sym);
	if (!kc) return -1;
	XTestFakeKeyEvent(d, kc, down, CurrentTime);
	XFlush(d);
	return 0;
}

static void xt_focus(Display *d, Window w) {
	Window root = DefaultRootWindow(d);
	XEvent e;
	memset(&e, 0, sizeof(e));
	e.xclient.type = ClientMessage;
	e.xclient.window = w;
	e.xclient.message_type = XInternAtom(d, "_NET_ACTIVE_WINDOW", False);
	e.xclient.format = 32;
	e.xclient.data.l[0] = 2; // source: pager, so WMs honor it
	e.xclient.data.l[1] = CurrentTime;
	XSendEvent(d, root, False, SubstructureRedirectMask | SubstructureNotifyMask, &e);
	XRaiseWindow(d, w);
	XFlush(d);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
)

type xtest struct {
	mu sync.Mutex
	d  *C.Display
}

// New returns an XTest-based injector for the X display in $DISPLAY.
func New() (Injector, error) {
	d := C.xt_open()
	if d == nil {
		return nil, errors.New("xtest: cannot open display or XTEST unavailable")
	}
	return &xtest{d: d}, nil
}

func (x *xtest) MouseMove(px, py int) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	C.xt_move(x.d, C.int(px), C.int(py))
	return nil
}

// X buttons: 1 left, 2 middle, 3 right, 4/5 wheel up/down, 6/7 left/right.
func (x *xtest) MouseButton(b int, down bool) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	C.xt_button(x.d, C.uint(b+1), cbool(down))
	return nil
}

func (x *xtest) Wheel(dx, dy int) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	click := func(b C.uint, n int) {
		for range n {
			C.xt_button(x.d, b, 1)
			C.xt_button(x.d, b, 0)
		}
	}
	if dy > 0 {
		click(5, dy)
	} else {
		click(4, -dy)
	}
	if dx > 0 {
		click(7, dx)
	} else {
		click(6, -dx)
	}
	return nil
}

func (x *xtest) Key(k Key, down bool) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if C.xt_key(x.d, C.ulong(k.Keysym), cbool(down)) != 0 {
		return fmt.Errorf("xtest: no keycode for keysym 0x%x", k.Keysym)
	}
	return nil
}

func (x *xtest) Focus(id uint64) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	C.xt_focus(x.d, C.Window(id))
	return nil
}

func (x *xtest) Close() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.d != nil {
		C.XCloseDisplay(x.d)
		x.d = nil
	}
	return nil
}

func cbool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}
