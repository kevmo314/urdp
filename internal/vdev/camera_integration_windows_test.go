package vdev

import (
	"os"
	"testing"
	"time"
)

// Run explicitly on a real Windows 11 host alongside scripts/camera-probe-windows.cpp.
// Ordinary unit test runs never create a device.
func TestNativeCameraIntegration(t *testing.T) {
	if os.Getenv("URDP_TEST_NATIVE_CAMERA") != "1" {
		t.Skip("requires an installed native camera and an interactive Windows host")
	}
	camera, err := OpenCamera()
	if err != nil {
		t.Fatal(err)
	}
	defer camera.Close()
	t.Log("camera opened; black-frame interval starts")
	if other, err := OpenCamera(); err == nil {
		other.Close()
		t.Fatal("accepted a second producer")
	}
	time.Sleep(15 * time.Second)
	t.Log("starting color bars")
	for frame := 0; frame < 900; frame++ {
		w, h := 960, 540
		if frame == 450 {
			t.Log("switching incoming resolution to 320x240")
		}
		if frame >= 450 {
			w, h = 320, 240
		}
		image := &Image{Width: w, Height: h, Y: make([]byte, w*h), U: make([]byte, w*h/4), V: make([]byte, w*h/4), YStride: w, UStride: w / 2, VStride: w / 2}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				base := 40
				if frame >= 450 {
					base = 50
				}
				image.Y[y*w+x] = byte(base + x/(w/4)*50)
			}
		}
		for i := range image.U {
			image.U[i] = 90
			image.V[i] = 170
		}
		if err := camera.WriteFrame(image); err != nil {
			t.Fatal(err)
		}
		// Leave the last frame untouched for five seconds to test stale input.
		if frame == 200 {
			t.Log("pausing writes for stale-frame capture")
			time.Sleep(5 * time.Second)
		}
		time.Sleep(time.Second / 30)
	}
}
