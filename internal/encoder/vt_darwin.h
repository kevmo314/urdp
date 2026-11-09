#pragma once
#include <stddef.h>
#include <stdint.h>
typedef struct ur_vt ur_vt;
int ur_vt_supported(int hevc);
ur_vt *ur_vt_open(int w, int h, int fps, int kbps, int keyint_frames,
                  int keyint_seconds, int hevc, int *status);
void ur_vt_close(ur_vt *e);
int ur_vt_bitrate(ur_vt *e, int kbps);
// ur_vt_property reads back a numeric/boolean session property by short name
// (for tests); returns 0 and sets *value on success.
int ur_vt_property(ur_vt *e, const char *name, int64_t *value);
void *ur_vt_pixel(int w, int h);
void ur_vt_release(void *pixel);
int ur_vt_upload(void *pixel, const uint8_t *y, int ys, const uint8_t *uv,
                 int uvs);
// ur_vt_encode submits one frame and waits for its own output. On success
// *data/*size hold Annex B for that frame (owned by e, valid until the next
// call); *size is 0 if the encoder dropped the frame.
int ur_vt_encode(ur_vt *e, void *pixel, int64_t pts_ms, int key, void **data,
                 size_t *size, int *is_key);
