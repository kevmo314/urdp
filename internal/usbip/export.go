package usbip

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ServeImport answers the USB/IP handshake on conn for one exported
// device, like usbipd. It returns true once conn has imported the device
// and is in the URB phase; false (with conn closed) if the peer only
// listed devices, asked for another bus id, or misbehaved.
func ServeImport(conn net.Conn, info DeviceInfo) (bool, error) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetDeadline(time.Time{})
	h, err := ReadOpHeader(conn)
	if err != nil {
		conn.Close()
		return false, err
	}
	if h.Version != Version {
		conn.Close()
		return false, fmt.Errorf("usbip: client version %#04x, want %#04x", h.Version, Version)
	}
	switch h.Code {
	case OpReqDevlist:
		_, err := conn.Write(MarshalDevlistReply([]DeviceInfo{info}))
		conn.Close()
		return false, err
	case OpReqImport:
		busID, err := ReadBusID(conn)
		if err != nil {
			conn.Close()
			return false, err
		}
		if busID != info.BusID {
			_, _ = conn.Write(MarshalImportReply(nil))
			conn.Close()
			return false, fmt.Errorf("usbip: import of unknown bus id %q", busID)
		}
		if _, err := conn.Write(MarshalImportReply(&info)); err != nil {
			conn.Close()
			return false, err
		}
		return true, nil
	}
	conn.Close()
	return false, fmt.Errorf("usbip: unexpected op %#04x", h.Code)
}

// Command attaches devices by running the system's USB/IP client (for
// example usbip-win2's usbip.exe) against a one-device USB/IP server that
// it starts on 127.0.0.1 for the duration of the attach.
type Command struct {
	// Attach is the client command line. "{port}" is replaced by the
	// local server's TCP port and "{busid}" by the device's bus id.
	AttachArgs []string
	// Detach, if set, is run when the device is detached; "{vport}" is
	// replaced by the last number Attach printed (the virtual port).
	// Either way our end of the connection is closed, which makes the
	// client drop the device.
	DetachArgs []string
	// Timeout bounds the attach command; 20s if zero.
	Timeout time.Duration
}

// UsbipWin2 runs usbip-win2's CLI (https://github.com/vadimgrn/usbip-win2),
// which must be installed and on PATH. Its vhci driver connects to our
// server itself and only sends OP_REQ_IMPORT.
func UsbipWin2() Command {
	return Command{
		AttachArgs: []string{"usbip.exe", "--tcp-port", "{port}", "attach", "--remote", "127.0.0.1", "--busid", "{busid}"},
		DetachArgs: []string{"usbip.exe", "detach", "--port", "{vport}"},
	}
}

// UsbipLinux runs the Linux usbip tool (linux-tools), an alternative to
// VHCI that needs the same privileges.
func UsbipLinux() Command {
	return Command{AttachArgs: []string{"usbip", "--tcp-port", "{port}", "attach", "-r", "127.0.0.1", "-b", "{busid}"}}
}

func expand(args []string, vars map[string]string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		for k, v := range vars {
			a = strings.ReplaceAll(a, "{"+k+"}", v)
		}
		out[i] = a
	}
	return out
}

// vportRe matches usbip-win2's "successfully attached to port N" (or just "N"
// with --terse).
var vportRe = regexp.MustCompile(`(?:attached to port (\d+)|^(\d+)$)`)

// Attach implements Attacher.
func (c Command) Attach(dev *Device) (Attachment, error) {
	if len(c.AttachArgs) == 0 {
		return nil, errors.New("usbip: no attach command")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	info := dev.Info()
	imported := make(chan net.Conn, 1)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				if ok, _ := ServeImport(conn, info); ok {
					select {
					case imported <- conn:
					default:
						conn.Close() // only one import per device
					}
				}
			}()
		}
	}()

	timeout := c.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	port := fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
	args := expand(c.AttachArgs, map[string]string{"port": port, "busid": dev.BusID})
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		select {
		case conn := <-imported:
			conn.Close()
		default:
		}
		return nil, fmt.Errorf("%s: %v: %s", args[0], err, strings.TrimSpace(out.String()))
	}
	select {
	case conn := <-imported:
		a := &cmdAttachment{Conn: conn, c: c}
		if m := vportRe.FindStringSubmatch(strings.TrimSpace(out.String())); m != nil {
			a.vport = m[1] + m[2]
		}
		return a, nil
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("%s succeeded but never imported the device: %s", args[0], strings.TrimSpace(out.String()))
	}
}

type cmdAttachment struct {
	net.Conn
	c     Command
	vport string
	once  sync.Once
	err   error
}

func (a *cmdAttachment) String() string {
	if a.vport != "" {
		return "usbip port " + a.vport
	}
	return "usbip " + a.RemoteAddr().String()
}

func (a *cmdAttachment) Close() error {
	a.once.Do(func() {
		if len(a.c.DetachArgs) > 0 && a.vport != "" {
			args := expand(a.c.DetachArgs, map[string]string{"vport": a.vport})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput(); err != nil {
				a.err = fmt.Errorf("%s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
			}
			cancel()
		}
		if err := a.Conn.Close(); a.err == nil {
			a.err = err
		}
	})
	return a.err
}
