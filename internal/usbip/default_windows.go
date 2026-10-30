package usbip

import (
	"fmt"
	"os/exec"
)

// Default returns the platform's attacher and whether it can work. On
// Windows that is usbip-win2's usbip.exe attaching to a USB/IP server we
// run on 127.0.0.1.
func Default() (Attacher, error) {
	c := UsbipWin2()
	if _, err := exec.LookPath(c.AttachArgs[0]); err != nil {
		return c, fmt.Errorf("%s not found; install usbip-win2 (https://github.com/vadimgrn/usbip-win2) and put it on PATH: %w", c.AttachArgs[0], err)
	}
	return c, nil
}
