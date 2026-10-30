//go:build !darwin

package vdev

import "fmt"

func installAudioDriver() error {
	return fmt.Errorf("%w: only macOS needs an audio driver", ErrUnsupported)
}
