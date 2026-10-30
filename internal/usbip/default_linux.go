package usbip

// Default returns the platform's attacher and whether it can work. On
// Linux that is direct attachment to vhci-hcd.
func Default() (Attacher, error) {
	v := VHCI{}
	return v, v.Check()
}
