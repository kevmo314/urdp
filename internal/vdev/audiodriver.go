package vdev

// InstallAudioDriver installs the "urdp Microphone" CoreAudio driver that
// the macOS virtual microphone plays into. It needs root. Other platforms
// need no driver and return ErrUnsupported.
func InstallAudioDriver() error { return installAudioDriver() }
