//go:build !linux && !windows && !darwin

package capture

import (
	"errors"
	"image"
)

var errUnsupported = errors.New("screen capture not supported on this platform")

func openDesktop() (Source, error)                        { return nil, errUnsupported }
func openWindow(id uint64) (Source, error)                { return nil, errUnsupported }
func listWindows() ([]Window, error)                      { return nil, errUnsupported }
func windowIcon(id uint64, size int) (image.Image, error) { return nil, errUnsupported }
func openDisplay(i int) (Source, error)                   { return nil, errUnsupported }
func listDisplays() ([]Display, error)                    { return nil, errUnsupported }
