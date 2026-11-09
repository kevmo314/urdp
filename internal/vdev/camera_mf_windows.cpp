#include "mfcam/sdk.h"
#include "mfcam/shared.h"
#include "camconv/convert.h"
#include <mfapi.h>
#include <mfidl.h>
#include <new>
#include <windows.h>
#include <wrl/client.h>
extern "C" {
#include "native_windows.h"
}
using Microsoft::WRL::ComPtr;
struct camera_native {
    obs_camera_native *obs = nullptr;
    HANDLE producer = nullptr;
    HANDLE thread = nullptr, ready = nullptr, stop = nullptr, mapping = nullptr;
    CameraRing *ring = nullptr;
    HRESULT result = E_FAIL;
};
static bool open_ring(camera_native *s) {
    if (!s->ring) {
        s->mapping = OpenFileMappingW(FILE_MAP_ALL_ACCESS, FALSE, URDP_CAMERA_MAPPING);
        if (!s->mapping)
            return false;
        s->ring = static_cast<CameraRing *>(
            MapViewOfFile(s->mapping, FILE_MAP_ALL_ACCESS, 0, 0, sizeof(CameraRing)));
        if (!s->ring) {
            CloseHandle(s->mapping);
            s->mapping = nullptr;
            return false;
        }
        s->ring->counter = 0;
        s->ring->version = 1;
        MemoryBarrier();
        s->ring->magic = URDP_CAMERA_MAGIC;
    }
    return true;
}
static DWORD WINAPI camera_thread(void *arg) {
    auto *s = static_cast<camera_native *>(arg);
    HRESULT com = CoInitializeEx(nullptr, COINIT_MULTITHREADED);
    // Media Foundation is loaded on demand so urdp.exe still starts on
    // Windows "N" editions without the Media Feature Pack.
    HMODULE mfplat = LoadLibraryW(L"mfplat.dll");
    auto startup = mfplat ? reinterpret_cast<HRESULT(WINAPI *)(ULONG, DWORD)>(
                                GetProcAddress(mfplat, "MFStartup"))
                          : nullptr;
    auto shutdown = mfplat ? reinterpret_cast<HRESULT(WINAPI *)()>(
                                 GetProcAddress(mfplat, "MFShutdown"))
                           : nullptr;
    HRESULT mf = FAILED(com)               ? com
                 : !startup || !shutdown ? HRESULT_FROM_WIN32(ERROR_MOD_NOT_FOUND)
                                         : startup(MF_VERSION, MFSTARTUP_LITE);
    HMODULE module = nullptr;
    ComPtr<IMFVirtualCamera> camera;
    s->result = mf;
    if (SUCCEEDED(mf)) {
        module = LoadLibraryW(L"mfsensorgroup.dll");
        auto create = module ? reinterpret_cast<CreateVirtualCamera>(
                                   GetProcAddress(module, "MFCreateVirtualCamera"))
                             : nullptr;
        s->result = create
                        ? create(MFVirtualCameraType_SoftwareCameraSource,
                                 MFVirtualCameraLifetime_Session, MFVirtualCameraAccess_CurrentUser,
                                 L"urdp Camera", URDP_CAMERA_CLSID, nullptr, 0, &camera)
                        : HRESULT_FROM_WIN32(ERROR_OLD_WIN_VERSION);
        if (SUCCEEDED(s->result)) {
            s->result = camera->Start(nullptr);
        }
        if (SUCCEEDED(s->result))
            open_ring(s);
        // FrameServer creates the Global section when a consumer activates the
        // source. Open it lazily on WriteFrame; no capture client is required
        // for registration to succeed.
    }
    SetEvent(s->ready);
    if (SUCCEEDED(s->result))
        WaitForSingleObject(s->stop, INFINITE);
    if (camera) {
        camera->Stop();
        camera->Remove();
        camera->Shutdown();
        camera.Reset();
    }
    if (module)
        FreeLibrary(module);
    if (SUCCEEDED(mf))
        shutdown();
    if (mfplat)
        FreeLibrary(mfplat);
    if (SUCCEEDED(com))
        CoUninitialize();
    return 0;
}
extern "C" void camera_close(camera_native *s) {
    if (!s)
        return;
    if (s->obs)
        obs_camera_close(s->obs);
    if (s->thread) {
        SetEvent(s->stop);
        WaitForSingleObject(s->thread, INFINITE);
        CloseHandle(s->thread);
    }
    if (s->ring) {
        InterlockedExchange(&s->ring->counter, 0);
        UnmapViewOfFile(s->ring);
    }
    if (s->mapping)
        CloseHandle(s->mapping);
    if (s->ready)
        CloseHandle(s->ready);
    if (s->stop)
        CloseHandle(s->stop);
    if (s->producer)
        CloseHandle(s->producer);
    delete s;
}
extern "C" unsigned long camera_open(camera_native **out) {
    auto *s = new (std::nothrow) camera_native;
    if (!s)
        return ERROR_OUTOFMEMORY;
    HKEY key;
    LONG registered = RegOpenKeyExW(HKEY_LOCAL_MACHINE, URDP_CAMERA_KEY L"\\InprocServer32", 0,
                                    KEY_READ | KEY_WOW64_64KEY, &key);
    if (registered != ERROR_SUCCESS) {
        DWORD error = obs_camera_open(&s->obs);
        if (error) {
            delete s;
            return error;
        }
    } else {
        RegCloseKey(key);
        // One writer owns the machine-wide ring; a second process must not
        // reset the active session or race another viewer's frames.
        s->producer = CreateMutexW(nullptr, FALSE, L"Global\\urdp.Camera.Producer.v1");
        DWORD producerError = GetLastError();
        if (!s->producer || producerError == ERROR_ALREADY_EXISTS) {
            camera_close(s);
            return producerError;
        }
        s->ready = CreateEventW(nullptr, TRUE, FALSE, nullptr);
        s->stop = CreateEventW(nullptr, TRUE, FALSE, nullptr);
        if (!s->ready || !s->stop) {
            DWORD error = GetLastError();
            camera_close(s);
            return error;
        }
        s->thread = CreateThread(nullptr, 0, camera_thread, s, 0, nullptr);
        if (!s->thread) {
            DWORD error = GetLastError();
            camera_close(s);
            return error;
        }
        WaitForSingleObject(s->ready, INFINITE);
        if (FAILED(s->result)) {
            DWORD error = s->result;
            camera_close(s);
            return error;
        }
    }
    *out = s;
    return 0;
}
extern "C" void camera_write(camera_native *s, const uint8_t *y, const uint8_t *u, const uint8_t *v,
                             int width, int height, int ys, int us, int vs) {
    if (s->obs) {
        obs_camera_write(s->obs, y, u, v, width, height, ys, us, vs);
        return;
    }
    if (width > URDP_CAMERA_WIDTH || height > URDP_CAMERA_HEIGHT)
        return;
    if (!open_ring(s))
        return;
    // NV12 requires even dimensions. Pad odd I420 edges by repeating pixels.
    int w = (width + 1) & ~1, h = (height + 1) & ~1;
    LONG previous = s->ring->counter;
    LONG counter = previous < 0x7fffffff ? previous + 1 : 1;
    CameraSlot &slot = s->ring->slots[static_cast<ULONG>(counter) % 3];
    InterlockedExchange(&slot.sequence, slot.sequence | 1);
    slot.width = w;
    slot.height = h;
    LARGE_INTEGER now;
    QueryPerformanceCounter(&now);
    slot.timestamp = now.QuadPart; // informational; the source stamps samples itself
    urdp_i420_to_nv12(slot.pixels, y, u, v, width, height, ys, us, vs);
    InterlockedIncrement(&slot.sequence);
    InterlockedExchange(&s->ring->counter, counter);
}
