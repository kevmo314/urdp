package vdev

/*
#include <linux/uinput.h>
#include <sys/ioctl.h>
#include <fcntl.h>
#include <unistd.h>
#include <errno.h>
#include <string.h>

static const int pad_keys[] = {
	BTN_SOUTH, BTN_EAST, BTN_NORTH, BTN_WEST, BTN_TL, BTN_TR,
	BTN_SELECT, BTN_START, BTN_MODE, BTN_THUMBL, BTN_THUMBR,
};

static int pad_abs(int fd, int code, int min, int max, int fuzz, int flat) {
	struct uinput_abs_setup a;
	memset(&a, 0, sizeof a);
	a.code = code;
	a.absinfo.minimum = min;
	a.absinfo.maximum = max;
	a.absinfo.fuzz = fuzz;
	a.absinfo.flat = flat;
	if (ioctl(fd, UI_SET_ABSBIT, code) < 0) return -1;
	return ioctl(fd, UI_ABS_SETUP, &a);
}

// Creates an Xbox 360 pad laid out like the xpad driver's.
static int pad_open(void) {
	int fd = open("/dev/uinput", O_WRONLY | O_NONBLOCK | O_CLOEXEC);
	if (fd < 0) return -errno;
	struct uinput_setup s;
	memset(&s, 0, sizeof s);
	s.id.bustype = BUS_USB;
	s.id.vendor = 0x045e;
	s.id.product = 0x028e;
	s.id.version = 0x0114;
	strcpy(s.name, "Microsoft X-Box 360 pad");
	if (ioctl(fd, UI_SET_EVBIT, EV_KEY) < 0 || ioctl(fd, UI_SET_EVBIT, EV_ABS) < 0) goto fail;
	for (unsigned i = 0; i < sizeof pad_keys / sizeof pad_keys[0]; i++)
		if (ioctl(fd, UI_SET_KEYBIT, pad_keys[i]) < 0) goto fail;
	if (pad_abs(fd, ABS_X, -32768, 32767, 16, 128) < 0 ||
	    pad_abs(fd, ABS_Y, -32768, 32767, 16, 128) < 0 ||
	    pad_abs(fd, ABS_RX, -32768, 32767, 16, 128) < 0 ||
	    pad_abs(fd, ABS_RY, -32768, 32767, 16, 128) < 0 ||
	    pad_abs(fd, ABS_Z, 0, 255, 0, 0) < 0 ||
	    pad_abs(fd, ABS_RZ, 0, 255, 0, 0) < 0 ||
	    pad_abs(fd, ABS_HAT0X, -1, 1, 0, 0) < 0 ||
	    pad_abs(fd, ABS_HAT0Y, -1, 1, 0, 0) < 0)
		goto fail;
	if (ioctl(fd, UI_DEV_SETUP, &s) < 0 || ioctl(fd, UI_DEV_CREATE) < 0) goto fail;
	return fd;
fail:;
	int e = errno;
	close(fd);
	return -e;
}

static int pad_sysname(int fd, char *buf, int len) {
	return ioctl(fd, UI_GET_SYSNAME(len), buf) < 0 ? -errno : 0;
}

static void pad_close(int fd) {
	ioctl(fd, UI_DEV_DESTROY);
	close(fd);
}
*/
import "C"

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"syscall"
	"unsafe"
)

const (
	evSyn = 0x00
	evKey = 0x01
	evAbs = 0x03

	absX, absY, absZ, absRX, absRY, absRZ = 0x00, 0x01, 0x02, 0x03, 0x04, 0x05
	absHat0X, absHat0Y                    = 0x10, 0x11
)

// evdev codes for standard-mapping buttons 0-5, 8-11 and 16.
var padButtons = [...]struct{ button, code int }{
	{0, 0x130},  // A: BTN_SOUTH
	{1, 0x131},  // B: BTN_EAST
	{2, 0x133},  // X: BTN_NORTH
	{3, 0x134},  // Y: BTN_WEST
	{4, 0x136},  // LB: BTN_TL
	{5, 0x137},  // RB: BTN_TR
	{8, 0x13a},  // Back: BTN_SELECT
	{9, 0x13b},  // Start: BTN_START
	{10, 0x13d}, // LS: BTN_THUMBL
	{11, 0x13e}, // RS: BTN_THUMBR
	{16, 0x13c}, // Guide: BTN_MODE
}

