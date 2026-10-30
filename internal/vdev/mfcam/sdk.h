// ABI declarations from the Windows SDK mfvirtualcamera.h. MinGW's SDK
// does not yet ship this Windows 11 interface. Load the entry point at runtime.
#pragma once
#include <windows.h>
#include <mfapi.h>
#include <mfidl.h>
#include <devpropdef.h>

struct IMFCameraSyncObject;
enum MFVirtualCameraType { MFVirtualCameraType_SoftwareCameraSource = 0 };
enum MFVirtualCameraLifetime {
    MFVirtualCameraLifetime_Session = 0,
    MFVirtualCameraLifetime_System = 1
};
enum MFVirtualCameraAccess {
    MFVirtualCameraAccess_CurrentUser = 0,
    MFVirtualCameraAccess_AllUsers = 1
};
struct IMFVirtualCamera : public IMFAttributes {
    virtual HRESULT STDMETHODCALLTYPE AddDeviceSourceInfo(LPCWSTR) = 0;
    virtual HRESULT STDMETHODCALLTYPE AddProperty(const DEVPROPKEY *, DEVPROPTYPE, const BYTE *,
                                                  ULONG) = 0;
    virtual HRESULT STDMETHODCALLTYPE AddRegistryEntry(LPCWSTR, LPCWSTR, DWORD, const BYTE *,
                                                       ULONG) = 0;
    virtual HRESULT STDMETHODCALLTYPE Start(IMFAsyncCallback *) = 0;
    virtual HRESULT STDMETHODCALLTYPE Stop() = 0;
    virtual HRESULT STDMETHODCALLTYPE Remove() = 0;
    virtual HRESULT STDMETHODCALLTYPE GetMediaSource(IMFMediaSource **) = 0;
    virtual HRESULT STDMETHODCALLTYPE SendCameraProperty(REFGUID, ULONG, ULONG, void *, ULONG,
                                                         void *, ULONG, ULONG *) = 0;
    virtual HRESULT STDMETHODCALLTYPE CreateSyncEvent(REFGUID, ULONG, ULONG, HANDLE,
                                                      IMFCameraSyncObject **) = 0;
    virtual HRESULT STDMETHODCALLTYPE CreateSyncSemaphore(REFGUID, ULONG, ULONG, HANDLE, LONG,
                                                          IMFCameraSyncObject **) = 0;
    virtual HRESULT STDMETHODCALLTYPE Shutdown() = 0;
};

static const GUID CLSID_UrdpCamera = {
    0xd8b04d25, 0x43b5, 0x4b57, {0x9b, 0x1b, 0xda, 0x8e, 0x5f, 0x2d, 0x6c, 0x90}};
using CreateVirtualCamera = HRESULT(WINAPI *)(MFVirtualCameraType, MFVirtualCameraLifetime,
                                              MFVirtualCameraAccess, LPCWSTR, LPCWSTR, const GUID *,
                                              ULONG, IMFVirtualCamera **);
