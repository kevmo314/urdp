package input

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestXTestMouse(t *testing.T) {
	if os.Getenv("DISPLAY") == "" {
		t.Skip("DISPLAY not set")
	}
	if _, err := exec.LookPath("xdotool"); err != nil {
		t.Skip("xdotool not installed")
	}
	inj, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer inj.Close()
	if err := inj.MouseMove(123, 77); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("xdotool", "getmouselocation").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "x:123 y:77 ") {
		t.Errorf("pointer at %q, want x:123 y:77", out)
	}
	if err := inj.Key(Keys["ShiftLeft"], true); err != nil {
		t.Fatal(err)
	}
	if err := inj.Key(Keys["ShiftLeft"], false); err != nil {
		t.Fatal(err)
	}
}