const uinputHelp = "allow access with a udev rule, e.g. " +
	"`echo 'KERNEL==\"uinput\", MODE=\"0660\", GROUP=\"input\"' | sudo tee /etc/udev/rules.d/60-urdp-uinput.rules && " +
	"sudo udevadm control --reload && sudo udevadm trigger /dev/uinput && sudo usermod -aG input $USER` " +
	"(then log in again), or for this boot only `sudo chmod 666 /dev/uinput`"

// padValues is the evdev value of every code the pad reports.
type padValues struct {
	keys [len(padButtons)]int32
	abs  [8]int32 // X, Y, RX, RY, Z, RZ, HAT0X, HAT0Y
}

var padAbsCodes = [8]uint16{absX, absY, absRX, absRY, absZ, absRZ, absHat0X, absHat0Y}

type uinputGamepad struct {
	mu   sync.Mutex
	fd   int
	last padValues
	buf  []byte
}

func openGamepad() (Gamepad, error) {
	fd := int(C.pad_open())
	switch {
	case fd == -int(syscall.EACCES) || fd == -int(syscall.EPERM):
		return nil, fmt.Errorf("%w: /dev/uinput is not writable; %s", ErrUnsupported, uinputHelp)
	case fd == -int(syscall.ENOENT) || fd == -int(syscall.ENODEV):
		return nil, fmt.Errorf("%w: /dev/uinput missing; load it with `sudo modprobe uinput`", ErrUnsupported)
	case fd < 0:
		return nil, fmt.Errorf("uinput: creating gamepad: %w", syscall.Errno(-fd))
	}
	return &uinputGamepad{fd: fd}, nil
}

func stickValue(v float64) int32 {
	v = math.Max(-1, math.Min(1, v))
	if v < 0 {
		return int32(math.Round(v * 32768))
	}
	return int32(math.Round(v * 32767))
}

func pressed(v float64) bool { return v >= 0.5 }

func b2i(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

func padValuesOf(s GamepadState) padValues {
	var p padValues
	for i, b := range padButtons {
		p.keys[i] = b2i(pressed(s.Buttons[b.button]))
	}
	for i := 0; i < 4; i++ {
		p.abs[i] = stickValue(s.Axes[i])
	}
	p.abs[4] = int32(math.Round(math.Max(0, math.Min(1, s.Buttons[6])) * 255))
	p.abs[5] = int32(math.Round(math.Max(0, math.Min(1, s.Buttons[7])) * 255))
	p.abs[6] = b2i(pressed(s.Buttons[15])) - b2i(pressed(s.Buttons[14]))
	p.abs[7] = b2i(pressed(s.Buttons[13])) - b2i(pressed(s.Buttons[12]))
	return p
}

// appendEvent encodes a struct input_event with a zero timestamp, which
// the kernel fills in.
func appendEvent(b []byte, typ, code uint16, value int32) []byte {
	var ev [24]byte
	n := 2 * int(unsafe.Sizeof(C.long(0)))
	binary.NativeEndian.PutUint16(ev[n:], typ)
	binary.NativeEndian.PutUint16(ev[n+2:], code)
	binary.NativeEndian.PutUint32(ev[n+4:], uint32(value))
	return append(b, ev[:n+8]...)
}

func (g *uinputGamepad) Update(s GamepadState) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fd < 0 {
		return errors.New("uinput: gamepad closed")
	}
	next := padValuesOf(s)
	b := g.buf[:0]
	for i, b0 := range padButtons {
		if next.keys[i] != g.last.keys[i] {
			b = appendEvent(b, evKey, uint16(b0.code), next.keys[i])
		}
	}
	for i, code := range padAbsCodes {
		if next.abs[i] != g.last.abs[i] {
			b = appendEvent(b, evAbs, code, next.abs[i])
		}
	}
	if len(b) == 0 {
		return nil
	}
	b = appendEvent(b, evSyn, 0, 0)
	g.buf = b
	if _, err := syscall.Write(g.fd, b); err != nil {
		return fmt.Errorf("uinput: %w", err)
	}
	g.last = next
	return nil
}

// sysname is the device's directory under /sys/devices/virtual/input.
func (g *uinputGamepad) sysname() (string, error) {
	var buf [64]C.char
	if r := C.pad_sysname(C.int(g.fd), &buf[0], C.int(len(buf))); r < 0 {
		return "", syscall.Errno(-r)
	}
	return C.GoString(&buf[0]), nil
}

func (g *uinputGamepad) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fd >= 0 {
		C.pad_close(C.int(g.fd))
		g.fd = -1
	}
	return nil
}
