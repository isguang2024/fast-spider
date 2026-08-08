package node

import (
	"fmt"
	"os"
	"path/filepath"
)

func writeAtomicFile(path string, data []byte, mode os.FileMode, expectedSHA256 string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".fast-spider-write-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode.Perm()); err != nil {
		return fmt.Errorf("apply file mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("re-read target file: %w", err)
	}
	if sha256String(current) != expectedSHA256 {
		return ErrRevisionConflict
	}
	if err := replaceFile(tmpPath, path); err != nil {
		return fmt.Errorf("replace target file: %w", err)
	}
	cleanup = false
	if err := syncParentDirectory(dir); err != nil {
		return fmt.Errorf("sync parent directory: %w", err)
	}
	return nil
}
