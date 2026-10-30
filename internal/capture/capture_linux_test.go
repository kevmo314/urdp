package capture

import (
	"fmt"
	"image"
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

func TestDisplays(t *testing.T) {
	needX(t)
	ds, err := Displays()
	if err != nil || len(ds) == 0 {
		t.Fatalf("displays: %v %v", ds, err)
	}
	src, err := Open("display1")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	f, _, err := src.Capture(0)
	if err != nil {
		t.Fatal(err)
	}
	if f.Width != ds[0].Width&^1 || f.Height != ds[0].Height&^1 {
		t.Errorf("display1 frame %dx%d, want %dx%d", f.Width, f.Height, ds[0].Width, ds[0].Height)
	}
	if _, err := Open(fmt.Sprintf("display%d", len(ds)+1)); err != ErrNotFound {
		t.Errorf("open missing display: %v, want ErrNotFound", err)
	}
}

// Monitors can't be faked under Xvfb, so exercise the crop directly.
func TestCrop(t *testing.T) {
	needX(t)
	crop := image.Rect(100, 50, 500, 350)
	src, err := openX11(0, crop)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	f, _, err := src.Capture(0)
	if err != nil {
		t.Fatal(err)
	}
	if f.Width != 400 || f.Height != 300 || src.Bounds() != crop {
		t.Errorf("frame %dx%d bounds %v, want 400x300 %v", f.Width, f.Height, src.Bounds(), crop)
	}
}

func TestWindowIconAndApp(t *testing.T) {
	needX(t)
	for _, tool := range []string{"xterm", "xprop"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	cmd := exec.Command("xterm", "-title", "urdp-icon-test", "-e", "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	var win *Window
	for deadline := time.Now().Add(5 * time.Second); win == nil && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		wins, _ := List()
		for _, w := range wins {
			if w.Title == "urdp-icon-test" {
				win = &w
			}
		}
	}
	if win == nil {
		t.Fatal("test window not listed")
	}
	if win.App != "XTerm" {
		t.Errorf("app = %q, want XTerm", win.App)
	}
	// Two icons, 1x1 and 2x2; asking for 2px must pick the 2x2 one.
	err := exec.Command("xprop", "-id", FormatID(win.ID), "-f", "_NET_WM_ICON", "32c", "-set", "_NET_WM_ICON",
		"1,1,0xffffffff,2,2,0xffff0000,0xff00ff00,0xff0000ff,0x80ffffff").Run()
	if err != nil {
		t.Fatal(err)
	}
	img, err := Icon(win.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 2 || b.Dy() != 2 {
		t.Fatalf("icon %v, want 2x2", b)
	}
	if r, g, b, a := img.At(0, 0).RGBA(); r>>8 != 255 || g != 0 || b != 0 || a>>8 != 255 {
		t.Errorf("pixel (0,0) = %d,%d,%d,%d, want opaque red", r>>8, g>>8, b>>8, a>>8)
	}
	if _, _, _, a := img.At(1, 1).RGBA(); a>>8 != 0x80 {
		t.Errorf("pixel (1,1) alpha = %d, want 128", a>>8)
	}
}
