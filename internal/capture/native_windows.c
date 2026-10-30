#define COBJMACROS
#define WIN32_LEAN_AND_MEAN
#include "native_windows.h"
#include <d3d11.h>
#include <dwmapi.h>
#include <dxgi1_2.h>
#include <stdlib.h>
#include <string.h>
#include <windows.h>

struct capture_native {
    HWND hwnd;
    capture_rect rect;
    ID3D11Device *device;
    ID3D11DeviceContext *context;
    IDXGIOutputDuplication *dup;
    ID3D11Texture2D *staging;
    DXGI_MODE_ROTATION rotation;
    HDC dc;
    HBITMAP bitmap;
    HGDIOBJ old;
    unsigned char *pixels;
    int bw, bh;
};
#define RELEASE(p) \
    do { \
        if (p) { \
            IUnknown_Release((IUnknown *)(p)); \
            (p) = NULL; \
        } \
    } while (0)
void capture_dpi(void) {
    typedef BOOL(WINAPI * dpi_fn)(HANDLE);
    dpi_fn fn =
        (dpi_fn)GetProcAddress(GetModuleHandleW(L"user32.dll"), "SetProcessDpiAwarenessContext");
    if (fn)
        fn((HANDLE)(intptr_t)-4);
}
static HRESULT desktop_init(capture_native *s) {
    RELEASE(s->staging);
    RELEASE(s->dup);
    RELEASE(s->context);
    RELEASE(s->device);
    IDXGIFactory1 *factory = NULL;
    IDXGIAdapter1 *adapter = NULL;
    IDXGIOutput *output = NULL;
    IDXGIOutput1 *output1 = NULL;
    HRESULT hr = CreateDXGIFactory1(&IID_IDXGIFactory1, (void **)&factory);
    if (FAILED(hr))
        return hr;
    for (UINT a = 0; SUCCEEDED(IDXGIFactory1_EnumAdapters1(factory, a, &adapter)); ++a) {
        for (UINT o = 0; SUCCEEDED(IDXGIAdapter1_EnumOutputs(adapter, o, &output)); ++o) {
            DXGI_OUTPUT_DESC desc;
            hr = IDXGIOutput_GetDesc(output, &desc);
            if (SUCCEEDED(hr) && desc.AttachedToDesktop && desc.DesktopCoordinates.left == 0 &&
                desc.DesktopCoordinates.top == 0) {
                RECT r = desc.DesktopCoordinates;
                s->rect =
                    (capture_rect){r.left, r.top, (r.right - r.left) & ~1, (r.bottom - r.top) & ~1};
                s->rotation = desc.Rotation;
                hr = D3D11CreateDevice((IDXGIAdapter *)adapter, D3D_DRIVER_TYPE_UNKNOWN, NULL, 0,
                                       NULL, 0, D3D11_SDK_VERSION, &s->device, NULL, &s->context);
                if (SUCCEEDED(hr))
                    hr = IDXGIOutput_QueryInterface(output, &IID_IDXGIOutput1, (void **)&output1);
                if (SUCCEEDED(hr))
                    hr = IDXGIOutput1_DuplicateOutput(output1, (IUnknown *)s->device, &s->dup);
                RELEASE(output1);
                RELEASE(output);
                RELEASE(adapter);
                RELEASE(factory);
                return hr;
            }
            RELEASE(output);
        }
        RELEASE(adapter);
    }
    RELEASE(factory);
    return DXGI_ERROR_NOT_FOUND;
}
long capture_bounds(capture_native *s, capture_rect *r) {
    if (s->hwnd) {
        RECT client;
        POINT origin = {0, 0};
        if (!IsWindow(s->hwnd))
            return HRESULT_FROM_WIN32(ERROR_INVALID_WINDOW_HANDLE);
        if (!GetClientRect(s->hwnd, &client) || !ClientToScreen(s->hwnd, &origin))
            return E_FAIL;
        s->rect = (capture_rect){origin.x, origin.y, client.right & ~1, client.bottom & ~1};
        if (s->rect.w <= 0 || s->rect.h <= 0)
            return E_FAIL;
    }
    *r = s->rect;
    return S_OK;
}
long capture_open(uint64_t hwnd, capture_native **out) {
    capture_native *s = calloc(1, sizeof(*s));
    if (!s)
        return E_OUTOFMEMORY;
    s->hwnd = (HWND)(uintptr_t)hwnd;
    capture_rect r;
    HRESULT hr = hwnd ? capture_bounds(s, &r) : desktop_init(s);
    if (FAILED(hr)) {
        capture_close(s);
        return hr;
    }
    *out = s;
    return S_OK;
}
static HRESULT window_frame(capture_native *s, unsigned char *dst) {
    int w = s->rect.w, h = s->rect.h;
    if (!IsWindow(s->hwnd))
        return HRESULT_FROM_WIN32(ERROR_INVALID_WINDOW_HANDLE);
    if (!s->dc) {
        HDC screen = GetDC(NULL);
        if (!screen)
            return E_FAIL;
        s->dc = CreateCompatibleDC(screen);
        ReleaseDC(NULL, screen);
    }
    if (!s->dc)
        return E_FAIL;
    if (!s->bitmap || w != s->bw || h != s->bh) {
        if (s->bitmap) {
            SelectObject(s->dc, s->old);
            DeleteObject(s->bitmap);
            s->bitmap = NULL;
        }
        BITMAPINFO bi = {0};
        bi.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
        bi.bmiHeader.biWidth = w;
        bi.bmiHeader.biHeight = -h;
        bi.bmiHeader.biPlanes = 1;
        bi.bmiHeader.biBitCount = 32;
        bi.bmiHeader.biCompression = BI_RGB;
        s->bitmap = CreateDIBSection(s->dc, &bi, DIB_RGB_COLORS, (void **)&s->pixels, NULL, 0);
        if (!s->bitmap)
            return E_OUTOFMEMORY;
        s->old = SelectObject(s->dc, s->bitmap);
        s->bw = w;
        s->bh = h;
    }
    // PW_CLIENTONLY | PW_RENDERFULLCONTENT: bounds and pixels both cover the client area.
    if (!PrintWindow(s->hwnd, s->dc, 3))
        return E_FAIL;
    GdiFlush();
    memcpy(dst, s->pixels, (size_t)w * h * 4);
    for (size_t i = 3; i < (size_t)w * h * 4; i += 4)
        dst[i] = 255;
    return S_OK;
}
long capture_frame(capture_native *s, unsigned char *dst, unsigned int timeout_ms) {
    if (s->hwnd)
        return window_frame(s, dst);
    if (!s->dup) {
        HRESULT hr = desktop_init(s);
        return FAILED(hr) ? hr : S_FALSE;
    }
    DXGI_OUTDUPL_FRAME_INFO info;
    IDXGIResource *resource = NULL;
    HRESULT hr = IDXGIOutputDuplication_AcquireNextFrame(s->dup, timeout_ms, &info, &resource);
    if (hr == DXGI_ERROR_WAIT_TIMEOUT)
        return S_FALSE;
    if (hr == DXGI_ERROR_ACCESS_LOST) {
        hr = desktop_init(s);
        return FAILED(hr) ? hr : S_FALSE;
    }
    if (FAILED(hr))
        return hr;
    // Pointer-only notifications can precede the first actual desktop image.
    if (!info.LastPresentTime.QuadPart) {
        RELEASE(resource);
        IDXGIOutputDuplication_ReleaseFrame(s->dup);
        return S_FALSE;
    }
    ID3D11Texture2D *texture = NULL;
    hr = IDXGIResource_QueryInterface(resource, &IID_ID3D11Texture2D, (void **)&texture);
    RELEASE(resource);
    if (SUCCEEDED(hr) && !s->staging) {
        D3D11_TEXTURE2D_DESC desc;
        ID3D11Texture2D_GetDesc(texture, &desc);
        desc.Usage = D3D11_USAGE_STAGING;
        desc.BindFlags = 0;
        desc.CPUAccessFlags = D3D11_CPU_ACCESS_READ;
        desc.MiscFlags = 0;
        hr = ID3D11Device_CreateTexture2D(s->device, &desc, NULL, &s->staging);
    }
    if (SUCCEEDED(hr)) {
        ID3D11DeviceContext_CopyResource(s->context, (ID3D11Resource *)s->staging,
                                         (ID3D11Resource *)texture);
        D3D11_MAPPED_SUBRESOURCE map;
        hr = ID3D11DeviceContext_Map(s->context, (ID3D11Resource *)s->staging, 0, D3D11_MAP_READ, 0,
                                     &map);
        if (SUCCEEDED(hr)) {
            D3D11_TEXTURE2D_DESC d;
            ID3D11Texture2D_GetDesc(texture, &d);
            for (int y = 0; y < s->rect.h; y++) {
                if (s->rotation == DXGI_MODE_ROTATION_IDENTITY ||
                    s->rotation == DXGI_MODE_ROTATION_UNSPECIFIED) {
                    memcpy(dst + (size_t)y * s->rect.w * 4,
                           (char *)map.pData + (size_t)y * map.RowPitch, (size_t)s->rect.w * 4);
                } else
                    for (int x = 0; x < s->rect.w; x++) {
                        int sx = x, sy = y;
                        switch (s->rotation) {
                        case DXGI_MODE_ROTATION_ROTATE90:
                            sx = y;
                            sy = d.Height - 1 - x;
                            break;
                        case DXGI_MODE_ROTATION_ROTATE180:
                            sx = d.Width - 1 - x;
                            sy = d.Height - 1 - y;
                            break;
                        case DXGI_MODE_ROTATION_ROTATE270:
                            sx = d.Width - 1 - y;
                            sy = x;
                            break;
                        default:
                            break;
                        }
                        memcpy(dst + ((size_t)y * s->rect.w + x) * 4,
                               (char *)map.pData + (size_t)sy * map.RowPitch + sx * 4, 4);
                    }
            }
            ID3D11DeviceContext_Unmap(s->context, (ID3D11Resource *)s->staging, 0);
        }
    }
    RELEASE(texture);
    IDXGIOutputDuplication_ReleaseFrame(s->dup);
    return hr;
}
void capture_close(capture_native *s) {
    if (!s)
        return;
    RELEASE(s->staging);
    RELEASE(s->dup);
    RELEASE(s->context);
    RELEASE(s->device);
    if (s->bitmap) {
        SelectObject(s->dc, s->old);
        DeleteObject(s->bitmap);
    }
    if (s->dc)
        DeleteDC(s->dc);
    free(s);
}
typedef struct {
    capture_window *items;
    int count;
    HRESULT hr;
} window_list;
static BOOL CALLBACK enum_window(HWND hwnd, LPARAM param) {
    window_list *list = (window_list *)param;
    DWORD cloaked = 0;
    RECT rect;
    if (!IsWindowVisible(hwnd) || (GetWindowLongPtrW(hwnd, GWL_EXSTYLE) & WS_EX_TOOLWINDOW))
        return TRUE;
    DwmGetWindowAttribute(hwnd, DWMWA_CLOAKED, &cloaked, sizeof(cloaked));
    if (cloaked || !GetClientRect(hwnd, &rect) || rect.right < 2 || rect.bottom < 2)
        return TRUE;
    WCHAR title[2048];
    if (!GetWindowTextW(hwnd, title, 2048))
        return TRUE;
    capture_window *items = realloc(list->items, (list->count + 1) * sizeof(*items));
    if (!items) {
        list->hr = E_OUTOFMEMORY;
        return FALSE;
    }
    list->items = items;
    capture_window *w = &items[list->count++];
    memset(w, 0, sizeof(*w));
    w->id = (uint64_t)(uintptr_t)hwnd;
    w->w = rect.right & ~1;
    w->h = rect.bottom & ~1;
    WideCharToMultiByte(CP_UTF8, 0, title, -1, w->title, sizeof(w->title), NULL, NULL);
    return TRUE;
}
long capture_list(capture_window **out, int *count) {
    window_list list = {0};
    EnumWindows(enum_window, (LPARAM)&list);
    if (FAILED(list.hr)) {
        free(list.items);
        return list.hr;
    }
    *out = list.items;
    *count = list.count;
    return S_OK;
}
void capture_free(void *p) { free(p); }
