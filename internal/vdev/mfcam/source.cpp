// Native Windows 11 camera source, following Microsoft's VirtualCamera sample:
// IMFActivate -> IMFMediaSourceEx -> IMFMediaStream, with event queues and KS
// control probing. No WinRT, D3D, codec, or redistributable runtime dependency.
#include <initguid.h>
#include <windows.h>
#include <mfapi.h>
#include <mfidl.h>
#include <mferror.h>
#include <ks.h>
#include <ksmedia.h>
#include <ksproxy.h>
#include <sddl.h>
#include <wrl/client.h>
#include <vector>
#include <new>
#include "shared.h"
#include "sdk.h"
using Microsoft::WRL::ComPtr;
#define CHECK(x)                                                                                   \
    do {                                                                                           \
        HRESULT hr_ = (x);                                                                         \
        if (FAILED(hr_))                                                                           \
            return hr_;                                                                            \
    } while (0)
static volatile LONG objects;
static const GUID StreamCategory = {
    0x2939e7b8, 0xa62e, 0x4579, {0xb6, 0x74, 0xd4, 7, 0x3d, 0xfa, 0xbb, 0xba}};
static const GUID StreamID = {
    0x11bd5120, 0xd124, 0x446b, {0x88, 0xe6, 0x17, 6, 2, 0x57, 0xff, 0xf9}};
static const GUID SharedStream = {
    0x1cb378e9, 0xb279, 0x41d4, {0xaf, 0x97, 0x34, 0xa2, 0x43, 0xe6, 0x83, 0x20}};
static const GUID FrameTypes = {
    0x17145fd1, 0x1b2b, 0x423c, {0x80, 1, 0x2b, 0x68, 0x33, 0xed, 0x35, 0x88}};

struct Lock {
    CRITICAL_SECTION cs;
    Lock() { InitializeCriticalSection(&cs); }
    ~Lock() { DeleteCriticalSection(&cs); }
};
struct Guard {
    Lock &lock;
    Guard(Lock &v) : lock(v) { EnterCriticalSection(&lock.cs); }
    ~Guard() { LeaveCriticalSection(&lock.cs); }
};
struct RefCount {
    volatile LONG refs = 1;
    RefCount() { InterlockedIncrement(&objects); }
    virtual ~RefCount() { InterlockedDecrement(&objects); }
    ULONG add() { return InterlockedIncrement(&refs); }
    ULONG release() {
        ULONG n = InterlockedDecrement(&refs);
        if (!n)
            delete this;
        return n;
    }
};
#define REFS                                                                                       \
    ULONG STDMETHODCALLTYPE AddRef() override { return add(); }                                    \
    ULONG STDMETHODCALLTYPE Release() override { return release(); }
#define EVENTS                                                                                     \
    HRESULT STDMETHODCALLTYPE GetEvent(DWORD f, IMFMediaEvent **e) override {                      \
        return events->GetEvent(f, e);                                                             \
    }                                                                                              \
    HRESULT STDMETHODCALLTYPE BeginGetEvent(IMFAsyncCallback *c, IUnknown *s) override {           \
        return events->BeginGetEvent(c, s);                                                        \
    }                                                                                              \
    HRESULT STDMETHODCALLTYPE EndGetEvent(IMFAsyncResult *r, IMFMediaEvent **e) override {         \
        return events->EndGetEvent(r, e);                                                          \
    }                                                                                              \
    HRESULT STDMETHODCALLTYPE QueueEvent(MediaEventType t, REFGUID g, HRESULT h,                   \
                                         const PROPVARIANT *v) override {                          \
        return events->QueueEventParamVar(t, g, h, v);                                             \
    }

template <class T> HRESULT copyInterface(T *value, T **out) {
    if (!out)
        return E_POINTER;
    *out = value;
    if (value)
        value->AddRef();
    return value ? S_OK : MF_E_SHUTDOWN;
}

