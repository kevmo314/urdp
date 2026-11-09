package vdev

import (
	"errors"
	"strings"
	"testing"
)

func TestCameraOpenError(t *testing.T) {
	for _, tc := range []struct {
		code        uint32
		unsupported bool
		want        string
	}{
		{2, true, "-install-camera"},
		{3, true, "-install-camera"},
		{183, false, "already in use"},
		{0x8007047E, true, "Windows 11"},
		{0x80040154, true, "-uninstall-camera"},
		{0x8007007E, true, "-uninstall-camera"},
		{0x80070005, false, "access denied"},
		{0x80004005, false, "0x80004005"},
	} {
		err := cameraOpenError(tc.code)
		if errors.Is(err, ErrUnsupported) != tc.unsupported || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("code 0x%x: %v (unsupported=%v), want %q unsupported=%v", tc.code, err, errors.Is(err, ErrUnsupported), tc.want, tc.unsupported)
		}
	}
}
