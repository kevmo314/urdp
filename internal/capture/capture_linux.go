package capture

/*
#cgo pkg-config: x11 xext xcomposite xdamage xrandr
#include <X11/Xlib.h>
#include <X11/Xutil.h>
#include <X11/Xatom.h>
#include <X11/extensions/XShm.h>
#include <X11/extensions/Xcomposite.h>
#include <X11/extensions/Xdamage.h>
#include <X11/extensions/Xrandr.h>
#include <poll.h>
#include <time.h>
#include <sys/ipc.h>
#include <sys/shm.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>

typedef struct {
	Display *dpy;
	Drawable target;
	Window win; // 0 when capturing the root window
	XShmSegmentInfo shm;
	XImage *img;
	int w, h;   // current image size
	int x, y;   // position of target in root coordinates
	int cx, cy, cw, ch; // crop of the root window (one monitor); cw == 0 for none
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
	if (g->shm.shmaddr == (char *)-1) { g->img->data = NULL; XDestroyImage(g->img); g->img = NULL; return -1; }
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
	if (!g) { XCloseDisplay(dpy); return NULL; }
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
	if (!g->win && g->cw) {
		a->width = g->cw;
		a->height = g->ch;
	}
	if ((a->width & ~1) <= 0 || (a->height & ~1) <= 0) return -2;
	if (g->win) {
		Window child;
		XTranslateCoordinates(g->dpy, g->win, DefaultRootWindow(g->dpy), 0, 0, &g->x, &g->y, &child);
	} else {
		g->x = g->cx;
		g->y = g->cy;
	}
	return 0;
}

// Like grabber_locate, but returns 1 if the size no longer matches the
// last grabbed image (resizes don't always produce damage).
static int grabber_relocate(grabber *g) {
	XWindowAttributes a;
	if (grabber_locate(g, &a)) return -2;
	return (a.width & ~1) != g->w || (a.height & ~1) != g->h;
}

// Returns 0 on success, -1 on failure, -2 if the window is gone/unmapped.
static int grabber_grab(grabber *g) {
	XWindowAttributes a;
	if (grabber_locate(g, &a)) return -2;
	int w = a.width & ~1, h = a.height & ~1;
	if (!g->img || w != g->w || h != g->h) {
		if (grabber_alloc_image(g, a.visual, a.depth, w, h)) return -1;
	}
	if (!XShmGetImage(g->dpy, g->target, g->img, g->win ? 0 : g->cx, g->win ? 0 : g->cy, AllPlanes)) return -1;
	return 0;
}

static unsigned char *grabber_data(grabber *g) { return (unsigned char *)g->img->data; }
static int grabber_stride(grabber *g) { return g->img->bytes_per_line; }
static int grabber_bpp(grabber *g) { return g->img->bits_per_pixel; }

static Atom atom(Display *d, const char *n) { return XInternAtom(d, n, False); }

// Fetches a property of the given type and format (8, 16 or 32), returning
// its item count, or 0 if absent or mismatched. Caller XFree()s *out.
static unsigned long get_prop(Display *d, Window w, Atom prop, Atom type, int format, unsigned char **out) {
	Atom actual; int fmt; unsigned long n, after;
	*out = NULL;
	if (XGetWindowProperty(d, w, prop, 0, 1 << 20, False, type, &actual, &fmt, &n, &after, out) != Success) return 0;
	if (actual != type || fmt != format) return 0;
	return n;
}

static Display *list_dpy(void) { return XOpenDisplay(NULL); }

// Client windows from the window manager, falling back to mapped
// top-level children of the root when no EWMH WM is running.
static unsigned long list_windows(Display *d, Window **out) {
	Window root = DefaultRootWindow(d);
	unsigned char *data;
	unsigned long n = get_prop(d, root, atom(d, "_NET_CLIENT_LIST"), XA_WINDOW, 32, &data);
	if (n > 0) {
		// Format-32 properties are returned as arrays of long.
		*out = malloc(n * sizeof(Window));
		if (!*out) { XFree(data); return 0; }
		for (unsigned long i = 0; i < n; i++) (*out)[i] = ((unsigned long *)data)[i];
		XFree(data);
		return n;
	}
	if (data) XFree(data);
	Window r, p, *kids; unsigned int nk;
	if (!XQueryTree(d, root, &r, &p, &kids, &nk)) return 0;
	*out = malloc((nk ? nk : 1) * sizeof(Window));
	if (!*out) { if (kids) XFree(kids); return 0; }
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
	if (get_prop(d, w, atom(d, "_NET_WM_NAME"), atom(d, "UTF8_STRING"), 8, &data) > 0) return (char *)data;
	if (data) XFree(data);
	char *name = NULL;
	if (XFetchName(d, w, &name) && name) return name;
	return NULL;
}

// Monitors as XRandR reports them; caller XRRFreeMonitors()s the result.
static XRRMonitorInfo *list_monitors(Display *d, int *n) {
	int ev, er;
	*n = 0;
	if (!XRRQueryExtension(d, &ev, &er)) return NULL;
	return XRRGetMonitors(d, DefaultRootWindow(d), True, n);
}

static XRRMonitorInfo *monitor_at(XRRMonitorInfo *m, int i) { return &m[i]; }

static char *window_class(Display *d, Window w) {
	XClassHint h;
	if (!XGetClassHint(d, w, &h)) return NULL;
	if (h.res_name) XFree(h.res_name);
	return h.res_class;
}

// Copies the _NET_WM_ICON image closest to size (preferring the smallest
// one at least that big) as 0xAARRGGBB pixels. Returns NULL if none; caller
// free()s.
static uint32_t *window_icon(Display *d, Window w, int size, int *iw, int *ih) {
	unsigned char *data;
	unsigned long n = get_prop(d, w, atom(d, "_NET_WM_ICON"), XA_CARDINAL, 32, &data);
	unsigned long *p = (unsigned long *)data, best = n;
	long bw = 0;
	for (unsigned long i = 0; i + 2 <= n; ) {
		long cw = p[i], ch = p[i + 1];
		if (cw <= 0 || ch <= 0 || i + 2 + cw * ch > n) break;
		int better = best == n ||
			(bw < size ? cw > bw : (cw >= size && cw < bw));
		if (better) { best = i; bw = cw; }
		i += 2 + cw * ch;
	}
	uint32_t *out = NULL;
	if (best < n) {
		*iw = p[best];
		*ih = p[best + 1];
		out = malloc((size_t)*iw * *ih * 4);
		// Format-32 properties are arrays of long, one pixel per element.
		for (long i = 0; out && i < (long)*iw * *ih; i++) out[i] = (uint32_t)p[best + 2 + i];
	}
	if (data) XFree(data);
	return out;
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

	"github.com/kevmo314/urdp/internal/xlib"
)

type x11Source struct {
	mu    sync.Mutex
	g     *C.grabber
	win   uint64
	frame Frame
	rect  image.Rectangle
}

func openX11(win uint64, crop image.Rectangle) (Source, error) {
	xlib.Init()
	g := C.grabber_open(C.ulong(win))
	if g == nil {
		return nil, errors.New("x11: cannot open display or MIT-SHM unavailable (is DISPLAY set?)")
	}
	g.cx, g.cy, g.cw, g.ch = C.int(crop.Min.X), C.int(crop.Min.Y), C.int(crop.Dx()), C.int(crop.Dy())
	s := &x11Source{g: g, win: win}
	if _, _, err := s.Capture(0); err != nil {
		C.grabber_close(g)
		return nil, err
	}
	return s, nil
}

// The root window already spans every monitor.
func openDesktop() (Source, error)         { return openX11(0, image.Rectangle{}) }
func openWindow(id uint64) (Source, error) { return openX11(id, image.Rectangle{}) }

func openDisplay(i int) (Source, error) {
	ds, err := listDisplays()
	if err != nil {
		return nil, err
	}
	if i < 1 || i > len(ds) {
		return nil, ErrNotFound
	}
	d := ds[i-1]
	return openX11(0, image.Rect(d.X, d.Y, d.X+d.Width, d.Y+d.Height))
}

func listDisplays() ([]Display, error) {
	xlib.Init()
	d := C.XOpenDisplay(nil)
	if d == nil {
		return nil, errors.New("x11: cannot open display")
	}
	defer C.XCloseDisplay(d)
	var n C.int
	ms := C.list_monitors(d, &n)
	if ms == nil || n == 0 {
		// No RandR: the screen is a single display.
		scr := C.XDefaultScreen(d)
		return []Display{{Index: 1, Name: "screen", Width: int(C.XDisplayWidth(d, scr)), Height: int(C.XDisplayHeight(d, scr)), Primary: true}}, nil
	}
	defer C.XRRFreeMonitors(ms)
	out := make([]Display, int(n))
	for i := range out {
		m := C.monitor_at(ms, C.int(i))
		out[i] = Display{Index: i + 1, X: int(m.x), Y: int(m.y), Width: int(m.width), Height: int(m.height), Primary: m.primary != 0}
		if name := C.XGetAtomName(d, m.name); name != nil {
			out[i].Name = C.GoString(name)
			C.XFree(unsafe.Pointer(name))
		}
	}
	return out, nil
}

func windowIcon(id uint64, size int) (image.Image, error) {
	xlib.Init()
	d := C.XOpenDisplay(nil)
	if d == nil {
		return nil, errors.New("x11: cannot open display")
	}
	defer C.XCloseDisplay(d)
	var w, h C.int
	px := C.window_icon(d, C.Window(id), C.int(size), &w, &h)
	if px == nil {
		return nil, ErrNotFound
	}
	defer C.free(unsafe.Pointer(px))
	img := image.NewNRGBA(image.Rect(0, 0, int(w), int(h)))
	for i, v := range unsafe.Slice((*uint32)(unsafe.Pointer(px)), int(w*h)) {
		img.Pix[i*4+0] = byte(v >> 16)
		img.Pix[i*4+1] = byte(v >> 8)
		img.Pix[i*4+2] = byte(v)
		img.Pix[i*4+3] = byte(v >> 24)
	}
	return img, nil
}

func (s *x11Source) Capture(wait time.Duration) (*Frame, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if C.grabber_wait(s.g, C.int(wait.Milliseconds())) == 0 && s.frame.Pix != nil {
		// Unchanged, but the window may have moved, resized or closed.
		switch C.grabber_relocate(s.g) {
		case -2:
			return nil, false, ErrNotFound
		case 0:
			s.rect = image.Rect(int(s.g.x), int(s.g.y), int(s.g.x)+s.frame.Width, int(s.g.y)+s.frame.Height)
			return &s.frame, false, nil
		}
	}
	// A failed grab may have freed the image s.frame points into; don't
	// hand it out again.
	s.frame = Frame{}
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
	xlib.Init()
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
		if c := C.window_class(d, id); c != nil {
			win.App = C.GoString(c)
			C.XFree(unsafe.Pointer(c))
		}
		out = append(out, win)
	}
	return out, nil
}
