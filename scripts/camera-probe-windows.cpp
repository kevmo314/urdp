// Throwaway host verification: enumerate MF cameras and validate native NV12
// pixels. Build with MinGW:
// x86_64-w64-mingw32-g++ -O2 -static -o camera-probe.exe scripts/camera-probe-windows.cpp
//   -lmf -lmfplat -lmfreadwrite -lmfuuid -lole32 -luuid
// Run alongside TestNativeCameraIntegration with URDP_TEST_NATIVE_CAMERA=1.
// --black checks the first fifteen seconds; the default checks bars across pauses
// and resolution changes. Exit 2 after the feeder closes confirms removal.
#include <cstdio>
#include <mfapi.h>
#include <mfidl.h>
#include <mfreadwrite.h>
#include <windows.h>
#include <wrl/client.h>
using Microsoft::WRL::ComPtr;
#define CHECK(x)                                                                                   \
    do {                                                                                           \
        HRESULT hr = (x);                                                                          \
        if (FAILED(hr)) {                                                                          \
            printf("%s: 0x%08lx\n", #x, (unsigned long)hr);                                        \
            return 1;                                                                              \
        }                                                                                          \
    } while (0)
int main(int argc, char **argv) {
    bool black = argc > 1 && strcmp(argv[1], "--black") == 0;
    int frames = black ? 20 : 240;
    setvbuf(stdout, nullptr, _IONBF, 0);
    CHECK(CoInitializeEx(nullptr, COINIT_MULTITHREADED));
    CHECK(MFStartup(MF_VERSION));
    ComPtr<IMFAttributes> attributes;
    CHECK(MFCreateAttributes(&attributes, 1));
    CHECK(attributes->SetGUID(MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE,
                              MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_GUID));
    IMFActivate **devices = nullptr;
    UINT32 count = 0;
    CHECK(MFEnumDeviceSources(attributes.Get(), &devices, &count));
    ComPtr<IMFMediaSource> source;
    for (UINT32 i = 0; i < count; i++) {
        WCHAR *name = nullptr;
        UINT32 len = 0;
        devices[i]->GetAllocatedString(MF_DEVSOURCE_ATTRIBUTE_FRIENDLY_NAME, &name, &len);
        printf("camera: %ls\n", name ? name : L"unknown");
        if (name && wcsstr(name, L"urdp Camera")) {
            CHECK(devices[i]->ActivateObject(IID_PPV_ARGS(&source)));
        }
        CoTaskMemFree(name);
        devices[i]->Release();
    }
    CoTaskMemFree(devices);
    if (!source) {
        puts("urdp Camera not found");
        return 2;
    }
    ComPtr<IMFSourceReader> reader;
    CHECK(MFCreateSourceReaderFromMediaSource(source.Get(), nullptr, &reader));
    const UINT32 sizes[][2] = {{1280, 720}, {640, 480}, {1920, 1080}};
    for (auto &size : sizes) {
        ComPtr<IMFMediaType> type;
        CHECK(MFCreateMediaType(&type));
        CHECK(type->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video));
        CHECK(type->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_NV12));
        CHECK(MFSetAttributeSize(type.Get(), MF_MT_FRAME_SIZE, size[0], size[1]));
        CHECK(MFSetAttributeRatio(type.Get(), MF_MT_FRAME_RATE, 30, 1));
        CHECK(
            reader->SetCurrentMediaType(MF_SOURCE_READER_FIRST_VIDEO_STREAM, nullptr, type.Get()));
        LONGLONG previous = -1;
        for (int frame = 0; frame < frames; frame++) {
            ComPtr<IMFSample> sample;
            DWORD flags = 0;
            LONGLONG timestamp = 0;
            CHECK(reader->ReadSample(MF_SOURCE_READER_FIRST_VIDEO_STREAM, 0, nullptr, &flags,
                                     &timestamp, &sample));
            if (!sample) {
                printf("stream event, flags=%lu\n", flags);
                frame--;
                continue;
            }
            if (previous >= timestamp) {
                puts("non-monotonic sample time");
                return 4;
            }
            previous = timestamp;
            ComPtr<IMFMediaBuffer> buffer;
            CHECK(sample->ConvertToContiguousBuffer(&buffer));
            BYTE *pixels = nullptr;
            DWORD length = 0;
            CHECK(buffer->Lock(&pixels, nullptr, &length));
            unsigned w = size[0], h = size[1];
            if (length != w * h * 3 / 2) {
                printf("unexpected buffer length: %lu\n", length);
                return 5;
            }
            // Feed has four luma bars and constant U/V; ignore initial black.
            bool valid = true;
            for (unsigned bar = 0; bar < 4; bar++) {
                unsigned x = (2 * bar + 1) * w / 8;
                BYTE value = pixels[h / 2 * w + x];
                if (value != (size[0] == 1920 ? 50 : 40) + bar * 50)
                    valid = false;
            }
            if (pixels[w * h] != 90 || pixels[w * h + 1] != 170)
                valid = false;
            if (black) {
                valid = true;
                for (unsigned i = 0; i < w * h; i++)
                    if (pixels[i] != 16)
                        valid = false;
                for (unsigned i = w * h; i < length; i++)
                    if (pixels[i] != 128)
                        valid = false;
            }
            if (frame == frames - 1 && !valid) {
                printf("observed Y=%u,%u,%u,%u UV=%u,%u\n", pixels[h / 2 * w + w / 8],
                       pixels[h / 2 * w + 3 * w / 8], pixels[h / 2 * w + 5 * w / 8],
                       pixels[h / 2 * w + 7 * w / 8], pixels[w * h], pixels[w * h + 1]);
            }
            buffer->Unlock();
            if (frame == frames - 1 && !valid) {
                puts("pixel mismatch");
                return 6;
            }
        }
        printf("PASS %ux%u NV12: %s; %d increasing timestamps\n", size[0], size[1],
               black ? "black Y=16 UV=128"
                     : (size[0] == 1920 ? "resized bars=50,100,150,200 U=90 V=170"
                                        : "bars=40,90,140,190 U=90 V=170"),
               frames);
    }
    reader.Reset();
    source->Shutdown();
    source.Reset();
    MFShutdown();
    CoUninitialize();
    return 0;
}
