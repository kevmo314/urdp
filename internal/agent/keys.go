package agent

import (
	"fmt"
	"strings"

	"github.com/kevmo314/urdp/internal/input"
)

// usLayout maps unshifted and shifted ASCII punctuation to key codes.
var usLayout = map[rune]struct {
	code  string
	shift bool
}{
	' ': {"Space", false}, '\n': {"Enter", false}, '\t': {"Tab", false},
	'-': {"Minus", false}, '_': {"Minus", true}, '=': {"Equal", false}, '+': {"Equal", true},
	'[': {"BracketLeft", false}, '{': {"BracketLeft", true}, ']': {"BracketRight", false}, '}': {"BracketRight", true},
	'\\': {"Backslash", false}, '|': {"Backslash", true}, ';': {"Semicolon", false}, ':': {"Semicolon", true},
	'\'': {"Quote", false}, '"': {"Quote", true}, '`': {"Backquote", false}, '~': {"Backquote", true},
	',': {"Comma", false}, '<': {"Comma", true}, '.': {"Period", false}, '>': {"Period", true},
	'/': {"Slash", false}, '?': {"Slash", true},
	'!': {"Digit1", true}, '@': {"Digit2", true}, '#': {"Digit3", true}, '$': {"Digit4", true}, '%': {"Digit5", true},
	'^': {"Digit6", true}, '&': {"Digit7", true}, '*': {"Digit8", true}, '(': {"Digit9", true}, ')': {"Digit0", true},
}

// runeKey returns the physical key (KeyboardEvent.code) and shift state
// that type r on a US layout.
func runeKey(r rune) (code string, shift, ok bool) {
	switch {
	case r >= 'a' && r <= 'z':
		return "Key" + strings.ToUpper(string(r)), false, true
	case r >= 'A' && r <= 'Z':
		return "Key" + string(r), true, true
	case r >= '0' && r <= '9':
		return "Digit" + string(r), false, true
	}
	k, ok := usLayout[r]
	return k.code, k.shift, ok
}

var keyAliases = map[string]string{
	"ctrl": "ControlLeft", "control": "ControlLeft", "shift": "ShiftLeft", "alt": "AltLeft", "option": "AltLeft",
	"meta": "MetaLeft", "cmd": "MetaLeft", "command": "MetaLeft", "win": "MetaLeft", "super": "MetaLeft",
	"enter": "Enter", "return": "Enter", "tab": "Tab", "esc": "Escape", "escape": "Escape", "space": "Space",
	"backspace": "Backspace", "delete": "Delete", "del": "Delete", "insert": "Insert",
	"up": "ArrowUp", "down": "ArrowDown", "left": "ArrowLeft", "right": "ArrowRight",
	"home": "Home", "end": "End", "pageup": "PageUp", "pagedown": "PageDown",
	"capslock": "CapsLock", "printscreen": "PrintScreen", "menu": "ContextMenu",
}

// parseCombo turns "ctrl+shift+t" into key codes, modifiers first.
// "ctrl++" means control and the plus key.
func parseCombo(s string) ([]string, error) {
	var tail []string
	if strings.HasSuffix(s, "++") {
		s, tail = strings.TrimSuffix(s, "++"), []string{"Equal"}
	} else if s == "+" {
		return []string{"Equal"}, nil
	}
	var codes []string
	for _, part := range strings.Split(s, "+") {
		code, err := keyCode(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return append(codes, tail...), nil
}

func keyCode(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty key name")
	}
	if _, ok := input.Keys[name]; ok { // already a KeyboardEvent.code
		return name, nil
	}
	lower := strings.ToLower(name)
	if c, ok := keyAliases[lower]; ok {
		return c, nil
	}
	if len(lower) >= 2 && lower[0] == 'f' {
		if _, ok := input.Keys["F"+lower[1:]]; ok {
			return "F" + lower[1:], nil
		}
	}
	if r := []rune(name); len(r) == 1 {
		if code, _, ok := runeKey([]rune(lower)[0]); ok {
			return code, nil
		}
	}
	return "", fmt.Errorf("unknown key %q", name)
}
