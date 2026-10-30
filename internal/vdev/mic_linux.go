package vdev

/*
#cgo pkg-config: libpulse libpulse-simple
#include <pulse/pulseaudio.h>
#include <pulse/simple.h>
#include <pulse/error.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
	pa_threaded_mainloop *ml;
	pa_context *ctx;
} pa_ctl;

typedef struct {
	pa_threaded_mainloop *ml;
	uint32_t idx;
	int done;
	const char *name, *needle; // module lookup
} pa_op_state;

static void ctl_state_cb(pa_context *c, void *ud) {
	pa_threaded_mainloop_signal((pa_threaded_mainloop *)ud, 0);
}

static void ctl_close(pa_ctl *p) {
	if (!p) return;
	if (p->ml) pa_threaded_mainloop_stop(p->ml);
	if (p->ctx) { pa_context_disconnect(p->ctx); pa_context_unref(p->ctx); }
	if (p->ml) pa_threaded_mainloop_free(p->ml);
	free(p);
}

static pa_ctl *ctl_open(int *err) {
	pa_ctl *p = calloc(1, sizeof(pa_ctl));
	*err = PA_ERR_INTERNAL;
	if (!p) return NULL;
	p->ml = pa_threaded_mainloop_new();
	if (!p->ml) goto fail;
	p->ctx = pa_context_new(pa_threaded_mainloop_get_api(p->ml), "urdp");
	if (!p->ctx) goto fail;
	pa_context_set_state_callback(p->ctx, ctl_state_cb, p->ml);
	if (pa_context_connect(p->ctx, NULL, PA_CONTEXT_NOAUTOSPAWN, NULL) < 0) {
		*err = pa_context_errno(p->ctx);
		goto fail;
	}
	pa_threaded_mainloop_lock(p->ml);
	if (pa_threaded_mainloop_start(p->ml) < 0) { pa_threaded_mainloop_unlock(p->ml); goto fail; }
	for (;;) {
		pa_context_state_t st = pa_context_get_state(p->ctx);
		if (st == PA_CONTEXT_READY) break;
		if (!PA_CONTEXT_IS_GOOD(st)) {
			*err = pa_context_errno(p->ctx);
			pa_threaded_mainloop_unlock(p->ml);
			goto fail;
		}
		pa_threaded_mainloop_wait(p->ml);
	}
	pa_threaded_mainloop_unlock(p->ml);
	*err = 0;
	return p;
fail:
	ctl_close(p);
	return NULL;
}

static void ctl_index_cb(pa_context *c, uint32_t idx, void *ud) {
	pa_op_state *s = ud;
	s->idx = idx;
	s->done = 1;
	pa_threaded_mainloop_signal(s->ml, 0);
}

static void ctl_success_cb(pa_context *c, int ok, void *ud) {
	pa_op_state *s = ud;
	s->idx = ok ? 0 : PA_INVALID_INDEX;
	s->done = 1;
	pa_threaded_mainloop_signal(s->ml, 0);
}

static void ctl_module_cb(pa_context *c, const pa_module_info *i, int eol, void *ud) {
	pa_op_state *s = ud;
	if (eol) {
		s->done = 1;
		pa_threaded_mainloop_signal(s->ml, 0);
		return;
	}
	if (s->idx == PA_INVALID_INDEX && i->name && i->argument &&
	    !strcmp(i->name, s->name) && strstr(i->argument, s->needle))
		s->idx = i->index;
}

static uint32_t ctl_wait(pa_ctl *p, pa_operation *o, pa_op_state *s) {
	if (!o) return PA_INVALID_INDEX;
	while (!s->done && pa_operation_get_state(o) == PA_OPERATION_RUNNING)
		pa_threaded_mainloop_wait(p->ml);
	pa_operation_unref(o);
	return s->done ? s->idx : PA_INVALID_INDEX;
}

static uint32_t ctl_load(pa_ctl *p, const char *name, const char *args) {
	pa_op_state s = { .ml = p->ml, .idx = PA_INVALID_INDEX };
	pa_threaded_mainloop_lock(p->ml);
	uint32_t r = ctl_wait(p, pa_context_load_module(p->ctx, name, args, ctl_index_cb, &s), &s);
	pa_threaded_mainloop_unlock(p->ml);
	return r;
}

static int ctl_unload(pa_ctl *p, uint32_t idx) {
	pa_op_state s = { .ml = p->ml, .idx = PA_INVALID_INDEX };
	pa_threaded_mainloop_lock(p->ml);
	uint32_t r = ctl_wait(p, pa_context_unload_module(p->ctx, idx, ctl_success_cb, &s), &s);
	pa_threaded_mainloop_unlock(p->ml);
	return r == 0 ? 0 : -1;
}

// Index of a loaded module called name whose arguments contain needle.
static uint32_t ctl_find(pa_ctl *p, const char *name, const char *needle) {
	pa_op_state s = { .ml = p->ml, .idx = PA_INVALID_INDEX, .name = name, .needle = needle };
	pa_threaded_mainloop_lock(p->ml);
	pa_operation *o = pa_context_get_module_info_list(p->ctx, ctl_module_cb, &s);
	if (o) {
		while (!s.done && pa_operation_get_state(o) == PA_OPERATION_RUNNING)
			pa_threaded_mainloop_wait(p->ml);
		pa_operation_unref(o);
	}
	pa_threaded_mainloop_unlock(p->ml);
	return s.idx;
}

// The server buffer is twice the target so writes never block; the caller
// drops audio to stay at the target. A small prebuf starts playback fast.
static pa_simple *mic_play_open(const char *dev, int rate, int channels, int target, int *err) {
	pa_sample_spec ss = { .format = PA_SAMPLE_S16LE, .rate = rate, .channels = channels };
	pa_buffer_attr attr = {
		.maxlength = target * 2, .tlength = target * 2,
		.prebuf = target / 3, .minreq = (uint32_t)-1, .fragsize = (uint32_t)-1,
	};
	return pa_simple_new(NULL, "urdp", PA_STREAM_PLAYBACK, dev,
		"viewer microphone", &ss, NULL, &attr, err);
}
*/
import "C"

