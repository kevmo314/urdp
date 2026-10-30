#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include "native_windows.h"
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

// MFCreateVirtualCamera needs a separate registered COM media source. Microsoft's
// sample installs that DLL in HKLM, and FrameServer/FrameServerMonitor load it in
// service processes; extracting a DLL and registering it in the caller's HKCU
// does not supply service-visible registration. Use the already installed OBS
// source instead, keeping URDP a single executable without an admin installer.
// https://github.com/microsoft/Windows-Camera/tree/master/Samples/VirtualCamera
// OBS wire layout:
// https://github.com/obsproject/obs-studio/blob/master/shared/obs-shared-memory-queue/shared-memory-queue.c
// Fixed output keeps the mapping valid while a consumer holds it open. Scale
// changing viewer dimensions into 1280x720 NV12; OBS consumers do their own
// negotiated output scaling. OBS itself must not own the virtual camera queue.
#define CAM_WIDTH 1280
#define CAM_HEIGHT 720
struct queue_header {
    volatile LONG write_idx, read_idx, state;
    uint32_t offsets[3];
    uint32_t type, cx, cy;
    uint64_t interval;
    uint32_t reserved[8];
};
_Static_assert(sizeof(struct queue_header) == 80, "OBS queue ABI");
struct camera_native {
    HANDLE mapping;
    struct queue_header *header;
    LARGE_INTEGER frequency;
};
void camera_close(camera_native *s) {
    if (!s)
        return;
    if (s->header) {
        InterlockedExchange(&s->header->state, 3); // STOPPING
        UnmapViewOfFile(s->header);
    }
    if (s->mapping)
        CloseHandle(s->mapping);
    free(s);
}
unsigned long camera_open(camera_native **out) {
    HKEY key;
    // The standard OBS Studio DirectShow virtual camera COM registration.
    LONG status = RegOpenKeyExW(HKEY_CLASSES_ROOT,
                                L"CLSID\\{A3FCE0F5-3493-419F-958A-ABA1250EC20B}\\InprocServer32", 0,
                                KEY_READ, &key);
    if (status != ERROR_SUCCESS)
        return status;
    RegCloseKey(key);
    camera_native *s = calloc(1, sizeof(*s));
    if (!s)
        return ERROR_OUTOFMEMORY;
    const DWORD frame_bytes = CAM_WIDTH * CAM_HEIGHT * 3 / 2;
    const DWORD slot_bytes = (frame_bytes + 32 + 31) & ~31u;
    const DWORD header_bytes = (sizeof(struct queue_header) + 31) & ~31u;
    s->mapping = CreateFileMappingW(INVALID_HANDLE_VALUE, NULL, PAGE_READWRITE, 0,
                                    header_bytes + 3 * slot_bytes, L"OBSVirtualCamVideo");
    DWORD err = GetLastError();
    if (!s->mapping || err == ERROR_ALREADY_EXISTS) {
        camera_close(s);
        return err;
    }
    s->header = MapViewOfFile(s->mapping, FILE_MAP_ALL_ACCESS, 0, 0, 0);
    if (!s->header) {
        err = GetLastError();
        camera_close(s);
        return err;
    }
    QueryPerformanceFrequency(&s->frequency);
    struct queue_header *h = s->header;
    h->cx = CAM_WIDTH;
    h->cy = CAM_HEIGHT;
    h->interval = 10000000 / 30; // OBS interval units are 100ns.
    for (int i = 0; i < 3; ++i)
        h->offsets[i] = header_bytes + i * slot_bytes;
    InterlockedExchange(&h->state, 1); // STARTING until first complete frame.
    *out = s;
    return ERROR_SUCCESS;
}
void camera_write(camera_native *s, const uint8_t *y, const uint8_t *u, const uint8_t *v, int width,
                  int height, int ys, int us, int vs) {
    struct queue_header *h = s->header;
    uint32_t inc = (uint32_t)h->write_idx + 1;
    uint8_t *slot = (uint8_t *)h + h->offsets[inc % 3];
    uint8_t *dst = slot + 32;
    for (int row = 0; row < CAM_HEIGHT; ++row) {
        const uint8_t *src = y + (int64_t)row * height / CAM_HEIGHT * ys;
        for (int col = 0; col < CAM_WIDTH; ++col)
            dst[row * CAM_WIDTH + col] = src[(int64_t)col * width / CAM_WIDTH];
    }
    dst += CAM_WIDTH * CAM_HEIGHT;
    int cw = (width + 1) / 2, ch = (height + 1) / 2;
    for (int row = 0; row < CAM_HEIGHT / 2; ++row) {
        int sr = row * ch / (CAM_HEIGHT / 2);
        for (int col = 0; col < CAM_WIDTH / 2; ++col) {
            int sc = col * cw / (CAM_WIDTH / 2);
            dst[row * CAM_WIDTH + col * 2] = u[sr * us + sc];
            dst[row * CAM_WIDTH + col * 2 + 1] = v[sr * vs + sc];
        }
    }
    LARGE_INTEGER now;
    QueryPerformanceCounter(&now);
    // Split the QPC conversion to avoid overflowing after long system uptime.
    *(uint64_t *)slot =
        (uint64_t)(now.QuadPart / s->frequency.QuadPart) * 1000000000 +
        (uint64_t)(now.QuadPart % s->frequency.QuadPart) * 1000000000 / s->frequency.QuadPart;
    InterlockedExchange(&h->write_idx, (LONG)inc);
    InterlockedExchange(&h->read_idx, (LONG)inc);
    InterlockedExchange(&h->state, 2); // READY; interlocked publication is a full barrier.
}
