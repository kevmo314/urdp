// Build: gcc -O2 -o xstamp scripts/xstamp.c -lX11 ; run on the host, then view /?probe=native.
// Draws a 12-bit wall-clock millisecond timestamp as 12 black/white 12px
// blocks at the top-left of the screen, redrawn every millisecond, in an
// override-redirect window kept on top.
#include <X11/Xlib.h>
#include <stdint.h>
#include <time.h>
#include <unistd.h>
int main(void) {
	Display *d = XOpenDisplay(NULL);
	if (!d) return 1;
	int s = DefaultScreen(d);
	XSetWindowAttributes a = { .override_redirect = True, .background_pixel = BlackPixel(d, s) };
	Window w = XCreateWindow(d, RootWindow(d, s), 0, 0, 144, 12, 0, CopyFromParent, InputOutput,
		CopyFromParent, CWOverrideRedirect | CWBackPixel, &a);
	XMapRaised(d, w);
	GC g = XCreateGC(d, w, 0, NULL);
	unsigned long white = WhitePixel(d, s), black = BlackPixel(d, s);
	int last = -1;
	for (int n = 0;; n++) {
		struct timespec t;
		clock_gettime(CLOCK_REALTIME, &t);
		int ms = (int)((t.tv_sec * 1000LL + t.tv_nsec / 1000000) & 0xfff);
		if (ms != last) {
			for (int i = 0; i < 12; i++) {
				XSetForeground(d, g, (ms >> (11 - i)) & 1 ? white : black);
				XFillRectangle(d, w, g, i * 12, 0, 12, 12);
			}
			if (n % 100 == 0) XRaiseWindow(d, w);
			XFlush(d);
			last = ms;
		}
		usleep(200);
	}
}
