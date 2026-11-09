package usbip

import (
	"encoding/binary"
	"strings"
	"testing"
)

// Offsets follow usbip-win2 v.0.9.8.1 include/usbip/vhci.h:
// struct plugin_hardware : base, imported_device_location { char serial[16]; bool wsk_events; }
func TestVHCIAttachRequestLayout(t *testing.T) {
	req, err := vhciAttachRequest("1-2", "50123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(req) != 1124 {
		t.Fatalf("size %d", len(req))
	}
	if got := binary.LittleEndian.Uint32(req); got != 1124 {
		t.Fatalf("base.size %d", got)
	}
	cstr := func(b []byte) string { return strings.TrimRight(string(b), "\x00") }
	if cstr(req[12:44]) != "1-2" || cstr(req[44:76]) != "50123" || cstr(req[76:1101]) != "127.0.0.1" {
		t.Fatalf("strings: %q %q %q", cstr(req[12:44]), cstr(req[44:76]), cstr(req[76:1101]))
	}
	if req[1120] != 1 {
		t.Fatal("wsk_events not set")
	}
	for i, b := range req[1104:1120] {
		if b != 0 {
			t.Fatalf("serial[%d]=%d, want empty", i, b)
		}
	}
	if vhciAttachOnce != 0x22<<16|3<<14|0x806<<2 || vhciDetach != 0x22<<16|3<<14|0x801<<2 {
		t.Fatal("IOCTL codes do not match CTL_CODE(FILE_DEVICE_UNKNOWN, fn, METHOD_BUFFERED, READ|WRITE)")
	}
}

func TestVHCIAttachRequestRejectsBadStrings(t *testing.T) {
	for _, c := range [][3]string{
		{"", "1", "127.0.0.1"},
		{strings.Repeat("x", 32), "1", "127.0.0.1"},
		{"1-1", strings.Repeat("9", 32), "127.0.0.1"},
		{"1-1", "", "127.0.0.1"},
	} {
		if _, err := vhciAttachRequest(c[0], c[1], c[2]); err == nil {
			t.Errorf("accepted %q", c)
		}
	}
}

func TestVHCIPorts(t *testing.T) {
	req := make([]byte, 8)
	for _, bad := range []uint32{0, 256} {
		binary.LittleEndian.PutUint32(req[4:], bad)
		if _, err := vhciAttachedPort(req); err == nil {
			t.Errorf("accepted port %d", bad)
		}
	}
	binary.LittleEndian.PutUint32(req[4:], 7)
	if p, err := vhciAttachedPort(req); err != nil || p != "7" {
		t.Fatalf("port %q %v", p, err)
	}
	d, err := vhciDetachRequest("7")
	if err != nil || binary.LittleEndian.Uint32(d) != 8 || binary.LittleEndian.Uint32(d[4:]) != 7 {
		t.Fatalf("detach %v %v", d, err)
	}
	for _, bad := range []string{"", "0", "x", "300"} {
		if _, err := vhciDetachRequest(bad); err == nil {
			t.Errorf("accepted detach port %q", bad)
		}
	}
}
