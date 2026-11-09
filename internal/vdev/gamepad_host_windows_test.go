package vdev

import (
	"os"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Opt-in real-host check: create one controller and confirm the exact state
// through the consumer API used by games, rather than only testing bus IOCTLs.
func TestWindowsGamepadXInput(t *testing.T) {
	if os.Getenv("URDP_TEST_WINDOWS_DRIVERS") != "1" {
		t.Skip("set URDP_TEST_WINDOWS_DRIVERS=1 on a driver test host")
	}
	pad, err := OpenGamepad()
	if err != nil {
		t.Fatal(err)
	}
	defer pad.Close()
	get := windows.NewLazySystemDLL("xinput1_4.dll").NewProc("XInputGetState")
	type state struct {
		Packet         uint32
		Buttons        uint16
		LT, RT         uint8
		LX, LY, RX, RY int16
	}
	input := GamepadState{Axes: [4]float64{0.5, 0, 0, 0}}
	input.Buttons[0] = 1
	deadline := time.Now().Add(15 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		if err = pad.Update(input); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 4; i++ {
			var s state
			code, _, _ := get.Call(uintptr(i), uintptr(unsafe.Pointer(&s)))
			if code == 0 && s.Buttons&0x1000 != 0 && s.LX == 16384 {
				found = true
				break
			}
		}
		if found {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !found {
		t.Fatal("XInput never received the injected A button and left-stick value")
	}
	t.Log("XInput sees Xbox 360 A button and LX=16384")
	// Optional observation window for WGI / browser diagnostics.
	if os.Getenv("URDP_TEST_GAMEPAD_HOLD") == "1" {
		for i := 0; i < 300; i++ {
			if err = pad.Update(input); err != nil {
				t.Fatal(err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if err = pad.Update(GamepadState{}); err != nil {
		t.Fatal(err)
	}
}
