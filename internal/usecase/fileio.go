package usecase

import (
	"os"
	"path/filepath"
)

// writeFile is a thin wrapper kept in its own file so the runner's core
// logic stays free of low-level I/O concerns (and easy to point at an
// in-memory fs in tests, if ever needed). It creates parent directories as
// needed, since screenshot paths are nested under a configurable directory.
func writeFile(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}
