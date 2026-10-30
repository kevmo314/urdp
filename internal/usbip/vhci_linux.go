package usbip

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
)

// DefaultVHCI is where the vhci-hcd driver's control files live.
const DefaultVHCI = "/sys/devices/platform/vhci_hcd.0"

// VHCI attaches devices straight to the Linux kernel's vhci-hcd, the way
// `usbip attach` does once it has finished its handshake with a remote
// usbipd: it hands the kernel one end of a connected stream socket by
// writing "port sockfd devid speed" to the attach file. The kernel then
// speaks only the URB phase of USB/IP on that socket, so no OP_REQ_IMPORT
// handshake is needed; the other end of the socket is our Attachment.
type VHCI struct {
	Dir string // DefaultVHCI if empty
}

func (v VHCI) dir() string {
	if v.Dir == "" {
		return DefaultVHCI
	}
	return v.Dir
}

// Check reports whether attaching can work, with an actionable error.
func (v VHCI) Check() error {
	attach := filepath.Join(v.dir(), "attach")
	if _, err := os.Stat(attach); err != nil {
		return fmt.Errorf("vhci-hcd not available (%v); load it with `sudo modprobe vhci-hcd`", err)
	}
	f, err := os.OpenFile(attach, os.O_WRONLY, 0)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			// The kernel checks nothing beyond file permissions, so
			// group write access is enough.
			files := attach + " " + filepath.Join(v.dir(), "detach")
			return fmt.Errorf("no write access to %s: run urdp as root, or let a group write the vhci files, e.g. with the udev rule "+
				`ACTION=="add", SUBSYSTEM=="platform", KERNEL=="vhci_hcd.0", RUN+="/bin/sh -c 'chgrp plugdev %s; chmod g+w %s'"`,
				attach, files, files)
		}
		return err
	}
	f.Close()
	if _, err := v.freePort(SpeedHigh); err != nil {
		return err
	}
	return nil
}

// vdevStNull is VDEV_ST_NULL: a free vhci port.
const vdevStNull = 4

// freePort picks an unused root hub port for a device of the given speed:
// SuperSpeed devices need an "ss" port, everything else an "hs" one.
func (v VHCI) freePort(speed uint32) (int, error) {
	want := "hs"
	if speed >= SpeedSuper {
		want = "ss"
	}
	files, _ := filepath.Glob(filepath.Join(v.dir(), "status*"))
	if len(files) == 0 {
		return 0, fmt.Errorf("no status file in %s", v.dir())
	}
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			return 0, err
		}
		port, ok := parseStatus(f, want)
		f.Close()
		if ok {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free %s port on vhci_hcd (all in use)", want)
}

// parseStatus finds the first free port of hub type want in a vhci status
// file:
//
//	hub port sta spd dev      sockfd local_busid
//	hs  0000 004 000 00000000 000000 0-0
func parseStatus(r io.Reader, want string) (int, bool) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || f[0] != want {
			continue
		}
		port, err1 := strconv.Atoi(f[1])
		sta, err2 := strconv.Atoi(f[2])
		if err1 == nil && err2 == nil && sta == vdevStNull {
			return port, true
		}
	}
	return 0, false
}

var devCounter atomic.Uint32

// Attach implements Attacher.
func (v VHCI) Attach(dev *Device) (Attachment, error) {
	speed := dev.Speed
	if speed > SpeedSuper {
		speed = SpeedSuper // vhci accepts low..super
	}
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	kernelFD := fds[1]
	defer syscall.Close(kernelFD) // the kernel holds its own reference
	f := os.NewFile(uintptr(fds[0]), "usbip")
	conn, err := net.FileConn(f)
	f.Close()
	if err != nil {
		return nil, err
	}
	// devid is busnum<<16 | devnum of the (virtual) remote device; the
	// kernel only echoes it back in CMD_SUBMIT.
	devid := 1<<16 | (devCounter.Add(1) & 0xffff)

	// Retry if another attacher grabs the port between status and attach.
	for range 4 {
		port, err := v.freePort(speed)
		if err != nil {
			conn.Close()
			return nil, err
		}
		err = writeSysfs(filepath.Join(v.dir(), "attach"), fmt.Sprintf("%d %d %d %d", port, kernelFD, devid, speed))
		if errors.Is(err, syscall.EBUSY) {
			continue
		}
		if err != nil {
			conn.Close()
			if errors.Is(err, fs.ErrPermission) {
				return nil, fmt.Errorf("%w (%v)", err, v.Check())
			}
			return nil, err
		}
		return &vhciPort{Conn: conn, v: v, port: port}, nil
	}
	conn.Close()
	return nil, errors.New("vhci_hcd ports kept being taken")
}

func writeSysfs(path, s string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(s)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

type vhciPort struct {
	net.Conn
	v    VHCI
	port int
	once sync.Once
	err  error
}

func (p *vhciPort) String() string { return fmt.Sprintf("vhci_hcd port %d", p.port) }

// Close detaches the port, then closes our end of the socket. (Closing
// the socket alone also makes the kernel detach, after logging an error.)
func (p *vhciPort) Close() error {
	p.once.Do(func() {
		err := writeSysfs(filepath.Join(p.v.dir(), "detach"), strconv.Itoa(p.port))
		// EINVAL: the kernel already detached it (e.g. the socket broke).
		if errors.Is(err, syscall.EINVAL) {
			err = nil
		}
		if cerr := p.Conn.Close(); err == nil {
			err = cerr
		}
		p.err = err
	})
	return p.err
}
