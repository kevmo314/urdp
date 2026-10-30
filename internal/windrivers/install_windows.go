package windrivers

/*
#cgo windows LDFLAGS: -lsetupapi -lnewdev -lcfgmgr32 -lshell32 -lole32
#include <stdlib.h>
#include "native_windows.h"
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func winerr(operation string, code C.DWORD) error {
	if code == 0 {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, syscall.Errno(code))
}
func wide(s string) *C.wchar_t { return (*C.wchar_t)(unsafe.Pointer(windows.StringToUTF16Ptr(s))) }
func fromWide(buf []C.wchar_t) string {
	return windows.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(&buf[0])), len(buf)))
}

// Run elevates once, waits for the child, and relays its complete output.
// logPath is an internal handoff for the elevated child.
func Run(uninstall bool, logPath string, notices []byte) error {
	if runtime.GOARCH != "amd64" {
		return errors.New("bundled drivers support Windows x64 only")
	}
	admin := windows.GetCurrentProcessToken().IsElevated()
	if !admin {
		if logPath != "" {
			return errors.New("driver child is not elevated")
		}
		return elevate(uninstall)
	}
	var out io.Writer = os.Stdout
	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	err := install(uninstall, out, notices)
	if err != nil {
		fmt.Fprintln(out, "Driver setup failed:", err)
	}
	return err
}

func elevate(uninstall bool) error {
	dir, err := os.MkdirTemp("", "urdp-driver-log-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	logPath := filepath.Join(dir, "output.log")
	if err = os.WriteFile(logPath, nil, 0600); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	action := "-install-drivers"
	if uninstall {
		action = "-uninstall-drivers"
	}
	args := action + " -driver-log " + windows.EscapeArg(logPath)
	fmt.Printf("UAC PROMPT: urdp %s (one elevated run for bundled drivers)\n", strings.TrimPrefix(action, "-"))
	var exitCode C.DWORD
	code := C.driver_elevate(wide(exe), wide(args), &exitCode)
	if data, readErr := os.ReadFile(logPath); readErr == nil {
		os.Stdout.Write(data)
	}
	if err = winerr("elevated driver setup (five-minute limit)", code); err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("elevated driver setup exited with code %d", exitCode)
	}
	return nil
}

func install(uninstall bool, out io.Writer, notices []byte) error {
	root, err := Directory()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(root, 0755); err != nil {
		return err
	}
	// Serialize installers, including uninstall, across all urdp processes.
	mutex, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(`Global\urdp-driver-setup`))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(mutex)
	wait, err := windows.WaitForSingleObject(mutex, 0)
	if err != nil {
		return err
	}
	if wait != windows.WAIT_OBJECT_0 && wait != windows.WAIT_ABANDONED {
		return errors.New("another urdp driver setup is running")
	}
	defer windows.ReleaseMutex(mutex)
	state := installed{}
	stateFile := filepath.Join(root, "installed.json")
	if data, e := os.ReadFile(stateFile); e == nil {
		if e = json.Unmarshal(data, &state); e != nil {
			return fmt.Errorf("read ownership record: %w", e)
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	save := func() error {
		data, e := json.MarshalIndent(state, "", "  ")
		if e != nil {
			return e
		}
		temp := stateFile + ".tmp"
		if e = os.WriteFile(temp, data, 0600); e != nil {
			return e
		}
		return os.Rename(temp, stateFile)
	}
	if uninstall {
		return remove(root, &state, save, out)
	}
	if err = extract(root); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(root, "THIRD_PARTY_NOTICES"), notices, 0644); err != nil {
		return err
	}
	stage := func(inf string) error {
		published := make([]C.wchar_t, windows.MAX_PATH)
		var added C.int
		if e := winerr("stage "+inf, C.driver_stage(wide(inf), &published[0], C.DWORD(len(published)), &added)); e != nil {
			return e
		}
		if added != 0 {
			state.Packages = append(state.Packages, filepath.Base(fromWide(published)))
			if e := save(); e != nil {
				return e
			}
		}
		return run(out, "pnputil.exe", "/add-driver", inf, "/install")
	}
	for _, d := range drivers {
		instance := make([]C.wchar_t, 512)
		var healthy C.int
		code := C.driver_find(wide(d.hardware), &instance[0], C.DWORD(len(instance)), &healthy)
		if code == 0 {
			fmt.Fprintf(out, "%s: existing device %s (healthy=%t); retained\n", d.name, fromWide(instance), healthy != 0)
			if healthy == 0 {
				return fmt.Errorf("%s exists but is not started; repair it in Device Manager before retrying", d.name)
			}
			continue
		}
		if code != C.ERROR_NOT_FOUND {
			return winerr("find "+d.name, code)
		}
		if d.filter != "" {
			if err = stage(filepath.Join(root, d.folder, d.filter)); err != nil {
				return err
			}
		}
		inf := filepath.Join(root, d.folder, d.inf)
		if err = stage(inf); err != nil {
			return err
		}
		var reboot C.int
		if err = winerr("create "+d.name, C.driver_create(wide(inf), wide(d.hardware), &instance[0], C.DWORD(len(instance)), &reboot)); err != nil {
			return err
		}
		state.Instances = append(state.Instances, fromWide(instance))
		if err = save(); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: installed %s (reboot required=%t)\n", d.name, fromWide(instance), reboot != 0)
	}
	fmt.Fprintln(out, "USB/IP: native usbip-win2 driver ABI; no external executable or VC++ runtime required.")
	fmt.Fprintln(out, "Microphone: no suitable Microsoft-signed redistributable package found; use VB-Cable (capture endpoint: CABLE Output).")
	return nil
}

func run(out io.Writer, executable string, args ...string) error {
	// System executables always resolve through System32, never PATH.
	if filepath.Base(executable) == executable {
		system, err := windows.GetSystemDirectory()
		if err != nil {
			return err
		}
		executable = filepath.Join(system, executable)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && (exit.ExitCode() == 3010 || exit.ExitCode() == 1641) {
			fmt.Fprintln(out, "Windows requires a reboot to finish this operation.")
			return nil
		}
		return fmt.Errorf("%s: %w", filepath.Base(executable), err)
	}
	return nil
}

func remove(root string, state *installed, save func() error, out io.Writer) error {
	for len(state.Instances) > 0 {
		id := state.Instances[len(state.Instances)-1]
		var reboot C.int
		if err := winerr("remove "+id, C.driver_remove(wide(id), &reboot)); err != nil {
			return err
		}
		state.Instances = state.Instances[:len(state.Instances)-1]
		if err := save(); err != nil {
			return err
		}
		fmt.Fprintf(out, "Removed urdp-owned device %s (reboot required=%t)\n", id, reboot != 0)
	}
	for len(state.Packages) > 0 {
		inf := state.Packages[len(state.Packages)-1]
		// Only remove packages staged by urdp. /uninstall is needed for the
		// extension INF bound to USB root hubs; never force-delete a package.
		if err := run(out, "pnputil.exe", "/delete-driver", inf, "/uninstall"); err != nil {
			return err
		}
		state.Packages = state.Packages[:len(state.Packages)-1]
		if err := save(); err != nil {
			return err
		}
	}
	for _, d := range drivers {
		if err := os.RemoveAll(filepath.Join(root, d.folder)); err != nil {
			return err
		}
	}
	for _, name := range []string{"THIRD_PARTY_NOTICES", "installed.json"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	// Leave Program Files\urdp itself if anything else lives there.
	_ = os.Remove(root)
	_ = os.Remove(filepath.Dir(root))
	fmt.Fprintln(out, "Removed urdp-owned drivers and runtime; pre-existing drivers were retained.")
	return nil
}
