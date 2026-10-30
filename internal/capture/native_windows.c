#define COBJMACROS
#define WIN32_LEAN_AND_MEAN
#include "native_windows.h"
#include "wgc_windows.h"
#include <d3d11.h>
#include <dwmapi.h>
#include <dxgi1_5.h>
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
    capture_image image;
    int mapped;
    unsigned char *rotated;
    size_t rotated_size;
    capture_wgc *wgc;
    unsigned char *metadata;
    UINT metadata_size;
    HDC dc;
    HBITMAP bitmap;
    HGDIOBJ old;
    unsigned char *pixels;
    int bw, bh;
};
#define RELEASE(p)                                                                                 \
    do {                                                                                           \
        if (p) {                                                                                   \
            IUnknown_Release((IUnknown *)(p));                                                     \
            (p) = NULL;                                                                            \
        }                                                                                          \
    } while (0)
void capture_dpi(void) {
    typedef BOOL(WINAPI * dpi_fn)(HANDLE);
    dpi_fn fn =
        (dpi_fn)GetProcAddress(GetModuleHandleW(L"user32.dll"), "SetProcessDpiAwarenessContext");
    if (fn)
        fn((HANDLE)(intptr_t)-4);
}
static void unmap(capture_native *s) {
    if (s->mapped) {
        ID3D11DeviceContext_Unmap(s->context, (ID3D11Resource *)s->staging, 0);
        s->mapped = 0;
    }
}
static HRESULT desktop_init(capture_native *s) {
    unmap(s);
    s->image = (capture_image){0};
    RELEASE(s->staging);
    RELEASE(s->dup);
    RELEASE(s->context);
    RELEASE(s->device);
    IDXGIFactory1 *factory = NULL;
    IDXGIAdapter1 *adapter = NULL;
    IDXGIOutput *output = NULL;
    IDXGIOutput1 *output1 = NULL;
    IDXGIOutput5 *output5 = NULL;
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
                if (SUCCEEDED(hr)) {
                    hr = IDXGIOutput_QueryInterface(output, &IID_IDXGIOutput5, (void **)&output5);
                    if (SUCCEEDED(hr)) {
                        const DXGI_FORMAT format = DXGI_FORMAT_B8G8R8A8_UNORM;
                        hr = IDXGIOutput5_DuplicateOutput1(output5, (IUnknown *)s->device, 0, 1,
                                                           &format, &s->dup);
                    }
                    if (FAILED(hr)) {
                        hr = IDXGIOutput_QueryInterface(output, &IID_IDXGIOutput1, (void **)&output1);
                        if (SUCCEEDED(hr))
                            hr = IDXGIOutput1_DuplicateOutput(output1, (IUnknown *)s->device, &s->dup);
                    }
                }
                RELEASE(output5);
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
static HRESULT window_bounds(capture_native *s) {
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
    return S_OK;
}
long capture_bounds(capture_native *s, capture_rect *r) {
    HRESULT hr = window_bounds(s);
    if (FAILED(hr))
        return hr;
    *r = s->rect;
    if (s->hwnd && s->image.pixels) {
        r->w = s->image.w;
        r->h = s->image.h;
    }
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
    if (hwnd) {
        hr = D3D11CreateDevice(NULL, D3D_DRIVER_TYPE_HARDWARE, NULL,
                               D3D11_CREATE_DEVICE_BGRA_SUPPORT, NULL, 0, D3D11_SDK_VERSION,
                               &s->device, NULL, &s->context);
        if (SUCCEEDED(hr))
            capture_wgc_open(s->hwnd, s->device, &s->wgc);
    }
    *out = s;
    return S_OK;
}
static HRESULT gdi_frame(capture_native *s) {
    int w = s->rect.w, h = s->rect.h;
    if (s->hwnd && !IsWindow(s->hwnd))
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
    int changed = 0;
    if (!s->bitmap || w != s->bw || h != s->bh) {
        changed = 1;
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
    if (s->hwnd) {
        // PW_CLIENTONLY | PW_RENDERFULLCONTENT: capture the client area.
        if (!PrintWindow(s->hwnd, s->dc, 3))
            return E_FAIL;
    } else {
        // Seed a new duplication even when DWM has no new presentation to report.
        HDC screen = GetDC(NULL);
        if (!screen)
            return E_FAIL;
        BOOL copied = BitBlt(s->dc, 0, 0, w, h, screen, s->rect.x, s->rect.y,
                             SRCCOPY | CAPTUREBLT);
        ReleaseDC(NULL, screen);
        if (!copied)
            return E_FAIL;
        GdiFlush();
        s->image = (capture_image){s->pixels, w * 4, w, h};
        return S_OK;
    }
    GdiFlush();
    size_t size = (size_t)w * h * 4;
    if (size > s->rotated_size) {
        unsigned char *p = realloc(s->rotated, size);
        if (!p)
            return E_OUTOFMEMORY;
        s->rotated = p;
        s->rotated_size = size;
        changed = 1;
    }
    // PrintWindow has no update notifications. Compare against the last image.
    if (!changed)
        changed = memcmp(s->rotated, s->pixels, size) != 0;
    if (changed)
        memcpy(s->rotated, s->pixels, size);
    s->image = (capture_image){s->pixels, w * 4, w, h};
    return changed ? S_OK : S_FALSE;
}
// Keep the readback mapped until a newer image replaces it. Timeouts retain the
// previous mapping so keyframes can still encode the last image without a copy.
static HRESULT texture_frame(capture_native *s, ID3D11Texture2D *texture, const D3D11_BOX *box,
                             const DXGI_OUTDUPL_FRAME_INFO *info) {
    D3D11_TEXTURE2D_DESC d, desc;
    ID3D11Texture2D_GetDesc(texture, &d);
    if (d.Format != DXGI_FORMAT_B8G8R8A8_UNORM)
        return DXGI_ERROR_UNSUPPORTED;
    desc = d;
    if (box) {
        desc.Width = box->right - box->left;
        desc.Height = box->bottom - box->top;
    }
    unmap(s);
    s->image = (capture_image){0};
    if (s->staging) {
        D3D11_TEXTURE2D_DESC old;
        ID3D11Texture2D_GetDesc(s->staging, &old);
        if (old.Width != desc.Width || old.Height != desc.Height || old.Format != desc.Format)
            RELEASE(s->staging);
    }
    int full = !s->staging;
    HRESULT hr = S_OK;
    if (!s->staging) {
        desc.Usage = D3D11_USAGE_STAGING;
        desc.BindFlags = 0;
        desc.CPUAccessFlags = D3D11_CPU_ACCESS_READ;
        desc.MiscFlags = 0;
        hr = ID3D11Device_CreateTexture2D(s->device, &desc, NULL, &s->staging);
    }
    if (FAILED(hr))
        return hr;
    if (box)
        ID3D11DeviceContext_CopySubresourceRegion(s->context, (ID3D11Resource *)s->staging, 0, 0, 0,
                                                  0, (ID3D11Resource *)texture, 0, box);
    else if (!full && info && info->TotalMetadataBufferSize &&
             (s->rotation == DXGI_MODE_ROTATION_IDENTITY ||
              s->rotation == DXGI_MODE_ROTATION_UNSPECIFIED)) {
        if (info->TotalMetadataBufferSize > s->metadata_size) {
            unsigned char *p = realloc(s->metadata, info->TotalMetadataBufferSize);
            if (!p)
                return E_OUTOFMEMORY;
            s->metadata = p;
            s->metadata_size = info->TotalMetadataBufferSize;
        }
        UINT moves = 0, dirty = 0;
        hr = IDXGIOutputDuplication_GetFrameMoveRects(
            s->dup, s->metadata_size, (DXGI_OUTDUPL_MOVE_RECT *)s->metadata, &moves);
        if (SUCCEEDED(hr))
            hr = IDXGIOutputDuplication_GetFrameDirtyRects(s->dup, s->metadata_size - moves,
                                                           (RECT *)(s->metadata + moves), &dirty);
        if (SUCCEEDED(hr)) {
            // Read move destinations from the current desktop texture. This also
            // handles overlapping moves without a temporary CPU image.
            for (UINT i = 0; i < moves / sizeof(DXGI_OUTDUPL_MOVE_RECT) + dirty / sizeof(RECT);
                 i++) {
                UINT count = moves / sizeof(DXGI_OUTDUPL_MOVE_RECT);
                RECT r = i < count ? ((DXGI_OUTDUPL_MOVE_RECT *)s->metadata)[i].DestinationRect
                                   : ((RECT *)(s->metadata + moves))[i - count];
                D3D11_BOX region = {r.left, r.top, 0, r.right, r.bottom, 1};
                ID3D11DeviceContext_CopySubresourceRegion(s->context, (ID3D11Resource *)s->staging,
                                                          0, r.left, r.top, 0,
                                                          (ID3D11Resource *)texture, 0, &region);
            }
        } else
            full = 1; // Metadata is an optimization; a full copy remains correct.
    } else
        full = 1;
    if (full && !box)
        ID3D11DeviceContext_CopyResource(s->context, (ID3D11Resource *)s->staging,
                                         (ID3D11Resource *)texture);
    D3D11_MAPPED_SUBRESOURCE map;
    hr = ID3D11DeviceContext_Map(s->context, (ID3D11Resource *)s->staging, 0, D3D11_MAP_READ, 0,
                                 &map);
    if (FAILED(hr))
        return hr;
    s->mapped = 1;
    int w = s->rect.w, h = s->rect.h;
    if (s->rotation == DXGI_MODE_ROTATION_IDENTITY ||
        s->rotation == DXGI_MODE_ROTATION_UNSPECIFIED) {
        s->image = (capture_image){map.pData, map.RowPitch, w, h};
        return S_OK;
    }
    size_t size = (size_t)w * h * 4;
    if (size > s->rotated_size) {
        unsigned char *p = realloc(s->rotated, size);
        if (!p)
            return E_OUTOFMEMORY;
        s->rotated = p;
        s->rotated_size = size;
    }
    for (int y = 0; y < h; y++)
        for (int x = 0; x < w; x++) {
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
            memcpy(s->rotated + ((size_t)y * w + x) * 4,
                   (char *)map.pData + (size_t)sy * map.RowPitch + sx * 4, 4);
        }
    unmap(s);
    s->image = (capture_image){s->rotated, w * 4, w, h};
    return S_OK;
}
long capture_frame(capture_native *s, capture_image *image, unsigned int timeout_ms) {
    HRESULT hr;
    if (s->hwnd) {
        hr = window_bounds(s);
        if (FAILED(hr))
            return hr;
        if (s->wgc) {
            ID3D11Texture2D *texture = NULL;
            D3D11_BOX box;
            hr = capture_wgc_frame(s->wgc, timeout_ms, &texture, &box);
            if (hr == S_OK) {
                s->rect.w = box.right - box.left;
                s->rect.h = box.bottom - box.top;
                hr = texture_frame(s, texture, &box, NULL);
            }
            RELEASE(texture);
            if (FAILED(hr)) {
                // A stopped compositor/device must not leave a permanently broken source.
                capture_wgc_close(s->wgc);
                s->wgc = NULL;
                unmap(s);
                RELEASE(s->staging);
                s->image = (capture_image){0};
                hr = gdi_frame(s);
            }
        } else
            hr = gdi_frame(s);
        *image = s->image;
        return hr;
    }
    if (!s->dup) {
        hr = desktop_init(s);
        if (FAILED(hr))
            return hr;
    }
    DXGI_OUTDUPL_FRAME_INFO info;
    IDXGIResource *resource = NULL;
    hr = IDXGIOutputDuplication_AcquireNextFrame(s->dup, timeout_ms, &info, &resource);
    if (hr == DXGI_ERROR_ACCESS_LOST) {
        hr = desktop_init(s);
        if (SUCCEEDED(hr))
            hr = gdi_frame(s);
        *image = s->image;
        return hr;
    }
    if (hr == DXGI_ERROR_WAIT_TIMEOUT) {
        hr = s->image.pixels ? S_FALSE : gdi_frame(s);
        *image = s->image;
        return hr;
    }
    if (FAILED(hr))
        return hr;
    // Pointer updates do not alter the desktop image (the client draws its cursor).
    if (!info.LastPresentTime.QuadPart) {
        RELEASE(resource);
        IDXGIOutputDuplication_ReleaseFrame(s->dup);
        hr = s->image.pixels ? S_FALSE : gdi_frame(s);
        *image = s->image;
        return hr;
    }
    ID3D11Texture2D *texture = NULL;
    hr = IDXGIResource_QueryInterface(resource, &IID_ID3D11Texture2D, (void **)&texture);
    RELEASE(resource);
    if (SUCCEEDED(hr))
        hr = texture_frame(s, texture, NULL, &info);
    RELEASE(texture);
    IDXGIOutputDuplication_ReleaseFrame(s->dup);
    *image = s->image;
    return hr;
}
void capture_close(capture_native *s) {
    if (!s)
        return;
    capture_wgc_close(s->wgc);
    unmap(s);
    RELEASE(s->staging);
    RELEASE(s->dup);
    RELEASE(s->context);
    RELEASE(s->device);
    free(s->rotated);
    free(s->metadata);
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
void capture_free(void *p) {
    free(p);
}
