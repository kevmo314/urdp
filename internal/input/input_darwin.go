package input

/*
#cgo darwin CFLAGS: -x objective-c -fobjc-arc -mmacosx-version-min=13.0
#cgo darwin LDFLAGS: -framework AppKit -framework ApplicationServices -mmacosx-version-min=13.0
#include <stdint.h>
int ur_trusted(void);
void ur_mouse(int,int,int,int,int,int);
void ur_wheel(int,int);
void ur_key(int,int,uint64_t,int);
int ur_focus(uint32_t);
*/
import "C"
import (
	"fmt"
	"sync"
)

// CGEventPost does not synthesize key repeats. Forward browser repeat events.
const hostAutoRepeats = false

type darwinInjector struct {
	mu       sync.Mutex
	buttons  [3]bool
	pressed  map[uint16]bool
	closed   bool
	capsLock bool
}

func New() (Injector, error) {
	if C.ur_trusted() == 0 {
		return nil, fmt.Errorf("Accessibility permission required: System Settings > Privacy & Security > Accessibility; enable urdp or its launching terminal/sshd-keygen-wrapper, then restart urdp")
	}
	return &darwinInjector{pressed: map[uint16]bool{}}, nil
}
func (i *darwinInjector) MouseMove(x, y int) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return fmt.Errorf("input closed")
	}
	held := -1
	for b, down := range i.buttons {
		if down {
			held = b
			break
		}
	}
	C.ur_mouse(C.int(x), C.int(y), 0, 0, 1, C.int(held))
	return nil
}
func (i *darwinInjector) MouseButton(b int, down bool) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return fmt.Errorf("input closed")
	}
	if b < 0 || b > 2 {
		return fmt.Errorf("invalid button %d", b)
	}
	i.buttons[b] = down
	C.ur_mouse(0, 0, C.int(b), macBool(down), 0, 0)
	return nil
}
func (i *darwinInjector) Wheel(dx, dy int) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return fmt.Errorf("input closed")
	}
	C.ur_wheel(C.int(dx), C.int(dy))
	return nil
}
func (i *darwinInjector) Key(k Key, down bool) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return fmt.Errorf("input closed")
	}
	v, ok := macKeys[k]
	if !ok {
		return nil
	}
	repeat := down && i.pressed[v]
	i.pressed[v] = down
	if v == 0x39 && down && !repeat {
		i.capsLock = !i.capsLock
	}
	flags := uint64(0)
	if i.capsLock {
		flags |= 1 << 16
	}
	for key, mask := range map[uint16]uint64{0x38: 1 << 17, 0x3c: 1 << 17, 0x3b: 1 << 18, 0x3e: 1 << 18, 0x3a: 1 << 19, 0x3d: 1 << 19, 0x37: 1 << 20, 0x36: 1 << 20} {
		if i.pressed[key] {
			flags |= mask
		}
	}
	C.ur_key(C.int(v), macBool(down), C.uint64_t(flags), macBool(repeat))
	return nil
}
func (i *darwinInjector) Focus(id uint64) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return fmt.Errorf("input closed")
	}
	if id > 1<<32-1 || C.ur_focus(C.uint32_t(id)) == 0 {
		return fmt.Errorf("window not found")
	}
	return nil
}
func (i *darwinInjector) Close() error { i.mu.Lock(); defer i.mu.Unlock(); i.closed = true; return nil }
func macBool(v bool) C.int {
	if v {
		return 1
	}
	return 0
}
