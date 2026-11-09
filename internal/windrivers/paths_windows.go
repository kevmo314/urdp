package windrivers

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Directory uses the system known folder, never an environment-controlled
// ProgramFiles path that an elevated process could be tricked into writing to.
func Directory() (string, error) {
	root, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "urdp", "drivers"), nil
}
