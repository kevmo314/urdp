// xcursor: covers the X screen with a window whose cursor is a 16x16
// bitmap (hotspot 3,5) and stays running until killed, like an app with a
// custom cursor. Used by e2e-cursor.mjs.
//   cc scripts/xcursor.c -o xcursor -lX11
#include <X11/Xlib.h>
#include <unistd.h>

int main(void) {
    Display *d = XOpenDisplay(NULL);
    if (!d)
        return 1;
    int screen = DefaultScreen(d);
    Window root = RootWindow(d, screen);
    int w = DisplayWidth(d, screen), h = DisplayHeight(d, screen);
    XSetWindowAttributes a = {0};
    a.override_redirect = True;
    Window win = XCreateWindow(d, root, 0, 0, w, h, 0, CopyFromParent, InputOnly, CopyFromParent,
                               CWOverrideRedirect, &a);
    static char bits[32];
    for (int i = 0; i < 32; i++)
        bits[i] = (char)0xff;
    Pixmap p = XCreateBitmapFromData(d, win, bits, 16, 16);
    XColor fg = {0}, bg = {0};
    fg.red = fg.green = fg.blue = 0xffff;
    XDefineCursor(d, win, XCreatePixmapCursor(d, p, p, &fg, &bg, 3, 5));
    XMapRaised(d, win);
    XWarpPointer(d, None, win, 0, 0, 0, 0, w / 2, h / 2);
    XFlush(d);
    for (;;)
        pause();
}
