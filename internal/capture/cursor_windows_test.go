package capture

import (
	"bytes"
	"context"
	"image/png"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

var user32 = syscall.NewLazyDLL("user32.dll")

// A 32x32 monochrome cursor: top half opaque white, bottom half transparent.
func testCursor(t *testing.T) uintptr {
	and, xor := make([]byte, 128), make([]byte, 128)
	for i := range 128 {
		if i < 64 {
			xor[i] = 0xff
		} else {
			and[i] = 0xff
		}
	}
	h, _, err := user32.NewProc("CreateCursor").Call(0, 3, 5, 32, 32, uintptr(unsafe.Pointer(&and[0])), uintptr(unsafe.Pointer(&xor[0])))
	if h == 0 {
		t.Fatal(err)
	}
	t.Cleanup(func() { user32.NewProc("DestroyCursor").Call(h) })
	return h
}

func TestCursorRender(t *testing.T) {
	h := testCursor(t)
	c, err := renderCursor(uint64(h), 32, 32)
	if err != nil {
		t.Fatal(err)
	}
	im, err := png.Decode(bytes.NewReader(c))
	if err != nil {
		t.Fatal(err)
	}
	if b := im.Bounds(); b.Dx() != 32 || b.Dy() != 32 {
		t.Fatalf("size %v", b)
	}
	r, g, b, a := im.At(10, 4).RGBA()
	if a>>8 != 0xff || r>>8 != 0xff || g>>8 != 0xff || b>>8 != 0xff {
		t.Errorf("top half pixel = %x %x %x %x, want opaque white", r, g, b, a)
	}
	if _, _, _, a := im.At(10, 24).RGBA(); a != 0 {
		t.Errorf("bottom half alpha = %x, want transparent", a)
	}
}

// The current cursor is reported promptly, whatever it is; this doesn't
// move the pointer or change the cursor.
func TestWatchCursorInitial(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got := make(chan Cursor, 1)
	go WatchCursor(ctx, func(c Cursor) {
		select {
		case got <- c:
		default:
		}
	})
	select {
	case c := <-got:
		t.Logf("current cursor: serial=%x css=%q png=%d bytes hot=%d,%d hidden=%v", c.Serial, c.CSS, len(c.PNG), c.HotX, c.HotY, c.Hidden)
		if !c.Hidden && c.CSS == "" && len(c.PNG) == 0 {
			t.Error("visible cursor with neither a CSS name nor an image")
		}
	case <-ctx.Done():
		t.Skip("no cursor reported (no interactive desktop?)")
	}
}
