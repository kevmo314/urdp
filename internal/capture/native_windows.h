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
void capture_dpi(void);
long capture_open(uint64_t hwnd, capture_native **out);
long capture_bounds(capture_native *s, capture_rect *r);
typedef struct {
    unsigned char *pixels;
    int stride, w, h;
} capture_image;
long capture_frame(capture_native *s, capture_image *image, unsigned int timeout_ms);
void capture_close(capture_native *s);
long capture_list(capture_window **out, int *count);
void capture_window_app(uint64_t hwnd, char *app, int capacity);
long capture_icon(uint64_t hwnd, int size, unsigned char *rgba);
void capture_free(void *p);

#ifdef __cplusplus
}
#endif
