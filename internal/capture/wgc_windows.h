#pragma once
#include <d3d11.h>
#ifdef __cplusplus
extern "C" {
#endif
typedef struct capture_wgc capture_wgc;
HRESULT capture_wgc_open(HWND hwnd, ID3D11Device *device, capture_wgc **out);
HRESULT capture_wgc_frame(capture_wgc *s, unsigned int timeout_ms, ID3D11Texture2D **texture,
                          D3D11_BOX *box);
void capture_wgc_close(capture_wgc *s);
#ifdef __cplusplus
}
#endif
