//go:build linux

// Package xlib performs the process-wide Xlib setup that every package
// talking to the X server needs, exactly once, before any other Xlib call.
package xlib

/*
#cgo pkg-config: x11
#include <X11/Xlib.h>

static int ignore_errors(Display *d, XErrorEvent *e) { return 0; }

static void xlib_init(void) {
	XInitThreads();
	// BadWindow/BadMatch from windows vanishing mid-call must not abort.
	XSetErrorHandler(ignore_errors);
}
*/
import "C"

import "sync"

var once sync.Once

// Init makes Xlib thread-safe and installs a non-fatal error handler.
func Init() { once.Do(func() { C.xlib_init() }) }
