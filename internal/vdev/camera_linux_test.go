package vdev

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func solidImage(w, h int, y, u, v byte) *Image {
	stride := w + 16 // strided like a decoder's output
	cs := (w+1)/2 + 8
	img := &Image{Width: w, Height: h, YStride: stride, UStride: cs, VStride: cs,
		Y: make([]byte, stride*h), U: make([]byte, cs*((h+1)/2)), V: make([]byte, cs*((h+1)/2))}
	for i := range img.Y {
		img.Y[i] = y
	}
	for i := range img.U {
		img.U[i], img.V[i] = u, v
	}
	return img
}

func TestPackPlane(t *testing.T) {
	src := []byte{
		1, 2, 3, 4, 0,
		5, 6, 7, 8, 0,
	}
	dst := make([]byte, 4)
	packPlane(dst, 4, 1, src, 5, 4, 2)
	if string(dst) != "\x01\x02\x03\x04" {
		t.Errorf("copy: %v", dst)
	}
	dst = make([]byte, 2*2)
	packPlane(dst, 2, 2, src[:4], 5, 2, 1) // upscale 2x1 -> 2x2
	if string(dst) != "\x01\x02\x01\x02" {
		t.Errorf("upscale: %v", dst)
	}
	dst = make([]byte, 2)
	packPlane(dst, 2, 1, src, 5, 4, 2) // downscale 4x2 -> 2x1
	if string(dst) != "\x01\x03" {
		t.Errorf("downscale: %v", dst)
	}
}

func TestOpenCamera(t *testing.T) {
	cam, err := OpenCamera()
	if errors.Is(err, ErrUnsupported) {
		if !strings.Contains(err.Error(), "PipeWire") ||
			(!strings.Contains(err.Error(), "modprobe v4l2loopback") && !strings.Contains(err.Error(), "video group")) {
			t.Errorf("unhelpful error: %v", err)
		}
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	cam.Close()
}

func TestLoopbackCamera(t *testing.T) {
	cam, err := openLoopbackCamera()
	if err != nil {
		t.Skip(err)
	}
	defer cam.Close()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	c := cam.(*loopbackCamera)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		img := solidImage(320, 240, 200, 90, 160)
		for {
			if err := cam.WriteFrame(img); err != nil {
				done <- err
				return
			}
			select {
			case <-ctx.Done():
				done <- nil
				return
			case <-time.After(30 * time.Millisecond):
			}
		}
	}()

	out := filepath.Join(t.TempDir(), "frame.yuv")
	fctx, fcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer fcancel()
	cmd := exec.CommandContext(fctx, "ffmpeg", "-loglevel", "error", "-f", "v4l2", "-i", c.path,
		"-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "yuv420p", "-y", out)
	msg, err := cmd.CombinedOutput()
	cancel()
	if werr := <-done; werr != nil {
		t.Fatal(werr)
	}
	if err != nil {
		t.Fatalf("ffmpeg reading %s: %v\n%s", c.path, err, msg)
	}
	frame, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(frame) != 320*240*3/2 {
		t.Fatalf("read %d bytes, want a 320x240 frame", len(frame))
	}
	if y, u, v := frame[0], frame[320*240], frame[320*240*5/4]; y != 200 || u != 90 || v != 160 {
		t.Fatalf("yuv %d,%d,%d, want 200,90,160", y, u, v)
	}

	// A new size either reformats the device or, if the driver keeps the
	// old format, is scaled into it.
	if err := cam.WriteFrame(solidImage(161, 121, 1, 2, 3)); err != nil {
		t.Fatal(err)
	}
	if (c.w != 160 || c.h != 120) && (c.w != 320 || c.h != 240) {
		t.Fatalf("format %dx%d after resize", c.w, c.h)
	}
	t.Logf("format after resize: %dx%d", c.w, c.h)
}
