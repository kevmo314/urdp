//go:build !linux && !windows && !darwin

package vdev

func openMicrophone() (Microphone, error) { return nil, ErrUnsupported }
func openCamera() (Camera, error)         { return nil, ErrUnsupported }
func openGamepad() (Gamepad, error)       { return nil, ErrUnsupported }
