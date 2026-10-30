package vdev

import "fmt"

// A virtual camera needs a CoreMediaIO camera extension, a system extension
// that only loads from an app signed with a Developer ID and the system
// extension entitlement (DAL plug-ins no longer load). A virtual gamepad
// needs IOHIDUserDevice, which requires the restricted
// com.apple.developer.hid.virtual.device entitlement: without it creation
// fails, even as root, and an ad-hoc signature claiming it is killed at
// launch.

func openCamera() (Camera, error) {
	return nil, fmt.Errorf("%w: a macOS virtual camera needs a Developer ID-signed camera extension", ErrUnsupported)
}

func openGamepad() (Gamepad, error) {
	return nil, fmt.Errorf("%w: a macOS virtual gamepad needs Apple's restricted HID virtual device entitlement", ErrUnsupported)
}
