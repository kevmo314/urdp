// Package vdev exposes the viewer's devices on the host as virtual devices:
// their microphone as a host audio input, their webcam as a host camera,
// and their gamepads as host game controllers.
package vdev

import "errors"

// ErrUnsupported is returned when the platform, or a driver it needs, is
// unavailable. The error text should tell the user what to install.
var ErrUnsupported = errors.New("virtual device not supported on this host")

// Microphone is a virtual audio input on the host fed with the viewer's
// microphone.
type Microphone interface {
	// Write plays interleaved 48kHz stereo PCM into the virtual input. It
	// must not block for longer than the audio takes to play and should
	// drop audio rather than build up latency.
	Write(pcm []int16) error
	Close() error
}

// Image is a planar I420 (YUV 4:2:0) picture.
type Image struct {
	Width, Height int
	Y, U, V       []byte
	YStride       int
	UStride       int
	VStride       int
}

// Camera is a virtual webcam on the host fed with the viewer's camera.
type Camera interface {
	// WriteFrame shows img on the virtual camera. The size may change
	// between calls.
	WriteFrame(img *Image) error
	Close() error
}

// GamepadState follows the browser Gamepad API's "standard" mapping.
type GamepadState struct {
	// Buttons are the 17 standard buttons' values in [0,1] (analog for
	// the triggers, 6 and 7): A, B, X, Y, LB, RB, LT, RT, Back, Start,
	// LS, RS, Up, Down, Left, Right, Guide.
	Buttons [17]float64
	// Axes are left stick X/Y and right stick X/Y in [-1,1], +Y down.
	Axes [4]float64
}

// Gamepad is a virtual game controller on the host (Xbox 360 layout).
type Gamepad interface {
	Update(GamepadState) error
	Close() error
}

// OpenMicrophone creates the virtual microphone.
func OpenMicrophone() (Microphone, error) { return openMicrophone() }

// OpenCamera creates the virtual camera.
func OpenCamera() (Camera, error) { return openCamera() }

// OpenGamepad plugs in a new virtual controller.
func OpenGamepad() (Gamepad, error) { return openGamepad() }
