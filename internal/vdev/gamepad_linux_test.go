package vdev

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestPadValues(t *testing.T) {
	var s GamepadState
	s.Buttons[0] = 1   // A
	s.Buttons[3] = 1   // Y
	s.Buttons[6] = 0.5 // LT
	s.Buttons[7] = 1   // RT
	s.Buttons[12] = 1  // up
	s.Buttons[15] = 1  // right
	s.Buttons[16] = 1  // guide
	s.Axes = [4]float64{-1, 1, 0.5, -2}
	p := padValuesOf(s)
	wantKeys := [len(padButtons)]int32{1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1}
	if p.keys != wantKeys {
		t.Errorf("keys %v, want %v", p.keys, wantKeys)
	}
	wantAbs := [8]int32{-32768, 32767, 16384, -32768, 128, 255, 1, -1}
	if p.abs != wantAbs {
		t.Errorf("abs %v, want %v", p.abs, wantAbs)
	}
}

type inputEvent struct {
	typ, code uint16
	value     int32
}

type evdevReader struct {
	t  *testing.T
	fd int
}

// read collects one SYN_REPORT-terminated packet, or nil on timeout.
func (r *evdevReader) read(timeout time.Duration) map[[2]uint16]int32 {
	n := 2 * int(unsafe.Sizeof(uintptr(0)))
	size := n + 8
	buf := make([]byte, size*64)
	got := map[[2]uint16]int32{}
	var pending []byte
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		k, err := syscall.Read(r.fd, buf)
		if err == syscall.EAGAIN {
			time.Sleep(2 * time.Millisecond)
			continue
		}
		if err != nil {
			r.t.Fatal(err)
		}
		pending = append(pending, buf[:k]...)
		for len(pending) >= size {
			ev := inputEvent{
				binary.NativeEndian.Uint16(pending[n:]),
				binary.NativeEndian.Uint16(pending[n+2:]),
				int32(binary.NativeEndian.Uint32(pending[n+4:])),
			}
			pending = pending[size:]
			if ev.typ == evSyn {
				return got
			}
			got[[2]uint16{ev.typ, ev.code}] = ev.value
		}
	}
	return nil
}

func TestGamepad(t *testing.T) {
	gp, err := OpenGamepad()
	if errors.Is(err, ErrUnsupported) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer gp.Close()
	g := gp.(*uinputGamepad)
	sys, err := g.sysname()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("/sys/devices/virtual/input", sys)
	name, _ := os.ReadFile(filepath.Join(dir, "name"))
	vendor, _ := os.ReadFile(filepath.Join(dir, "id/vendor"))
	product, _ := os.ReadFile(filepath.Join(dir, "id/product"))
	if got := strings.TrimSpace(string(name)); got != "Microsoft X-Box 360 pad" {
		t.Errorf("name %q", got)
	}
	if v, p := strings.TrimSpace(string(vendor)), strings.TrimSpace(string(product)); v != "045e" || p != "028e" {
		t.Errorf("id %s:%s, want 045e:028e", v, p)
	}

	var node string
	for deadline := time.Now().Add(3 * time.Second); node == "" && time.Now().Before(deadline); {
		evs, _ := filepath.Glob(filepath.Join(dir, "event*"))
		if len(evs) > 0 {
			if _, err := os.Stat("/dev/input/" + filepath.Base(evs[0])); err == nil {
				node = "/dev/input/" + filepath.Base(evs[0])
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if node == "" {
		t.Fatalf("no /dev/input/event* node for %s", sys)
	}
	var fd int
	for deadline := time.Now().Add(2 * time.Second); ; {
		fd, err = syscall.Open(node, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		// udev may still be applying permissions.
		if err != syscall.EACCES || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err == syscall.EACCES {
		t.Skipf("%s not readable; skipping event read-back", node)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	r := &evdevReader{t, fd}

	key := func(code uint16) [2]uint16 { return [2]uint16{evKey, code} }
	abs := func(code uint16) [2]uint16 { return [2]uint16{evAbs, code} }

	var s GamepadState
	s.Buttons[0] = 1  // A
	s.Buttons[5] = 1  // RB
	s.Buttons[7] = 1  // RT
	s.Buttons[9] = 1  // Start
	s.Buttons[13] = 1 // down
	s.Buttons[14] = 1 // left
	s.Axes = [4]float64{-1, 0.5, 0, 1}
	if err := gp.Update(s); err != nil {
		t.Fatal(err)
	}
	got := r.read(time.Second)
	want := map[[2]uint16]int32{
		key(0x130): 1, key(0x137): 1, key(0x13b): 1,
		abs(absX): -32768, abs(absY): 16384, abs(absRY): 32767,
		abs(absRZ): 255, abs(absHat0X): -1, abs(absHat0Y): 1,
	}
	if !equalEvents(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}

	// Unchanged state emits nothing.
	if err := gp.Update(s); err != nil {
		t.Fatal(err)
	}
	if got := r.read(100 * time.Millisecond); got != nil {
		t.Fatalf("unchanged update produced %v", got)
	}

	s.Buttons[0] = 0
	s.Buttons[14] = 0
	s.Buttons[16] = 1 // guide
	s.Axes[0] = 0
	if err := gp.Update(s); err != nil {
		t.Fatal(err)
	}
	got = r.read(time.Second)
	want = map[[2]uint16]int32{key(0x130): 0, key(0x13c): 1, abs(absX): 0, abs(absHat0X): 0}
	if !equalEvents(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
}

func equalEvents(a, b map[[2]uint16]int32) bool {
	if a == nil || len(a) != len(b) {
		return false
	}
	for k, v := range b {
		if w, ok := a[k]; !ok || w != v {
			return false
		}
	}
	return true
}
