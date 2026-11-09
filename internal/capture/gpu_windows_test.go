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
