package main

import (
	"errors"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mockCommands(calls map[string]int, fail bool) []installCommand {
	var cmds []installCommand
	for _, c := range installCommands() {
		name := c.name
		c.run = func() error {
			calls[name]++
			if fail {
				return errors.New("boom")
			}
			return nil
		}
		cmds = append(cmds, c)
	}
	return cmds
}

func parse(t *testing.T, args []string, fail bool) (map[string]int, []int) {
	t.Helper()
	calls := map[string]int{}
	var exits []int
	fs := flag.NewFlagSet("urdp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.String("addr", ":8080", "")
	fs.Bool("usb", false, "")
	registerInstallFlags(fs, mockCommands(calls, fail), func(code int) { exits = append(exits, code) })
	_ = fs.Parse(args)
	return calls, exits
}

func TestNoInstallWithoutFlag(t *testing.T) {
	for _, args := range [][]string{nil, {"-addr", ":9000"}, {"-usb"}, {"-addr=:1", "install-camera"}} {
		calls, exits := parse(t, args, false)
		if len(calls) != 0 || len(exits) != 0 {
			t.Errorf("%v: ran %v, exits %v", args, calls, exits)
		}
	}
}

func TestInstallFlagRunsOnlyThatCommand(t *testing.T) {
	for _, name := range []string{"install-camera", "uninstall-camera", "install-audio-driver"} {
		calls, exits := parse(t, []string{"-" + name}, false)
		if len(calls) != 1 || calls[name] != 1 || len(exits) != 1 || exits[0] != 0 {
			t.Errorf("-%s: ran %v, exits %v", name, calls, exits)
		}
	}
	calls, exits := parse(t, []string{"-install-camera"}, true)
	if calls["install-camera"] != 1 || len(exits) == 0 || exits[0] != 1 {
		t.Errorf("failing install: ran %v, exits %v", calls, exits)
	}
}

// TestInstallersOnlyCalledFromFlags guards against an install creeping into
// the server: the vdev install/uninstall entry points may only be referenced
// by installdriver.go.
func TestInstallersOnlyCalledFromFlags(t *testing.T) {
	guarded := map[string]bool{"InstallCamera": true, "UninstallCamera": true, "InstallAudioDriver": true}
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && (info.Name() == "third_party" || info.Name() == "bin" || strings.HasPrefix(info.Name(), ".") && path != ".") {
			return filepath.SkipDir
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || path == "installdriver.go" {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && guarded[sel.Sel.Name] {
				t.Errorf("%s references %s; installs must only happen via installdriver.go flags", path, sel.Sel.Name)
			}
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && guarded[id.Name] {
					t.Errorf("%s calls %s; installs must only happen via installdriver.go flags", path, id.Name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
