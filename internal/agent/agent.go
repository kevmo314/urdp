// Package agent exposes the host to AI agents as an MCP server: list
// sources, take screenshots, and drive the mouse and keyboard.
//
// Coordinates passed to the input tools are in the pixel space of the most
// recent screenshot of the same source (screenshots may be downscaled), so
// an agent can act directly on what it saw.
package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/image/draw"

	"github.com/kevmo314/urdp/internal/capture"
	"github.com/kevmo314/urdp/internal/input"
)

// Deps are the host facilities the tools use.
type Deps struct {
	OpenSource   func(id string) (capture.Source, error)
	ListWindows  func() ([]capture.Window, error)
	ListDisplays func() ([]capture.Display, error)
	// Injector returns the shared input injector, or nil if input is
	// unavailable.
	Injector func() input.Injector
}

// view remembers how the last screenshot of a source maps to the screen.
type view struct {
	bounds image.Rectangle // screen rectangle the screenshot covered
	scale  float64         // screenshot pixels per source pixel
	window uint64
}

type tools struct {
	deps Deps
	mu   sync.Mutex
	// views is keyed by source id ("" is the whole desktop).
	views map[string]view
}

// Handler serves MCP over streamable HTTP.
func Handler(deps Deps) http.Handler {
	t := &tools{deps: deps, views: map[string]view{}}
	s := mcp.NewServer(&mcp.Implementation{Name: "urdp", Version: "0.1"}, &mcp.ServerOptions{
		Instructions: "Remote control of a computer. Call screenshot first; coordinates for click, move, drag " +
			"and scroll are pixels in the most recent screenshot of the same source. source is \"\" for the whole " +
			"desktop (all monitors), \"display1\"/\"display2\" for one monitor, or a window id from list_sources.",
	})
	t.register(s)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
}

type sourceArg struct {
	Source string `json:"source,omitempty" jsonschema:"\"\" for the whole desktop, \"display1\" etc. for one monitor, or a window id like 0x1a00007"`
}

type screenshotArgs struct {
	sourceArg
	MaxWidth int    `json:"max_width,omitempty" jsonschema:"downscale so the image is at most this wide (default 1280; 0 or negative keeps full resolution)"`
	Format   string `json:"format,omitempty" jsonschema:"png (default) or jpeg"`
}

type pointArgs struct {
	sourceArg
	X float64 `json:"x" jsonschema:"x in screenshot pixels"`
	Y float64 `json:"y" jsonschema:"y in screenshot pixels"`
}

type clickArgs struct {
	pointArgs
	Button string `json:"button,omitempty" jsonschema:"left (default), right or middle"`
	Count  int    `json:"count,omitempty" jsonschema:"number of clicks, e.g. 2 for a double click (default 1)"`
}

type dragArgs struct {
	sourceArg
	X1     float64 `json:"x1"`
	Y1     float64 `json:"y1"`
	X2     float64 `json:"x2"`
	Y2     float64 `json:"y2"`
	Button string  `json:"button,omitempty" jsonschema:"left (default), right or middle"`
}

type scrollArgs struct {
	pointArgs
	DX int `json:"dx,omitempty" jsonschema:"horizontal notches, positive scrolls right"`
	DY int `json:"dy,omitempty" jsonschema:"vertical notches, positive scrolls down"`
}

type typeArgs struct {
	Text string `json:"text" jsonschema:"text to type into the focused control"`
}

type keyArgs struct {
	Keys string `json:"keys" jsonschema:"a key or combination, e.g. Enter, ctrl+c, ctrl+shift+t, alt+F4, cmd+space"`
}

type focusArgs struct {
	ID string `json:"id" jsonschema:"window id from list_sources"`
}

type empty struct{}

func text(format string, a ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, a...)}}}
}

func (t *tools) register(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "list_sources", Description: "List monitors and windows that can be captured and controlled."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, any, error) {
			return t.listSources()
		})
	mcp.AddTool(s, &mcp.Tool{Name: "screenshot", Description: "Capture the desktop, a monitor or a window. Later input coordinates are in this image's pixels."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a screenshotArgs) (*mcp.CallToolResult, any, error) {
			return t.screenshot(a)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "click", Description: "Move the pointer to (x, y) and click."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a clickArgs) (*mcp.CallToolResult, any, error) {
			return t.click(a)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "move", Description: "Move the pointer to (x, y) without clicking (e.g. to hover)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a pointArgs) (*mcp.CallToolResult, any, error) {
			return t.move(a)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "drag", Description: "Press at (x1, y1), move to (x2, y2) and release."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a dragArgs) (*mcp.CallToolResult, any, error) {
			return t.drag(a)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "scroll", Description: "Scroll the mouse wheel at (x, y)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a scrollArgs) (*mcp.CallToolResult, any, error) {
			return t.scroll(a)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "type_text", Description: "Type text into the focused control (US keyboard layout; ASCII)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a typeArgs) (*mcp.CallToolResult, any, error) {
			return t.typeText(a)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "key", Description: "Press a key or key combination, e.g. Enter, Escape, ctrl+a, alt+tab."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a keyArgs) (*mcp.CallToolResult, any, error) {
			return t.key(a)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "focus_window", Description: "Bring a window to the front and give it keyboard focus."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a focusArgs) (*mcp.CallToolResult, any, error) {
			return t.focus(a)
		})
}

