#define WIN32_LEAN_AND_MEAN
#include "native_windows.h"
#include <windows.h>
#include <limits.h>
#include <stdlib.h>
#include <string.h>
#include <wchar.h>

// QueryFullProcessImageName supports long executable paths without requiring
// PROCESS_VM_READ (which is commonly unavailable for other applications).
static BOOL process_path(DWORD pid, WCHAR *path) {
    HANDLE process = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, FALSE, pid);
    if (!process)
        return FALSE;
    DWORD length = 32768;
    BOOL ok = QueryFullProcessImageNameW(process, 0, path, &length);
    CloseHandle(process);
    return ok;
}

typedef struct {
    DWORD host_pid;
    WCHAR *path;
    BOOL found;
} app_search;

static BOOL CALLBACK core_window(HWND hwnd, LPARAM param) {
    app_search *search = (app_search *)param;
    WCHAR name[128];
    DWORD pid = 0;
    GetWindowThreadProcessId(hwnd, &pid);
    if (pid && pid != search->host_pid && GetClassNameW(hwnd, name, 128) &&
        wcscmp(name, L"Windows.UI.Core.CoreWindow") == 0 && process_path(pid, search->path)) {
        search->found = TRUE;
        return FALSE;
    }
    return TRUE;
}

static WCHAR *base_name(WCHAR *path) {
    WCHAR *base = wcsrchr(path, L'\\');
    return base ? base + 1 : path;
}

static BOOL window_path(HWND hwnd, WCHAR *path) {
    DWORD pid = 0;
    GetWindowThreadProcessId(hwnd, &pid);
    if (!pid || !process_path(pid, path))
        return FALSE;
    if (_wcsicmp(base_name(path), L"ApplicationFrameHost.exe") == 0) {
        // A failed child query leaves the host path intact.
        WCHAR child_path[32768];
        app_search search = {pid, child_path, FALSE};
        EnumChildWindows(hwnd, core_window, (LPARAM)&search);
        if (search.found)
            wcscpy(path, child_path);
    }
    return TRUE;
}

void capture_window_app(uint64_t id, char *app, int capacity) {
    WCHAR path[32768];
    if (window_path((HWND)(uintptr_t)id, path))
        WideCharToMultiByte(CP_UTF8, 0, base_name(path), -1, app, capacity, NULL, NULL);
}

static void consider_icon(HICON icon, int size, HICON *best, int *distance) {
    ICONINFO info = {0};
    if (!icon || !GetIconInfo(icon, &info))
        return;
    BITMAP bitmap;
    HBITMAP source = info.hbmColor ? info.hbmColor : info.hbmMask;
    if (GetObjectW(source, sizeof(bitmap), &bitmap)) {
        int delta = abs(bitmap.bmWidth - size);
        // On ties keep the first candidate (large icons are queried first).
        if (delta < *distance) {
            *best = icon;
            *distance = delta;
        }
    }
    if (info.hbmColor)
        DeleteObject(info.hbmColor);
    if (info.hbmMask)
        DeleteObject(info.hbmMask);
}

static HICON window_icon(HWND hwnd, int size) {
    HICON best = NULL;
    int distance = INT_MAX;
    const WPARAM kinds[] = {ICON_BIG, ICON_SMALL2, ICON_SMALL};
    ULONGLONG deadline = GetTickCount64() + 50;
    for (int i = 0; i < 3; i++) {
        ULONGLONG now = GetTickCount64();
        if (now >= deadline)
            break;
        DWORD_PTR result = 0;
        if (!SendMessageTimeoutW(hwnd, WM_GETICON, kinds[i], 0, SMTO_ABORTIFHUNG | SMTO_BLOCK,
                                 (UINT)(deadline - now), &result))
            break;
        consider_icon((HICON)result, size, &best, &distance);
    }
    if (!best) {
        consider_icon((HICON)GetClassLongPtrW(hwnd, GCLP_HICON), size, &best, &distance);
        consider_icon((HICON)GetClassLongPtrW(hwnd, GCLP_HICONSM), size, &best, &distance);
    }
    return best;
}

static HRESULT render_icon(HICON icon, int size, unsigned char *rgba) {
    HRESULT hr = E_FAIL;
    HDC dc = CreateCompatibleDC(NULL);
    if (!dc)
        return hr;
    BITMAPINFO bi = {0};
    bi.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
    bi.bmiHeader.biWidth = size;
    bi.bmiHeader.biHeight = -size;
    bi.bmiHeader.biPlanes = 1;
    bi.bmiHeader.biBitCount = 32;
    bi.bmiHeader.biCompression = BI_RGB;
    unsigned char *pixels = NULL;
    HBITMAP bitmap = CreateDIBSection(dc, &bi, DIB_RGB_COLORS, (void **)&pixels, NULL, 0);
    if (!bitmap) {
        DeleteDC(dc);
        return E_OUTOFMEMORY;
    }
    HGDIOBJ old = SelectObject(dc, bitmap);
    size_t bytes = (size_t)size * size * 4;
    memset(pixels, 0, bytes);
    if (!DrawIconEx(dc, 0, 0, icon, size, size, 0, NULL, DI_NORMAL))
        goto done;
    GdiFlush();
    memcpy(rgba, pixels, bytes);
    BOOL alpha = FALSE;
    for (size_t i = 3; i < bytes; i += 4)
        alpha |= rgba[i] != 0;
    if (!alpha) {
        // Legacy icons have no alpha. DI_MASK expands the AND mask to RGB:
        // white is transparent, black is opaque (including opaque black pixels).
        memset(pixels, 0, bytes);
        if (!DrawIconEx(dc, 0, 0, icon, size, size, 0, NULL, DI_MASK))
            goto done;
        GdiFlush();
        for (size_t i = 0; i < bytes; i += 4)
            rgba[i + 3] = pixels[i] ? 0 : 255;
    }
    // DrawIconEx on a zeroed DIB produces premultiplied BGRA. Go's RGBA
    // expects the same alpha representation, with red and blue exchanged.
    for (size_t i = 0; i < bytes; i += 4) {
        unsigned char blue = rgba[i];
        rgba[i] = rgba[i + 2];
        rgba[i + 2] = blue;
        if (!rgba[i + 3])
            rgba[i] = rgba[i + 1] = rgba[i + 2] = 0;
    }
    hr = S_OK;
done:
    SelectObject(dc, old);
    DeleteObject(bitmap);
    DeleteDC(dc);
    return hr;
}

long capture_icon(uint64_t id, int size, unsigned char *rgba) {
    HWND hwnd = (HWND)(uintptr_t)id;
    if (!IsWindow(hwnd))
        return HRESULT_FROM_WIN32(ERROR_INVALID_WINDOW_HANDLE);
    HICON icon = window_icon(hwnd, size);
    BOOL owned = FALSE;
    if (!icon) {
        WCHAR path[32768];
        if (window_path(hwnd, path)) {
            UINT count = PrivateExtractIconsW(path, 0, size, size, &icon, NULL, 1, 0);
            owned = count != 0 && count != UINT_MAX && icon != NULL;
            if (!owned)
                icon = NULL;
        }
    }
    if (!icon)
        return HRESULT_FROM_WIN32(ERROR_INVALID_WINDOW_HANDLE);
    HRESULT hr = render_icon(icon, size, rgba);
    // WM_GETICON and class icons are borrowed; only extracted icons are ours.
    if (owned)
        DestroyIcon(icon);
    return hr;
}
