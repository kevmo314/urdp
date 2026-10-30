package audio

/*
#cgo pkg-config: libpulse-simple
#include <pulse/simple.h>
#include <pulse/error.h>
#include <stdlib.h>

static pa_simple *pa_open(int rate, int channels, int frag_bytes, int *err) {
	pa_sample_spec ss = { .format = PA_SAMPLE_S16LE, .rate = rate, .channels = channels };
	// A small fragsize is what makes the server deliver audio promptly
	// instead of batching ~2s of samples.
	// maxlength bounds how much audio queues up if we fall behind; older
	// samples are dropped rather than played late.
	pa_buffer_attr attr = {
		.maxlength = frag_bytes * 8, .tlength = (uint32_t)-1,
		.prebuf = (uint32_t)-1, .minreq = (uint32_t)-1,
		.fragsize = frag_bytes,
	};
	return pa_simple_new(NULL, "urdp", PA_STREAM_RECORD, "@DEFAULT_MONITOR@",
		"desktop audio", &ss, NULL, &attr, err);
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

type pulse struct{ s *C.pa_simple }

func openLoopback() (Source, error) {
	var cerr C.int
	frag := FrameSamples * Channels * 2
	s := C.pa_open(SampleRate, Channels, C.int(frag), &cerr)
	if s == nil {
		return nil, fmt.Errorf("pulseaudio: %s", C.GoString(C.pa_strerror(cerr)))
	}
	return &pulse{s: s}, nil
}

func (p *pulse) Read(buf []int16) error {
	var cerr C.int
	if C.pa_simple_read(p.s, unsafe.Pointer(unsafe.SliceData(buf)), C.size_t(len(buf)*2), &cerr) < 0 {
		return fmt.Errorf("pulseaudio: %s", C.GoString(C.pa_strerror(cerr)))
	}
	return nil
}

func (p *pulse) Close() error {
	if p.s != nil {
		C.pa_simple_free(p.s)
		p.s = nil
	}
	return nil
}