func (t *tools) listSources() (*mcp.CallToolResult, any, error) {
	var b strings.Builder
	if ds, err := t.deps.ListDisplays(); err == nil {
		fmt.Fprintf(&b, "Displays (source \"\" is all of them combined):\n")
		for _, d := range ds {
			primary := ""
			if d.Primary {
				primary = " primary"
			}
			fmt.Fprintf(&b, "  display%d  %dx%d at (%d,%d)  %s%s\n", d.Index, d.Width, d.Height, d.X, d.Y, d.Name, primary)
		}
	}
	wins, err := t.deps.ListWindows()
	if err != nil {
		return nil, nil, err
	}
	fmt.Fprintf(&b, "Windows:\n")
	for _, w := range wins {
		fmt.Fprintf(&b, "  %s  %dx%d  [%s] %s\n", capture.FormatID(w.ID), w.Width, w.Height, w.App, w.Title)
	}
	return text("%s", b.String()), nil, nil
}

func (t *tools) screenshot(a screenshotArgs) (*mcp.CallToolResult, any, error) {
	src, err := t.deps.OpenSource(a.Source)
	if err != nil {
		return nil, nil, fmt.Errorf("open source %q: %w", a.Source, err)
	}
	defer src.Close()
	f, _, err := src.Capture(0)
	if err != nil {
		return nil, nil, err
	}
	if f.Pix == nil {
		return nil, nil, errors.New("source returned no CPU pixels")
	}
	img := toRGBA(f)
	maxW := a.MaxWidth
	if maxW == 0 {
		maxW = 1280
	}
	scale := 1.0
	if maxW > 0 && img.Bounds().Dx() > maxW {
		scale = float64(maxW) / float64(img.Bounds().Dx())
		dst := image.NewRGBA(image.Rect(0, 0, maxW, int(float64(img.Bounds().Dy())*scale+0.5)))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
		img = dst
	}
	t.mu.Lock()
	t.views[a.Source] = view{bounds: src.Bounds(), scale: scale, window: src.WindowID()}
	t.mu.Unlock()

	var buf bytes.Buffer
	mime := "image/png"
	if strings.EqualFold(a.Format, "jpeg") || strings.EqualFold(a.Format, "jpg") {
		mime = "image/jpeg"
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85})
	} else {
		err = png.Encode(&buf, img)
	}
	if err != nil {
		return nil, nil, err
	}
	b := img.Bounds()
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{Data: buf.Bytes(), MIMEType: mime},
		&mcp.TextContent{Text: fmt.Sprintf("%dx%d screenshot of source %q (%dx%d on screen). Use these image coordinates for input.",
			b.Dx(), b.Dy(), a.Source, f.Width, f.Height)},
	}}, nil, nil
}

// toRGBA converts a BGRA frame to an opaque RGBA image.
func toRGBA(f *capture.Frame) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, f.Width, f.Height))
	for y := 0; y < f.Height; y++ {
		src := f.Pix[y*f.Stride : y*f.Stride+f.Width*4]
		dst := img.Pix[y*img.Stride : y*img.Stride+f.Width*4]
		for x := 0; x < f.Width*4; x += 4 {
			dst[x], dst[x+1], dst[x+2], dst[x+3] = src[x+2], src[x+1], src[x], 255
		}
	}
	return img
}

// view returns how source's screenshot coordinates map to the screen,
// opening the source briefly if it hasn't been captured yet.
func (t *tools) view(source string) (view, error) {
	t.mu.Lock()
	v, ok := t.views[source]
	t.mu.Unlock()
	if ok {
		return v, nil
	}
	src, err := t.deps.OpenSource(source)
	if err != nil {
		return view{}, fmt.Errorf("open source %q: %w", source, err)
	}
	defer src.Close()
	return view{bounds: src.Bounds(), scale: 1, window: src.WindowID()}, nil
}

// screenPoint converts screenshot coordinates to absolute screen pixels.
func (v view) screenPoint(x, y float64) (int, int, error) {
	px, py := int(x/v.scale), int(y/v.scale)
	if px < 0 || py < 0 || px >= v.bounds.Dx() || py >= v.bounds.Dy() {
		return 0, 0, fmt.Errorf("(%g, %g) is outside the screenshot", x, y)
	}
	return v.bounds.Min.X + px, v.bounds.Min.Y + py, nil
}

func (t *tools) injector() (input.Injector, error) {
	if inj := t.deps.Injector(); inj != nil {
		return inj, nil
	}
	return nil, errors.New("input injection is unavailable on this host")
}

func button(name string) (int, error) {
	switch strings.ToLower(name) {
	case "", "left":
		return input.ButtonLeft, nil
	case "middle":
		return input.ButtonMiddle, nil
	case "right":
		return input.ButtonRight, nil
	}
	return 0, fmt.Errorf("unknown button %q", name)
}

