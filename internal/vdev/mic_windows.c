#define COBJMACROS
#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include "native_windows.h"
#include <audioclient.h>
#include <mmdeviceapi.h>
#include <propsys.h>
#include <stdlib.h>
#include <string.h>
#include <wchar.h>

static const GUID clsid_enumerator = {
    0xbcde0395, 0xe52f, 0x467c, {0x8e, 0x3d, 0xc4, 0x57, 0x92, 0x91, 0x69, 0x2e}};
static const GUID iid_enumerator = {
    0xa95664d2, 0x9614, 0x4f35, {0xa7, 0x46, 0xde, 0x8d, 0xb6, 0x36, 0x17, 0xe6}};
static const GUID iid_client = {
    0x1cb9ad4c, 0xdbfa, 0x4c32, {0xb1, 0x78, 0xc2, 0xf5, 0x68, 0xa7, 0x03, 0xb2}};
static const GUID iid_render = {
    0xf294acfc, 0x3146, 0x4483, {0xa7, 0xbf, 0xad, 0xdc, 0xa7, 0xc2, 0x60, 0xe2}};
static const PROPERTYKEY friendly_name = {
    {0xa45c254e, 0xdf1c, 0x4efd, {0x80, 0x20, 0x67, 0xd1, 0x46, 0xa8, 0x50, 0xe0}}, 14};
struct mic_native {
    IAudioClient *client;
    IAudioRenderClient *render;
    UINT32 capacity;
    int com;
};
void mic_close(mic_native *s) {
    if (!s)
        return;
    if (s->render)
        IAudioRenderClient_Release(s->render);
    if (s->client) {
        IAudioClient_Stop(s->client);
        IAudioClient_Release(s->client);
    }
    int com = s->com;
    free(s);
    if (com)
        CoUninitialize();
}
static int contains_name(const wchar_t *name, const wchar_t *part) {
    size_t length = wcslen(part);
    for (; *name; ++name)
        if (!_wcsnicmp(name, part, length))
            return 1;
    return 0;
}
static int cable_name(const wchar_t *name) {
    // Endpoint direction is eRender: CABLE Input feeds CABLE Output.
    return contains_name(name, L"CABLE Input") || contains_name(name, L"CABLE-A Input") ||
           contains_name(name, L"CABLE-B Input") ||
           (contains_name(name, L"Line 1") && contains_name(name, L"Virtual Audio Cable")) ||
           contains_name(name, L"VoiceMeeter Input") ||
           contains_name(name, L"VoiceMeeter Aux Input") ||
           contains_name(name, L"VoiceMeeter VAIO3 Input");
}

long mic_open(mic_native **out, const char *name) {
    mic_native *s = calloc(1, sizeof(*s));
    if (!s)
        return E_OUTOFMEMORY;
    HRESULT hr = CoInitializeEx(NULL, COINIT_MULTITHREADED);
    if (FAILED(hr)) {
        free(s);
        return hr;
    }
    s->com = 1;
    wchar_t *override = NULL;
    if (name && *name) {
        int n = MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, name, -1, NULL, 0);
        if (!n) {
            mic_close(s);
            return E_INVALIDARG;
        }
        override = calloc(n, sizeof(wchar_t));
        if (!override) {
            mic_close(s);
            return E_OUTOFMEMORY;
        }
        MultiByteToWideChar(CP_UTF8, 0, name, -1, override, n);
    }
    IMMDeviceEnumerator *enumerator = NULL;
    IMMDeviceCollection *devices = NULL;
    IMMDevice *chosen = NULL;
    hr = CoCreateInstance(&clsid_enumerator, NULL, CLSCTX_ALL, &iid_enumerator,
                          (void **)&enumerator);
    if (SUCCEEDED(hr))
        hr = IMMDeviceEnumerator_EnumAudioEndpoints(enumerator, eRender, DEVICE_STATE_ACTIVE,
                                                    &devices);
    UINT count = 0;
    if (SUCCEEDED(hr))
        hr = IMMDeviceCollection_GetCount(devices, &count);
    for (UINT i = 0; SUCCEEDED(hr) && i < count; ++i) {
        IMMDevice *device = NULL;
        IPropertyStore *props = NULL;
        PROPVARIANT value;
        PropVariantInit(&value);
        HRESULT item = IMMDeviceCollection_Item(devices, i, &device);
        if (SUCCEEDED(item))
            item = IMMDevice_OpenPropertyStore(device, STGM_READ, &props);
        if (SUCCEEDED(item))
            item = IPropertyStore_GetValue(props, &friendly_name, &value);
        int match = SUCCEEDED(item) && value.vt == VT_LPWSTR &&
                    (override ? !_wcsicmp(override, value.pwszVal) : cable_name(value.pwszVal));
        PropVariantClear(&value);
        if (props)
            IPropertyStore_Release(props);
        if (match) {
            chosen = device;
            break;
        }
        if (device)
            IMMDevice_Release(device);
    }
    free(override);
    if (devices)
        IMMDeviceCollection_Release(devices);
    if (enumerator)
        IMMDeviceEnumerator_Release(enumerator);
    if (SUCCEEDED(hr) && !chosen)
        hr = HRESULT_FROM_WIN32(ERROR_NOT_FOUND);
    if (SUCCEEDED(hr))
        hr = IMMDevice_Activate(chosen, &iid_client, CLSCTX_ALL, NULL, (void **)&s->client);
    if (chosen)
        IMMDevice_Release(chosen);
    WAVEFORMATEX format = {WAVE_FORMAT_PCM, 2, 48000, 192000, 4, 16, 0};
    if (SUCCEEDED(hr))
        hr = IAudioClient_Initialize(s->client, AUDCLNT_SHAREMODE_SHARED,
                                     AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM |
                                         AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY,
                                     200000, 0, &format, NULL);
    if (SUCCEEDED(hr))
        hr = IAudioClient_GetBufferSize(s->client, &s->capacity);
    if (SUCCEEDED(hr))
        hr = IAudioClient_GetService(s->client, &iid_render, (void **)&s->render);
    if (SUCCEEDED(hr))
        hr = IAudioClient_Start(s->client);
    if (FAILED(hr)) {
        mic_close(s);
        return hr;
    }
    *out = s;
    return S_OK;
}
long mic_write(mic_native *s, const int16_t *pcm, unsigned frames) {
    UINT32 padding;
    HRESULT hr = IAudioClient_GetCurrentPadding(s->client, &padding);
    if (FAILED(hr))
        return hr;
    // Never wait for playback or accumulate more than 20ms, even if the engine
    // selected a larger shared buffer. Keep the newest samples on overflow.
    UINT32 limit = s->capacity < 960 ? s->capacity : 960;
    if (padding >= limit)
        return S_OK;
    UINT32 available = limit - padding;
    if (frames > available) {
        pcm += (frames - available) * 2;
        frames = available;
    }
    if (!frames)
        return S_OK;
    BYTE *dst;
    hr = IAudioRenderClient_GetBuffer(s->render, frames, &dst);
    if (FAILED(hr))
        return hr;
    memcpy(dst, pcm, frames * 4);
    return IAudioRenderClient_ReleaseBuffer(s->render, frames, 0);
}
