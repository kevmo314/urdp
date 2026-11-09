package usbip

import (
	"os"
	"testing"
	"time"
)

// This exercises real kernel enumeration through the signed usbip-win2 driver.
// Opt in only on a Windows test host with the bundled drivers installed.
func TestWindowsNativeAttach(t *testing.T) {
	if os.Getenv("URDP_TEST_WINDOWS_DRIVERS") != "1" {
		t.Skip("set URDP_TEST_WINDOWS_DRIVERS=1 on a driver test host")
	}
	att, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeBrowser(t, att)
	defer f.b.Close()
	f.start()
	f.waitStatus(StatusAttached)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ops := f.opsOf(MsgControl)
		for _, op := range ops {
			if op.Setup[1] == 6 {
				t.Logf("kernel enumerated fake USB device, %d WebUSB transfers", len(ops))
				f.b.mu.Lock()
				active := f.b.att
				f.b.mu.Unlock()
				f.b.Close()
				if err := active.Close(); err != nil {
					t.Fatalf("native detach: %v", err)
				}
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Windows never requested a USB descriptor")
}
