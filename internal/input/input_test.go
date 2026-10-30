package input

import (
	"fmt"
	"image"
	"slices"
	"testing"
)

type recorder struct{ calls []string }

func (r *recorder) MouseMove(x, y int) error { r.add("move %d,%d", x, y); return nil }
func (r *recorder) MouseButton(b int, down bool) error {
	r.add("button %d %v", b, down)
	return nil
}
func (r *recorder) Wheel(dx, dy int) error     { r.add("wheel %d,%d", dx, dy); return nil }
func (r *recorder) Key(k Key, down bool) error { r.add("key %#x %v", k.Keysym, down); return nil }
func (r *recorder) Focus(id uint64) error      { r.add("focus %#x", id); return nil }
func (r *recorder) Close() error               { return nil }
func (r *recorder) add(f string, a ...any)     { r.calls = append(r.calls, fmt.Sprintf(f, a...)) }

func TestDispatcher(t *testing.T) {
	rec := &recorder{}
	d := &Dispatcher{Inj: rec, Bounds: func() image.Rectangle { return image.Rect(100, 200, 1101, 701) }, WindowID: 0x42}
	msgs := []string{
		`{"t":"m","x":0,"y":0}`,
		`{"t":"m","x":1,"y":1}`,
		`{"t":"m","x":2,"y":-1}`,
		`{"t":"d","b":2,"x":0.5,"y":0.5}`,
		`{"t":"u","b":2,"x":0.5,"y":0.5}`,
		`{"t":"w","dy":60}`,
		`{"t":"w","dy":60}`,
		`{"t":"w","dx":-250}`,
		`{"t":"kd","c":"ShiftLeft"}`,
		`{"t":"kd","c":"KeyA"}`,
		`{"t":"ku","c":"KeyA"}`,
		`{"t":"kd","c":"NotAKey"}`,
		`{"t":"reset"}`,
	}
	for _, m := range msgs {
		if err := d.Handle([]byte(m)); err != nil {
			t.Fatalf("%s: %v", m, err)
		}
	}
	want := []string{
		"move 100,200",
		"move 1100,700",
		"move 1100,200",
		"focus 0x42", "move 600,450", "button 2 true",
		"move 600,450", "button 2 false",
		"wheel 0,1",
		"wheel -2,0",
		"key 0xffe1 true",
		"key 0x61 true",
		"key 0x61 false",
		"key 0xffe1 false", // released by reset
	}
	if !slices.Equal(rec.calls, want) {
		t.Errorf("calls:\n%q\nwant:\n%q", rec.calls, want)
	}
}

func TestKeymapScancodesUnique(t *testing.T) {
	seen := map[uint16]string{}
	for code, k := range Keys {
		if prev, ok := seen[k.Scan]; ok {
			t.Errorf("%s and %s share scancode %#x", code, prev, k.Scan)
		}
		seen[k.Scan] = code
	}
}

func TestWheelClamped(t *testing.T) {
	rec := &recorder{}
	d := &Dispatcher{Inj: rec, Bounds: func() image.Rectangle { return image.Rect(0, 0, 10, 10) }}
	for _, m := range []string{`{"t":"w","dy":1e15}`, `{"t":"w","dy":-1e300}`} {
		if err := d.Handle([]byte(m)); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"wheel 0,20", "wheel 0,-20"}; !slices.Equal(rec.calls, want) {
		t.Errorf("calls %q, want %q", rec.calls, want)
	}
}
