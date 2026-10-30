package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kevmo314/urdp/internal/vdev"
)

// -install-audio-driver acts as soon as it's parsed and exits, so it needs
// nothing from main.
func init() {
	flag.BoolFunc("install-audio-driver", `macOS: install the "urdp Microphone" CoreAudio driver (run with sudo) and exit`,
		func(string) error {
			if err := vdev.InstallAudioDriver(); err != nil {
				fmt.Fprintln(os.Stderr, "urdp:", err)
				os.Exit(1)
			}
			os.Exit(0)
			return nil
		})
}
