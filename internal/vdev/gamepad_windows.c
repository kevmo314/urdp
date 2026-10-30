#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include "native_windows.h"
#include <setupapi.h>
#include <stdint.h>
#include <stdlib.h>
#include <winioctl.h>

// ViGEm's public bus ABI (no ViGEmClient DLL dependency):
// https://github.com/nefarius/ViGEmClient/blob/master/include/ViGEm/km/BusShared.h
static const GUID bus_guid = {
    0x96e42b22, 0xf5e9, 0x42f8, {0xb0, 0x43, 0xed, 0x0f, 0x93, 0x2f, 0x01, 0x4f}};
#define BUS_IOCTL(index)                                                                           \
    CTL_CODE(FILE_DEVICE_BUS_EXTENDER, (0x801 + (index)), METHOD_BUFFERED, FILE_WRITE_DATA)
struct pad_native {
    HANDLE bus;
    ULONG serial;
};
struct bus_request {
    ULONG size, value;
};
struct plugin_request {
    ULONG size, serial, type;
    USHORT vid, pid;
};
struct xusb_report {
    USHORT buttons;
    BYTE lt, rt;
    SHORT lx, ly, rx, ry;
};
struct report_request {
    ULONG size, serial;
    struct xusb_report report;
};
_Static_assert(sizeof(struct plugin_request) == 16, "ViGEm plugin ABI");
_Static_assert(sizeof(struct report_request) == 20, "ViGEm report ABI");

static DWORD bus_call(HANDLE bus, DWORD code, void *data, DWORD size) {
    // Overlapped operations allow a broken/disconnected driver to time out.
    OVERLAPPED ov = {0};
    ov.hEvent = CreateEventW(NULL, TRUE, FALSE, NULL);
    if (!ov.hEvent)
        return GetLastError();
    DWORD transferred = 0, err = ERROR_SUCCESS;
    if (!DeviceIoControl(bus, code, data, size, NULL, 0, &transferred, &ov)) {
        err = GetLastError();
        if (err == ERROR_IO_PENDING) {
            DWORD wait = WaitForSingleObject(ov.hEvent, 5000);
            if (wait != WAIT_OBJECT_0) {
                err = wait == WAIT_TIMEOUT ? ERROR_TIMEOUT : GetLastError();
                CancelIoEx(bus, &ov);
                // Drain cancellation before stack OVERLAPPED storage goes away.
                GetOverlappedResult(bus, &ov, &transferred, TRUE);
            } else if (!GetOverlappedResult(bus, &ov, &transferred, FALSE)) {
                err = GetLastError();
            } else
                err = ERROR_SUCCESS;
        }
    }
    CloseHandle(ov.hEvent);
    return err;
}
unsigned long pad_close(pad_native *s) {
    if (!s)
        return ERROR_SUCCESS;
    DWORD err = ERROR_SUCCESS;
    if (s->serial) {
        struct bus_request unplug = {sizeof(unplug), s->serial};
        err = bus_call(s->bus, BUS_IOCTL(1), &unplug, sizeof(unplug));
    }
    CloseHandle(s->bus);
    free(s);
    return err;
}
unsigned long pad_open(pad_native **out) {
    HDEVINFO devices =
        SetupDiGetClassDevsW(&bus_guid, NULL, NULL, DIGCF_PRESENT | DIGCF_DEVICEINTERFACE);
    if (devices == INVALID_HANDLE_VALUE)
        return GetLastError();
    DWORD err = ERROR_NOT_FOUND;
    HANDLE bus = INVALID_HANDLE_VALUE;
    SP_DEVICE_INTERFACE_DATA iface = {0};
    iface.cbSize = sizeof(iface);
    for (DWORD i = 0; SetupDiEnumDeviceInterfaces(devices, NULL, &bus_guid, i, &iface); ++i) {
        DWORD needed = 0;
        SetupDiGetDeviceInterfaceDetailW(devices, &iface, NULL, 0, &needed, NULL);
        if (!needed) {
            err = GetLastError();
            continue;
        }
        PSP_DEVICE_INTERFACE_DETAIL_DATA_W detail = malloc(needed);
        if (!detail) {
            err = ERROR_OUTOFMEMORY;
            break;
        }
        detail->cbSize = sizeof(*detail);
        if (SetupDiGetDeviceInterfaceDetailW(devices, &iface, detail, needed, NULL, NULL)) {
            bus = CreateFileW(detail->DevicePath, GENERIC_READ | GENERIC_WRITE,
                              FILE_SHARE_READ | FILE_SHARE_WRITE, NULL, OPEN_EXISTING,
                              FILE_FLAG_OVERLAPPED, NULL);
            err = bus == INVALID_HANDLE_VALUE ? GetLastError() : ERROR_SUCCESS;
        } else
            err = GetLastError();
        free(detail);
        if (bus != INVALID_HANDLE_VALUE)
            break;
    }
    SetupDiDestroyDeviceInfoList(devices);
    if (bus == INVALID_HANDLE_VALUE)
        return err;
    struct bus_request version = {sizeof(version), 1};
    err = bus_call(bus, BUS_IOCTL(2), &version, sizeof(version));
    if (err) {
        CloseHandle(bus);
        return err;
    }
    pad_native *s = calloc(1, sizeof(*s));
    if (!s) {
        CloseHandle(bus);
        return ERROR_OUTOFMEMORY;
    }
    s->bus = bus;
    for (ULONG serial = 1; serial <= 65535; ++serial) {
        struct plugin_request plugin = {sizeof(plugin), serial, 0, 0x045e, 0x028e};
        err = bus_call(bus, BUS_IOCTL(0), &plugin, sizeof(plugin));
        if (!err) {
            s->serial = serial;
            struct bus_request ready = {sizeof(ready), serial};
            err = bus_call(bus, BUS_IOCTL(3), &ready, sizeof(ready));
            // ViGEmBus before 1.17 does not implement WAIT_DEVICE_READY.
            if (!err || err == ERROR_INVALID_PARAMETER) {
                *out = s;
                return ERROR_SUCCESS;
            }
            break;
        }
        // Only a serial collision warrants trying a different slot.
        if (err != ERROR_INVALID_PARAMETER && err != ERROR_ALREADY_EXISTS)
            break;
    }
    pad_close(s);
    return err;
}
unsigned long pad_update(pad_native *s, uint16_t buttons, uint8_t lt, uint8_t rt, int16_t lx,
                         int16_t ly, int16_t rx, int16_t ry) {
    struct report_request req = {sizeof(req), s->serial, {buttons, lt, rt, lx, ly, rx, ry}};
    DWORD err = bus_call(s->bus, BUS_IOCTL(0x201), &req, sizeof(req));
    // XUSB has no pending USB IN request briefly during startup or when the
    // consumer is not polling. Like ViGEmClient, drop that report and let the
    // next browser update supply the current state instead of failing the pad.
    return err == ERROR_NO_MORE_ITEMS ? ERROR_SUCCESS : err;
}
