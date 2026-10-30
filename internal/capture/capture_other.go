//go:build !linux && !windows

package capture

import "errors"

var errUnsupported = errors.New("screen capture not supported on this platform")

func openDesktop() (Source, error)         { return nil, errUnsupported }
func openWindow(id uint64) (Source, error) { return nil, errUnsupported }
func listWindows() ([]Window, error)       { return nil, errUnsupported }
