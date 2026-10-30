//go:build !linux && !windows

package audio

import "errors"

func openLoopback() (Source, error) {
	return nil, errors.New("audio capture not supported on this platform")
}
