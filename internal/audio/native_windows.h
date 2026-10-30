#include <stdint.h>
typedef struct audio_native audio_native;
long audio_open(audio_native **out);
long audio_poll(audio_native *s, int16_t *dst, unsigned capacity, unsigned *written);
long audio_wait(audio_native *s, unsigned timeout_ms);
void audio_close(audio_native *s);
