package vdev

/*
#cgo CFLAGS: -I/usr/include/pipewire-0.3 -I/usr/include/spa-0.2 -D_GNU_SOURCE
#cgo LDFLAGS: -ldl -lpthread
#include <stdlib.h>
#include "pwcam_linux.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"
)

// pipewireCamera publishes a PipeWire "Video/Source" node with
// media.role=Camera, which is what the camera portal, Chrome/Firefox
// (libwebrtc's PipeWire capturer) and OBS look for.
type pipewireCamera struct {
	mu   sync.Mutex
	c    *C.struct_pwcam
	slot int
	Name string // node.name
}

var (
	pwCamMu    sync.Mutex
	pwCamSlots = map[int]bool{}
)

// openPipeWireCamera fails if libpipewire or a PipeWire daemon is missing.
func openPipeWireCamera() (*pipewireCamera, error) {
	pwCamMu.Lock()
	slot := 1
	for pwCamSlots[slot] {
		slot++
	}
	pwCamSlots[slot] = true
	pwCamMu.Unlock()

	name, desc := "urdp-camera", cameraLabel
	if slot > 1 {
		name, desc = fmt.Sprintf("urdp-camera-%d", slot), fmt.Sprintf("%s %d", cameraLabel, slot)
	}
	cname, cdesc := C.CString(name), C.CString(desc)
	defer C.free(unsafe.Pointer(cname))
	defer C.free(unsafe.Pointer(cdesc))
	var errbuf [256]C.char
	c := C.pwcam_open(cname, cdesc, &errbuf[0], C.int(len(errbuf)))
	if c == nil {
		releasePWSlot(slot)
		return nil, errors.New(C.GoString(&errbuf[0]))
	}
	return &pipewireCamera{c: c, slot: slot, Name: name}, nil
}

func releasePWSlot(slot int) {
	pwCamMu.Lock()
	delete(pwCamSlots, slot)
	pwCamMu.Unlock()
}

func (p *pipewireCamera) WriteFrame(img *Image) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.c == nil {
		return errors.New("pipewire: camera closed")
	}
	w, h := img.Width&^1, img.Height&^1
	if w <= 0 || h <= 0 {
		return nil
	}
	cw, ch := w/2, h/2
	if img.YStride < w || img.UStride < cw || img.VStride < cw ||
		len(img.Y) < (h-1)*img.YStride+w || len(img.U) < (ch-1)*img.UStride+cw || len(img.V) < (ch-1)*img.VStride+cw {
		return fmt.Errorf("pipewire: short %dx%d image", img.Width, img.Height)
	}
	var errbuf [256]C.char
	if C.pwcam_write(p.c,
		(*C.uint8_t)(unsafe.Pointer(&img.Y[0])), C.int(img.YStride),
		(*C.uint8_t)(unsafe.Pointer(&img.U[0])), C.int(img.UStride),
		(*C.uint8_t)(unsafe.Pointer(&img.V[0])), C.int(img.VStride),
		C.int(w), C.int(h), &errbuf[0], C.int(len(errbuf))) < 0 {
		return errors.New(C.GoString(&errbuf[0]))
	}
	return nil
}

type pwCamStats struct {
	Width, Height int
	Format        uint32 // SPA_VIDEO_FORMAT_*, 0 until a consumer connects
	Frames        uint64
	NodeID        uint32
}

func (p *pipewireCamera) stats() pwCamStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	var w, h C.int
	var f, node C.uint32_t
	var n C.uint64_t
	C.pwcam_stats(p.c, &w, &h, &f, &n, &node)
	return pwCamStats{int(w), int(h), uint32(f), uint64(n), uint32(node)}
}

func (p *pipewireCamera) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.c == nil {
		return nil
	}
	C.pwcam_close(p.c)
	p.c = nil
	releasePWSlot(p.slot)
	return nil
}
