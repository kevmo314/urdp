package usbip

import (
	"encoding/binary"
	"fmt"
	"strconv"
)

// usbip-win2 0.9.8.1 VHCI IOCTL layout (include/usbip/vhci.h, MSVC x64).
// It is plain bytes so it can be checked on any platform.
const (
	vhciAttachOnce = 0x0022e018 // CTL_CODE(FILE_DEVICE_UNKNOWN, 0x806, METHOD_BUFFERED, READ|WRITE)
	vhciDetach     = 0x0022e004
	// MSVC's base subobjects have tail padding: location is 1100 bytes.
	vhciAttachSize = 1124

	vhciOffPort    = 4    // out: assigned virtual port
	vhciOffBusID   = 12   // char busid[32]
	vhciOffService = 44   // char service[32]
	vhciOffHost    = 76   // char node[1025]
	vhciOffEvents  = 1120 // wsk_events flag
)

func putCString(dst []byte, s, what string) error {
	if len(s) == 0 || len(s) >= len(dst) {
		return fmt.Errorf("invalid USB/IP %s %q", what, s)
	}
	copy(dst, s)
	return nil
}

// vhciAttachRequest builds the ioctl_plugin_hardware buffer.
func vhciAttachRequest(busid, service, host string) ([]byte, error) {
	req := make([]byte, vhciAttachSize)
	binary.LittleEndian.PutUint32(req, uint32(len(req)))
	if err := putCString(req[vhciOffBusID:vhciOffService], busid, "bus id"); err != nil {
		return nil, err
	}
	if err := putCString(req[vhciOffService:vhciOffHost], service, "port"); err != nil {
		return nil, err
	}
	if err := putCString(req[vhciOffHost:vhciOffHost+1025], host, "host"); err != nil {
		return nil, err
	}
	// serial[16] stays empty (keep the device's own serial).
	req[vhciOffEvents] = 1 // low-latency receive mode
	return req, nil
}

// vhciAttachedPort reads the virtual port the driver wrote back.
func vhciAttachedPort(req []byte) (string, error) {
	if len(req) < vhciOffPort+4 {
		return "", fmt.Errorf("usbip-win2 attach reply too short")
	}
	vport := binary.LittleEndian.Uint32(req[vhciOffPort:])
	if vport < 1 || vport > 255 {
		return "", fmt.Errorf("usbip-win2 returned invalid port %d", vport)
	}
	return strconv.Itoa(int(vport)), nil
}

// vhciDetachRequest builds the ioctl_plugout_hardware buffer.
func vhciDetachRequest(port string) ([]byte, error) {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 255 {
		return nil, fmt.Errorf("invalid usbip-win2 port %q", port)
	}
	req := make([]byte, 8)
	binary.LittleEndian.PutUint32(req, 8)
	binary.LittleEndian.PutUint32(req[4:], uint32(n))
	return req, nil
}
