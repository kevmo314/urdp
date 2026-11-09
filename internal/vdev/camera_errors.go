package vdev

import (
	"errors"
	"fmt"
)

// Windows codes camera_open can return. Win32 errors come from the producer
// mutex and the OBS fallback; HRESULTs from MFCreateVirtualCamera.
const (
	winErrorFileNotFound  = 2
	winErrorPathNotFound  = 3
	winErrorAlreadyExists = 183
	hresultOldWinVersion  = 0x8007047E // HRESULT_FROM_WIN32(ERROR_OLD_WIN_VERSION)
	hresultClassNotReg    = 0x80040154 // REGDB_E_CLASSNOTREG
	hresultModNotFound    = 0x8007007E // HRESULT_FROM_WIN32(ERROR_MOD_NOT_FOUND)
	hresultAccessDenied   = 0x80070005 // E_ACCESSDENIED
	cameraInstallHint     = "run `urdp.exe -install-camera` once from an Administrator terminal"
	cameraReinstallHint   = "the urdp Camera registration is broken; run `urdp.exe -uninstall-camera` then `urdp.exe -install-camera` from an Administrator terminal"
)

// cameraOpenError explains a Windows camera_open failure to the viewer. Only
// presence is detected at run time: urdp never installs the camera itself.
func cameraOpenError(code uint32) error {
	switch code {
	case winErrorAlreadyExists:
		return errors.New("virtual camera is already in use; close the other urdp camera session or stop OBS Virtual Camera before retrying")
	case winErrorFileNotFound, winErrorPathNotFound:
		// Neither urdp Camera nor OBS Virtual Camera is registered.
		return fmt.Errorf("%w: no virtual camera is installed on the host; %s", ErrUnsupported, cameraInstallHint)
	case hresultOldWinVersion:
		return fmt.Errorf("%w: urdp Camera needs Windows 11 (build 22000 or later)", ErrUnsupported)
	case hresultClassNotReg, hresultModNotFound:
		return fmt.Errorf("%w: %s", ErrUnsupported, cameraReinstallHint)
	case hresultAccessDenied:
		return errors.New("virtual camera: access denied; check Settings > Privacy & security > Camera allows desktop apps")
	}
	return fmt.Errorf("virtual camera: open (Windows error/HRESULT 0x%08x)", code)
}
