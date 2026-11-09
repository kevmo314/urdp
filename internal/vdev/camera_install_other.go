//go:build !windows

package vdev

import "fmt"

// InstallCamera is only needed on Windows; other platforms create their
// virtual camera at run time without installing anything.
func InstallCamera() error {
	return fmt.Errorf("%w: only Windows needs the urdp Camera installed", ErrUnsupported)
}

// UninstallCamera mirrors InstallCamera.
func UninstallCamera() error {
	return fmt.Errorf("%w: only Windows installs the urdp Camera", ErrUnsupported)
}
