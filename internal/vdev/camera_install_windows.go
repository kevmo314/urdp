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

const cameraCLSIDKey = `SOFTWARE\Classes\CLSID\` + CameraCLSID

// cameraDir is %ProgramData%\urdp\camera. It's resolved through the shell
// rather than the environment so an elevated install can't be redirected.
func cameraDir() (string, error) {
	base, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return "", fmt.Errorf("camera: locate ProgramData: %w", err)
	}
	return filepath.Join(base, "urdp", "camera"), nil
}

func checkCameraInstallable() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("installing urdp Camera writes to HKLM and ProgramData; run this from an Administrator terminal")
	}
	if v := windows.RtlGetVersion(); v.MajorVersion < 10 || v.BuildNumber < 22000 {
		return fmt.Errorf("%w: urdp Camera needs Windows 11 (build 22000 or later); this is build %d", ErrUnsupported, v.BuildNumber)
	}
	return nil
}

// secureDir creates dir (if needed) owned by Administrators with a protected
// DACL: SYSTEM and Administrators full control, Users/LocalService/
// AppContainers read+execute. FrameServer (LocalService) loads the DLL from
// here, so no unprivileged account may be able to replace it. Directories
// that are reparse points are refused: a user could otherwise pre-create a
// junction to a folder they own.
func secureDir(dir string) error {
	for _, d := range []string{filepath.Dir(dir), dir} {
		if err := os.Mkdir(d, 0755); err != nil && !os.IsExist(err) {
			return err
		}
		fi, err := os.Lstat(d)
		if err != nil {
			return err
		}
		if !fi.IsDir() || fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return fmt.Errorf("camera: %s is not a plain directory; remove it and retry", d)
		}
		if err := checkAdminOwned(d); err != nil {
			return err
		}
	}
	sd, err := windows.SecurityDescriptorFromString("O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;GRGX;;;BU)(A;OICI;GRGX;;;LS)(A;OICI;GRGX;;;AC)")
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, acl, nil)
}

// checkAdminOwned refuses directories an unprivileged user created: their
// owner could rewrite the DACL or rename the camera directory away later.
func checkAdminOwned(d string) error {
	sd, err := windows.GetNamedSecurityInfo(d, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) || owner.IsWellKnown(windows.WinLocalSystemSid) {
		return nil
	}
	return fmt.Errorf("camera: %s is owned by %s, not Administrators; remove it and retry", d, owner)
}

// InstallCamera installs the embedded source for the system Frame Server. It
// is only ever called from the explicit -install-camera flag; the server
// merely detects whether the registration exists. It writes:
//
//	%ProgramData%\urdp\camera\urdpcam.dll           (Administrators-owned, protected DACL)
//	HKLM\SOFTWARE\Classes\CLSID\{D8B04D25-...}\InprocServer32
//	    (Default)      = path of urdpcam.dll
//	    ThreadingModel = Both
//
// No driver, service, INF or certificate is installed. The camera device
// itself only exists while a viewer camera session is open (session
// lifetime, current user access) and disappears when urdp.exe exits.
func InstallCamera() error {
	if err := checkCameraInstallable(); err != nil {
		return err
	}
	dir, err := cameraDir()
	if err != nil {
		return err
	}
	if err := secureDir(dir); err != nil {
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

// UninstallCamera removes exactly what InstallCamera wrote: the CLSID key and
// the DLL (deferred to reboot if FrameServer still has it loaded), then the
// install directories if they are empty.
func UninstallCamera() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("removing urdp Camera writes to HKLM and ProgramData; run this from an Administrator terminal")
	}
	for _, name := range []string{cameraCLSIDKey + `\InprocServer32`, cameraCLSIDKey} {
		err := registry.DeleteKey(registry.LOCAL_MACHINE, name)
		if err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("unregister camera: %w", err)
		}
	}
	dir, err := cameraDir()
	if err != nil {
		return err
	}
	if err := removeCameraDLL(filepath.Join(dir, "urdpcam.dll")); err != nil {
		return err
	}
	// Leave the directories if a retired DLL awaits reboot or the user put
	// something else there; os.Remove refuses non-empty directories.
	_ = os.Remove(dir)
	_ = os.Remove(filepath.Dir(dir))
	return nil
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
