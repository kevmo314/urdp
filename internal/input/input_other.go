//go:build !linux && !windows && !darwin

package input

import "errors"

const hostAutoRepeats = false

// New returns the platform injector.
func New() (Injector, error) {
	return nil, errors.New("input injection not supported on this platform")
}
