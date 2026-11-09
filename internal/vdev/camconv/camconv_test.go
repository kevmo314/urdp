package camconv

import (
	"bytes"
	"testing"
)

func TestI420ToNV12Even(t *testing.T) {
	// 4x2 luma with stride 6, 2x1 chroma with stride 3.
	y := []byte{1, 2, 3, 4, 0, 0, 5, 6, 7, 8, 0, 0}
	u := []byte{10, 11, 0}
	v := []byte{20, 21, 0}
	got, w, h := I420ToNV12(y, u, v, 4, 2, 6, 3, 3)
	want := []byte{1, 2, 3, 4, 5, 6, 7, 8, 10, 20, 11, 21}
	if w != 4 || h != 2 || !bytes.Equal(got, want) {
		t.Fatalf("got %dx%d %v, want 4x2 %v", w, h, got, want)
	}
}

func TestI420ToNV12OddPadsEdges(t *testing.T) {
	// 3x3 image: the fourth column and row repeat the last pixel/row.
	y := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9}
	u := []byte{10, 11, 12, 13}
	v := []byte{20, 21, 22, 23}
	got, w, h := I420ToNV12(y, u, v, 3, 3, 3, 2, 2)
	want := []byte{
		1, 2, 3, 3,
		4, 5, 6, 6,
		7, 8, 9, 9,
		7, 8, 9, 9,
		10, 20, 11, 21,
		12, 22, 13, 23,
	}
	if w != 4 || h != 4 || !bytes.Equal(got, want) {
		t.Fatalf("got %dx%d %v, want 4x4 %v", w, h, got, want)
	}
}

func solid(w, h int, y, u, v byte) []byte {
	img := make([]byte, w*h*3/2)
	for i := range w * h {
		img[i] = y
	}
	for i := w * h; i < len(img); i += 2 {
		img[i], img[i+1] = u, v
	}
	return img
}

func TestNV12FitSameSizeIsCopy(t *testing.T) {
	src := make([]byte, 8*4*3/2)
	for i := range src {
		src[i] = byte(i)
	}
	if got := NV12Fit(src, 8, 4, 8, 4); !bytes.Equal(got, src) {
		t.Fatalf("identity scale changed pixels")
	}
}

func TestNV12FitPillarboxes4x3Into16x9(t *testing.T) {
	// 640x480 into 1280x720: image is 960x720 centred at x=160.
	src := solid(640, 480, 200, 90, 170)
	dw, dh := 1280, 720
	got := NV12Fit(src, 640, 480, dw, dh)
	luma := func(x, y int) byte { return got[y*dw+x] }
	chroma := func(x, y int) (byte, byte) {
		i := dw*dh + y*dw + x*2
		return got[i], got[i+1]
	}
	for _, y := range []int{0, 359, 719} {
		if luma(0, y) != 16 || luma(159, y) != 16 || luma(1120, y) != 16 || luma(1279, y) != 16 {
			t.Fatalf("bars not black on row %d", y)
		}
		if luma(160, y) != 200 || luma(1119, y) != 200 {
			t.Fatalf("image edge wrong on row %d: %d %d", y, luma(160, y), luma(1119, y))
		}
	}
	if u, v := chroma(0, 0); u != 128 || v != 128 {
		t.Fatalf("bar chroma %d,%d", u, v)
	}
	if u, v := chroma(80, 0); u != 90 || v != 170 {
		t.Fatalf("image chroma %d,%d", u, v)
	}
	if u, v := chroma(559, 359); u != 90 || v != 170 {
		t.Fatalf("image chroma at right edge %d,%d", u, v)
	}
	if u, v := chroma(560, 0); u != 128 || v != 128 {
		t.Fatalf("right bar chroma %d,%d", u, v)
	}
}

func TestNV12FitLetterboxesPortrait(t *testing.T) {
	// 720x1280 portrait into 640x480: 270x480 rounded to 270 even, centred.
	src := solid(720, 1280, 100, 1, 2)
	got := NV12Fit(src, 720, 1280, 640, 480)
	if got[0] != 16 || got[639] != 16 {
		t.Fatal("expected pillar bars at the sides")
	}
	if got[320] != 100 || got[479*640+320] != 100 {
		t.Fatal("expected the image in the middle column")
	}
}

func TestNV12FitTinyAndHugeSources(t *testing.T) {
	// Extreme aspect ratios must not write outside the destination; the
	// slices would panic in Go if the C side miscomputed sizes.
	for _, s := range [][2]int{{2, 2}, {4096, 2}, {2, 2160}, {4096, 2160}} {
		src := solid(s[0], s[1], 50, 60, 70)
		for _, d := range [][2]int{{640, 480}, {1280, 720}, {1920, 1080}} {
			got := NV12Fit(src, s[0], s[1], d[0], d[1])
			if len(got) != d[0]*d[1]*3/2 {
				t.Fatal("bad size")
			}
			if !bytes.Contains(got[:d[0]*d[1]], []byte{50, 50}) {
				t.Fatalf("%v into %v: image missing", s, d)
			}
		}
	}
}

func TestNV12Black(t *testing.T) {
	got := NV12Black(4, 2)
	want := []byte{16, 16, 16, 16, 16, 16, 16, 16, 128, 128, 128, 128}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
}
