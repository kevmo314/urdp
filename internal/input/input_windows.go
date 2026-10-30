package input

/*
#cgo windows LDFLAGS: -luser32
#define WIN32_LEAN_AND_MEAN
#include <stdint.h>
#include <windows.h>
static void input_dpi(void) {
    typedef BOOL(WINAPI * dpi_fn)(HANDLE);
    dpi_fn fn =
        (dpi_fn)GetProcAddress(GetModuleHandleW(L"user32.dll"), "SetProcessDpiAwarenessContext");
    if (fn)
        fn((HANDLE)(intptr_t)-4);
}
static DWORD input_move(int x, int y) {
    if (SetCursorPos(x, y))
        return 0;
    return GetLastError();
}
static DWORD input_send(DWORD flags, DWORD data, WORD scan, int keyboard) {
    INPUT in = {0};
    if (keyboard) {
        in.type = INPUT_KEYBOARD;
        in.ki.wScan = scan;
        in.ki.dwFlags = flags;
    } else {
        in.type = INPUT_MOUSE;
        in.mi.dwFlags = flags;
        in.mi.mouseData = data;
    }
    if (SendInput(1, &in, sizeof(in)) == 1)
        return 0;
    DWORD err = GetLastError();
    return err ? err : ERROR_ACCESS_DENIED;
}
static DWORD input_focus(uint64_t id) {
    HWND hwnd = (HWND)(uintptr_t)id;
    if (!IsWindow(hwnd))
        return ERROR_INVALID_WINDOW_HANDLE;
    if (IsIconic(hwnd))
        ShowWindow(hwnd, SW_RESTORE);
    if (GetForegroundWindow() == hwnd || SetForegroundWindow(hwnd))
        return 0;
    MSG message;
    PeekMessageW(&message, NULL, 0, 0, PM_NOREMOVE);
    DWORD current = GetCurrentThreadId();
    DWORD foreground = GetWindowThreadProcessId(GetForegroundWindow(), NULL);
    DWORD target = GetWindowThreadProcessId(hwnd, NULL);
    BOOL attachedForeground = foreground != current && AttachThreadInput(current, foreground, TRUE);
    BOOL attachedTarget =
        target != current && target != foreground && AttachThreadInput(current, target, TRUE);
    BringWindowToTop(hwnd);
    BOOL ok = SetForegroundWindow(hwnd);
    if (attachedTarget)
        AttachThreadInput(current, target, FALSE);
    if (attachedForeground)
        AttachThreadInput(current, foreground, FALSE);
    if (!ok && !(GetAsyncKeyState(VK_MENU) & 0x8000)) {
        // An ALT transition lets Windows release its foreground activation lock.
        INPUT alt[2] = {0};
        alt[0].type = alt[1].type = INPUT_KEYBOARD;
        alt[0].ki.wVk = alt[1].ki.wVk = VK_MENU;
        alt[1].ki.dwFlags = KEYEVENTF_KEYUP;
        if (SendInput(2, alt, sizeof(INPUT)) == 2)
            ok = SetForegroundWindow(hwnd);
    }
    if (GetForegroundWindow() == hwnd)
        ok = TRUE;
    return ok ? 0 : ERROR_ACCESS_DENIED;
}
*/
import "C"

import (
	"fmt"
	"sync"
)

var dpiOnce sync.Once

type windowsInjector struct{}

func New() (Injector, error) {
	dpiOnce.Do(func() { C.input_dpi() })
	return &windowsInjector{}, nil
}
func inputError(op string, code C.DWORD) error {
	if code != 0 {
		return fmt.Errorf("%s: Windows error %d", op, uint32(code))
	}
	return nil
}
func (i *windowsInjector) MouseMove(x, y int) error {
	return inputError("move mouse", C.input_move(C.int(x), C.int(y)))
}
func (i *windowsInjector) MouseButton(button int, down bool) error {
	var flags C.DWORD
	switch button {
	case ButtonLeft:
		flags = C.MOUSEEVENTF_LEFTUP
		if down {
			flags = C.MOUSEEVENTF_LEFTDOWN
		}
	case ButtonMiddle:
		flags = C.MOUSEEVENTF_MIDDLEUP
		if down {
			flags = C.MOUSEEVENTF_MIDDLEDOWN
		}
	case ButtonRight:
		flags = C.MOUSEEVENTF_RIGHTUP
		if down {
			flags = C.MOUSEEVENTF_RIGHTDOWN
		}
	default:
		return fmt.Errorf("invalid mouse button %d", button)
	}
	return inputError("mouse button", C.input_send(flags, 0, 0, 0))
}
func (i *windowsInjector) Wheel(dx, dy int) error {
	if dy != 0 {
		if err := inputError("vertical wheel", C.input_send(C.MOUSEEVENTF_WHEEL, C.DWORD(-dy*120), 0, 0)); err != nil {
			return err
		}
	}
	if dx != 0 {
		return inputError("horizontal wheel", C.input_send(C.MOUSEEVENTF_HWHEEL, C.DWORD(dx*120), 0, 0))
	}
	return nil
}
func (i *windowsInjector) Key(k Key, down bool) error {
	flags := C.DWORD(C.KEYEVENTF_SCANCODE)
	if k.Scan&0xff00 == 0xe000 {
		flags |= C.KEYEVENTF_EXTENDEDKEY
	}
	if !down {
		flags |= C.KEYEVENTF_KEYUP
	}
	return inputError("key", C.input_send(flags, 0, C.WORD(k.Scan&0xff), 1))
}
func (i *windowsInjector) Focus(id uint64) error {
	return inputError("focus window", C.input_focus(C.uint64_t(id)))
}
func (i *windowsInjector) Close() error { return nil }
