// Package d3dtest makes D3D11 textures with known content for testing
// GPU-frame encoders on Windows.
package d3dtest

/*
#cgo LDFLAGS: -ld3d11
#define COBJMACROS
#include <d3d10.h>
#include <d3d11.h>
#include <stdlib.h>

static long pattern(int w, int h, const unsigned char *bgra, void **device, void **texture) {
	ID3D11Device *d = NULL;
	ID3D11DeviceContext *c = NULL;
	HRESULT hr = D3D11CreateDevice(NULL, D3D_DRIVER_TYPE_HARDWARE, NULL,
		D3D11_CREATE_DEVICE_BGRA_SUPPORT | D3D11_CREATE_DEVICE_VIDEO_SUPPORT, NULL, 0, D3D11_SDK_VERSION, &d, NULL, &c);
	if (FAILED(hr)) return hr;
	ID3D10Multithread *mt = NULL;
	if (SUCCEEDED(ID3D11Device_QueryInterface(d, &IID_ID3D10Multithread, (void **)&mt))) {
		ID3D10Multithread_SetMultithreadProtected(mt, TRUE);
		ID3D10Multithread_Release(mt);
	}
	D3D11_TEXTURE2D_DESC desc = {(UINT)w, (UINT)h, 1, 1, DXGI_FORMAT_B8G8R8A8_UNORM, {1, 0},
		D3D11_USAGE_DEFAULT, D3D11_BIND_SHADER_RESOURCE, 0, 0};
	D3D11_SUBRESOURCE_DATA data = {bgra, (UINT)w * 4, 0};
	ID3D11Texture2D *t = NULL;
	hr = ID3D11Device_CreateTexture2D(d, &desc, &data, &t);
	ID3D11DeviceContext_Release(c);
	if (FAILED(hr)) { ID3D11Device_Release(d); return hr; }
	*device = d;
	*texture = t;
	return 0;
}
static void release(void *p) { if (p) IUnknown_Release((IUnknown *)p); }
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// Texture is a BGRA texture on its own hardware device.
type Texture struct {
	Device, Texture unsafe.Pointer
}

// New uploads w x h BGRA pixels into a texture on a new device.
func New(w, h int, bgra []byte) (*Texture, error) {
	if len(bgra) < w*h*4 {
		return nil, fmt.Errorf("d3dtest: need %d bytes", w*h*4)
	}
	cpix := C.CBytes(bgra)
	defer C.free(cpix)
	var d, t unsafe.Pointer
	if hr := C.pattern(C.int(w), C.int(h), (*C.uchar)(cpix), &d, &t); hr != 0 {
		return nil, fmt.Errorf("d3dtest: HRESULT 0x%08x", uint32(hr))
	}
	return &Texture{Device: d, Texture: t}, nil
}

func (t *Texture) Close() {
	C.release(t.Texture)
	C.release(t.Device)
}
