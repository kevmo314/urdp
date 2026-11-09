package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kevmo314/urdp/internal/vdev"
)

// installCommand is a flag that installs or removes something on the host
// and exits. These are the only code paths that change system state; the
// server itself only detects whether a device is present and tells the
// viewer what to run (see vdev's ErrUnsupported messages).
type installCommand struct {
	name, usage string
	run         func() error
	done        string
}

func installCommands() []installCommand {
	return []installCommand{
		{"install-audio-driver", `macOS: install the "urdp Microphone" CoreAudio driver (run with sudo) and exit`,
			vdev.InstallAudioDriver, ""},
		{"install-camera", `Windows 11: install the "urdp Camera" media source (HKLM COM registration + DLL in ProgramData; run as Administrator) and exit`,
			vdev.InstallCamera, "urdp Camera installed; start urdp normally to make the camera available"},
		{"uninstall-camera", `Windows: remove everything -install-camera wrote (run as Administrator) and exit`,
			vdev.UninstallCamera, "urdp Camera uninstalled"},
	}
}

// registerInstallFlags makes each command act as soon as its flag is parsed
// and exit, so it needs nothing from main and never starts the server.
func registerInstallFlags(fs *flag.FlagSet, cmds []installCommand, exit func(int)) {
	for _, c := range cmds {
		fs.BoolFunc(c.name, c.usage, func(string) error {
			if err := c.run(); err != nil {
				fmt.Fprintln(os.Stderr, "urdp:", err)
				exit(1)
				return err
			}
			if c.done != "" {
				fmt.Fprintln(os.Stderr, "urdp:", c.done)
			}
			exit(0)
			return nil
		})
	}
}

func init() {
	registerInstallFlags(flag.CommandLine, installCommands(), os.Exit)
}
