package input

import "testing"

func TestMacKeymapCoverage(t *testing.T) {
	for code, key := range Keys {
		want, ok := macKeycodes[code]
		if !ok {
			t.Errorf("missing macOS mapping for %s", code)
			continue
		}
		if got, ok := macKeys[key]; !ok || got != want {
			t.Errorf("key lookup for %s = %d, %v; want %d", code, got, ok, want)
		}
	}
	if macKeycodes["KeyA"] != 0 || macKeycodes["MetaLeft"] != 0x37 || macKeycodes["ControlLeft"] != 0x3b {
		t.Fatal("incorrect ANSI or modifier keycode")
	}
}
