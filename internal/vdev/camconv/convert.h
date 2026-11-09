// Pixel conversion shared by the urdp Camera producer (urdp.exe) and the
// Media Foundation source DLL. Plain C with no Windows headers so the exact
// code that ships is unit-tested on every platform (see camconv_test.go).
#pragma once
#include <stdint.h>
#include <string.h>

#define URDP_CAM_BLACK_Y 16
#define URDP_CAM_BLACK_UV 128

// urdp_i420_to_nv12 writes an NV12 image of (width+1)&~1 x (height+1)&~1
// into dst, padding odd edges by repeating the last pixel or row.
static inline void urdp_i420_to_nv12(uint8_t *dst, const uint8_t *y, const uint8_t *u,
                                     const uint8_t *v, int width, int height, int ys, int us,
                                     int vs) {
    int w = (width + 1) & ~1, h = (height + 1) & ~1;
    for (int row = 0; row < h; row++) {
        const uint8_t *src = y + (size_t)(row < height ? row : height - 1) * ys;
        uint8_t *out = dst + (size_t)row * w;
        memcpy(out, src, (size_t)width);
        if (w > width)
            out[width] = src[width - 1];
    }
    uint8_t *uv = dst + (size_t)w * h;
    for (int row = 0; row < h / 2; row++)
        for (int col = 0; col < w / 2; col++) {
            uv[(size_t)row * w + 2 * col] = u[(size_t)row * us + col];
            uv[(size_t)row * w + 2 * col + 1] = v[(size_t)row * vs + col];
        }
}

// urdp_nv12_black fills a w x h NV12 image with video-range black.
static inline void urdp_nv12_black(uint8_t *dst, uint32_t w, uint32_t h) {
    memset(dst, URDP_CAM_BLACK_Y, (size_t)w * h);
    memset(dst + (size_t)w * h, URDP_CAM_BLACK_UV, (size_t)w * h / 2);
}

// urdp_nv12_fit scales an even-sized NV12 source into an even-sized NV12
// destination with nearest-neighbour sampling, preserving aspect ratio and
// filling the bars with black. The image rectangle is aligned to even
// coordinates so chroma samples line up with luma.
static inline void urdp_nv12_fit(uint8_t *dst, uint32_t dw, uint32_t dh, const uint8_t *src,
                                 uint32_t sw, uint32_t sh) {
    uint32_t fw = dw, fh = dh;
    if ((uint64_t)sw * dh > (uint64_t)sh * dw)
        fh = (uint32_t)((uint64_t)dw * sh / sw);
    else
        fw = (uint32_t)((uint64_t)dh * sw / sh);
    fw &= ~1u;
    fh &= ~1u;
    if (fw < 2)
        fw = 2;
    if (fh < 2)
        fh = 2;
    uint32_t ox = ((dw - fw) / 2) & ~1u, oy = ((dh - fh) / 2) & ~1u;
    if (fw != dw || fh != dh)
        urdp_nv12_black(dst, dw, dh);
    for (uint32_t y = 0; y < fh; y++) {
        const uint8_t *row = src + (uint64_t)y * sh / fh * sw;
        uint8_t *out = dst + (size_t)(oy + y) * dw + ox;
        for (uint32_t x = 0; x < fw; x++)
            out[x] = row[(uint64_t)x * sw / fw];
    }
    const uint8_t *suv = src + (size_t)sw * sh;
    uint8_t *duv = dst + (size_t)dw * dh;
    for (uint32_t y = 0; y < fh / 2; y++) {
        const uint8_t *row = suv + (uint64_t)y * (sh / 2) / (fh / 2) * sw;
        uint8_t *out = duv + (size_t)(oy / 2 + y) * dw + ox;
        for (uint32_t x = 0; x < fw / 2; x++) {
            size_t sx = (uint64_t)x * (sw / 2) / (fw / 2) * 2;
            out[2 * x] = row[sx];
            out[2 * x + 1] = row[sx + 1];
        }
    }
}
