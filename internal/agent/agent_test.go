package agent

import (
	"context"
	"fmt"
	"image"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kevmo314/urdp/internal/capture"
	"github.com/kevmo314/urdp/internal/input"
)

type recorder struct{ calls []string }

func (r *recorder) MouseMove(x, y int) error {
	r.calls = append(r.calls, fmt.Sprintf("move %d,%d", x, y))
	return nil
}
func (r *recorder) MouseButton(b int, d bool) error {
	r.calls = append(r.calls, fmt.Sprintf("button %d %v", b, d))
	return nil
}
func (r *recorder) Wheel(dx, dy int) error {
	r.calls = append(r.calls, fmt.Sprintf("wheel %d,%d", dx, dy))
	return nil
}
func (r *recorder) Key(k input.Key, d bool) error {
	r.calls = append(r.calls, fmt.Sprintf("key %#x %v", k.Keysym, d))
	return nil
}
func (r *recorder) Focus(id uint64) error {
	r.calls = append(r.calls, fmt.Sprintf("focus %#x", id))
	return nil
}
func (r *recorder) Close() error { return nil }

// offsetSource is a 2560x1440 test pattern placed at (100, 50) on screen.
type offsetSource struct{ *capture.TestSource }

func (offsetSource) Bounds() image.Rectangle { return image.Rect(100, 50, 2660, 1490) }

func TestTools(t *testing.T) {
	rec := &recorder{}
	h := Handler(Deps{
		OpenSource: func(string) (capture.Source, error) { return offsetSource{capture.NewTestSource(2560, 1440)}, nil },
		ListWindows: func() ([]capture.Window, error) {
			return []capture.Window{{ID: 0x42, Title: "Editor", App: "code"}}, nil
		},
		ListDisplays: func() ([]capture.Display, error) {
			return []capture.Display{{Index: 1, Width: 2560, Height: 1440}}, nil
		},
		Injector: func() input.Injector { return rec },
	})
	ts := httptest.NewServer(h)
	defer ts.Close()

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.IsError {
			t.Fatalf("%s: %v", name, res.Content[0].(*mcp.TextContent).Text)
		}
		return res
	}

	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	for _, want := range []string{"list_sources", "screenshot", "click", "move", "drag", "scroll", "type_text", "key", "focus_window"} {
		if !slices.Contains(names, want) {
			t.Errorf("missing tool %s (have %v)", want, names)
		}
	}

	// A 2560-wide source is downscaled to 1280, so screenshot (640, 360)
	// is source (1280, 720), i.e. screen (1380, 770).
	res := call("screenshot", map[string]any{})
	img, ok := res.Content[0].(*mcp.ImageContent)
	if !ok || img.MIMEType != "image/png" || len(img.Data) == 0 {
		t.Fatalf("screenshot content %#v", res.Content[0])
	}
	call("click", map[string]any{"x": 640, "y": 360, "button": "right"})
	if want := []string{"move 1380,770", "button 2 true", "button 2 false"}; !slices.Equal(rec.calls, want) {
		t.Errorf("click calls %q, want %q", rec.calls, want)
	}

	rec.calls = nil
	call("key", map[string]any{"keys": "ctrl+shift+t"})
	if want := []string{"key 0xffe3 true", "key 0xffe1 true", "key 0x74 true", "key 0x74 false", "key 0xffe1 false", "key 0xffe3 false"}; !slices.Equal(rec.calls, want) {
		t.Errorf("key calls %q, want %q", rec.calls, want)
	}

	rec.calls = nil
	call("type_text", map[string]any{"text": "Hi!"})
	if want := []string{
		"key 0xffe1 true", "key 0x68 true", "key 0x68 false", "key 0xffe1 false", // H
		"key 0x69 true", "key 0x69 false", // i
		"key 0xffe1 true", "key 0x31 true", "key 0x31 false", "key 0xffe1 false", // !
	}; !slices.Equal(rec.calls, want) {
		t.Errorf("type calls %q", rec.calls)
	}

	res, _ = sess.CallTool(ctx, &mcp.CallToolParams{Name: "click", Arguments: map[string]any{"x": 5000, "y": 10}})
	if !res.IsError {
		t.Error("click outside the screenshot should fail")
	}
	if txt := call("list_sources", nil).Content[0].(*mcp.TextContent).Text; !strings.Contains(txt, "0x42") || !strings.Contains(txt, "display1") {
		t.Errorf("list_sources: %s", txt)
	}
}

func TestParseCombo(t *testing.T) {
	cases := map[string][]string{
		"Enter":     {"Enter"},
		"ctrl+c":    {"ControlLeft", "KeyC"},
		"alt+F4":    {"AltLeft", "F4"},
		"cmd+space": {"MetaLeft", "Space"},
		"ctrl++":    {"ControlLeft", "Equal"},
		"ArrowUp":   {"ArrowUp"},
	}
	for in, want := range cases {
		got, err := parseCombo(in)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("parseCombo(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseCombo("ctrl+nope"); err == nil {
		t.Error("expected error for unknown key")
	}
}
