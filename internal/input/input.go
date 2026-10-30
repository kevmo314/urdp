// Package input injects remote mouse and keyboard events into the host.
package input

import (
	"encoding/json"
	"fmt"
	"image"
	"math"
)

// Event is the wire format sent by the browser over the data channels.
//
//	{"t":"m","x":0.5,"y":0.5}            mouse move (normalized to the stream)
//	{"t":"d","b":0,"x":..,"y":..}        mouse button down (0 left,1 middle,2 right)
//	{"t":"u","b":0,"x":..,"y":..}        mouse button up
//	{"t":"w","dx":0,"dy":-120}           wheel (browser deltaX/deltaY in pixels)
//	{"t":"kd","c":"KeyA"}                key down (KeyboardEvent.code)
//	{"t":"kd","c":"KeyA","r":true}       auto-repeat of a held key
//	{"t":"ku","c":"KeyA"}                key up
type Event struct {
	Type string  `json:"t"`
	X    float64 `json:"x,omitempty"`
	Y    float64 `json:"y,omitempty"`
	B    int     `json:"b,omitempty"`
	DX   float64 `json:"dx,omitempty"`
	DY   float64 `json:"dy,omitempty"`
	Code string  `json:"c,omitempty"`
	// Repeat marks browser auto-repeat keydowns. They're dropped on hosts
	// that auto-repeat held keys themselves.
	Repeat bool `json:"r,omitempty"`
}

// Button identifiers, matching MouseEvent.button.
const (
	ButtonLeft   = 0
	ButtonMiddle = 1
	ButtonRight  = 2
)

// Injector delivers events to the OS. Coordinates are absolute screen pixels.
type Injector interface {
	MouseMove(x, y int) error
	MouseButton(button int, down bool) error
	// Wheel scrolls by the given number of notches (positive dy = down).
	Wheel(dx, dy int) error
	Key(k Key, down bool) error
	// Focus raises/activates a window before input is sent to it.
	Focus(windowID uint64) error
	Close() error
}

// Dispatcher translates wire events into injector calls, mapping normalized
// coordinates onto the current bounds of the captured source.
type Dispatcher struct {
	Inj      Injector
	Bounds   func() image.Rectangle
	WindowID uint64

	wheelX, wheelY float64
	pressed        map[string]bool
	buttons        map[int]bool
}

// Handle parses and applies one JSON message.
func (d *Dispatcher) Handle(msg []byte) error {
	var e Event
	if err := json.Unmarshal(msg, &e); err != nil {
		return err
	}
	return d.Apply(e)
}

func (d *Dispatcher) point(x, y float64) (int, int) {
	r := d.Bounds()
	clamp := func(v float64) float64 { return min(max(v, 0), 1) }
	px := r.Min.X + int(clamp(x)*float64(r.Dx()-1)+0.5)
	py := r.Min.Y + int(clamp(y)*float64(r.Dy()-1)+0.5)
	return px, py
}

// Apply executes a parsed event.
func (d *Dispatcher) Apply(e Event) error {
	if d.pressed == nil {
		d.pressed = map[string]bool{}
		d.buttons = map[int]bool{}
	}
	switch e.Type {
	case "m":
		return d.Inj.MouseMove(d.point(e.X, e.Y))
	case "d", "u":
		if e.B < 0 || e.B > 2 {
			return nil
		}
		down := e.Type == "d"
		if down && d.WindowID != 0 {
			_ = d.Inj.Focus(d.WindowID)
		}
		if err := d.Inj.MouseMove(d.point(e.X, e.Y)); err != nil {
			return err
		}
		d.buttons[e.B] = down
		return d.Inj.MouseButton(e.B, down)
	case "w":
		// Browsers report ~100-120px per notch; accumulate fractional
		// scrolling (trackpads) until a full notch is reached.
		const notch, maxNotches = 100.0, 20
		if math.IsNaN(e.DX+e.DY) || math.IsInf(e.DX+e.DY, 0) {
			return nil
		}
		d.wheelX += e.DX
		d.wheelY += e.DY
		nx, ny := int(d.wheelX/notch), int(d.wheelY/notch)
		d.wheelX -= float64(nx) * notch
		d.wheelY -= float64(ny) * notch
		// Bound the work one message can cause.
		if nx < -maxNotches || nx > maxNotches || ny < -maxNotches || ny > maxNotches {
			nx, ny = max(min(nx, maxNotches), -maxNotches), max(min(ny, maxNotches), -maxNotches)
			d.wheelX, d.wheelY = 0, 0
		}
		if nx == 0 && ny == 0 {
			return nil
		}
		return d.Inj.Wheel(nx, ny)
	case "kd", "ku":
		k, ok := Keys[e.Code]
		if !ok {
			return nil
		}
		down := e.Type == "kd"
		if down && e.Repeat && hostAutoRepeats {
			return nil
		}
		d.pressed[e.Code] = down
		return d.Inj.Key(k, down)
	case "reset":
		d.Release()
		return nil
	}
	return fmt.Errorf("unknown event type %q", e.Type)
}

// Release lifts every key and button still held, e.g. when the client
// disconnects or loses focus, so nothing stays stuck down on the host.
func (d *Dispatcher) Release() {
	for code, down := range d.pressed {
		if down {
			_ = d.Inj.Key(Keys[code], false)
		}
	}
	for b, down := range d.buttons {
		if down {
			_ = d.Inj.MouseButton(b, false)
		}
	}
	d.pressed = map[string]bool{}
	d.buttons = map[int]bool{}
}
