#define WIN32_LEAN_AND_MEAN
#include "wgc_windows.h"
#include <dwmapi.h>
#include <roapi.h>
// MinGW aliases boolean and BYTE; suppress the duplicate unused specialization.
#define ____FIReference_1_boolean_INTERFACE_DEFINED__
#include <new>
#include <windows.graphics.capture.h>
#include <windows.graphics.capture.interop.h>
#include <winstring.h>
#include <wrl/client.h>

using Microsoft::WRL::ComPtr;
namespace WGC = ABI::Windows::Graphics::Capture;
namespace D3D = ABI::Windows::Graphics::DirectX::Direct3D11;
using ABI::Windows::Foundation::IClosable;
using ABI::Windows::Graphics::SizeInt32;
using ABI::Windows::Graphics::DirectX::DirectXPixelFormat_B8G8R8A8UIntNormalized;
using Arrived =
    ABI::Windows::Foundation::ITypedEventHandler<WGC::Direct3D11CaptureFramePool *, IInspectable *>;

// MinGW provides the WinRT interfaces, but not direct3d11.interop.h.
struct DxgiAccess : IUnknown {
    virtual HRESULT STDMETHODCALLTYPE GetInterface(REFIID iid, void **object) = 0;
};
static const GUID iidDxgiAccess = {
    0xa9b3d012, 0x3df2, 0x4ee3, {0xb8, 0xd1, 0x86, 0x95, 0xf4, 0x57, 0xd3, 0xc1}};

// Scope COM initialization to each native call: Go can migrate between threads.
struct Apartment {
    HRESULT hr = RoInitialize(RO_INIT_MULTITHREADED);
    ~Apartment() {
        if (SUCCEEDED(hr))
            RoUninitialize();
    }
};
static void close_object(IUnknown *object) {
    ComPtr<IClosable> closable;
    if (object && SUCCEEDED(object->QueryInterface(IID_PPV_ARGS(&closable))))
        closable->Close();
}
class FrameEvent final : public Arrived {
    LONG refs = 1;

