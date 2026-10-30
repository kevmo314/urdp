#define COBJMACROS
#define WIN32_LEAN_AND_MEAN
#include "native_windows.h"
#include <audioclient.h>
#include <mmdeviceapi.h>
#include <stdlib.h>
#include <string.h>
#include <windows.h>

// Explicit GUIDs avoid relying on which WASAPI GUIDs mingw's uuid library exports.
static const GUID clsid_enumerator = {
    0xbcde0395, 0xe52f, 0x467c, {0x8e, 0x3d, 0xc4, 0x57, 0x92, 0x91, 0x69, 0x2e}};
static const GUID iid_enumerator = {
    0xa95664d2, 0x9614, 0x4f35, {0xa7, 0x46, 0xde, 0x8d, 0xb6, 0x36, 0x17, 0xe6}};
static const GUID iid_client = {
    0x1cb9ad4c, 0xdbfa, 0x4c32, {0xb1, 0x78, 0xc2, 0xf5, 0x68, 0xa7, 0x03, 0xb2}};
static const GUID iid_capture = {
    0xc8adbd64, 0xe71e, 0x48a0, {0xa4, 0xde, 0x18, 0x5c, 0x39, 0x5c, 0xd3, 0x17}};
struct audio_native {
    IAudioClient *client;
    IAudioCaptureClient *capture;
    BYTE *packet;
    BYTE *storage;
    size_t allocated;
    UINT32 remaining;
    int com;
};
void audio_close(audio_native *s) {
    if (!s)
        return;
    if (s->capture)
        IAudioCaptureClient_Release(s->capture);
    if (s->client) {
        IAudioClient_Stop(s->client);
        IAudioClient_Release(s->client);
    }
    free(s->storage);
    int com = s->com;
    free(s);
    if (com)
        CoUninitialize();
}
long audio_open(audio_native **out) {
    audio_native *s = calloc(1, sizeof(*s));
    if (!s)
        return E_OUTOFMEMORY;
    HRESULT hr = CoInitializeEx(NULL, COINIT_MULTITHREADED);
    if (FAILED(hr)) {
        free(s);
        return hr;
    }
    s->com = 1;
    IMMDeviceEnumerator *enumerator = NULL;
    IMMDevice *device = NULL;
    hr = CoCreateInstance(&clsid_enumerator, NULL, CLSCTX_ALL, &iid_enumerator,
                          (void **)&enumerator);
    if (SUCCEEDED(hr))
        hr = IMMDeviceEnumerator_GetDefaultAudioEndpoint(enumerator, eRender, eConsole, &device);
    if (SUCCEEDED(hr))
        hr = IMMDevice_Activate(device, &iid_client, CLSCTX_ALL, NULL, (void **)&s->client);
    if (device)
        IMMDevice_Release(device);
    if (enumerator)
        IMMDeviceEnumerator_Release(enumerator);
    WAVEFORMATEX format = {0};
    format.wFormatTag = WAVE_FORMAT_PCM;
    format.nChannels = 2;
    format.nSamplesPerSec = 48000;
    format.wBitsPerSample = 16;
    format.nBlockAlign = 4;
    format.nAvgBytesPerSec = 192000;
    if (SUCCEEDED(hr))
        hr = IAudioClient_Initialize(s->client, AUDCLNT_SHAREMODE_SHARED,
                                     AUDCLNT_STREAMFLAGS_LOOPBACK |
                                         AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM |
                                         AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY,
                                     200000, 0, &format, NULL);
    if (SUCCEEDED(hr))
        hr = IAudioClient_GetService(s->client, &iid_capture, (void **)&s->capture);
    if (SUCCEEDED(hr))
        hr = IAudioClient_Start(s->client);
    if (FAILED(hr)) {
        audio_close(s);
        return hr;
    }
    *out = s;
    return S_OK;
}
long audio_poll(audio_native *s, int16_t *dst, unsigned capacity, unsigned *written) {
    *written = 0;
    while (capacity >= 2) {
        if (!s->remaining) {
            UINT32 available = 0;
            HRESULT hr = IAudioCaptureClient_GetNextPacketSize(s->capture, &available);
            if (FAILED(hr))
                return hr;
            if (!available)
                return S_OK;
            UINT64 position, qpc;
            DWORD flags;
            hr = IAudioCaptureClient_GetBuffer(s->capture, &s->packet, &s->remaining, &flags,
                                               &position, &qpc);
            if (FAILED(hr))
                return hr;
            if (!s->remaining) {
                IAudioCaptureClient_ReleaseBuffer(s->capture, 0);
                return S_OK;
            }
            size_t bytes = (size_t)s->remaining * 4;
            if (bytes > s->allocated) {
                BYTE *storage = realloc(s->storage, bytes);
                if (!storage) {
                    IAudioCaptureClient_ReleaseBuffer(s->capture, s->remaining);
                    s->remaining = 0;
                    return E_OUTOFMEMORY;
                }
                s->storage = storage;
                s->allocated = bytes;
            }
            if (flags & AUDCLNT_BUFFERFLAGS_SILENT)
                memset(s->storage, 0, bytes);
            else
                memcpy(s->storage, s->packet, bytes);
            hr = IAudioCaptureClient_ReleaseBuffer(s->capture, s->remaining);
            if (FAILED(hr)) {
                s->remaining = 0;
                return hr;
            }
            s->packet = s->storage;
        }
        UINT32 frames = capacity / 2;
        if (frames > s->remaining)
            frames = s->remaining;
        memcpy(dst, s->packet, frames * 4);
        s->packet += frames * 4;
        s->remaining -= frames;
        dst += frames * 2;
        capacity -= frames * 2;
        *written += frames * 2;
    }
    return S_OK;
}
