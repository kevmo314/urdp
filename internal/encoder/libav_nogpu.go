//go:build libav && !windows

package encoder

/*
#include <libavcodec/avcodec.h>
*/
import "C"

import (
	"errors"

	"github.com/kevmo314/urdp/internal/capture"
)

func libavGPUSetup(*C.AVCodecContext, any, int, int) error {
	return errors.New("GPU frames unsupported on this platform")
}

func (e *libavEncoder) sendGPU(*capture.Frame, int64, bool) error {
	return errors.New("GPU frames unsupported on this platform")
}