import (
	"fmt"
	"sync"
	"unsafe"
)

const (
	micSink     = "urdp_mic_sink"
	micSource   = "urdp_mic"
	micRate     = 48000
	micChannels = 2
	// Audio queued in the stream. Once that much is waiting, further writes
	// are dropped, so a viewer whose clock runs fast never adds latency.
	micTargetMs = 30
)

var (
	micArgs = []struct{ module, args, needle string }{
		{"module-null-sink",
			"sink_name=" + micSink + " rate=48000 channels=2 format=s16le " +
				`sink_properties='device.description="urdp Microphone (output)" device.class=abstract'`,
			"sink_name=" + micSink + " "},
		{"module-remap-source",
			"master=" + micSink + ".monitor source_name=" + micSource + " " +
				`source_properties='device.description="urdp Microphone"'`,
			"source_name=" + micSource + " "},
	}

	micMu      sync.Mutex
	micRefs    int
	micModules []C.uint32_t
)

func paError(err C.int) error {
	return fmt.Errorf("pulseaudio: %s", C.GoString(C.pa_strerror(err)))
}

func withPulse(f func(*C.pa_ctl) error) error {
	var cerr C.int
	ctl := C.ctl_open(&cerr)
	if ctl == nil {
		return paError(cerr)
	}
	defer C.ctl_close(ctl)
	return f(ctl)
}

// micAcquire loads the shared modules on first use. Modules left behind by
// a crashed run are adopted rather than duplicated.
func micAcquire() error {
	micMu.Lock()
	defer micMu.Unlock()
	if micRefs > 0 {
		micRefs++
		return nil
	}
	err := withPulse(func(ctl *C.pa_ctl) error {
		var mods []C.uint32_t
		for _, m := range micArgs {
			name, args, needle := C.CString(m.module), C.CString(m.args), C.CString(m.needle)
			idx := C.ctl_find(ctl, name, needle)
			if idx == C.PA_INVALID_INDEX {
				idx = C.ctl_load(ctl, name, args)
			}
			C.free(unsafe.Pointer(name))
			C.free(unsafe.Pointer(args))
			C.free(unsafe.Pointer(needle))
			if idx == C.PA_INVALID_INDEX {
				for i := len(mods) - 1; i >= 0; i-- {
					C.ctl_unload(ctl, mods[i])
				}
				return fmt.Errorf("pulseaudio: loading %s failed", m.module)
			}
			mods = append(mods, idx)
		}
		micModules = mods
		return nil
	})
	if err != nil {
		return err
	}
	micRefs = 1
	return nil
}

func micRelease() error {
	micMu.Lock()
	defer micMu.Unlock()
	if micRefs--; micRefs > 0 {
		return nil
	}
	mods := micModules
	micModules = nil
	return withPulse(func(ctl *C.pa_ctl) error {
		var err error
		for i := len(mods) - 1; i >= 0; i-- {
			if C.ctl_unload(ctl, mods[i]) < 0 && err == nil {
				err = fmt.Errorf("pulseaudio: unloading module %d failed", mods[i])
			}
		}
		return err
	})
}

type pulseMic struct {
	mu sync.Mutex
	s  *C.pa_simple
}

func openMicrophone() (Microphone, error) {
	if err := micAcquire(); err != nil {
		return nil, err
	}
	dev := C.CString(micSink)
	defer C.free(unsafe.Pointer(dev))
	var cerr C.int
	target := micRate * micChannels * 2 * micTargetMs / 1000
	s := C.mic_play_open(dev, micRate, micChannels, C.int(target), &cerr)
	if s == nil {
		micRelease()
		return nil, paError(cerr)
	}
	return &pulseMic{s: s}, nil
}

func (m *pulseMic) Write(pcm []int16) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.s == nil {
		return fmt.Errorf("pulseaudio: microphone closed")
	}
	if len(pcm) == 0 {
		return nil
	}
	var cerr C.int
	if lat := C.pa_simple_get_latency(m.s, &cerr); lat != C.pa_usec_t(^uint64(0)) && lat > micTargetMs*1000 {
		return nil
	}
	if C.pa_simple_write(m.s, unsafe.Pointer(unsafe.SliceData(pcm)), C.size_t(len(pcm)*2), &cerr) < 0 {
		return paError(cerr)
	}
	return nil
}

func (m *pulseMic) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.s == nil {
		return nil
	}
	C.pa_simple_free(m.s)
	m.s = nil
	return micRelease()
}
