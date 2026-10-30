package capture

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func needX(t *testing.T) {
	t.Helper()
	if os.Getenv("DISPLAY") == "" {
		t.Skip("DISPLAY not set")
	}
}

func TestDesktopCapture(t *testing.T) {
	needX(t)
	src, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	f, _, err := src.Capture(0)
	if err != nil {
		t.Fatal(err)
	}
	if f.Width < 16 || f.Height < 16 || len(f.Pix) < f.Stride*f.Height {
		t.Fatalf("bad frame %dx%d stride %d len %d", f.Width, f.Height, f.Stride, len(f.Pix))
	}
	if b := src.Bounds(); b.Dx() != f.Width || b.Dy() != f.Height {
		t.Errorf("bounds %v do not match frame %dx%d", b, f.Width, f.Height)
	}
}

func TestWindowCapture(t *testing.T) {
	needX(t)
	if _, err := exec.LookPath("xterm"); err != nil {
		t.Skip("xterm not installed")
	}
	cmd := exec.Command("xterm", "-title", "urdp-capture-test", "-geometry", "80x24+40+30", "-e", "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	var win *Window
	for deadline := time.Now().Add(5 * time.Second); win == nil && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		wins, err := List()
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range wins {
			if w.Title == "urdp-capture-test" {
				win = &w
			}
		}
	}
	if win == nil {
		t.Fatal("test window not listed")
	}
	src, err := Open(FormatID(win.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	f, _, err := src.Capture(0)
	if err != nil {
		t.Fatal(err)
	}
	if f.Width != win.Width&^1 || f.Height != win.Height&^1 {
		t.Errorf("frame %dx%d, window %dx%d", f.Width, f.Height, win.Width, win.Height)
	}
	if src.WindowID() != win.ID || src.Bounds().Min.X < 0 {
		t.Errorf("window id %x bounds %v", src.WindowID(), src.Bounds())
	}
	// Nothing draws to an idle xterm, so damage tracking should report no
	// change and the wait should honor its timeout.
	start := time.Now()
	if _, changed, err := src.Capture(100 * time.Millisecond); err != nil || changed {
		t.Errorf("idle capture: changed=%v err=%v", changed, err)
	}
	if d := time.Since(start); d < 80*time.Millisecond {
		t.Errorf("idle capture returned after %v, want ~100ms", d)
	}
	exec.Command("xdotool", "windowsize", FormatID(win.ID), "500", "300").Run()
	if _, changed, err := src.Capture(time.Second); err != nil || !changed {
		t.Errorf("capture after resize: changed=%v err=%v", changed, err)
	}

	cmd.Process.Kill()
	cmd.Wait()
	time.Sleep(200 * time.Millisecond)
	if _, _, err := src.Capture(0); err != ErrNotFound {
		t.Errorf("capture after close: %v, want ErrNotFound", err)
	}
}
