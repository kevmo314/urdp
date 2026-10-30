package vdev

import (
	"math"
	"testing"
)

func TestImageValidationWindows(t *testing.T) {
	// Odd sizes and padding are legal; the final row need not include padding.
	valid := Image{Width: 3, Height: 3, Y: make([]byte, 13), U: make([]byte, 5), V: make([]byte, 6), YStride: 5, UStride: 3, VStride: 4}
	if err := validateImage(&valid); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*Image)
	}{
		{"short luma", func(i *Image) { i.Y = i.Y[:12] }},
		{"short chroma", func(i *Image) { i.U = i.U[:4] }},
		{"negative stride", func(i *Image) { i.VStride = -1 }},
		{"narrow stride", func(i *Image) { i.YStride = 2 }},
		{"overflow stride", func(i *Image) { i.YStride = math.MaxInt }},
		{"oversized", func(i *Image) { i.Height = 16385 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img := valid
			tc.change(&img)
			if validateImage(&img) == nil {
				t.Fatal("accepted invalid native buffer")
			}
		})
	}
	if validateImage(nil) == nil {
		t.Fatal("accepted nil image")
	}
}

func TestStickWindows(t *testing.T) {
	for _, tc := range []struct {
		value float64
		want  int16
	}{
		{-2, -32768}, {-1, -32768}, {-.5, -16384}, {0, 0}, {.5, 16384}, {1, 32767}, {2, 32767}, {math.NaN(), 0}, {math.Inf(1), 32767},
	} {
		if got := int16(stick(tc.value)); got != tc.want {
			t.Errorf("stick(%v)=%d, want %d", tc.value, got, tc.want)
		}
	}
}
