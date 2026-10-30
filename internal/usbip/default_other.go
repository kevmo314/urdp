//go:build !linux && !windows

package usbip

import "errors"

// Default reports that USB passthrough isn't supported on this platform.
func Default() (Attacher, error) {
	return nil, errors.New("USB passthrough is only supported on Linux and Windows hosts")
}
