package vdev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// feedFrames writes *img to cam every 20ms until the returned stop is
// called. Store a new image to change what's sent.
func feedFrames(t *testing.T, cam Camera, img *atomic.Pointer[Image]) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		for {
			if err := cam.WriteFrame(img.Load()); err != nil {
				done <- err
				return
			}
			select {
			case <-ctx.Done():
				done <- nil
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	return func() {
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func pipewireNode(t *testing.T, name string) map[string]any {
	out, err := exec.Command("pw-dump").Output()
	if err != nil {
		t.Skipf("pw-dump: %v", err)
	}
	var objs []struct {
		Type string
		Info struct {
			Props map[string]any
		}
	}
	if err := json.Unmarshal(out, &objs); err != nil {
		t.Fatalf("pw-dump output: %v", err)
	}
	for _, o := range objs {
		if o.Type == "PipeWire:Interface:Node" && o.Info.Props["node.name"] == name {
			return o.Info.Props
		}
	}
	return nil
}

// gstCapture reads n frames from the PipeWire node with gst-launch.
func gstCapture(dir, node, caps string, n int) ([]byte, string, error) {
	out := filepath.Join(dir, "frames")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gst-launch-1.0", "-v", "pipewiresrc", "target-object="+node,
		fmt.Sprintf("num-buffers=%d", n), "!", caps, "!", "filesink", "location="+out)
	msg, err := cmd.CombinedOutput()
	if err != nil {
		return nil, string(msg), fmt.Errorf("gst-launch reading %s: %v\n%s", node, err, msg)
	}
	data, err := os.ReadFile(out)
	return data, string(msg), err
}

func TestPipeWireCamera(t *testing.T) {
	cam, err := openPipeWireCamera()
	if err != nil {
		t.Skip("no PipeWire: ", err)
	}
	defer cam.Close()

	// A second camera gets its own node.
	cam2, err := openPipeWireCamera()
	if err != nil {
		t.Fatal(err)
	}
	if cam2.Name == cam.Name {
		t.Errorf("both cameras are %q", cam.Name)
	}
	cam2.Close()

	if _, err := exec.LookPath("pw-dump"); err == nil {
		props := pipewireNode(t, cam.Name)
		if props == nil {
			t.Fatalf("node %s not in pw-dump", cam.Name)
		}
		if props["media.class"] != "Video/Source" || props["media.role"] != "Camera" ||
			props["node.description"] != cameraLabel {
			t.Errorf("node props %v", props)
		}
	}

	if _, err := exec.LookPath("gst-launch-1.0"); err != nil {
		t.Skip("gst-launch-1.0 not installed")
	}
	if exec.Command("gst-inspect-1.0", "pipewiresrc").Run() != nil {
		t.Skip("gstreamer1.0-pipewire not installed")
	}

	var img atomic.Pointer[Image]
	img.Store(solidImage(320, 240, 200, 90, 160))
	stop := feedFrames(t, cam, &img)
	defer stop()

	for _, tc := range []struct {
		format string
		check  func([]byte) bool
		size   int
	}{
		{"I420", func(f []byte) bool { return f[0] == 200 && f[320*240] == 90 && f[320*240*5/4] == 160 }, 320 * 240 * 3 / 2},
		{"NV12", func(f []byte) bool { return f[0] == 200 && f[320*240] == 90 && f[320*240+1] == 160 }, 320 * 240 * 3 / 2},
		{"YUY2", func(f []byte) bool { return bytes.Equal(f[:4], []byte{200, 90, 200, 160}) }, 320 * 240 * 2},
	} {
		data, _, err := gstCapture(t.TempDir(), cam.Name, "video/x-raw,format="+tc.format, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) != 3*tc.size {
			t.Fatalf("%s: read %d bytes, want 3 frames of %d", tc.format, len(data), tc.size)
		}
		if f := data[2*tc.size:]; !tc.check(f) {
			t.Errorf("%s: wrong pixels %v", tc.format, f[:8])
		}
	}

	// A new size is renegotiated with the consumer mid-stream.
	done := make(chan error, 1)
	var log string
	var data []byte
	dir := t.TempDir()
	go func() {
		var err error
		data, log, err = gstCapture(dir, cam.Name, "video/x-raw,format=I420", 30)
		done <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for cam.stats().Format == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	img.Store(solidImage(160, 120, 1, 2, 3))
	var st pwCamStats
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if st = cam.stats(); st.Width == 160 {
			break
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log, "width=(int)160, height=(int)120") {
		t.Errorf("consumer never saw 160x120:\n%s", log)
	}
	if n := len(data); n < 160*120*3/2 || data[n-160*120*3/2] != 1 || data[n-1] != 3 {
		t.Errorf("last frame isn't the resized image")
	}
	if st.Width != 160 || st.Height != 120 {
		t.Errorf("negotiated %dx%d after resize", st.Width, st.Height)
	}
	t.Logf("stats after resize: %+v", st)
}