class Stream : public IMFMediaStream, public RefCount {
  public:
    Lock lock;
    ComPtr<IMFMediaEventQueue> events;
    ComPtr<IMFStreamDescriptor> descriptor;
    ComPtr<IMFAttributes> attributes;
    ComPtr<IMFMediaSource> parent; // Shutdown breaks the source/stream cycle.
    bool running = false, shutdown = false;
    UINT32 width = 1280, height = 720;
    LONGLONG nextTime = 0;
    HANDLE mapping = nullptr;
    const CameraRing *ring = nullptr;
    std::vector<BYTE> lastFrame;
    UINT32 inputWidth = 0, inputHeight = 0;
    LONG lastCounter = 0;
    Stream(IMFMediaSource *s) : parent(s) {}
    ~Stream() {
        if (ring)
            UnmapViewOfFile(ring);
        if (mapping)
            CloseHandle(mapping);
    }
    REFS HRESULT STDMETHODCALLTYPE QueryInterface(REFIID id, void **out) override {
        if (!out)
            return E_POINTER;
        *out = nullptr;
        if (id == IID_IUnknown || id == IID_IMFMediaEventGenerator || id == IID_IMFMediaStream)
            *out = static_cast<IMFMediaStream *>(this);
        if (!*out)
            return E_NOINTERFACE;
        AddRef();
        return S_OK;
    }
    EVENTS
    HRESULT initialize() {
        CHECK(MFCreateEventQueue(&events));
        CHECK(MFCreateAttributes(&attributes, 4));
        CHECK(attributes->SetGUID(StreamCategory, PINNAME_VIDEO_CAPTURE));
        CHECK(attributes->SetUINT32(StreamID, 0));
        CHECK(attributes->SetUINT32(SharedStream, 1));
        CHECK(attributes->SetUINT32(FrameTypes, 1));
        ComPtr<IMFMediaType> types[3];
        const UINT32 sizes[3][2] = {{1280, 720}, {640, 480}, {1920, 1080}};
        for (int i = 0; i < 3; i++) {
            CHECK(MFCreateMediaType(&types[i]));
            CHECK(types[i]->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video));
            CHECK(types[i]->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_NV12));
            CHECK(types[i]->SetUINT32(MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive));
            CHECK(types[i]->SetUINT32(MF_MT_ALL_SAMPLES_INDEPENDENT, TRUE));
            CHECK(types[i]->SetUINT32(MF_MT_FIXED_SIZE_SAMPLES, TRUE));
            CHECK(types[i]->SetUINT32(MF_MT_SAMPLE_SIZE, sizes[i][0] * sizes[i][1] * 3 / 2));
            CHECK(types[i]->SetUINT32(MF_MT_DEFAULT_STRIDE, sizes[i][0]));
            CHECK(MFSetAttributeSize(types[i].Get(), MF_MT_FRAME_SIZE, sizes[i][0], sizes[i][1]));
            CHECK(MFSetAttributeRatio(types[i].Get(), MF_MT_FRAME_RATE, 30, 1));
            CHECK(MFSetAttributeRatio(types[i].Get(), MF_MT_PIXEL_ASPECT_RATIO, 1, 1));
        }
        IMFMediaType *raw[] = {types[0].Get(), types[1].Get(), types[2].Get()};
        CHECK(MFCreateStreamDescriptor(0, 3, raw, &descriptor));
        CHECK(attributes->CopyAllItems(descriptor.Get()));
        ComPtr<IMFMediaTypeHandler> handler;
        CHECK(descriptor->GetMediaTypeHandler(&handler));
        CHECK(handler->SetCurrentMediaType(types[0].Get()));
        // LocalService can create Global mappings without elevating the writer.
        // Keep the mapping alive for the entire activated source lifetime.
        PSECURITY_DESCRIPTOR sd = nullptr;
        if (!ConvertStringSecurityDescriptorToSecurityDescriptorW(L"D:(A;;GA;;;WD)",
                                                                  SDDL_REVISION_1, &sd, nullptr))
            return HRESULT_FROM_WIN32(GetLastError());
        SECURITY_ATTRIBUTES sa = {sizeof(sa), sd, FALSE};
        mapping = CreateFileMappingW(INVALID_HANDLE_VALUE, &sa, PAGE_READWRITE, 0,
                                     sizeof(CameraRing), URDP_CAMERA_MAPPING);
        DWORD error = GetLastError();
        LocalFree(sd);
        // Start may validate this source in the unelevated registering process.
        // Only the service needs to create the Global section; validation must
        // not fail just because that process lacks SeCreateGlobalPrivilege.
        if (!mapping && error == ERROR_ACCESS_DENIED)
            return S_OK;
        if (!mapping)
            return HRESULT_FROM_WIN32(error);
        ring = static_cast<const CameraRing *>(
            MapViewOfFile(mapping, FILE_MAP_READ, 0, 0, sizeof(CameraRing)));
        if (!ring)
            return HRESULT_FROM_WIN32(GetLastError());
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE GetMediaSource(IMFMediaSource **out) override {
        Guard guard(lock);
        return copyInterface(parent.Get(), out);
    }
    HRESULT STDMETHODCALLTYPE GetStreamDescriptor(IMFStreamDescriptor **out) override {
        Guard guard(lock);
        if (shutdown)
            return MF_E_SHUTDOWN;
        return copyInterface(descriptor.Get(), out);
    }
    void refreshFrame() {
        if (!ring || ring->magic != URDP_CAMERA_MAGIC || ring->version != 1)
            return;
        LONG counter = ring->counter;
        MemoryBarrier();
        if (!counter) {
            // A new producer resets the ring before sending input. FrameServer
            // may reuse a source, so do not expose the previous session's image.
            lastFrame.clear();
            inputWidth = 0;
            inputHeight = 0;
            lastCounter = 0;
            return;
        }
        if (counter == lastCounter)
            return;
        const CameraSlot &slot = ring->slots[static_cast<ULONG>(counter) % 3];
        LONG sequence = slot.sequence;
        MemoryBarrier();
        if (sequence & 1)
            return;
        UINT32 w = slot.width, h = slot.height;
        if (!w || !h || w > URDP_CAMERA_WIDTH || h > URDP_CAMERA_HEIGHT || (w | h) & 1)
            return;
        std::vector<BYTE> frame(w * h * 3 / 2);
        memcpy(frame.data(), slot.pixels, frame.size());
        MemoryBarrier();
        if (sequence != slot.sequence)
            return;
        lastFrame.swap(frame);
        inputWidth = w;
        inputHeight = h;
        lastCounter = counter;
    }
    HRESULT STDMETHODCALLTYPE RequestSample(IUnknown *token) override {
        Guard guard(lock);
        if (shutdown)
            return MF_E_SHUTDOWN;
        if (!running)
            return MF_E_INVALIDREQUEST;
        LONGLONG now = MFGetSystemTime();
        if (nextTime > now)
            Sleep(static_cast<DWORD>((nextTime - now) / 10000));
        now = MFGetSystemTime();
        if (nextTime < now - 333333)
            nextTime = now;
        LONGLONG timestamp = nextTime;
        nextTime += 333333;
        try {
            refreshFrame();
        } catch (const std::bad_alloc &) {
            return E_OUTOFMEMORY;
        }
        ComPtr<IMFSample> sample;
        ComPtr<IMFMediaBuffer> buffer;
        DWORD bytes = width * height * 3 / 2;
        CHECK(MFCreateSample(&sample));
        CHECK(MFCreateMemoryBuffer(bytes, &buffer));
        BYTE *dst = nullptr;
        CHECK(buffer->Lock(&dst, nullptr, nullptr));
        if (lastFrame.empty()) {
            memset(dst, 16, width * height);
            memset(dst + width * height, 128, width * height / 2);
        } else {
            const BYTE *src = lastFrame.data();
            for (UINT32 y = 0; y < height; y++)
                for (UINT32 x = 0; x < width; x++)
                    dst[y * width + x] = src[(uint64_t)y * inputHeight / height * inputWidth +
                                             (uint64_t)x * inputWidth / width];
            src += inputWidth * inputHeight;
            BYTE *uv = dst + width * height;
            for (UINT32 y = 0; y < height / 2; y++)
                for (UINT32 x = 0; x < width / 2; x++) {
                    size_t offset = (uint64_t)y * (inputHeight / 2) / (height / 2) * inputWidth +
                                    ((uint64_t)x * (inputWidth / 2) / (width / 2)) * 2;
                    uv[y * width + x * 2] = src[offset];
                    uv[y * width + x * 2 + 1] = src[offset + 1];
                }
        }
        CHECK(buffer->Unlock());
        CHECK(buffer->SetCurrentLength(bytes));
        CHECK(sample->AddBuffer(buffer.Get()));
        CHECK(sample->SetSampleTime(timestamp));
        CHECK(sample->SetSampleDuration(333333));
        if (token)
            CHECK(sample->SetUnknown(MFSampleExtension_Token, token));
        return events->QueueEventParamUnk(MEMediaSample, GUID_NULL, S_OK, sample.Get());
    }
};

