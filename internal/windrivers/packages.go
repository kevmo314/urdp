// Package windrivers installs unmodified, released x64 driver packages. Only
// the explicit -install-drivers / -uninstall-drivers commands use it; the
// server never installs anything on its own.
package windrivers

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

//go:embed packages.zip
var packages []byte

type installed struct {
	Instances []string `json:"instances,omitempty"`
	Packages  []string `json:"packages,omitempty"`
}

type driver struct{ name, folder, inf, hardware, filter string }

var drivers = []driver{
	{"ViGEmBus", "vigem", "ViGEmBus.inf", `Nefarius\ViGEmBus\Gen1`, ""},
	{"USB/IP host controller", "usbip-0.9.8.1", "usbip2_ude.inf", `ROOT\USBIP_WIN2\UDE`, "usbip2_filter.inf"},
}

// Action is what the driver command line asked for.
type Action int

const (
	None Action = iota
	Install
	Uninstall
)

// ParseAction maps the -install-drivers / -uninstall-drivers flags to an
// action. Nothing is installed unless install is explicitly set.
func ParseAction(install, uninstall bool, logPath string) (Action, error) {
	switch {
	case install && uninstall:
		return None, errors.New("choose either -install-drivers or -uninstall-drivers")
	case install:
		return Install, nil
	case uninstall:
		return Uninstall, nil
	case logPath != "":
		return None, errors.New("-driver-log is internal to -install-drivers / -uninstall-drivers")
	}
	return None, nil
}

func extract(root string) error {
	archive, err := zip.NewReader(bytes.NewReader(packages), int64(len(packages)))
	if err != nil {
		return err
	}
	for _, entry := range archive.File {
		if !filepath.IsLocal(entry.Name) {
			return fmt.Errorf("invalid embedded package path %q", entry.Name)
		}
		path := filepath.Join(root, filepath.FromSlash(entry.Name))
		if entry.FileInfo().IsDir() {
			continue
		}
		src, err := entry.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(src)
		src.Close()
		if err != nil {
			return err
		}
		if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
			continue
		}
		if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		temp := path + ".tmp"
		if err = os.WriteFile(temp, data, 0644); err != nil {
			return err
		}
		if err = os.Rename(temp, path); err != nil {
			return err
		}
	}
	return nil
}
