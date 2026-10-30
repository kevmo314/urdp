package vdev

/*
#include <linux/videodev2.h>
#include <sys/ioctl.h>
#include <fcntl.h>
#include <unistd.h>
#include <errno.h>
#include <string.h>
#include <stdlib.h>

// Opens path if it is a v4l2loopback device, copying its card label.
static int cam_probe(const char *path, char *card, int cardlen) {
	int fd = open(path, O_RDWR | O_CLOEXEC);
	if (fd < 0) return -errno;
	struct v4l2_capability cap;
	memset(&cap, 0, sizeof cap);
	if (ioctl(fd, VIDIOC_QUERYCAP, &cap) < 0 || strcmp((char *)cap.driver, "v4l2 loopback")) {
		close(fd);
		return -ENODEV;
	}
	strncpy(card, (char *)cap.card, cardlen - 1);
	card[cardlen - 1] = 0;
	return fd;
}

// Sets w×h YUV420, or reports the format another opener has fixed.
static int cam_set_format(int fd, int w, int h, int *aw, int *ah, unsigned *sizeimage) {
	struct v4l2_format f;
	memset(&f, 0, sizeof f);
	f.type = V4L2_BUF_TYPE_VIDEO_OUTPUT;
	f.fmt.pix.width = w;
	f.fmt.pix.height = h;
	f.fmt.pix.pixelformat = V4L2_PIX_FMT_YUV420;
	f.fmt.pix.field = V4L2_FIELD_NONE;
	f.fmt.pix.bytesperline = w;
	f.fmt.pix.sizeimage = w * h * 3 / 2;
	f.fmt.pix.colorspace = V4L2_COLORSPACE_SMPTE170M;
	if (ioctl(fd, VIDIOC_S_FMT, &f) < 0) return -errno;
	if (f.fmt.pix.pixelformat != V4L2_PIX_FMT_YUV420) return -EINVAL;
	*aw = f.fmt.pix.width;
	*ah = f.fmt.pix.height;
	*sizeimage = f.fmt.pix.sizeimage;
	return 0;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

const cameraLabel = "urdp Camera"

var errNoLoopback = errors.New("no v4l2loopback device found (for V4L2-only apps: " +
	"`sudo apt install v4l2loopback-dkms && " +
	"sudo modprobe v4l2loopback exclusive_caps=1 card_label=\"urdp Camera\"`)")

// openCamera prefers a PipeWire camera node, which needs no kernel module
// and is what browsers (via the camera portal) and OBS use. v4l2loopback
// is the fallback, and the only option V4L2-only apps can see without
// a PipeWire V4L2 shim. URDP_CAMERA=v4l2 or =pipewire picks one.
func openCamera() (Camera, error) {
	backend := os.Getenv("URDP_CAMERA")
	if backend == "v4l2" {
		c, err := openLoopbackCamera()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
		}
		return c, nil
	}
	pc, perr := openPipeWireCamera()
	if perr == nil {
		return pc, nil
	}
	if backend == "pipewire" {
		return nil, fmt.Errorf("%w: PipeWire camera unavailable: %v", ErrUnsupported, perr)
	}
	lc, lerr := openLoopbackCamera()
	if lerr == nil {
		return lc, nil
	}
	return nil, fmt.Errorf("%w: PipeWire camera unavailable (%v; start PipeWire, e.g. "+
		"`systemctl --user start pipewire wireplumber`); %v", ErrUnsupported, perr, lerr)
}

var (
	camMu    sync.Mutex
	camInUse = map[string]bool{}
)

type loopbackCamera struct {
	mu   sync.Mutex
	path string
	fd   int
	w, h int // current device format, 0 until the first frame
	// Last size asked for. If a reader holds the old format we scale
	// into it, retrying only when the incoming size changes again.
	tryW, tryH int
	buf        []byte
}

func videoDevices() []string {
	paths, _ := filepath.Glob("/dev/video*")
	num := func(p string) int { n, _ := strconv.Atoi(strings.TrimPrefix(p, "/dev/video")); return n }
	sort.Slice(paths, func(i, j int) bool { return num(paths[i]) < num(paths[j]) })
	return paths
}

func openLoopbackCamera() (Camera, error) {
	camMu.Lock()
	defer camMu.Unlock()
	type cand struct {
		path string
		fd   int
	}
	var found []cand
	var permErr error
	best := -1
	for _, p := range videoDevices() {
		if camInUse[p] {
			continue
		}
		cp := C.CString(p)
		var card [64]C.char
		fd := int(C.cam_probe(cp, &card[0], C.int(len(card))))
		C.free(unsafe.Pointer(cp))
		if fd == -int(syscall.EACCES) {
			permErr = fmt.Errorf("v4l2loopback device %s is not accessible; add your user to the video group", p)
		}
		if fd < 0 {
			continue
		}
		if best < 0 && C.GoString(&card[0]) == cameraLabel {
			best = len(found)
		}
		found = append(found, cand{p, fd})
	}
	if len(found) == 0 {
		if permErr != nil {
			return nil, permErr
		}
		return nil, errNoLoopback
	}
	if best < 0 {
		best = 0
	}
	for i, c := range found {
		if i != best {
			syscall.Close(c.fd)
		}
	}
	c := found[best]
	camInUse[c.path] = true
	return &loopbackCamera{path: c.path, fd: c.fd}, nil
}

func (c *loopbackCamera) WriteFrame(img *Image) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fd < 0 {
		return errors.New("v4l2loopback: camera closed")
	}
	w, h := img.Width&^1, img.Height&^1
	if w <= 0 || h <= 0 {
		return nil
	}
	if (w != c.w || h != c.h) && (c.w == 0 || w != c.tryW || h != c.tryH) {
		if err := c.setFormat(w, h); err != nil {
			return err
		}
	}
	if len(c.buf) < c.w*c.h*3/2 {
		return fmt.Errorf("v4l2loopback: %s wants %d-byte frames", c.path, len(c.buf))
	}
	cw, ch := c.w/2, c.h/2
	y := c.buf[:c.w*c.h]
	u := c.buf[len(y) : len(y)+cw*ch]
	v := c.buf[len(y)+cw*ch : len(y)+2*cw*ch]
	sw, sh := img.Width, img.Height
	packPlane(y, c.w, c.h, img.Y, img.YStride, sw, sh)
	packPlane(u, cw, ch, img.U, img.UStride, (sw+1)/2, (sh+1)/2)
	packPlane(v, cw, ch, img.V, img.VStride, (sw+1)/2, (sh+1)/2)
	if _, err := syscall.Write(c.fd, c.buf); err != nil {
		return fmt.Errorf("v4l2loopback: write %s: %w", c.path, err)
	}
	return nil
}

func (c *loopbackCamera) setFormat(w, h int) error {
	c.tryW, c.tryH = w, h
	if c.w != 0 {
		// The format can't change once this opener has written frames,
		// so start over with a fresh one.
		fd, err := syscall.Open(c.path, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("v4l2loopback: reopening %s: %w", c.path, err)
		}
		syscall.Close(c.fd)
		c.fd = fd
	}
	var aw, ah C.int
	var size C.uint
	if r := C.cam_set_format(C.int(c.fd), C.int(w), C.int(h), &aw, &ah, &size); r < 0 {
		return fmt.Errorf("v4l2loopback: setting %dx%d YUV420 on %s: %w", w, h, c.path, syscall.Errno(-r))
	}
	if aw&1 != 0 || ah&1 != 0 {
		return fmt.Errorf("v4l2loopback: %s is fixed at odd size %dx%d", c.path, aw, ah)
	}
	c.w, c.h = int(aw), int(ah)
	c.buf = make([]byte, max(int(size), int(aw)*int(ah)*3/2))
	return nil
}

// packPlane copies a strided sw×sh plane into a tight dw×dh one,
// nearest-neighbour scaling if the sizes differ.
func packPlane(dst []byte, dw, dh int, src []byte, stride, sw, sh int) {
	if dw <= sw && dh <= sh && (sw-dw) <= 1 && (sh-dh) <= 1 {
		for r := 0; r < dh; r++ {
			copy(dst[r*dw:(r+1)*dw], src[r*stride:])
		}
		return
	}
	for r := 0; r < dh; r++ {
		row := src[(r*sh/dh)*stride:]
		out := dst[r*dw : (r+1)*dw]
		for x := range out {
			out[x] = row[x*sw/dw]
		}
	}
}

func (c *loopbackCamera) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fd < 0 {
		return nil
	}
	err := syscall.Close(c.fd)
	c.fd = -1
	camMu.Lock()
	delete(camInUse, c.path)
	camMu.Unlock()
	return err
}
