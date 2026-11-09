// Standalone unpackaged WinRT ABI probe; no SDK projection or driver required.
// ABI source: microsoft/windows-rs Windows/UI/Input/Preview/Injection/mod.rs.
#include <inspectable.h>
#include <roapi.h>
#include <stdio.h>
#include <windows.h>
#include <winstring.h>
#include <xinput.h>
struct Statics : IInspectable {
  virtual HRESULT STDMETHODCALLTYPE TryCreate(IInspectable **) = 0;
};
struct Statics2 : IInspectable {
  virtual HRESULT STDMETHODCALLTYPE
  TryCreateForAppBroadcastOnly(IInspectable **) = 0;
};
struct Injector2 : IInspectable {
  virtual HRESULT STDMETHODCALLTYPE InitializeGamepadInjection() = 0;
  virtual HRESULT STDMETHODCALLTYPE InjectGamepadInput(IInspectable *) = 0;
  virtual HRESULT STDMETHODCALLTYPE UninitializeGamepadInjection() = 0;
};
struct GamepadInfo : IInspectable {
  virtual HRESULT STDMETHODCALLTYPE getButtons(UINT32 *) = 0;
  virtual HRESULT STDMETHODCALLTYPE setButtons(UINT32) = 0;
  virtual HRESULT STDMETHODCALLTYPE getLX(double *) = 0;
  virtual HRESULT STDMETHODCALLTYPE setLX(double) = 0;
  virtual HRESULT STDMETHODCALLTYPE getLY(double *) = 0;
  virtual HRESULT STDMETHODCALLTYPE setLY(double) = 0;
  virtual HRESULT STDMETHODCALLTYPE getLT(double *) = 0;
  virtual HRESULT STDMETHODCALLTYPE setLT(double) = 0;
  virtual HRESULT STDMETHODCALLTYPE getRX(double *) = 0;
  virtual HRESULT STDMETHODCALLTYPE setRX(double) = 0;
  virtual HRESULT STDMETHODCALLTYPE getRY(double *) = 0;
  virtual HRESULT STDMETHODCALLTYPE setRY(double) = 0;
  virtual HRESULT STDMETHODCALLTYPE getRT(double *) = 0;
  virtual HRESULT STDMETHODCALLTYPE setRT(double) = 0;
};
struct VectorView : IInspectable {
  virtual HRESULT STDMETHODCALLTYPE GetAt(UINT32, IInspectable **) = 0;
  virtual HRESULT STDMETHODCALLTYPE Size(UINT32 *) = 0;
};
struct GamepadStatics : IInspectable {
  virtual HRESULT STDMETHODCALLTYPE Added(void *, INT64 *) = 0;
  virtual HRESULT STDMETHODCALLTYPE RemoveAdded(INT64) = 0;
  virtual HRESULT STDMETHODCALLTYPE Removed(void *, INT64 *) = 0;
  virtual HRESULT STDMETHODCALLTYPE RemoveRemoved(INT64) = 0;
  virtual HRESULT STDMETHODCALLTYPE Gamepads(VectorView **) = 0;
};
static void observe() {
  const GUID iid = {0x8bbce529,
                    0xd49c,
                    0x39e9,
                    {0x95, 0x60, 0xe4, 0x7d, 0xde, 0x96, 0xb7, 0xc8}};
  HSTRING n;
  const wchar_t *cls = L"Windows.Gaming.Input.Gamepad";
  WindowsCreateString(cls, (UINT32)wcslen(cls), &n);
  GamepadStatics *f = nullptr;
  HRESULT hr = RoGetActivationFactory(n, iid, (void **)&f);
  WindowsDeleteString(n);
  if (f) {
    VectorView *v = nullptr;
    hr = f->Gamepads(&v);
    if (v) {
      UINT32 count = 0;
      hr = v->Size(&count);
      printf("WGI Gamepads.Size=%u hr=0x%08lx\n", count, (unsigned long)hr);
      v->Release();
    }
    f->Release();
  }
  HMODULE dll = LoadLibraryW(L"xinput1_4.dll");
  if (dll) {
    auto get = (DWORD(WINAPI *)(DWORD, XINPUT_STATE *))GetProcAddress(
        dll, "XInputGetState");
    if (get)
      for (DWORD i = 0; i < 4; i++) {
        XINPUT_STATE state = {};
        DWORD err = get(i, &state);
        printf("XInput[%lu]=%lu buttons=0x%04x lx=%d\n", i, err,
               state.Gamepad.wButtons, state.Gamepad.sThumbLX);
      }
    FreeLibrary(dll);
  }
}
static const GUID staticsIID = {
    0xdeae6943,
    0x7402,
    0x4141,
    {0xa5, 0xc6, 0x0c, 0x01, 0xaa, 0x57, 0xb1, 0x6a}};
