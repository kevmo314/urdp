package windrivers

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

//go:embed packages.sha256.json
var packageManifest []byte

// This checks the exact released package bytes after extraction, including
// signed catalogs, and that a repeat extraction leaves existing files alone.
func TestExtractReleasedPackages(t *testing.T) {
	root := t.TempDir()
	if err := extract(root); err != nil {
		t.Fatal(err)
	}
	var expected map[string]string
	if err := json.Unmarshal(packageManifest, &expected); err != nil {
		t.Fatal(err)
	}
	for name, want := range expected {
		path := filepath.Join(root, filepath.FromSlash(name))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != want {
			t.Errorf("released package hash mismatch for %s", name)
		}
		before, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = extract(root); err != nil {
			t.Fatal(err)
		}
		after, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !before.ModTime().Equal(after.ModTime()) {
			t.Errorf("repeat extraction rewrote %s", name)
		}
	}
}

func zipFiles(t *testing.T) map[string][]byte {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(packages), int64(len(packages)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name] = data
	}
	return files
}

// Every embedded file is listed in the manifest and nothing else is shipped.
func TestPackagesMatchManifestExactly(t *testing.T) {
	var expected map[string]string
	if err := json.Unmarshal(packageManifest, &expected); err != nil {
		t.Fatal(err)
	}
	files := zipFiles(t)
	if len(files) != len(expected) {
		t.Errorf("zip has %d files, manifest %d", len(files), len(expected))
	}
	for name, data := range files {
		sum := sha256.Sum256(data)
		if expected[name] != hex.EncodeToString(sum[:]) {
			t.Errorf("%s not in manifest or hash differs", name)
		}
	}
}

func infText(data []byte) string {
	if len(data) >= 2 && data[0] == 0xff && data[1] == 0xfe {
		u := make([]uint16, (len(data)-2)/2)
		for i := range u {
			u[i] = uint16(data[2+2*i]) | uint16(data[3+2*i])<<8
		}
		return string(utf16.Decode(u))
	}
	return strings.TrimPrefix(string(data), "\ufeff")
}

// infValue returns the first "key = value" in [section], comments stripped.
func infValue(text, section, key string) string {
	in := false
	for _, line := range strings.Split(text, "\n") {
		if i := strings.IndexByte(line, ';'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			in = strings.EqualFold(strings.Trim(line, "[]"), section)
			continue
		}
		if k, v, ok := strings.Cut(line, "="); in && ok && strings.EqualFold(strings.TrimSpace(k), key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// The driver table must agree with the INFs it installs: the hardware ID we
// create a root device for is one the INF matches, and each INF's catalog
// and binaries are present (case-insensitively, as Windows resolves them).
func TestDriverTableMatchesINFs(t *testing.T) {
	files := zipFiles(t)
	lower := map[string]bool{}
	for name := range files {
		lower[strings.ToLower(name)] = true
	}
	for _, d := range drivers {
		infs := []string{d.inf}
		if d.filter != "" {
			infs = append(infs, d.filter)
		}
		for _, inf := range infs {
			data, ok := files[d.folder+"/"+inf]
			if !ok {
				t.Fatalf("%s/%s missing", d.folder, inf)
			}
			text := infText(data)
			cat := infValue(text, "Version", "CatalogFile")
			if cat == "" || !lower[strings.ToLower(d.folder+"/"+cat)] {
				t.Errorf("%s: catalog %q missing", inf, cat)
			}
			if !strings.Contains(strings.ToLower(text), ".sys") {
				t.Errorf("%s: no driver binary", inf)
			}
			for _, line := range strings.Split(text, "\n") {
				if f, _, ok := strings.Cut(strings.TrimSpace(line), "="); ok && strings.HasSuffix(strings.ToLower(strings.TrimSpace(f)), ".sys") {
					if !lower[strings.ToLower(d.folder+"/"+strings.TrimSpace(f))] {
						t.Errorf("%s: %s missing", inf, f)
					}
				}
			}
		}
		if !strings.Contains(strings.ToLower(infText(files[d.folder+"/"+d.inf])), strings.ToLower(", "+d.hardware)) {
			t.Errorf("%s does not match hardware ID %s", d.inf, d.hardware)
		}
	}
	// The filter is an extension INF bound to USB 3 root hubs; document-worthy.
	if c := infValue(infText(files["usbip-0.9.8.1/usbip2_filter.inf"]), "Version", "Class"); c != "Extension" {
		t.Errorf("usbip2_filter.inf class %q, want Extension", c)
	}
}

func TestParseActionNeverInstallsByDefault(t *testing.T) {
	cases := []struct {
		install, uninstall bool
		log                string
		want               Action
		err                bool
	}{
		{false, false, "", None, false},
		{true, false, "", Install, false},
		{false, true, "", Uninstall, false},
		{true, true, "", None, true},
		{false, false, `C:\x.log`, None, true},
		{true, false, `C:\x.log`, Install, false},
	}
	for _, c := range cases {
		got, err := ParseAction(c.install, c.uninstall, c.log)
		if got != c.want || (err != nil) != c.err {
			t.Errorf("ParseAction(%v,%v,%q) = %v,%v", c.install, c.uninstall, c.log, got, err)
		}
	}
}
