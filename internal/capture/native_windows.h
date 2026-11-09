#pragma once
#include <stdint.h>
#ifdef __cplusplus
extern "C" {
#endif
typedef struct capture_native capture_native;
typedef struct {
    int x, y, w, h;
} capture_rect;
typedef struct {
    uint64_t id;
    char title[8192];
    char app[1024];
    int w, h;
} capture_window;
typedef struct {
    unsigned int adapter, output;
    capture_rect rect;
    int primary;
    char device[128], name[512];
} capture_display;
long capture_displays(capture_display **out, int *count);
long capture_open_display(unsigned int adapter, unsigned int output, capture_native **out);
void capture_dpi(void);
long capture_open(uint64_t hwnd, capture_native **out);
long capture_bounds(capture_native *s, capture_rect *r);
// A frame is either CPU pixels or, in GPU mode, a D3D11 texture (BGRA,
// at least w x h) on device. The texture is owned by the capture and is
// updated in place by the next capture_frame.
typedef struct {
    unsigned char *pixels;
    int stride, w, h;
    void *texture, *device;
    // mip is the texture's mip level holding the w x h image (scaling).
    int mip;
    // When the frame was put on screen, in 100ns units of the
    // QueryPerformanceCounter clock (capture_now_100ns); 0 if unknown.
    int64_t present;
} capture_image;
int64_t capture_now_100ns(void);
// capture_set_gpu switches between CPU frames and GPU textures. It returns
// whether GPU mode is now on: it is refused when monitors span adapters.
// Rotated monitors and fallback paths still produce CPU frames.
int capture_set_gpu(capture_native *s, int on);
// capture_set_scale makes GPU frames 1/scale of the size (1, 2 or 4), via
// the texture's mip chain. It returns whether GPU frames will be scaled;
// CPU frames are always full size.
int capture_set_scale(capture_native *s, int scale);
// capture_device is the ID3D11Device GPU textures will live on, or NULL.
void *capture_device(capture_native *s);
long capture_frame(capture_native *s, capture_image *image, unsigned int timeout_ms);
void capture_close(capture_native *s);
long capture_list(capture_window **out, int *count);
void capture_window_app(uint64_t hwnd, char *app, int capacity);
long capture_icon(uint64_t hwnd, int size, unsigned char *rgba);
void capture_free(void *p);

#ifdef __cplusplus
}
#endif
