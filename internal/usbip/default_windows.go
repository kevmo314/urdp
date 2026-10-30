package usbip

import (
	"context"
	"fmt"
	"runtime"

	"golang.org/x/sys/windows"
)

// usbip-win2 0.9.8.1 public ABI, include/usbip/vhci.h and consts.h:
// https://github.com/vadimgrn/usbip-win2/tree/v.0.9.8.1/include/usbip
// The kernel connects to our local server; no external CLI or runtime is needed.
var vhciInterface = windows.GUID{Data1: 0xb4030c06, Data2: 0xdc5f, Data3: 0x4fcc, Data4: [8]byte{0x87, 0xeb, 0xe5, 0x51, 0x5a, 0x09, 0x35, 0xc0}}

type windowsAttacher struct{}

func Default() (Attacher, error) {
	h, err := openVHCI()
	if err != nil {
		return windowsAttacher{}, fmt.Errorf("USB/IP controller: %w; run urdp.exe -install-drivers", err)
	}
	windows.CloseHandle(h)
	return windowsAttacher{}, nil
}

func openVHCI() (windows.Handle, error) {
	paths, err := windows.CM_Get_Device_Interface_List("", &vhciInterface, windows.CM_GET_DEVICE_INTERFACE_LIST_PRESENT)
	if err != nil {
		return 0, err
	}
	if len(paths) != 1 {
		return 0, fmt.Errorf("expected one usbip-win2 controller, found %d", len(paths))
	}
	path, err := windows.UTF16PtrFromString(paths[0])
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
}

func vhciCall(ctx context.Context, code uint32, request []byte, outputBytes uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h, err := openVHCI()
	if err != nil {
		return fmt.Errorf("USB/IP controller unavailable (%w); on the host run urdp.exe -install-drivers", err)
	}
	defer windows.CloseHandle(h)
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(event)
	ov := windows.Overlapped{HEvent: event}
	// The kernel retains these addresses until completion or cancellation drains.
	var pinned runtime.Pinner
	pinned.Pin(&request[0])
	pinned.Pin(&ov)
	defer pinned.Unpin()
	var count uint32
	var output *byte
	if outputBytes > 0 {
		output = &request[0]
	}
	err = windows.DeviceIoControl(h, code, &request[0], uint32(len(request)), output, outputBytes, &count, &ov)
	if err == windows.ERROR_IO_PENDING {
		for {
			wait, waitErr := windows.WaitForSingleObject(event, 50)
			if waitErr != nil || ctx.Err() != nil {
				windows.CancelIoEx(h, &ov)
				// Drain cancellation before the request and OVERLAPPED can be released.
				_ = windows.GetOverlappedResult(h, &ov, &count, true)
				if waitErr != nil {
					return waitErr
				}
				return ctx.Err()
			}
			if wait == windows.WAIT_OBJECT_0 {
				err = windows.GetOverlappedResult(h, &ov, &count, false)
				break
			}
		}
	}
	if err != nil {
		return err
	}
	if count != outputBytes {
		return fmt.Errorf("usbip-win2 IOCTL response: got %d bytes, want %d", count, outputBytes)
	}
	return nil
}

func (windowsAttacher) Attach(dev *Device) (Attachment, error) {
	return attachLocal(dev, 0, func(ctx context.Context, port, busid string) (string, error) {
		req, err := vhciAttachRequest(busid, port, "127.0.0.1")
		if err != nil {
			return "", err
		}
		if err := vhciCall(ctx, vhciAttachOnce, req, 8); err != nil {
			return "", fmt.Errorf("usbip-win2 attach: %w", err)
		}
		return vhciAttachedPort(req)
	}, func(ctx context.Context, port string) error {
		req, err := vhciDetachRequest(port)
		if err != nil {
			return err
		}
		return vhciCall(ctx, vhciDetach, req, 0)
	})
}
