package capture

/*
#cgo pkg-config: x11 xext xcomposite xdamage
#include <X11/Xlib.h>
#include <X11/Xutil.h>
#include <X11/Xatom.h>
#include <X11/extensions/XShm.h>
#include <X11/extensions/Xcomposite.h>
#include <X11/extensions/Xdamage.h>
#include <poll.h>
#include <time.h>
#include <sys/ipc.h>
#include <sys/shm.h>
#include <stdlib.h>
#include <string.h>

static int ignore_errors(Display *d, XErrorEvent *e) { return 0; }

static void urdp_init(void) {
	XInitThreads();
	// BadWindow/BadMatch from windows vanishing mid-capture must not abort.
	XSetErrorHandler(ignore_errors);
}

typedef struct {
	Display *dpy;
	Drawable target;
	Window win; // 0 when capturing the root window
	XShmSegmentInfo shm;
	XImage *img;
	int w, h;   // current image size
	int x, y;   // position of target in root coordinates
	Damage damage;
	int damage_event;
} grabber;

static void grabber_free_image(grabber *g) {
	if (!g->img) return;
	XShmDetach(g->dpy, &g->shm);
	XDestroyImage(g->img);
	shmdt(g->shm.shmaddr);
	g->img = NULL;
}

static int grabber_alloc_image(grabber *g, Visual *vis, int depth, int w, int h) {
	grabber_free_image(g);
	g->img = XShmCreateImage(g->dpy, vis, depth, ZPixmap, NULL, &g->shm, w, h);
	if (!g->img) return -1;
	g->shm.shmid = shmget(IPC_PRIVATE, g->img->bytes_per_line * h, IPC_CREAT | 0600);
	if (g->shm.shmid < 0) { XDestroyImage(g->img); g->img = NULL; return -1; }
	g->shm.shmaddr = g->img->data = shmat(g->shm.shmid, NULL, 0);
	// Mark for deletion now; it is freed once both sides detach.
	shmctl(g->shm.shmid, IPC_RMID, NULL);
	g->shm.readOnly = False;
	if (!XShmAttach(g->dpy, &g->shm)) { shmdt(g->shm.shmaddr); XDestroyImage(g->img); g->img = NULL; return -1; }
	XSync(g->dpy, False);
	g->w = w; g->h = h;
	return 0;
}

static grabber *grabber_open(unsigned long win) {
	Display *dpy = XOpenDisplay(NULL);
	if (!dpy) return NULL;
	if (!XShmQueryExtension(dpy)) { XCloseDisplay(dpy); return NULL; }
	grabber *g = calloc(1, sizeof(grabber));
	g->dpy = dpy;
	g->win = win;
	g->target = win ? win : DefaultRootWindow(dpy);
	if (win) {
		int ev, er;
		// Redirecting keeps an off-screen copy of the window so obscured
		// regions still capture correctly. Automatic mode leaves the
		// on-screen appearance unchanged.
		if (XCompositeQueryExtension(dpy, &ev, &er))
			XCompositeRedirectWindow(dpy, win, CompositeRedirectAutomatic);
	}
	int ev, er;
	if (XDamageQueryExtension(dpy, &ev, &er)) {
		g->damage_event = ev + XDamageNotify;
		g->damage = XDamageCreate(dpy, g->target, XDamageReportNonEmpty);
	}
	return g;
}

static long now_ms(void) {
	struct timespec t;
	clock_gettime(CLOCK_MONOTONIC, &t);
	return t.tv_sec * 1000 + t.tv_nsec / 1000000;
}

// Waits up to timeout_ms for the target to be drawn to. Returns 1 if it
// changed (or change tracking is unavailable), 0 on timeout.
static int grabber_wait(grabber *g, int timeout_ms) {
	if (!g->damage) return 1;
	long deadline = now_ms() + timeout_ms;
	int damaged = 0;
	for (;;) {
		while (XPending(g->dpy)) {
			XEvent e;
			XNextEvent(g->dpy, &e);
			if (e.type == g->damage_event) damaged = 1;
		}
		long left = deadline - now_ms();
		if (damaged || left <= 0) break;
		struct pollfd p = { ConnectionNumber(g->dpy), POLLIN, 0 };
		if (poll(&p, 1, (int)left) <= 0) break;
	}
	// Clear before grabbing, so drawing that lands mid-grab re-arms the
	// notification instead of being lost.
	if (damaged) XDamageSubtract(g->dpy, g->damage, None, None);
	return damaged;
}

static void grabber_close(grabber *g) {
	grabber_free_image(g);
	if (g->damage) XDamageDestroy(g->dpy, g->damage);
	if (g->win) XCompositeUnredirectWindow(g->dpy, g->win, CompositeRedirectAutomatic);
	XCloseDisplay(g->dpy);
	free(g);
}

// Refreshes the target's geometry. Returns 0, or -2 if the window is
// gone/unmapped.
static int grabber_locate(grabber *g, XWindowAttributes *a) {
	if (!XGetWindowAttributes(g->dpy, g->target, a)) return -2;
	if (g->win && a->map_state != IsViewable) return -2;
	if ((a->width & ~1) <= 0 || (a->height & ~1) <= 0) return -2;
	if (g->win) {
		Window child;
		XTranslateCoordinates(g->dpy, g->win, DefaultRootWindow(g->dpy), 0, 0, &g->x, &g->y, &child);
	} else {
		g->x = g->y = 0;
	}
	return 0;
}

static int grabber_relocate(grabber *g) {
	XWindowAttributes a;
	return grabber_locate(g, &a);
}

// Returns 0 on success, -1 on failure, -2 if the window is gone/unmapped.
static int grabber_grab(grabber *g) {
	XWindowAttributes a;
	if (grabber_locate(g, &a)) return -2;
	int w = a.width & ~1, h = a.height & ~1;
	if (!g->img || w != g->w || h != g->h) {
		if (grabber_alloc_image(g, a.visual, a.depth, w, h)) return -1;
	}
	if (!XShmGetImage(g->dpy, g->target, g->img, 0, 0, AllPlanes)) return -1;
	return 0;
}

static unsigned char *grabber_data(grabber *g) { return (unsigned char *)g->img->data; }
static int grabber_stride(grabber *g) { return g->img->bytes_per_line; }
static int grabber_bpp(grabber *g) { return g->img->bits_per_pixel; }

static Atom atom(Display *d, const char *n) { return XInternAtom(d, n, False); }

// Fetches a window-list or string property. Caller XFree()s *out.
static unsigned long get_prop(Display *d, Window w, Atom prop, Atom type, unsigned char **out) {
	Atom actual; int fmt; unsigned long n, after;
	*out = NULL;
	if (XGetWindowProperty(d, w, prop, 0, 1 << 20, False, type, &actual, &fmt, &n, &after, out) != Success) return 0;
	return n;
}

static Display *list_dpy(void) { return XOpenDisplay(NULL); }

// Client windows from the window manager, falling back to mapped
// top-level children of the root when no EWMH WM is running.
static unsigned long list_windows(Display *d, Window **out) {
	Window root = DefaultRootWindow(d);
	unsigned char *data;
	unsigned long n = get_prop(d, root, atom(d, "_NET_CLIENT_LIST"), XA_WINDOW, &data);
	if (n > 0) {
		*out = malloc(n * sizeof(Window));
		for (unsigned long i = 0; i < n; i++) (*out)[i] = ((unsigned long *)data)[i];
		XFree(data);
		return n;
	}
	if (data) XFree(data);
	Window r, p, *kids; unsigned int nk;
	if (!XQueryTree(d, root, &r, &p, &kids, &nk)) return 0;
	*out = malloc((nk ? nk : 1) * sizeof(Window));
	unsigned long m = 0;
	for (unsigned int i = 0; i < nk; i++) {
		XWindowAttributes a;
		if (XGetWindowAttributes(d, kids[i], &a) && a.map_state == IsViewable && !a.override_redirect && a.width > 1 && a.height > 1)
			(*out)[m++] = kids[i];
	}
	if (kids) XFree(kids);
	return m;
}

static char *window_title(Display *d, Window w) {
	unsigned char *data;
	if (get_prop(d, w, atom(d, "_NET_WM_NAME"), atom(d, "UTF8_STRING"), &data) > 0) return (char *)data;
	if (data) XFree(data);
	char *name = NULL;
	if (XFetchName(d, w, &name) && name) return name;
	return NULL;
}

static int window_size(Display *d, Window w, int *width, int *height) {
	XWindowAttributes a;
	if (!XGetWindowAttributes(d, w, &a)) return 0;
	*width = a.width; *height = a.height;
	return a.map_state == IsViewable;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"image"
	"sync"
	"time"
	"unsafe"
)

var initOnce sync.Once

func xinit() { initOnce.Do(func() { C.urdp_init() }) }

type x11Source struct {
	mu    sync.Mutex
	g     *C.grabber
	win   uint64
	frame Frame
	rect  image.Rectangle
}

func openX11(win uint64) (Source, error) {
	xinit()
	g := C.grabber_open(C.ulong(win))
	if g == nil {
		return nil, errors.New("x11: cannot open display or MIT-SHM unavailable (is DISPLAY set?)")
	}
	s := &x11Source{g: g, win: win}
	if _, _, err := s.Capture(0); err != nil {
		C.grabber_close(g)
		return nil, err
	}
	return s, nil
}

func openDesktop() (Source, error)         { return openX11(0) }
func openWindow(id uint64) (Source, error) { return openX11(id) }

func (s *x11Source) Capture(wait time.Duration) (*Frame, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if C.grabber_wait(s.g, C.int(wait.Milliseconds())) == 0 && s.frame.Pix != nil {
		// Unchanged, but the window may have moved or closed.
		if C.grabber_relocate(s.g) != 0 {
			return nil, false, ErrNotFound
		}
		s.rect = image.Rect(int(s.g.x), int(s.g.y), int(s.g.x)+s.frame.Width, int(s.g.y)+s.frame.Height)
		return &s.frame, false, nil
	}
	switch C.grabber_grab(s.g) {
	case -2:
		return nil, false, ErrNotFound
	case -1:
		return nil, false, errors.New("x11: XShmGetImage failed")
	}
	if bpp := C.grabber_bpp(s.g); bpp != 32 {
		return nil, false, fmt.Errorf("x11: unsupported %d bits per pixel", bpp)
	}
	w, h, stride := int(s.g.w), int(s.g.h), int(C.grabber_stride(s.g))
	s.frame = Frame{
		Pix:    unsafe.Slice((*byte)(C.grabber_data(s.g)), stride*h),
		Stride: stride,
		Width:  w,
		Height: h,
	}
	x, y := int(s.g.x), int(s.g.y)
	s.rect = image.Rect(x, y, x+w, y+h)
	return &s.frame, true, nil
}

func (s *x11Source) Bounds() image.Rectangle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rect
}

func (s *x11Source) WindowID() uint64 { return s.win }

func (s *x11Source) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.g != nil {
		C.grabber_close(s.g)
		s.g = nil
	}
	return nil
}

func listWindows() ([]Window, error) {
	xinit()
	d := C.list_dpy()
	if d == nil {
		return nil, errors.New("x11: cannot open display")
	}
	defer C.XCloseDisplay(d)
	var ids *C.Window
	n := C.list_windows(d, &ids)
	if ids == nil {
		return nil, nil
	}
	defer C.free(unsafe.Pointer(ids))
	var out []Window
	for _, id := range unsafe.Slice(ids, n) {
		var w, h C.int
		if C.window_size(d, id, &w, &h) == 0 || w < 2 || h < 2 {
			continue
		}
		win := Window{ID: uint64(id), Width: int(w), Height: int(h)}
		if t := C.window_title(d, id); t != nil {
			win.Title = C.GoString(t)
			C.XFree(unsafe.Pointer(t))
		}
		out = append(out, win)
	}
	return out, nil
}
