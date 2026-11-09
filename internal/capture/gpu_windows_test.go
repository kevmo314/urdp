package capture

import (
	"testing"
	"time"
)

// TestGPUOutput switches real captures (the whole desktop and the first
// monitor) to GPU frames and back. It only checks frame kinds and sizes;
// no pixels are read.
func TestGPUOutput(t *testing.T) {
	for _, id := range []string{"", "display1"} {
		t.Run("source="+id, func(t *testing.T) {
			src, err := Open(id)
			if err != nil {
				t.Skip(err)
			}
			defer src.Close()
			g, ok := src.(interface {
				SetGPUOutput(bool) error
				GPUDevice() any
			})
			if !ok {
				t.Fatal("source has no GPU output")
			}
			if g.GPUDevice() == nil {
				t.Fatal("no GPU device")
			}
			if err := g.SetGPUOutput(true); err != nil {
				t.Skip(err) // e.g. monitors on several adapters
			}
			b := src.Bounds()
			kinds := map[string]int{}
			for i := 0; i < 20; i++ {
				f, _, err := src.Capture(30 * time.Millisecond)
				if err != nil {
					t.Fatal(err)
				}
				// The presentation time is in the past, and recent unless
				// the screen has been idle.
				if f.Time.IsZero() || f.Time.After(time.Now()) {
					t.Fatalf("frame time %v (now %v)", f.Time, time.Now())
				}
				if f.Width != b.Dx() || f.Height != b.Dy() {
					t.Fatalf("frame %dx%d, bounds %v", f.Width, f.Height, b)
				}
				switch tex := f.GPU.(type) {
				case D3D11Texture:
					if tex.Texture == nil || D3D11Device(tex.Device) != g.GPUDevice().(D3D11Device) || f.Pix != nil {
						t.Fatalf("bad GPU frame %+v", tex)
					}
					kinds["gpu"]++
				case nil:
					if f.Pix == nil {
						t.Fatal("frame with neither pixels nor texture")
					}
					kinds["cpu"]++
				}
			}
			t.Logf("%v: %v", b, kinds)
			// The first frame may be a CPU seed (GDI) until DXGI reports a
			// change; after that everything should stay on the GPU.
			if kinds["gpu"] == 0 {
				t.Log("no GPU frames: the screen may not have changed during the test")
			}
			// Scaled GPU frames come from the texture's mip chain; the
			// bounds (for input mapping) stay full size.
			sc := src.(interface{ SetScale(int) bool })
			for _, scale := range []int{2, 4, 1} {
				if !sc.SetScale(scale) {
					t.Fatalf("SetScale(%d) refused in GPU mode", scale)
				}
				var f *Frame
				for i := 0; i < 20; i++ {
					if f, _, err = src.Capture(30 * time.Millisecond); err != nil {
						t.Fatal(err)
					}
					if f.GPU != nil {
						break
					}
				}
				tex, ok := f.GPU.(D3D11Texture)
				if !ok {
					t.Logf("scale %d: no GPU frame yet (static screen)", scale)
					continue
				}
				wantMip := map[int]int{1: 0, 2: 1, 4: 2}[scale]
				if tex.Mip != wantMip || f.Width != b.Dx()/scale || f.Height != b.Dy()/scale || src.Bounds() != b {
					t.Fatalf("scale %d: mip %d, %dx%d, bounds %v (want mip %d, %dx%d, %v)", scale, tex.Mip, f.Width, f.Height, src.Bounds(), wantMip, b.Dx()/scale, b.Dy()/scale, b)
				}
				t.Logf("scale %d: %dx%d from mip %d", scale, f.Width, f.Height, tex.Mip)
			}
			if err := g.SetGPUOutput(false); err != nil {
				t.Fatal(err)
			}
			f, _, err := src.Capture(30 * time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			if f.Pix == nil || f.GPU != nil {
				t.Fatal("CPU mode returned a GPU frame")
			}
		})
	}
}
