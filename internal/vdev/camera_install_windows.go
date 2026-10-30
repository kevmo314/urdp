package vdev

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

//go:embed mfcam/urdpcam.dll
var cameraDLL []byte

const cameraCLSIDKey = `SOFTWARE\Classes\CLSID\{D8B04D25-43B5-4B57-9B1B-DA8E5F2D6C90}`

// InstallCamera installs the embedded source for the system Frame Server.
// The caller must run elevated; registration is deliberately machine-wide.
func InstallCamera() error {
	base := os.Getenv("ProgramData")
	if base == "" {
		return fmt.Errorf("camera installation: ProgramData is not set")
	}
	dir := filepath.Join(base, "urdp")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	// FrameServer runs as LocalService, which is not a member of Users.
	// Protect the install directory against replacement by ordinary users.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;GRGX;;;BU)(A;OICI;GRGX;;;LS)(A;OICI;GRGX;;;AC)")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return err
	}
	path := filepath.Join(dir, "urdpcam.dll")
	// Stage in the same directory so a failed upgrade preserves the old DLL.
	temp, err := os.CreateTemp(dir, "urdpcam-*.dll")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, err = temp.Write(cameraDLL)
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return err
		}
		if err := removeCameraDLL(path); err != nil {
			return err
		}
		if err := os.Rename(temp.Name(), path); err != nil {
			return err
		}
	}
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, cameraCLSIDKey+`\InprocServer32`, registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return fmt.Errorf("register camera; run as Administrator: %w", err)
	}
	defer key.Close()
	if err := key.SetStringValue("", path); err != nil {
		return err
	}
	return key.SetStringValue("ThreadingModel", "Both")
}

// UninstallCamera removes the source registration and installed DLL.
func UninstallCamera() error {
	for _, name := range []string{cameraCLSIDKey + `\InprocServer32`, cameraCLSIDKey} {
		err := registry.DeleteKey(registry.LOCAL_MACHINE, name)
		if err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("unregister camera; run as Administrator: %w", err)
		}
	}
	base := os.Getenv("ProgramData")
	if base == "" {
		return fmt.Errorf("camera removal: ProgramData is not set")
	}
	return removeCameraDLL(filepath.Join(base, "urdp", "urdpcam.dll"))
}

// FrameServer can retain a loaded DLL after the final camera closes. Windows
// permits renaming a loaded image, but its deletion must wait until reboot.
func removeCameraDLL(path string) error {
	err := os.Remove(path)
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return err
	}
	retired := fmt.Sprintf("%s.%d.old", path, time.Now().UnixNano())
	if err := os.Rename(path, retired); err != nil {
		return fmt.Errorf("retire loaded camera DLL: %w", err)
	}
	name, err := windows.UTF16PtrFromString(retired)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(name, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT); err != nil {
		return fmt.Errorf("schedule removal of %s: %w", retired, err)
	}
	return nil
}
