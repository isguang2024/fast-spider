package node

import (
	"fmt"
	"os"
	"path/filepath"
)

func writeAtomicEditedFile(path string, data []byte, mode os.FileMode, expectedSHA string) error {
	return writePreparedEdit(path, data, mode, func(temp string) error {
		current, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("re-read target file: %w", err)
		}
		if sha256String(current) != expectedSHA {
			return ErrRevisionConflict
		}
		return atomicReplaceEditedFile(temp, path)
	})
}

func writeAtomicCreatedFile(path string, data []byte, mode os.FileMode) error {
	return writePreparedEdit(path, data, mode, func(temp string) error {
		if _, err := os.Lstat(path); err == nil {
			return ErrFileAlreadyExists
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := atomicCreateEditedFile(temp, path); err != nil {
			if _, statErr := os.Lstat(path); statErr == nil {
				return ErrFileAlreadyExists
			}
			return err
		}
		return nil
	})
}

func writePreparedEdit(path string, data []byte, mode os.FileMode, install func(string) error) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".fast-spider-edit-*")
	if err != nil {
		return fmt.Errorf("create edit temporary file: %w", err)
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
		return fmt.Errorf("write edit temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync edit temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close edit temporary file: %w", err)
	}
	if err := install(tmpPath); err != nil {
		return err
	}
	cleanup = false
	if err := syncEditedParent(dir); err != nil {
		return fmt.Errorf("sync parent directory: %w", err)
	}
	return nil
}
