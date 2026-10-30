#define COBJMACROS
#define WIN32_LEAN_AND_MEAN
#include "native_windows.h"
#include <dxgi1_2.h>
#include <stdlib.h>
#include <string.h>
#include <windows.h>

// Names are optional: an unsupported/transient DisplayConfig query must not
// prevent DXGI enumeration or capture.
static void friendly_names(capture_display *items, int count) {
    UINT32 paths_count, modes_count;
    for (int attempt = 0; attempt < 3; attempt++) {
        if (GetDisplayConfigBufferSizes(QDC_ONLY_ACTIVE_PATHS, &paths_count, &modes_count))
            return;
        DISPLAYCONFIG_PATH_INFO *paths = calloc(paths_count, sizeof(*paths));
        DISPLAYCONFIG_MODE_INFO *modes = calloc(modes_count, sizeof(*modes));
        if (!paths || !modes) {
            free(paths);
            free(modes);
            return;
        }
        LONG err = QueryDisplayConfig(QDC_ONLY_ACTIVE_PATHS, &paths_count, paths, &modes_count,
                                      modes, NULL);
        if (!err) {
            for (UINT32 p = 0; p < paths_count; p++) {
                DISPLAYCONFIG_SOURCE_DEVICE_NAME source = {0};
                source.header = (DISPLAYCONFIG_DEVICE_INFO_HEADER){
                    DISPLAYCONFIG_DEVICE_INFO_GET_SOURCE_NAME, sizeof(source),
                    paths[p].sourceInfo.adapterId, paths[p].sourceInfo.id};
                DISPLAYCONFIG_TARGET_DEVICE_NAME target = {0};
                target.header = (DISPLAYCONFIG_DEVICE_INFO_HEADER){
                    DISPLAYCONFIG_DEVICE_INFO_GET_TARGET_NAME, sizeof(target),
                    paths[p].targetInfo.adapterId, paths[p].targetInfo.id};
                if (DisplayConfigGetDeviceInfo(&source.header) ||
                    DisplayConfigGetDeviceInfo(&target.header) ||
                    !target.monitorFriendlyDeviceName[0])
                    continue;
                for (int i = 0; i < count; i++) {
                    WCHAR device[32];
                    MultiByteToWideChar(CP_UTF8, 0, items[i].device, -1, device, 32);
                    if (!wcscmp(device, source.viewGdiDeviceName))
                        WideCharToMultiByte(CP_UTF8, 0, target.monitorFriendlyDeviceName, -1,
                                            items[i].name, sizeof(items[i].name), NULL, NULL);
                }
            }
        }
        free(paths);
        free(modes);
        if (err != ERROR_INSUFFICIENT_BUFFER)
            return;
    }
}
static int display_order(const void *a, const void *b) {
    const capture_display *x = a, *y = b;
    if (x->primary != y->primary)
        return y->primary - x->primary;
    if (x->rect.x != y->rect.x)
        return x->rect.x < y->rect.x ? -1 : 1;
    if (x->rect.y != y->rect.y)
        return x->rect.y < y->rect.y ? -1 : 1;
    return strcmp(x->device, y->device);
}
long capture_displays(capture_display **out, int *count) {
    *out = NULL;
    *count = 0;
    IDXGIFactory1 *factory = NULL;
    HRESULT hr = CreateDXGIFactory1(&IID_IDXGIFactory1, (void **)&factory);
    if (FAILED(hr))
        return hr;
    for (UINT a = 0;; a++) {
        IDXGIAdapter1 *adapter = NULL;
        hr = IDXGIFactory1_EnumAdapters1(factory, a, &adapter);
        if (hr == DXGI_ERROR_NOT_FOUND) {
            hr = S_OK;
            break;
        }
        if (FAILED(hr))
            break;
        for (UINT o = 0;; o++) {
            IDXGIOutput *output = NULL;
            hr = IDXGIAdapter1_EnumOutputs(adapter, o, &output);
            if (hr == DXGI_ERROR_NOT_FOUND) {
                hr = S_OK;
                break;
            }
            if (FAILED(hr))
                break;
            DXGI_OUTPUT_DESC desc;
            hr = IDXGIOutput_GetDesc(output, &desc);
            IDXGIOutput_Release(output);
            if (FAILED(hr))
                break;
            RECT r = desc.DesktopCoordinates;
            if (!desc.AttachedToDesktop || r.right <= r.left || r.bottom <= r.top)
                continue;
            capture_display *items = realloc(*out, (*count + 1) * sizeof(*items));
            if (!items) {
                hr = E_OUTOFMEMORY;
                break;
            }
            *out = items;
            capture_display *item = &items[(*count)++];
            memset(item, 0, sizeof(*item));
            item->adapter = a;
            item->output = o;
            item->rect = (capture_rect){r.left, r.top, r.right - r.left, r.bottom - r.top};
            MONITORINFO monitor = {.cbSize = sizeof(monitor)};
            item->primary =
                GetMonitorInfoW(desc.Monitor, &monitor) && (monitor.dwFlags & MONITORINFOF_PRIMARY);
            WideCharToMultiByte(CP_UTF8, 0, desc.DeviceName, -1, item->device, sizeof(item->device),
                                NULL, NULL);
            strcpy(item->name, item->device);
        }
        IDXGIAdapter1_Release(adapter);
        if (FAILED(hr))
            break;
    }
    IDXGIFactory1_Release(factory);
    if (FAILED(hr)) {
        free(*out);
        *out = NULL;
        *count = 0;
        return hr;
    }
    qsort(*out, *count, sizeof(**out), display_order);
    friendly_names(*out, *count);
    return S_OK;
}
