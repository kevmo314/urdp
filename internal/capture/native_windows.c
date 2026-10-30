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
    UINT adapter_index, output_index;
    capture_native **outputs;
    int output_count, next_output;
    unsigned char *composite;
    HANDLE poll_timer;
    int full_update;
    UINT move_bytes, dirty_bytes;
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
    hr = IDXGIFactory1_EnumAdapters1(factory, s->adapter_index, &adapter);
    if (SUCCEEDED(hr))
        hr = IDXGIAdapter1_EnumOutputs(adapter, s->output_index, &output);
    DXGI_OUTPUT_DESC desc;
    if (SUCCEEDED(hr))
        hr = IDXGIOutput_GetDesc(output, &desc);
    if (SUCCEEDED(hr) && !desc.AttachedToDesktop)
        hr = DXGI_ERROR_NOT_FOUND;
    if (SUCCEEDED(hr)) {
        RECT r = desc.DesktopCoordinates;
        s->rect = (capture_rect){r.left, r.top, r.right - r.left, r.bottom - r.top};
        s->rotation = desc.Rotation;
        hr = D3D11CreateDevice((IDXGIAdapter *)adapter, D3D_DRIVER_TYPE_UNKNOWN, NULL, 0, NULL, 0,
                               D3D11_SDK_VERSION, &s->device, NULL, &s->context);
    }
    if (SUCCEEDED(hr)) {
        hr = IDXGIOutput_QueryInterface(output, &IID_IDXGIOutput5, (void **)&output5);
        if (SUCCEEDED(hr)) {
            const DXGI_FORMAT format = DXGI_FORMAT_B8G8R8A8_UNORM;
            hr = IDXGIOutput5_DuplicateOutput1(output5, (IUnknown *)s->device, 0, 1, &format,
                                               &s->dup);
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
long capture_open_display(unsigned int adapter, unsigned int output, capture_native **out) {
    capture_native *s = calloc(1, sizeof(*s));
    if (!s)
        return E_OUTOFMEMORY;
    s->adapter_index = adapter;
    s->output_index = output;
    HRESULT hr = desktop_init(s);
    if (FAILED(hr)) {
        capture_close(s);
        return hr;
    }
    *out = s;
    return S_OK;
}
static HRESULT composite_init(capture_native *s) {
    capture_display *displays = NULL;
    int count = 0;
    HRESULT hr = capture_displays(&displays, &count);
    if (FAILED(hr))
        return hr;
    if (!count) {
        free(displays);
        return DXGI_ERROR_NOT_FOUND;
    }
    s->outputs = calloc(count, sizeof(*s->outputs));
    if (!s->outputs) {
        free(displays);
        return E_OUTOFMEMORY;
    }
    s->output_count = count;
    for (int i = 0; i < count; i++) {
        hr = capture_open_display(displays[i].adapter, displays[i].output, &s->outputs[i]);
        if (FAILED(hr))
            break;
    }
    free(displays);
    return hr;
}
// Recompute after a duplication is recreated: mode/position changes can resize
// the virtual desktop. Rebuild only the CPU canvas; other duplications survive.
static HRESULT composite_bounds(capture_native *s, int force, int *reset) {
    capture_rect r = s->outputs[0]->rect;
    int right = r.x + r.w, bottom = r.y + r.h;
    for (int i = 1; i < s->output_count; i++) {
        capture_rect o = s->outputs[i]->rect;
        if (o.x < r.x)
            r.x = o.x;
        if (o.y < r.y)
            r.y = o.y;
        if (o.x + o.w > right)
            right = o.x + o.w;
        if (o.y + o.h > bottom)
            bottom = o.y + o.h;
    }
    r.w = right - r.x;
    r.h = bottom - r.y;
    *reset = force || !s->composite || memcmp(&r, &s->rect, sizeof(r));
    if (*reset) {
        size_t size = (size_t)r.w * r.h * 4;
        unsigned char *p = calloc(1, size);
        if (!p)
            return E_OUTOFMEMORY;
        free(s->composite);
        s->composite = p;
        s->rect = r;
    }
    s->image = (capture_image){s->composite, r.w * 4, r.w, r.h};
    return S_OK;
}
long capture_open(uint64_t hwnd, capture_native **out) {
    capture_native *s = calloc(1, sizeof(*s));
    if (!s)
        return E_OUTOFMEMORY;
    s->hwnd = (HWND)(uintptr_t)hwnd;
    capture_rect r;
    HRESULT hr = hwnd ? capture_bounds(s, &r) : composite_init(s);
    if (SUCCEEDED(hr) && !hwnd) {
        int reset;
        hr = composite_bounds(s, 0, &reset);
    }
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
        BOOL copied = BitBlt(s->dc, 0, 0, w, h, screen, s->rect.x, s->rect.y, SRCCOPY | CAPTUREBLT);
        ReleaseDC(NULL, screen);
        if (!copied)
            return E_FAIL;
        GdiFlush();
        s->full_update = 1;
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
static RECT update_rect(capture_native *s, UINT i) {
    UINT moves = s->move_bytes / sizeof(DXGI_OUTDUPL_MOVE_RECT);
    return i < moves ? ((DXGI_OUTDUPL_MOVE_RECT *)s->metadata)[i].DestinationRect
                     : ((RECT *)(s->metadata + s->move_bytes))[i - moves];
}
static RECT rotated_rect(capture_native *s, RECT r) {
    int w = s->rect.w, h = s->rect.h;
    switch (s->rotation) {
    case DXGI_MODE_ROTATION_ROTATE90:
        return (RECT){w - r.bottom, r.left, w - r.top, r.right};
    case DXGI_MODE_ROTATION_ROTATE180:
        return (RECT){w - r.right, h - r.bottom, w - r.left, h - r.top};
    case DXGI_MODE_ROTATION_ROTATE270:
        return (RECT){r.top, h - r.right, r.bottom, h - r.left};
    default:
        return r;
    }
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
    s->move_bytes = s->dirty_bytes = 0;
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
    else if (!full && info && info->TotalMetadataBufferSize) {
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
            s->move_bytes = moves;
            s->dirty_bytes = dirty;
            // Read move destinations from the current desktop texture. This also
            // handles overlapping moves without a temporary CPU image.
            for (UINT i = 0; i < moves / sizeof(DXGI_OUTDUPL_MOVE_RECT) + dirty / sizeof(RECT);
                 i++) {
                RECT r = update_rect(s, i);
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
    s->full_update = full || box != NULL;
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
    UINT count = s->full_update ? 1
                                : s->move_bytes / sizeof(DXGI_OUTDUPL_MOVE_RECT) +
                                      s->dirty_bytes / sizeof(RECT);
    for (UINT i = 0; i < count; i++) {
        RECT r = s->full_update ? (RECT){0, 0, w, h} : rotated_rect(s, update_rect(s, i));
        if (r.left < 0)
            r.left = 0;
        if (r.top < 0)
            r.top = 0;
        if (r.right > w)
            r.right = w;
        if (r.bottom > h)
            r.bottom = h;
        for (int y = r.top; y < r.bottom; y++)
            for (int x = r.left; x < r.right; x++) {
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
    }
    unmap(s);
    s->image = (capture_image){s->rotated, w * 4, w, h};
    return S_OK;
}
static HRESULT composite_frame(capture_native *s, unsigned int timeout_ms);
long capture_frame(capture_native *s, capture_image *image, unsigned int timeout_ms) {
    if (s->outputs) {
        HRESULT hr = composite_frame(s, timeout_ms);
        *image = s->image;
        return hr;
    }
    s->full_update = 0;
    s->move_bytes = s->dirty_bytes = 0;
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
// Metadata rectangles use the unrotated duplication surface. Rotate their
// edges into desktop orientation, then copy just those rows into the canvas.
static void composite_copy(capture_native *s, capture_native *o, RECT r, int rotate) {
    int w = o->rect.w, h = o->rect.h;
    if (rotate)
        r = rotated_rect(o, r);
    if (r.left < 0)
        r.left = 0;
    if (r.top < 0)
        r.top = 0;
    if (r.right > w)
        r.right = w;
    if (r.bottom > h)
        r.bottom = h;
    if (r.right <= r.left || r.bottom <= r.top || !o->image.pixels)
        return;
    int x = o->rect.x - s->rect.x, y = o->rect.y - s->rect.y;
    for (int row = r.top; row < r.bottom; row++)
        memcpy(s->composite + (size_t)(y + row) * s->image.stride + (x + r.left) * 4,
               o->image.pixels + (size_t)row * o->image.stride + r.left * 4,
               (size_t)(r.right - r.left) * 4);
}
static HRESULT composite_frame(capture_native *s, unsigned int timeout_ms) {
    LARGE_INTEGER frequency, start, now;
    QueryPerformanceFrequency(&frequency);
    QueryPerformanceCounter(&start);
    for (;;) {
        int changed = 0;
        for (int n = 0; n < s->output_count; n++) {
            int i = (s->next_output + n) % s->output_count;
            capture_native *o = s->outputs[i];
            capture_image image;
            capture_rect previous = o->rect;
            HRESULT hr = capture_frame(o, &image, 0);
            if (FAILED(hr))
                return hr;
            int reset;
            hr = composite_bounds(s, memcmp(&previous, &o->rect, sizeof(previous)), &reset);
            if (FAILED(hr))
                return hr;
            if (reset) {
                for (int j = 0; j < s->output_count; j++) {
                    capture_native *other = s->outputs[j];
                    composite_copy(s, other, (RECT){0, 0, other->rect.w, other->rect.h}, 0);
                }
                changed = 1;
            }
            // A timeout retains the same pixels; full_update is cleared after use.
            if (o->full_update) {
                composite_copy(s, o, (RECT){0, 0, o->rect.w, o->rect.h}, 0);
                changed = 1;
            } else if (o->move_bytes || o->dirty_bytes) {
                UINT moves = o->move_bytes / sizeof(DXGI_OUTDUPL_MOVE_RECT);
                UINT dirty = o->dirty_bytes / sizeof(RECT);
                for (UINT j = 0; j < moves + dirty; j++) {
                    RECT r = update_rect(o, j);
                    composite_copy(s, o, r, 1);
                }
                changed = 1;
            }
            o->full_update = 0;
            o->move_bytes = o->dirty_bytes = 0;
        }
        s->next_output = (s->next_output + 1) % s->output_count;
        if (changed)
            return S_OK;
        QueryPerformanceCounter(&now);
        if ((now.QuadPart - start.QuadPart) * 1000 >= (LONGLONG)timeout_ms * frequency.QuadPart)
            return S_FALSE;
        // A waitable timer avoids Sleep(1)'s ~15.6ms default timer granularity,
        // without changing the process/system timer resolution.
        LARGE_INTEGER due = {.QuadPart = -10000};
        if (!s->poll_timer)
            s->poll_timer = CreateWaitableTimerExW(NULL, NULL, CREATE_WAITABLE_TIMER_HIGH_RESOLUTION,
                                                     TIMER_ALL_ACCESS);
        if (s->poll_timer && SetWaitableTimer(s->poll_timer, &due, 0, NULL, NULL, FALSE))
            WaitForSingleObject(s->poll_timer, INFINITE);
        else
            Sleep(1);
    }
}
void capture_close(capture_native *s) {
    if (!s)
        return;
    for (int i = 0; i < s->output_count; i++)
        capture_close(s->outputs[i]);
    free(s->outputs);
    free(s->composite);
    if (s->poll_timer)
        CloseHandle(s->poll_timer);
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
    capture_window_app(w->id, w->app, sizeof(w->app));
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
