package vdev

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"time"
)

// The ad-hoc signed driver bundle that `make mac` builds. Plain `go build`
// embeds only the placeholder, and installing then fails with a hint.
//
//go:embed all:macdriver/build
var driverFS embed.FS

const (
	driverBundle = "urdp-audio.driver"
	driverRoot   = "macdriver/build/" + driverBundle
	halDir       = "/Library/Audio/Plug-Ins/HAL"
)

func installAudioDriver() error {
	if _, err := fs.Stat(driverFS, driverRoot+"/Contents/MacOS/urdp_audio"); err != nil {
		return errors.New("this urdp was built without the audio driver; build it with `make mac`")
	}
	if os.Geteuid() != 0 {
		exe, _ := os.Executable()
		if exe == "" {
			exe = "urdp"
		}
		return fmt.Errorf("installing into %s needs root: run `sudo %s -install-audio-driver`", halDir, exe)
	}
	dst := filepath.Join(halDir, driverBundle)
	tmp := filepath.Join(halDir, "."+driverBundle+".new")
	old := filepath.Join(halDir, "."+driverBundle+".old")
	os.RemoveAll(tmp)
	os.RemoveAll(old)
	if err := os.MkdirAll(halDir, 0o755); err != nil {
		return err
	}
	err := fs.WalkDir(driverFS, driverRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(driverRoot, p)
		out := filepath.Join(tmp, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := driverFS.ReadFile(p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if path.Base(path.Dir(p)) == "MacOS" {
			mode = 0o755
		}
		return os.WriteFile(out, data, mode)
	})
	if err != nil {
		os.RemoveAll(tmp)
		return fmt.Errorf("writing driver: %w", err)
	}
	if _, err := os.Stat(dst); err == nil {
		if err := os.Rename(dst, old); err != nil {
			os.RemoveAll(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Rename(old, dst)
		return err
	}
	os.RemoveAll(old)

	// coreaudiod only scans for drivers at startup; launchd restarts it.
	if out, err := exec.Command("killall", "coreaudiod").CombinedOutput(); err != nil {
		return fmt.Errorf("installed %s, but restarting coreaudiod failed (%v: %s); reboot to load it", dst, err, out)
	}
	time.Sleep(time.Second) // let the old coreaudiod go first
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		if audioDriverLoaded() {
			fmt.Printf("installed %s; \"urdp Microphone\" is available\n", dst)
			return nil
		}
	}
	return fmt.Errorf("installed %s, but coreaudiod hasn't loaded it; check `log show --last 1m --predicate 'process CONTAINS \"audio\"'`", dst)
}
