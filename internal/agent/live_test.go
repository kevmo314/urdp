package agent

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestLive drives a running urdp's /mcp endpoint (URDP_MCP_URL) on an X
// display: it screenshots, clicks into a window and types, then checks
// the pointer position with xdotool.
func TestLive(t *testing.T) {
	url := os.Getenv("URDP_MCP_URL")
	if url == "" {
		t.Skip("URDP_MCP_URL not set")
	}
	ctx := context.Background()
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "live"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "screenshot", Arguments: map[string]any{"max_width": 960}})
	if err != nil || res.IsError {
		t.Fatalf("screenshot: %v %+v", err, res)
	}
	t.Log(res.Content[1].(*mcp.TextContent).Text)
	// (480, 270) in a 960-wide shot of a 1920x1080 desktop is (960, 540).
	if res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: "move", Arguments: map[string]any{"x": 480, "y": 270}}); err != nil || res.IsError {
		t.Fatalf("move: %v %+v", err, res)
	}
	out, _ := exec.Command("xdotool", "getmouselocation").Output()
	if !strings.HasPrefix(string(out), "x:960 y:540 ") {
		t.Errorf("pointer at %q, want x:960 y:540", out)
	}
}