class Source : public IMFMediaSourceEx, public IKsControl, public IMFGetService, public RefCount {
    Lock lock;
    ComPtr<IMFMediaEventQueue> events;
    ComPtr<IMFAttributes> attributes;
    ComPtr<IMFPresentationDescriptor> presentation;
    ComPtr<Stream> stream;
    bool shutdown = false, announced = false;

  public:
    REFS HRESULT STDMETHODCALLTYPE QueryInterface(REFIID id, void **out) override {
        if (!out)
            return E_POINTER;
        *out = nullptr;
        if (id == IID_IUnknown || id == IID_IMFMediaEventGenerator || id == IID_IMFMediaSource ||
            id == IID_IMFMediaSourceEx)
            *out = static_cast<IMFMediaSourceEx *>(this);
        else if (id == IID_IKsControl)
            *out = static_cast<IKsControl *>(this);
        else if (id == IID_IMFGetService)
            *out = static_cast<IMFGetService *>(this);
        if (!*out)
            return E_NOINTERFACE;
        AddRef();
        return S_OK;
    }
    EVENTS
    HRESULT initialize(IMFAttributes *activation) {
        CHECK(MFCreateEventQueue(&events));
        CHECK(MFCreateAttributes(&attributes, 4));
        CHECK(activation->CopyAllItems(attributes.Get()));
        stream.Attach(new (std::nothrow) Stream(this));
        if (!stream)
            return E_OUTOFMEMORY;
        HRESULT initialized = stream->initialize();
        if (FAILED(initialized)) {
            Shutdown();
            return initialized;
        }
        IMFStreamDescriptor *raw = stream->descriptor.Get();
        CHECK(MFCreatePresentationDescriptor(1, &raw, &presentation));
        return presentation->SelectStream(0);
    }
    ~Source() { Shutdown(); }
    HRESULT STDMETHODCALLTYPE GetCharacteristics(DWORD *out) override {
        if (!out)
            return E_POINTER;
        Guard guard(lock);
        if (shutdown)
            return MF_E_SHUTDOWN;
        *out = MFMEDIASOURCE_IS_LIVE;
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE
    CreatePresentationDescriptor(IMFPresentationDescriptor **out) override {
        Guard guard(lock);
        if (shutdown)
            return MF_E_SHUTDOWN;
        return presentation->Clone(out);
    }
    HRESULT STDMETHODCALLTYPE Start(IMFPresentationDescriptor *pd, const GUID *format,
                                    const PROPVARIANT *position) override {
        if (!pd || !position)
            return E_POINTER;
        if (format && *format != GUID_NULL)
            return MF_E_UNSUPPORTED_TIME_FORMAT;
        if (position->vt != VT_EMPTY && (position->vt != VT_I8 || position->hVal.QuadPart != 0))
            return MF_E_INVALIDREQUEST;
        Guard guard(lock);
        if (shutdown)
            return MF_E_SHUTDOWN;
        Guard streamGuard(stream->lock);
        DWORD count = 0;
        CHECK(pd->GetStreamDescriptorCount(&count));
        if (count != 1)
            return MF_E_INVALIDREQUEST;
        BOOL selected = FALSE;
        ComPtr<IMFStreamDescriptor> sd;
        CHECK(pd->GetStreamDescriptorByIndex(0, &selected, &sd));
        if (!selected)
            return MF_E_INVALIDREQUEST;
        ComPtr<IMFMediaTypeHandler> handler, ownHandler;
        ComPtr<IMFMediaType> type;
        CHECK(sd->GetMediaTypeHandler(&handler));
        CHECK(handler->GetCurrentMediaType(&type));
        CHECK(stream->descriptor->GetMediaTypeHandler(&ownHandler));
        CHECK(ownHandler->IsMediaTypeSupported(type.Get(), nullptr));
        UINT32 width = 0, height = 0, numerator = 0, denominator = 0;
        GUID subtype, major;
        CHECK(type->GetGUID(MF_MT_MAJOR_TYPE, &major));
        CHECK(type->GetGUID(MF_MT_SUBTYPE, &subtype));
        CHECK(MFGetAttributeSize(type.Get(), MF_MT_FRAME_SIZE, &width, &height));
        CHECK(MFGetAttributeRatio(type.Get(), MF_MT_FRAME_RATE, &numerator, &denominator));
        bool validSize = (width == 1280 && height == 720) || (width == 640 && height == 480) ||
                         (width == 1920 && height == 1080);
        if (!validSize || major != MFMediaType_Video || subtype != MFVideoFormat_NV12 ||
            !denominator || numerator != uint64_t(30) * denominator)
            return MF_E_INVALIDMEDIATYPE;
        CHECK(ownHandler->SetCurrentMediaType(type.Get()));
        stream->width = width;
        stream->height = height;
        stream->running = true;
        stream->nextTime = MFGetSystemTime();
        CHECK(events->QueueEventParamUnk(announced ? MEUpdatedStream : MENewStream, GUID_NULL, S_OK,
                                         stream.Get()));
        announced = true;
        PROPVARIANT start;
        PropVariantInit(&start);
        start.vt = VT_I8;
        start.hVal.QuadPart = 0;
        CHECK(stream->events->QueueEventParamVar(MEStreamStarted, GUID_NULL, S_OK, &start));
        return events->QueueEventParamVar(MESourceStarted, GUID_NULL, S_OK, &start);
    }
    HRESULT STDMETHODCALLTYPE Stop() override {
        Guard guard(lock);
        if (shutdown)
            return MF_E_SHUTDOWN;
        Guard streamGuard(stream->lock);
        stream->running = false;
        CHECK(stream->events->QueueEventParamVar(MEStreamStopped, GUID_NULL, S_OK, nullptr));
        return events->QueueEventParamVar(MESourceStopped, GUID_NULL, S_OK, nullptr);
    }
    HRESULT STDMETHODCALLTYPE Pause() override { return MF_E_INVALID_STATE_TRANSITION; }
    HRESULT STDMETHODCALLTYPE Shutdown() override {
        Guard guard(lock);
        if (shutdown)
            return S_OK;
        shutdown = true;
        if (stream) {
            Guard streamGuard(stream->lock);
            stream->shutdown = true;
            stream->running = false;
            stream->parent = nullptr;
            if (stream->events)
                stream->events->Shutdown();
        }
        if (events)
            events->Shutdown();
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE GetSourceAttributes(IMFAttributes **out) override {
        Guard guard(lock);
        if (shutdown)
            return MF_E_SHUTDOWN;
        return copyInterface(attributes.Get(), out);
    }
    HRESULT STDMETHODCALLTYPE GetStreamAttributes(DWORD id, IMFAttributes **out) override {
        Guard guard(lock);
        if (shutdown)
            return MF_E_SHUTDOWN;
        if (id)
            return MF_E_INVALIDSTREAMNUMBER;
        return copyInterface(stream->attributes.Get(), out);
    }
    HRESULT STDMETHODCALLTYPE SetD3DManager(IUnknown *) override { return E_NOTIMPL; }
    HRESULT STDMETHODCALLTYPE GetService(REFGUID, REFIID, void **out) override {
        if (!out)
            return E_POINTER;
        *out = nullptr;
        return MF_E_UNSUPPORTED_SERVICE;
    }
    HRESULT STDMETHODCALLTYPE KsProperty(PKSPROPERTY, ULONG, void *, ULONG, ULONG *bytes) override {
        if (bytes)
            *bytes = 0;
        return HRESULT_FROM_WIN32(ERROR_SET_NOT_FOUND);
    }
    HRESULT STDMETHODCALLTYPE KsMethod(PKSMETHOD, ULONG, void *, ULONG, ULONG *bytes) override {
        if (bytes)
            *bytes = 0;
        return HRESULT_FROM_WIN32(ERROR_SET_NOT_FOUND);
    }
    HRESULT STDMETHODCALLTYPE KsEvent(PKSEVENT, ULONG, void *, ULONG, ULONG *bytes) override {
        if (bytes)
            *bytes = 0;
        return HRESULT_FROM_WIN32(ERROR_SET_NOT_FOUND);
    }
};

class Activate : public IMFActivate, public RefCount {
    Lock lock;
    ComPtr<IMFAttributes> attributes;
    ComPtr<Source> source;

  public:
    REFS
    HRESULT initialize() { return MFCreateAttributes(&attributes, 4); }
    HRESULT STDMETHODCALLTYPE QueryInterface(REFIID id, void **out) override {
        if (!out)
            return E_POINTER;
        *out = nullptr;
        if (id == IID_IUnknown || id == IID_IMFAttributes || id == IID_IMFActivate)
            *out = static_cast<IMFActivate *>(this);
        if (!*out)
            return E_NOINTERFACE;
        AddRef();
        return S_OK;
    }
#include "attributes.h"
    HRESULT STDMETHODCALLTYPE ActivateObject(REFIID id, void **out) override {
        Guard guard(lock);
        if (!source) {
            ComPtr<Source> next;
            next.Attach(new (std::nothrow) Source);
            if (!next)
                return E_OUTOFMEMORY;
            HRESULT initialized = next->initialize(attributes.Get());
            if (FAILED(initialized)) {
                next->Shutdown();
                return initialized;
            }
            source = next;
        }
        return source->QueryInterface(id, out);
    }
    HRESULT STDMETHODCALLTYPE ShutdownObject() override {
        Guard guard(lock);
        if (source)
            source->Shutdown();
        source.Reset();
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE DetachObject() override {
        Guard guard(lock);
        source.Reset();
        return S_OK;
    }
};
class Factory : public IClassFactory, public RefCount {
  public:
    REFS HRESULT STDMETHODCALLTYPE QueryInterface(REFIID id, void **out) override {
        if (!out)
            return E_POINTER;
        *out = nullptr;
        if (id == IID_IUnknown || id == IID_IClassFactory)
            *out = static_cast<IClassFactory *>(this);
        if (!*out)
            return E_NOINTERFACE;
        AddRef();
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE CreateInstance(IUnknown *outer, REFIID id, void **out) override {
        if (outer)
            return CLASS_E_NOAGGREGATION;
        ComPtr<Activate> activation;
        activation.Attach(new (std::nothrow) Activate);
        if (!activation)
            return E_OUTOFMEMORY;
        CHECK(activation->initialize());
        return activation->QueryInterface(id, out);
    }
    HRESULT STDMETHODCALLTYPE LockServer(BOOL locked) override {
        if (locked)
            InterlockedIncrement(&objects);
        else
            InterlockedDecrement(&objects);
        return S_OK;
    }
};
extern "C" __declspec(dllexport) HRESULT WINAPI DllGetClassObject(REFCLSID clsid, REFIID id,
                                                                  void **out) {
    if (!out)
        return E_POINTER;
    *out = nullptr;
    if (clsid != CLSID_UrdpCamera)
        return CLASS_E_CLASSNOTAVAILABLE;
    ComPtr<Factory> factory;
    factory.Attach(new (std::nothrow) Factory);
    if (!factory)
        return E_OUTOFMEMORY;
    return factory->QueryInterface(id, out);
}
extern "C" __declspec(dllexport) HRESULT WINAPI DllCanUnloadNow() {
    return objects ? S_FALSE : S_OK;
}
