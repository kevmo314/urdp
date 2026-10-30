package vpx

import (
	"encoding/binary"
	"os"
	"testing"
)

// The fixtures were made with
//
//	ffmpeg -f lavfi -i "color=white:s=36x50,pad=70:50:0:0:black" -frames:v 3 \
//	  -c:v libvpx -b:v 1M -crf 4 -qmin 0 -qmax 10 -auto-alt-ref 0 split70x50.ivf
//	ffmpeg -f lavfi -i "color=0x3366cc:s=32x24" -frames:v 2 \
//	  -c:v libvpx -b:v 1M -crf 4 -qmin 0 -qmax 10 blue32x24.ivf
func ivfFrames(t *testing.T, name string) [][]byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 32 || string(b[:4]) != "DKIF" || string(b[8:12]) != "VP80" {
		t.Fatalf("%s: not a VP8 IVF file", name)
	}
	var frames [][]byte
	for b = b[binary.LittleEndian.Uint16(b[6:]):]; len(b) >= 12; {
		n := int(binary.LittleEndian.Uint32(b))
		frames = append(frames, b[12:12+n])
		b = b[12+n:]
	}
	return frames
}

func near(a, b byte) bool { return int(a)-int(b) <= 3 && int(b)-int(a) <= 3 }

func TestDecode(t *testing.T) {
	d, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	cases := []struct {
		file    string
		w, h    int
		pixel   func(x, y int) (Y, U, V byte)
		nframes int
	}{
		{"split70x50.ivf", 70, 50, func(x, y int) (byte, byte, byte) {
			if x < 36 {
				return 235, 128, 128
			}
			return 16, 128, 128
		}, 3},
		// A smaller keyframe on the same decoder: the size changes.
		{"blue32x24.ivf", 32, 24, func(x, y int) (byte, byte, byte) { return 100, 180, 98 }, 2},
	}
	for _, c := range cases {
		frames := ivfFrames(t, c.file)
		if len(frames) != c.nframes {
			t.Fatalf("%s: %d frames", c.file, len(frames))
		}
		for i, f := range frames {
			img, err := d.Decode(f)
			if err != nil {
				t.Fatalf("%s frame %d: %v", c.file, i, err)
			}
			if img == nil {
				t.Fatalf("%s frame %d: no picture", c.file, i)
			}
			if img.Width != c.w || img.Height != c.h {
				t.Fatalf("%s frame %d: %dx%d, want %dx%d", c.file, i, img.Width, img.Height, c.w, c.h)
			}
			if img.YStride < c.w || img.UStride < (c.w+1)/2 || img.VStride < (c.w+1)/2 {
				t.Fatalf("strides %d/%d/%d too small", img.YStride, img.UStride, img.VStride)
			}
			for y := 0; y < c.h; y++ {
				for x := 0; x < c.w; x++ {
					// Skip the blurred edge between the halves.
					if x >= 34 && x < 38 {
						continue
					}
					wy, wu, wv := c.pixel(x, y)
					gy := img.Y[y*img.YStride+x]
					gu := img.U[(y/2)*img.UStride+x/2]
					gv := img.V[(y/2)*img.VStride+x/2]
					if !near(gy, wy) || !near(gu, wu) || !near(gv, wv) {
						t.Fatalf("%s frame %d (%d,%d): yuv %d,%d,%d, want %d,%d,%d",
							c.file, i, x, y, gy, gu, gv, wy, wu, wv)
					}
				}
			}
		}
	}

	if _, err := d.Decode([]byte{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4, 5, 6}); err == nil {
		t.Error("garbage frame decoded without error")
	}
}
