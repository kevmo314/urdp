//go:build libav

package encoder

// Zero-copy input on Windows: the encoder shares the capture's D3D11
// device, and each frame is one GPU-to-GPU copy from the capture texture
// into FFmpeg's frame pool. AMF and NVENC take the BGRA texture and convert
// it to YUV on the GPU.

/*
#cgo LDFLAGS: -ld3d11
#define COBJMACROS
#include <d3d11.h>
#include <libavcodec/avcodec.h>
#include <libavutil/hwcontext.h>
#include <libavutil/hwcontext_d3d11va.h>

static int setup_d3d11(AVCodecContext *c, void *device, int w, int h) {
	AVBufferRef *dev = av_hwdevice_ctx_alloc(AV_HWDEVICE_TYPE_D3D11VA);
	if (!dev) return AVERROR(ENOMEM);
	AVD3D11VADeviceContext *d = ((AVHWDeviceContext *)dev->data)->hwctx;
	d->device = device;
	ID3D11Device_AddRef(d->device);
	int r = av_hwdevice_ctx_init(dev);
	if (r < 0) { av_buffer_unref(&dev); return r; }
	AVBufferRef *frames = av_hwframe_ctx_alloc(dev);
	if (!frames) { av_buffer_unref(&dev); return AVERROR(ENOMEM); }
	AVHWFramesContext *fc = (AVHWFramesContext *)frames->data;
	fc->format = AV_PIX_FMT_D3D11;
	fc->sw_format = AV_PIX_FMT_BGRA;
	fc->width = w;
	fc->height = h;
	// One frame is in flight at a time; a few spare slots cost little.
	fc->initial_pool_size = 4;
	((AVD3D11VAFramesContext *)fc->hwctx)->BindFlags = D3D11_BIND_RENDER_TARGET | D3D11_BIND_SHADER_RESOURCE;
	if ((r = av_hwframe_ctx_init(frames)) < 0) { av_buffer_unref(&frames); av_buffer_unref(&dev); return r; }
	c->pix_fmt = AV_PIX_FMT_D3D11;
	c->sw_pix_fmt = AV_PIX_FMT_BGRA;
	c->hw_frames_ctx = frames;
	c->hw_device_ctx = dev;
	return 0;
}

// next_frame takes a frame from the pool.
static int next_frame(AVCodecContext *c, AVFrame *hw) {
	av_frame_unref(hw);
	return av_hwframe_get_buffer(c->hw_frames_ctx, hw, 0);
}

// copy_texture copies the top-left w x h of a capture texture into hw.
static void copy_texture(AVCodecContext *c, AVFrame *hw, void *texture) {
	AVHWFramesContext *fc = (AVHWFramesContext *)c->hw_frames_ctx->data;
	AVD3D11VADeviceContext *d = fc->device_ctx->hwctx;
	D3D11_BOX box = {0, 0, 0, (UINT)c->width, (UINT)c->height, 1};
	d->lock(d->lock_ctx);
	ID3D11DeviceContext_CopySubresourceRegion(d->device_context, (ID3D11Resource *)hw->data[0],
		(UINT)(intptr_t)hw->data[1], 0, 0, 0, (ID3D11Resource *)texture, 0, &box);
	d->unlock(d->lock_ctx);
}

// send_hw submits a hardware frame (no make_writable: it can't be copied).
static int send_hw(AVCodecContext *c, AVFrame *f, int64_t pts, int key) {
	f->pts = pts;
	f->pict_type = key ? AV_PICTURE_TYPE_I : AV_PICTURE_TYPE_NONE;
	if (key) f->flags |= AV_FRAME_FLAG_KEY; else f->flags &= ~AV_FRAME_FLAG_KEY;
	return avcodec_send_frame(c, f);
}

static AVFrame *alloc_bgra(int w, int h) {
	AVFrame *f = av_frame_alloc();
	f->format = AV_PIX_FMT_BGRA;
	f->width = w;
	f->height = h;
	if (av_frame_get_buffer(f, 64) < 0) av_frame_free(&f);
	return f;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/kevmo314/urdp/internal/capture"
)

func libavGPUSetup(ctx *C.AVCodecContext, device any, w, h int) error {
	d, ok := device.(capture.D3D11Device)
	if !ok || d == nil {
		return fmt.Errorf("unsupported GPU device %T", device)
	}
	if r := C.setup_d3d11(ctx, unsafe.Pointer(d), C.int(w), C.int(h)); r < 0 {
		return averr("D3D11 frames", r)
	}
	return nil
}

// sendGPU submits f: a capture texture is copied on the GPU, CPU pixels
// (some captures fall back to them) are uploaded.
func (e *libavEncoder) sendGPU(f *capture.Frame, ptsMs int64, keyframe bool) error {
	if r := C.next_frame(e.ctx, e.hw); r < 0 {
		return averr("frame pool", r)
	}
	switch {
	case f.GPU != nil:
		t, ok := f.GPU.(capture.D3D11Texture)
		if !ok {
			return fmt.Errorf("libav: unsupported GPU frame %T", f.GPU)
		}
		if capture.D3D11Device(t.Device) != e.device {
			return fmt.Errorf("%w: capture GPU device changed", ErrReopen)
		}
		C.copy_texture(e.ctx, e.hw, t.Texture)
	case f.Pix != nil:
		if e.bgra == nil {
			if e.bgra = C.alloc_bgra(C.int(e.w), C.int(e.h)); e.bgra == nil {
				return errors.New("libav: out of memory")
			}
		}
		if r := C.av_frame_make_writable(e.bgra); r < 0 {
			return averr("frame", r)
		}
		stride := int(e.bgra.linesize[0])
		dst := unsafe.Slice((*byte)(unsafe.Pointer(e.bgra.data[0])), stride*e.h)
		for y := 0; y < e.h; y++ {
			copy(dst[y*stride:y*stride+e.w*4], f.Pix[y*f.Stride:])
		}
		if r := C.av_hwframe_transfer_data(e.hw, e.bgra, 0); r < 0 {
			return averr("upload", r)
		}
	default:
		return errors.New("libav: empty frame")
	}
	if r := C.send_hw(e.ctx, e.hw, C.int64_t(ptsMs), cbool(keyframe)); r < 0 {
		return averr("send frame", r)
	}
	return nil
}
