package audio

/*
#cgo pkg-config: opus
#include <opus.h>

static OpusEncoder *opus_new(int rate, int ch, int bitrate, int *err) {
	OpusEncoder *e = opus_encoder_create(rate, ch, OPUS_APPLICATION_RESTRICTED_LOWDELAY, err);
	if (!e) return NULL;
	opus_encoder_ctl(e, OPUS_SET_BITRATE(bitrate));
	opus_encoder_ctl(e, OPUS_SET_COMPLEXITY(5));
	opus_encoder_ctl(e, OPUS_SET_SIGNAL(OPUS_SIGNAL_MUSIC));
	return e;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// OpusEncoder wraps libopus configured for low-delay stereo.
type OpusEncoder struct{ e *C.OpusEncoder }

func NewOpusEncoder(bitrate int) (*OpusEncoder, error) {
	var cerr C.int
	e := C.opus_new(SampleRate, Channels, C.int(bitrate), &cerr)
	if e == nil {
		return nil, fmt.Errorf("opus: %s", C.GoString(C.opus_strerror(cerr)))
	}
	return &OpusEncoder{e: e}, nil
}

// Encode compresses one frame of interleaved PCM into out, returning the
// packet length.
func (o *OpusEncoder) Encode(pcm []int16, out []byte) (int, error) {
	n := C.opus_encode(o.e, (*C.opus_int16)(unsafe.Pointer(unsafe.SliceData(pcm))), C.int(len(pcm)/Channels),
		(*C.uchar)(unsafe.SliceData(out)), C.opus_int32(len(out)))
	if n < 0 {
		return 0, fmt.Errorf("opus: %s", C.GoString(C.opus_strerror(n)))
	}
	return int(n), nil
}

func (o *OpusEncoder) Close() {
	if o.e != nil {
		C.opus_encoder_destroy(o.e)
		o.e = nil
	}
}

// OpusDecoder wraps a libopus decoder producing 48kHz stereo PCM.
type OpusDecoder struct{ d *C.OpusDecoder }

func NewOpusDecoder() (*OpusDecoder, error) {
	var cerr C.int
	d := C.opus_decoder_create(SampleRate, Channels, &cerr)
	if d == nil {
		return nil, fmt.Errorf("opus: %s", C.GoString(C.opus_strerror(cerr)))
	}
	return &OpusDecoder{d: d}, nil
}

// Decode decodes one packet into pcm (interleaved), returning the number
// of samples per channel. A nil packet conceals a lost one.
func (o *OpusDecoder) Decode(pkt []byte, pcm []int16) (int, error) {
	var data *C.uchar
	if len(pkt) > 0 {
		data = (*C.uchar)(unsafe.SliceData(pkt))
	}
	n := C.opus_decode(o.d, data, C.opus_int32(len(pkt)),
		(*C.opus_int16)(unsafe.Pointer(unsafe.SliceData(pcm))), C.int(len(pcm)/Channels), 0)
	if n < 0 {
		return 0, fmt.Errorf("opus: %s", C.GoString(C.opus_strerror(n)))
	}
	return int(n), nil
}

func (o *OpusDecoder) Close() {
	if o.d != nil {
		C.opus_decoder_destroy(o.d)
		o.d = nil
	}
}