  public:
    HANDLE event = CreateEventW(nullptr, FALSE, FALSE, nullptr);
    ~FrameEvent() {
        if (event)
            CloseHandle(event);
    }
    HRESULT STDMETHODCALLTYPE QueryInterface(REFIID iid, void **out) override {
        *out = nullptr;
        if (iid == __uuidof(IUnknown) || iid == __uuidof(Arrived) ||
            iid == __uuidof(IAgileObject)) {
            *out = static_cast<Arrived *>(this);
            AddRef();
            return S_OK;
        }
        return E_NOINTERFACE;
    }
    ULONG STDMETHODCALLTYPE AddRef() override {
        return InterlockedIncrement(&refs);
    }
    ULONG STDMETHODCALLTYPE Release() override {
        ULONG n = InterlockedDecrement(&refs);
        if (!n)
            delete this;
        return n;
    }
    HRESULT STDMETHODCALLTYPE Invoke(WGC::IDirect3D11CaptureFramePool *, IInspectable *) override {
        SetEvent(event);
        return S_OK;
    }
};
struct capture_wgc {
    HWND hwnd;
    CO_MTA_USAGE_COOKIE mta = nullptr;
    ComPtr<D3D::IDirect3DDevice> device;
    ComPtr<WGC::IGraphicsCaptureItem> item;
    ComPtr<WGC::IDirect3D11CaptureFramePool> pool;
    ComPtr<WGC::IGraphicsCaptureSession> session;
    ComPtr<WGC::IDirect3D11CaptureFrame> held;
    ComPtr<FrameEvent> arrived;
    EventRegistrationToken token = {};
    bool subscribed = false;
    SizeInt32 size = {};
    ~capture_wgc() {
        close_object(held.Get());
        if (subscribed)
            pool->remove_FrameArrived(token);
        close_object(session.Get());
        close_object(pool.Get());
        held.Reset();
        session.Reset();
        pool.Reset();
        item.Reset();
        device.Reset();
        if (mta)
            CoDecrementMTAUsage(mta);
    }
};
static HRESULT factory(const wchar_t *name, REFIID iid, void **out) {
    HSTRING str;
    HRESULT hr = WindowsCreateString(name, lstrlenW(name), &str);
    if (FAILED(hr))
        return hr;
    hr = RoGetActivationFactory(str, iid, out);
    WindowsDeleteString(str);
    return hr;
}
extern "C" HRESULT capture_wgc_open(HWND hwnd, ID3D11Device *device, capture_wgc **out) {
    Apartment apartment;
    if (FAILED(apartment.hr))
        return apartment.hr;
    auto s = new (std::nothrow) capture_wgc;
    if (!s)
        return E_OUTOFMEMORY;
    s->hwnd = hwnd;
    // The export is absent on older systems; keep PrintWindow available there.
    using CreateDevice = HRESULT(WINAPI *)(IDXGIDevice *, IInspectable **);
    auto create = reinterpret_cast<CreateDevice>(
        GetProcAddress(GetModuleHandleW(L"d3d11.dll"), "CreateDirect3D11DeviceFromDXGIDevice"));
    ComPtr<IDXGIDevice> dxgi;
    ComPtr<IInspectable> inspectable;
    ComPtr<IGraphicsCaptureItemInterop> interop;
    ComPtr<WGC::IDirect3D11CaptureFramePoolStatics2> statics;
    HRESULT hr = CoIncrementMTAUsage(&s->mta);
    if (SUCCEEDED(hr))
        hr = create ? device->QueryInterface(IID_PPV_ARGS(&dxgi)) : E_NOINTERFACE;
    if (SUCCEEDED(hr))
        hr = create(dxgi.Get(), &inspectable);
    if (SUCCEEDED(hr))
        hr = inspectable.As(&s->device);
    if (SUCCEEDED(hr))
        hr = factory(L"Windows.Graphics.Capture.GraphicsCaptureItem", IID_PPV_ARGS(&interop));
    if (SUCCEEDED(hr))
        hr = interop->CreateForWindow(hwnd, IID_PPV_ARGS(&s->item));
    if (SUCCEEDED(hr))
        hr = s->item->get_Size(&s->size);
    if (SUCCEEDED(hr))
        hr =
            factory(L"Windows.Graphics.Capture.Direct3D11CaptureFramePool", IID_PPV_ARGS(&statics));
    if (SUCCEEDED(hr))
        hr = statics->CreateFreeThreaded(s->device.Get(), DirectXPixelFormat_B8G8R8A8UIntNormalized,
                                         2, s->size, &s->pool);
    if (SUCCEEDED(hr))
        hr = s->pool->CreateCaptureSession(s->item.Get(), &s->session);
    if (SUCCEEDED(hr)) {
        ComPtr<WGC::IGraphicsCaptureSession2> cursor;
        ComPtr<WGC::IGraphicsCaptureSession3> border;
        if (SUCCEEDED(s->session.As(&cursor)))
            cursor->put_IsCursorCaptureEnabled(false);
        if (SUCCEEDED(s->session.As(&border)))
            border->put_IsBorderRequired(false);
        s->arrived.Attach(new (std::nothrow) FrameEvent);
        if (!s->arrived || !s->arrived->event)
            hr = E_OUTOFMEMORY;
    }
    if (SUCCEEDED(hr)) {
        hr = s->pool->add_FrameArrived(s->arrived.Get(), &s->token);
        s->subscribed = SUCCEEDED(hr);
    }
    if (SUCCEEDED(hr))
        hr = s->session->StartCapture();
    if (FAILED(hr)) {
        delete s;
        return hr;
    }
    *out = s;
    return S_OK;
}
extern "C" HRESULT capture_wgc_frame(capture_wgc *s, unsigned int timeout_ms,
                                     ID3D11Texture2D **texture, D3D11_BOX *box) {
    Apartment apartment;
    if (FAILED(apartment.hr))
        return apartment.hr;
    if (!IsWindow(s->hwnd))
        return HRESULT_FROM_WIN32(ERROR_INVALID_WINDOW_HANDLE);
    close_object(s->held.Get());
    s->held.Reset();
    ULONGLONG deadline = GetTickCount64() + timeout_ms;
    ComPtr<WGC::IDirect3D11CaptureFrame> frame;
    HRESULT hr;
    for (;;) {
        // Reset before probing the queue to avoid losing an arrival during the probe.
        ResetEvent(s->arrived->event);
        hr = s->pool->TryGetNextFrame(&frame);
        if (FAILED(hr) || frame)
            break;
        ULONGLONG now = GetTickCount64();
        if (now >= deadline)
            return S_FALSE;
        DWORD result = WaitForSingleObject(s->arrived->event, (DWORD)(deadline - now));
        if (result == WAIT_TIMEOUT)
            return S_FALSE;
        if (result == WAIT_FAILED)
            return HRESULT_FROM_WIN32(GetLastError());
    }
    if (FAILED(hr))
        return hr;
    // Drain stale frames: a slow encoder should always receive the newest image.
    for (;;) {
        ComPtr<WGC::IDirect3D11CaptureFrame> next;
        hr = s->pool->TryGetNextFrame(&next);
        if (FAILED(hr) || !next)
            break;
        close_object(frame.Get());
        frame = next;
    }
    SizeInt32 size;
    hr = frame->get_ContentSize(&size);
    if (SUCCEEDED(hr) && (size.Width != s->size.Width || size.Height != s->size.Height)) {
        close_object(frame.Get());
        frame.Reset();
        s->size = size;
        if (size.Width <= 0 || size.Height <= 0)
            return S_FALSE;
        hr = s->pool->Recreate(s->device.Get(), DirectXPixelFormat_B8G8R8A8UIntNormalized, 2, size);
        return FAILED(hr) ? hr : S_FALSE;
    }
    ComPtr<D3D::IDirect3DSurface> surface;
    ComPtr<DxgiAccess> access;
    if (SUCCEEDED(hr))
        hr = frame->get_Surface(&surface);
    if (SUCCEEDED(hr))
        hr = surface->QueryInterface(iidDxgiAccess,
                                     reinterpret_cast<void **>(access.GetAddressOf()));
    if (SUCCEEDED(hr))
        hr = access->GetInterface(IID_PPV_ARGS(texture));
    if (SUCCEEDED(hr)) {
        RECT bounds, client;
        POINT origin = {0, 0};
        hr = DwmGetWindowAttribute(s->hwnd, DWMWA_EXTENDED_FRAME_BOUNDS, &bounds, sizeof(bounds));
        if (SUCCEEDED(hr) && GetClientRect(s->hwnd, &client) && ClientToScreen(s->hwnd, &origin)) {
            int x = origin.x - bounds.left, y = origin.y - bounds.top;
            int w = client.right & ~1, h = client.bottom & ~1;
            D3D11_TEXTURE2D_DESC desc;
            (*texture)->GetDesc(&desc);
            if (x < 0 || y < 0 || w <= 0 || h <= 0 || x + w > size.Width || y + h > size.Height ||
                x + w > (int)desc.Width || y + h > (int)desc.Height)
                hr = S_FALSE; // Geometry changed while the compositor produced this frame.
            else
                *box = D3D11_BOX{(UINT)x, (UINT)y, 0, (UINT)(x + w), (UINT)(y + h), 1};
        } else if (SUCCEEDED(hr))
            hr = E_FAIL;
    }
    // Keep the pool buffer checked out until the next Capture, after readback.
    if (hr == S_OK)
        s->held = frame;
    else
        close_object(frame.Get());
    return hr;
}
extern "C" void capture_wgc_close(capture_wgc *s) {
    Apartment apartment;
    delete s;
}
