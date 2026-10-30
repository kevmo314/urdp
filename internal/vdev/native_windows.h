#pragma once
#include <stdint.h>
typedef struct mic_native mic_native;
long mic_open(mic_native **out, const char *name);
long mic_write(mic_native *s, const int16_t *pcm, unsigned frames);
void mic_close(mic_native *s);
typedef struct camera_native camera_native;
unsigned long camera_open(camera_native **out);
void camera_write(camera_native *s, const uint8_t *y, const uint8_t *u, const uint8_t *v, int width,
                  int height, int ys, int us, int vs);
void camera_close(camera_native *s);
typedef struct pad_native pad_native;
unsigned long pad_open(pad_native **out);
unsigned long pad_update(pad_native *s, uint16_t buttons, uint8_t lt, uint8_t rt, int16_t lx,
                         int16_t ly, int16_t rx, int16_t ry);
unsigned long pad_close(pad_native *s);
