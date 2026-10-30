//go:build !linux && !windows

package input

import "errors"

// New returns the platform injector.
func New() (Injector, error) {
	return nil, errors.New("input injection not supported on this platform")
}
