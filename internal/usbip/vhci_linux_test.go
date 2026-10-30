package usbip

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestParseStatus(t *testing.T) {
	status := `hub port sta spd dev      sockfd local_busid
hs  0000 006 003 00010002 000005 1-1
hs  0001 004 000 00000000 000000 0-0
ss  0008 004 000 00000000 000000 0-0
`
	if p, ok := parseStatus(strings.NewReader(status), "hs"); !ok || p != 1 {
		t.Fatalf("hs: %d %v", p, ok)
	}
	if p, ok := parseStatus(strings.NewReader(status), "ss"); !ok || p != 8 {
		t.Fatalf("ss: %d %v", p, ok)
	}
	if _, ok := parseStatus(strings.NewReader(status[:strings.Index(status, "hs  0001")]), "hs"); ok {
		t.Fatal("found a port in a full hub")
	}
}

func TestVHCICheckMissing(t *testing.T) {
	err := VHCI{Dir: t.TempDir()}.Check()
	if err == nil || !strings.Contains(err.Error(), "modprobe vhci-hcd") {
		t.Fatalf("got %v", err)
	}
}

// waitFor polls fn until it returns true or the timeout passes.
func waitFor(t *testing.T, what string, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func readAttr(dir, name string) string {
	b, _ := os.ReadFile(filepath.Join(dir, name))
	return strings.TrimSpace(string(b))
}

// findFakeDevice returns the sysfs directory of the fake device on a vhci
// bus, or "".
func findFakeDevice() string {
	dirs, _ := filepath.Glob("/sys/bus/usb/devices/*")
	for _, d := range dirs {
		if readAttr(d, "idVendor") != "1209" || readAttr(d, "idProduct") != "0001" {
			continue
		}
		if real, err := filepath.EvalSymlinks(d); err == nil && strings.Contains(real, "vhci_hcd") {
			return d
		}
	}
	return ""
}

func makeRaw(t *testing.T, fd uintptr) {
	var tio syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&tio))); e != 0 {
		t.Fatal(e)
	}
	tio.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	tio.Oflag &^= syscall.OPOST
	tio.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	tio.Cflag &^= syscall.CSIZE | syscall.PARENB
	tio.Cflag |= syscall.CS8
	tio.Cc[syscall.VMIN], tio.Cc[syscall.VTIME] = 1, 0
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&tio))); e != 0 {
		t.Fatal(e)
	}
}

func countReads(f *fakeBrowser) int {
	n := 0
	for _, o := range f.opsOf(MsgTransfer) {
		if o.EP == 0x83 {
			n++
		}
	}
	return n
}

// echoTTY opens tty raw, writes msg and expects it back.
func echoTTY(t *testing.T, tty string, msg []byte) {
	t.Helper()
	tf, err := os.OpenFile(tty, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer tf.Close()
	makeRaw(t, tf.Fd())
	go tf.Write(msg)
	readDone := make(chan []byte, 1)
	go func() {
		got := make([]byte, 0, len(msg))
		buf := make([]byte, 4096)
		for len(got) < len(msg) {
			n, err := tf.Read(buf)
			if err != nil {
				break
			}
			got = append(got, buf[:n]...)
		}
		readDone <- got
	}()
	select {
	case got := <-readDone:
		if !bytes.Equal(got, msg) {
			t.Fatalf("echoed %d bytes that differ from the %d written", len(got), len(msg))
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("echo of %d bytes through %s timed out", len(msg), tty)
	}
	t.Logf("echoed %d bytes through %s", len(msg), tty)
}

// TestVHCIKernel attaches the fake CDC-ACM device to the real kernel via
// vhci-hcd and checks that Linux enumerates it, binds cdc_acm, and that
// bytes written to /dev/ttyACM* come back through the echoing fake.
// Needs root:
//
//	sudo URDP_USBIP_ROOT_TEST=1 [URDP_USBIP_BIN=/path/to/usbip] go test -run VHCIKernel -v ./internal/usbip
func TestVHCIKernel(t *testing.T) {
	if os.Getenv("URDP_USBIP_ROOT_TEST") == "" {
		t.Skip("set URDP_USBIP_ROOT_TEST=1 and run as root to attach a fake device to vhci-hcd")
	}
	_ = exec.Command("modprobe", "vhci-hcd").Run()
	_ = exec.Command("modprobe", "cdc-acm").Run()
	if d := findFakeDevice(); d != "" {
		t.Fatalf("a fake device is already attached at %s", d)
	}
	v := VHCI{}
	if err := v.Check(); err != nil {
		t.Fatal(err)
	}
	t.Run("sysfs", func(t *testing.T) { kernelTest(t, v) })
	// The localhost USB/IP server + `usbip attach` path (what Windows
	// uses with usbip-win2), against the Linux usbip tool if given.
	if bin := os.Getenv("URDP_USBIP_BIN"); bin != "" {
		c := UsbipLinux()
		c.AttachArgs[0] = bin
		t.Run("usbip-attach", func(t *testing.T) { kernelTest(t, c) })
	}
}

func kernelTest(t *testing.T, att Attacher) {
	v := VHCI{}
	f := newFakeBrowser(t, att)
	f.start()
	where := f.waitStatus(StatusAttached)
	t.Logf("attached as %s", where)
	t.Cleanup(f.b.Close)

	var dev string
	waitFor(t, "the kernel to enumerate the device", 10*time.Second, func() bool {
		dev = findFakeDevice()
		return dev != ""
	})
	t.Logf("enumerated at %s", dev)
	if p := readAttr(dev, "product"); p != "urdp fake ACM" {
		t.Errorf("product %q", p)
	}
	if s := readAttr(dev, "speed"); s != "12" {
		t.Errorf("speed %q, want 12 (full speed)", s)
	}
	out, err := exec.Command("lsusb", "-d", "1209:0001").CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("1209:0001")) {
		t.Errorf("lsusb: %v %s", err, out)
	}
	t.Logf("lsusb: %s", bytes.TrimSpace(out))

	// cdc_acm binds interface 0 and creates a tty.
	var tty string
	waitFor(t, "cdc_acm to create a tty", 10*time.Second, func() bool {
		m, _ := filepath.Glob(dev + ":1.0/tty/ttyACM*")
		if len(m) > 0 {
			tty = "/dev/" + filepath.Base(m[0])
			_, err := os.Stat(tty)
			return err == nil
		}
		return false
	})
	if f.opsOf(MsgSetConf) == nil {
		t.Error("kernel's SET_CONFIGURATION didn't reach the browser as selectConfiguration")
	}
	// Closing the tty makes cdc_acm kill its pending reads (CMD_UNLINK);
	// reopening resubmits them, and they must adopt the abandoned
	// browser reads rather than queue behind them.
	echoTTY(t, tty, bytes.Repeat([]byte("urdp usb passthrough "), 800)) // ~16 KiB
	reads := countReads(f)
	echoTTY(t, tty, []byte("second open"))
	echoTTY(t, tty, []byte("third open"))
	// Each short echo completes one read, which cdc_acm resubmits; the
	// other 15 of its 16 reads per open must be adopted, not reissued.
	if n := countReads(f); n-reads != 2 {
		t.Errorf("two reopens issued %d new browser reads, want 2", n-reads)
	}
	t.Logf("%d browser reads on ep 0x83", reads)

	// Detach (as when the viewer unplugs the device or disconnects).
	f.b.Close()
	waitFor(t, "the device to disappear", 10*time.Second, func() bool { return findFakeDevice() == "" })
	if _, err := v.freePort(SpeedFull); err != nil {
		t.Fatal(err)
	}
}
