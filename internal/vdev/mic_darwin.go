package vdev

/*
#cgo LDFLAGS: -framework CoreAudio -framework CoreFoundation -framework AVFoundation
#include <CoreAudio/CoreAudio.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>

// Plays into the output side of the "urdp Microphone" loopback driver
// with an IOProc: the device is ours, so its format is fixed at 48kHz
// float stereo and no converter or AUHAL is needed.

enum {
	MIC_RATE = 48000,
	MIC_RING = 16384,              // frames, power of two
	MIC_PRIME = MIC_RATE / 100,    // start playing once 10ms is queued
	MIC_HIGH = MIC_RATE * 60 / 1000, // above 60ms queued...
	MIC_TRIM = MIC_RATE * 30 / 1000, // ...skip ahead to the newest 30ms
};

typedef struct {
	AudioObjectID dev;
	AudioDeviceIOProcID proc;
	float ring[MIC_RING * 2];
	_Atomic uint64_t w, r; // frame counters; w by Write, r by the IOProc
	int primed;            // IOProc only
	_Atomic uint64_t played, dropped;
} mic_t;

static OSStatus mic_ioproc(AudioObjectID dev, const AudioTimeStamp *now, const AudioBufferList *in,
			   const AudioTimeStamp *inTime, AudioBufferList *out, const AudioTimeStamp *outTime, void *ud) {
	mic_t *m = ud;
	if (!out || out->mNumberBuffers < 1 || !out->mBuffers[0].mData)
		return noErr;
	AudioBuffer *b = &out->mBuffers[0];
	UInt32 ch = b->mNumberChannels ? b->mNumberChannels : 2;
	UInt32 frames = b->mDataByteSize / (sizeof(float) * ch);
	float *dst = b->mData;
	memset(dst, 0, b->mDataByteSize);
	uint64_t w = atomic_load_explicit(&m->w, memory_order_acquire);
	uint64_t r = atomic_load_explicit(&m->r, memory_order_relaxed);
	uint64_t avail = w - r;
	if (avail > MIC_HIGH) {
		atomic_fetch_add(&m->dropped, avail - MIC_TRIM);
		r = w - MIC_TRIM;
		avail = MIC_TRIM;
	}
	if (!m->primed) {
		if (avail < MIC_PRIME)
			return noErr;
		m->primed = 1;
	}
	UInt32 n = avail < frames ? (UInt32)avail : frames;
	for (UInt32 i = 0; i < n; i++) {
		const float *s = &m->ring[((r + i) & (MIC_RING - 1)) * 2];
		dst[i * ch] = s[0];
		if (ch > 1)
			dst[i * ch + 1] = s[1];
	}
	if (n < frames)
		m->primed = 0; // ran dry: rebuild a little cushion
	atomic_store_explicit(&m->r, r + n, memory_order_release);
	atomic_fetch_add(&m->played, n);
	return noErr;
}

// Returns the device's ID, or 0 if the driver isn't installed.
static AudioObjectID mic_find_device(void) {
	AudioObjectPropertyAddress a = {kAudioHardwarePropertyTranslateUIDToDevice,
		kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
	CFStringRef uid = CFSTR("urdp-microphone");
	AudioObjectID dev = kAudioObjectUnknown;
	UInt32 size = sizeof dev;
	if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &a, sizeof uid, &uid, &size, &dev) != noErr)
		return 0;
	return dev;
}

static OSStatus mic_open(mic_t **out) {
	AudioObjectID dev = mic_find_device();
	if (!dev)
		return kAudioHardwareBadDeviceError;
	mic_t *m = calloc(1, sizeof *m);
	if (!m)
		return kAudioHardwareUnspecifiedError;
	m->dev = dev;
	// 5ms IO cycles; the HAL keeps this per process.
	UInt32 bufFrames = 240;
	AudioObjectPropertyAddress a = {kAudioDevicePropertyBufferFrameSize,
		kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
	AudioObjectSetPropertyData(dev, &a, 0, NULL, sizeof bufFrames, &bufFrames);
	OSStatus err = AudioDeviceCreateIOProcID(dev, mic_ioproc, m, &m->proc);
	if (err == noErr && (err = AudioDeviceStart(dev, m->proc)) != noErr)
		AudioDeviceDestroyIOProcID(dev, m->proc);
	if (err != noErr) {
		free(m);
		return err;
	}
	*out = m;
	return noErr;
}

// Queues interleaved stereo int16; drops what doesn't fit.
static void mic_write(mic_t *m, const int16_t *pcm, int frames) {
	uint64_t w = atomic_load_explicit(&m->w, memory_order_relaxed);
	uint64_t r = atomic_load_explicit(&m->r, memory_order_acquire);
	uint64_t space = MIC_RING - (w - r);
	if ((uint64_t)frames > space) {
		atomic_fetch_add(&m->dropped, frames - space);
		frames = (int)space;
	}
	for (int i = 0; i < frames; i++) {
		float *d = &m->ring[((w + i) & (MIC_RING - 1)) * 2];
		d[0] = pcm[2 * i] / 32768.0f;
		d[1] = pcm[2 * i + 1] / 32768.0f;
	}
	atomic_store_explicit(&m->w, w + frames, memory_order_release);
}

static void mic_stats(mic_t *m, uint64_t *played, uint64_t *dropped, uint64_t *queued) {
	*played = atomic_load(&m->played);
	*dropped = atomic_load(&m->dropped);
	*queued = atomic_load(&m->w) - atomic_load(&m->r);
}

static void mic_close(mic_t *m) {
	AudioDeviceStop(m->dev, m->proc);
	AudioDeviceDestroyIOProcID(m->dev, m->proc);
	free(m);
}

int urdp_mic_authorized(void);

// A recorder on the input side, for tests: the first channel of whatever
// apps would record, up to cap frames.
typedef struct {
	AudioObjectID dev;
	AudioDeviceIOProcID proc;
	float *buf;
	uint64_t cap;
	_Atomic uint64_t n;
} rec_t;

static OSStatus rec_ioproc(AudioObjectID dev, const AudioTimeStamp *now, const AudioBufferList *in,
			   const AudioTimeStamp *inTime, AudioBufferList *out, const AudioTimeStamp *outTime, void *ud) {
	rec_t *r = ud;
	if (!in || in->mNumberBuffers < 1 || !in->mBuffers[0].mData)
		return noErr;
	const AudioBuffer *b = &in->mBuffers[0];
	UInt32 ch = b->mNumberChannels ? b->mNumberChannels : 1;
	UInt32 frames = b->mDataByteSize / (sizeof(float) * ch);
	const float *src = b->mData;
	uint64_t n = atomic_load(&r->n);
	for (UInt32 i = 0; i < frames && n < r->cap; i++)
		r->buf[n++] = src[i * ch];
	atomic_store(&r->n, n);
	return noErr;
}

static OSStatus rec_open(float *buf, uint64_t cap, rec_t **out) {
	AudioObjectID dev = mic_find_device();
	if (!dev)
		return kAudioHardwareBadDeviceError;
	rec_t *r = calloc(1, sizeof *r);
	r->dev = dev;
	r->buf = buf;
	r->cap = cap;
	OSStatus err = AudioDeviceCreateIOProcID(dev, rec_ioproc, r, &r->proc);
	if (err == noErr && (err = AudioDeviceStart(dev, r->proc)) != noErr)
		AudioDeviceDestroyIOProcID(dev, r->proc);
	if (err != noErr) {
		free(r);
		return err;
	}
	*out = r;
	return noErr;
}

// Stops recording and returns the number of frames recorded.
static uint64_t rec_close(rec_t *r) {
	AudioDeviceStop(r->dev, r->proc);
	AudioDeviceDestroyIOProcID(r->dev, r->proc);
	uint64_t n = atomic_load(&r->n);
	free(r);
	return n;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"
)

const micRate = C.MIC_RATE

var errNoAudioDriver = fmt.Errorf("%w: the urdp Microphone audio driver is not installed; "+
	"install it with `sudo urdp -install-audio-driver`", ErrUnsupported)

type coreAudioMic struct {
	mu sync.Mutex
	m  *C.mic_t
}

func openMicrophone() (Microphone, error) {
	var m *C.mic_t
	if err := C.mic_open(&m); err != 0 {
		if err == C.kAudioHardwareBadDeviceError {
			return nil, errNoAudioDriver
		}
		return nil, fmt.Errorf("coreaudio: starting urdp Microphone: %w", osStatus(err))
	}
	return &coreAudioMic{m: m}, nil
}

func (c *coreAudioMic) Write(pcm []int16) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		return errors.New("coreaudio: microphone closed")
	}
	if n := len(pcm) / 2; n > 0 {
		C.mic_write(c.m, (*C.int16_t)(unsafe.Pointer(&pcm[0])), C.int(n))
	}
	return nil
}

type micStats struct{ Played, Dropped, Queued uint64 }

func (c *coreAudioMic) stats() micStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	var p, d, q C.uint64_t
	C.mic_stats(c.m, &p, &d, &q)
	return micStats{uint64(p), uint64(d), uint64(q)}
}

func (c *coreAudioMic) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m != nil {
		C.mic_close(c.m)
		c.m = nil
	}
	return nil
}

func audioDriverLoaded() bool { return C.mic_find_device() != 0 }

// micAuthorized reports whether this process has the Microphone
// permission, without which recording returns silence.
func micAuthorized() bool { return C.urdp_mic_authorized() != 0 }

// micRecorder records the first channel of the device's input side.
type micRecorder struct {
	r   *C.rec_t
	buf *C.float
	cap int
}

func recordMicrophone(frames int) (*micRecorder, error) {
	buf := (*C.float)(C.calloc(C.size_t(frames), C.sizeof_float))
	var r *C.rec_t
	if err := C.rec_open(buf, C.uint64_t(frames), &r); err != 0 {
		C.free(unsafe.Pointer(buf))
		return nil, osStatus(err)
	}
	return &micRecorder{r, buf, frames}, nil
}

// stop ends the recording and returns it.
func (m *micRecorder) stop() []float32 {
	n := int(C.rec_close(m.r))
	out := make([]float32, n)
	copy(out, unsafe.Slice((*float32)(unsafe.Pointer(m.buf)), n))
	C.free(unsafe.Pointer(m.buf))
	return out
}

// osStatus renders a CoreAudio status, which is often a four-char code.
type osStatus C.OSStatus

func (s osStatus) Error() string {
	u := uint32(s)
	b := []byte{byte(u >> 24), byte(u >> 16), byte(u >> 8), byte(u)}
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return fmt.Sprintf("OSStatus %d", int32(s))
		}
	}
	return fmt.Sprintf("OSStatus '%s'", b)
}