static const GUID statics2IID = {
    0xa4db38fb,
    0xdd8c,
    0x414f,
    {0x95, 0xea, 0xf8, 0x7e, 0xf4, 0xc0, 0xae, 0x6c}};
static const GUID injector2IID = {
    0x8e7a905d,
    0x1453,
    0x43a7,
    {0x9b, 0xcb, 0x06, 0xd6, 0xd7, 0xb3, 0x05, 0xf7}};
int main(int argc, char **) {
  setvbuf(stdout, nullptr, _IONBF, 0);
  HRESULT hr = RoInitialize(RO_INIT_MULTITHREADED);
  printf("RoInitialize=0x%08lx\n", (unsigned long)hr);
  if (FAILED(hr))
    return 1;
  observe();
  if (argc > 1) {
    for (int i = 0; i < 5; i++) {
      Sleep(1000);
      observe();
    }
    RoUninitialize();
    return 0;
  }
  HSTRING name;
  const wchar_t *cls = L"Windows.UI.Input.Preview.Injection.InputInjector";
  WindowsCreateString(cls, (UINT32)wcslen(cls), &name);
  Statics2 *f2 = nullptr;
  hr = RoGetActivationFactory(name, statics2IID, (void **)&f2);
  printf("IInputInjectorStatics2=0x%08lx\n", (unsigned long)hr);
  if (f2)
    f2->Release();
  Statics *f = nullptr;
  hr = RoGetActivationFactory(name, staticsIID, (void **)&f);
  WindowsDeleteString(name);
  printf("IInputInjectorStatics=0x%08lx\n", (unsigned long)hr);
  IInspectable *obj = nullptr;
  if (SUCCEEDED(hr)) {
    hr = f->TryCreate(&obj);
    f->Release();
    printf("TryCreate=0x%08lx object=%s\n", (unsigned long)hr,
           obj ? "non-null" : "null");
  }
  if (obj) {
    Injector2 *pad = nullptr;
    hr = obj->QueryInterface(injector2IID, (void **)&pad);
    printf("IInputInjector2=0x%08lx\n", (unsigned long)hr);
    if (pad) {
      hr = pad->InitializeGamepadInjection();
      printf("InitializeGamepadInjection=0x%08lx\n", (unsigned long)hr);
      if (SUCCEEDED(hr)) {
        HSTRING infoName;
        const wchar_t *infoClass =
            L"Windows.UI.Input.Preview.Injection.InjectedInputGamepadInfo";
        WindowsCreateString(infoClass, (UINT32)wcslen(infoClass), &infoName);
        IInspectable *info = nullptr;
        hr = RoActivateInstance(infoName, &info);
        WindowsDeleteString(infoName);
        printf("RoActivateInstance(GamepadInfo)=0x%08lx\n", (unsigned long)hr);
        if (info) {
          const GUID iid = {0x20ae9a3f,
                            0xdf11,
                            0x4572,
                            {0xa9, 0xab, 0xd7, 0x5b, 0x8a, 0x5e, 0x48, 0xad}};
          GamepadInfo *fields = nullptr;
          hr = info->QueryInterface(iid, (void **)&fields);
          if (fields) {
            fields->setButtons(4);
            fields->setLX(0.5);
            fields->Release();
          }
          for (int i = 0; i < 300; i++) {
            hr = pad->InjectGamepadInput(info);
            if (i == 0 || i == 50) {
              printf("InjectGamepadInput=0x%08lx\n", (unsigned long)hr);
              observe();
            }
            Sleep(100);
          }
          info->Release();
        }
        pad->UninitializeGamepadInjection();
      }
      pad->Release();
    }
    obj->Release();
  }
  RoUninitialize();
  return 0;
}
