package vdev

// Placeholders until the linux backends land.

func openMicrophone() (Microphone, error) { return nil, ErrUnsupported }
func openCamera() (Camera, error)         { return nil, ErrUnsupported }
func openGamepad() (Gamepad, error)       { return nil, ErrUnsupported }
