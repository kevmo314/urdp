#ifndef URDP_PWCAM_LINUX_H
#define URDP_PWCAM_LINUX_H

#include <stdint.h>

struct pwcam;

struct pwcam *pwcam_open(const char *name, const char *desc, char *err, int errlen);
int pwcam_write(struct pwcam *c, const uint8_t *y, int ys, const uint8_t *u, int us,
		const uint8_t *v, int vs, int w, int h, char *err, int errlen);
void pwcam_stats(struct pwcam *c, int *w, int *h, uint32_t *fmt, uint64_t *frames, uint32_t *node);
void pwcam_close(struct pwcam *c);

#endif