// pointer resolves a source and point, focusing the window for window
// sources, and moves the pointer there.
func (t *tools) pointer(source string, x, y float64) (input.Injector, int, int, error) {
	inj, err := t.injector()
	if err != nil {
		return nil, 0, 0, err
	}
	v, err := t.view(source)
	if err != nil {
		return nil, 0, 0, err
	}
	px, py, err := v.screenPoint(x, y)
	if err != nil {
		return nil, 0, 0, err
	}
	if v.window != 0 {
		_ = inj.Focus(v.window)
	}
	return inj, px, py, inj.MouseMove(px, py)
}

// pause gives the host's UI a moment between synthetic events, which some
// toolkits need to register a click or drag.
func pause() { time.Sleep(15 * time.Millisecond) }

func (t *tools) click(a clickArgs) (*mcp.CallToolResult, any, error) {
	b, err := button(a.Button)
	if err != nil {
		return nil, nil, err
	}
	inj, px, py, err := t.pointer(a.Source, a.X, a.Y)
	if err != nil {
		return nil, nil, err
	}
	n := max(a.Count, 1)
	for range n {
		pause()
		if err := inj.MouseButton(b, true); err != nil {
			return nil, nil, err
		}
		pause()
		if err := inj.MouseButton(b, false); err != nil {
			return nil, nil, err
		}
	}
	return text("clicked %d time(s) at screen (%d, %d)", n, px, py), nil, nil
}

func (t *tools) move(a pointArgs) (*mcp.CallToolResult, any, error) {
	_, px, py, err := t.pointer(a.Source, a.X, a.Y)
	if err != nil {
		return nil, nil, err
	}
	return text("moved to screen (%d, %d)", px, py), nil, nil
}

func (t *tools) drag(a dragArgs) (*mcp.CallToolResult, any, error) {
	b, err := button(a.Button)
	if err != nil {
		return nil, nil, err
	}
	inj, _, _, err := t.pointer(a.Source, a.X1, a.Y1)
	if err != nil {
		return nil, nil, err
	}
	v, _ := t.view(a.Source)
	x2, y2, err := v.screenPoint(a.X2, a.Y2)
	if err != nil {
		return nil, nil, err
	}
	x1, y1, _ := v.screenPoint(a.X1, a.Y1)
	pause()
	if err := inj.MouseButton(b, true); err != nil {
		return nil, nil, err
	}
	// Move in steps: many apps ignore a drag that jumps straight to the end.
	const steps = 12
	for i := 1; i <= steps; i++ {
		pause()
		inj.MouseMove(x1+(x2-x1)*i/steps, y1+(y2-y1)*i/steps)
	}
	pause()
	if err := inj.MouseButton(b, false); err != nil {
		return nil, nil, err
	}
	return text("dragged from screen (%d, %d) to (%d, %d)", x1, y1, x2, y2), nil, nil
}

func (t *tools) scroll(a scrollArgs) (*mcp.CallToolResult, any, error) {
	inj, px, py, err := t.pointer(a.Source, a.X, a.Y)
	if err != nil {
		return nil, nil, err
	}
	if err := inj.Wheel(a.DX, a.DY); err != nil {
		return nil, nil, err
	}
	return text("scrolled (%d, %d) notches at screen (%d, %d)", a.DX, a.DY, px, py), nil, nil
}

func (t *tools) typeText(a typeArgs) (*mcp.CallToolResult, any, error) {
	inj, err := t.injector()
	if err != nil {
		return nil, nil, err
	}
	for _, r := range a.Text {
		code, shift, ok := runeKey(r)
		if !ok {
			return nil, nil, fmt.Errorf("can't type %q: only US-layout ASCII is supported", r)
		}
		if err := press(inj, code, shift); err != nil {
			return nil, nil, err
		}
	}
	return text("typed %d characters", len([]rune(a.Text))), nil, nil
}

func (t *tools) key(a keyArgs) (*mcp.CallToolResult, any, error) {
	inj, err := t.injector()
	if err != nil {
		return nil, nil, err
	}
	codes, err := parseCombo(a.Keys)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range codes {
		if err := inj.Key(input.Keys[c], true); err != nil {
			return nil, nil, err
		}
		pause()
	}
	for i := len(codes) - 1; i >= 0; i-- {
		if err := inj.Key(input.Keys[codes[i]], false); err != nil {
			return nil, nil, err
		}
	}
	return text("pressed %s", a.Keys), nil, nil
}

func (t *tools) focus(a focusArgs) (*mcp.CallToolResult, any, error) {
	inj, err := t.injector()
	if err != nil {
		return nil, nil, err
	}
	id, err := capture.ParseID(a.ID)
	if err != nil {
		return nil, nil, err
	}
	if err := inj.Focus(id); err != nil {
		return nil, nil, err
	}
	return text("focused %s", a.ID), nil, nil
}

func press(inj input.Injector, code string, shift bool) error {
	if shift {
		if err := inj.Key(input.Keys["ShiftLeft"], true); err != nil {
			return err
		}
	}
	err := inj.Key(input.Keys[code], true)
	if err == nil {
		err = inj.Key(input.Keys[code], false)
	}
	if shift {
		inj.Key(input.Keys["ShiftLeft"], false)
	}
	return err
}
