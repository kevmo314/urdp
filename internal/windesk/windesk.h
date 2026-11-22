// Package windesk (header only): follow the Windows input desktop.
//
// UAC's secure prompt, the lock screen and Ctrl+Alt+Del run on the Winlogon
// desktop, not Default. Input injection (SendInput, SetCursorPos) and
// capture (DXGI DuplicateOutput, GDI) only reach the desktop the calling
// thread is attached to, so a thread left on Default can neither see nor
// drive those screens. Opening Winlogon requires running as SYSTEM (urdp's
// startup service); otherwise OpenInputDesktop fails and nothing changes.
//
// Each native call is wrapped in windesk_enter/windesk_leave: the thread
// joins the current input desktop for the call and goes back afterwards.
// One cgo call runs on one OS thread, and restoring keeps Go runtime
// threads (which other code may later create windows on) on Default.
#ifndef URDP_WINDESK_H
#define URDP_WINDESK_H

#define WIN32_LEAN_AND_MEAN
#include <wchar.h>
#include <windows.h>

typedef struct {
    HDESK prev;  // the thread's own desktop; never closed
    HDESK input; // opened input desktop while switched, else NULL
} windesk_scope;

static inline int windesk_name(HDESK d, WCHAR *name, DWORD size) {
    DWORD n = 0;
    name[0] = 0;
    return GetUserObjectInformationW(d, UOI_NAME, name, size - sizeof(WCHAR), &n);
}

static inline windesk_scope windesk_enter(void) {
    windesk_scope s = {0};
    HDESK input = OpenInputDesktop(0, FALSE, GENERIC_ALL);
    if (!input)
        return s;
    HDESK cur = GetThreadDesktop(GetCurrentThreadId());
    WCHAR a[64], b[64];
    if (cur && windesk_name(cur, a, sizeof(a)) && windesk_name(input, b, sizeof(b)) &&
        wcscmp(a, b) == 0) {
        CloseDesktop(input);
        return s;
    }
    // Fails if this thread owns windows or hooks; then stay where we are.
    if (!SetThreadDesktop(input)) {
        CloseDesktop(input);
        return s;
    }
    s.prev = cur;
    s.input = input;
    return s;
}

static inline void windesk_leave(windesk_scope s) {
    if (!s.input)
        return;
    SetThreadDesktop(s.prev);
    CloseDesktop(s.input);
}

// Whether the input desktop is the normal one (or can't be inspected).
static inline int windesk_input_is_default(void) {
    HDESK input = OpenInputDesktop(0, FALSE, DESKTOP_READOBJECTS);
    if (!input)
        // Access is denied to non-SYSTEM callers exactly when a secure
        // desktop (Winlogon) has the input.
        return GetLastError() != ERROR_ACCESS_DENIED;
    WCHAR name[64];
    int ok = windesk_name(input, name, sizeof(name));
    CloseDesktop(input);
    return !ok || _wcsicmp(name, L"Default") == 0;
}

#endif
