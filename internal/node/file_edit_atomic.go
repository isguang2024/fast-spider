package node

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func writeAtomicEditedFile(path string, data []byte, mode os.FileMode, expectedSHA string) error {
	return writePreparedEdit(path, data, mode, func(temp string) error {
		currentSHA, err := boundedFileSHA256(path, maxEditableFileBytes)
		if err != nil {
			return fmt.Errorf("re-read target file: %w", err)
		}
		if currentSHA != expectedSHA {
			return &FileRevisionError{Path: filepath.Clean(path), Expected: expectedSHA, Actual: currentSHA}
		}
		return atomicReplaceEditedFile(temp, path)
	})
}

func boundedFileSHA256(path string, limit int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", ErrNotRegularFile
	}
	if info.Size() > limit {
		return "", ErrRevisionConflict
	}
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(file, limit+1))
	if err != nil {
		return "", err
	}
	if written > limit {
		return "", ErrRevisionConflict
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
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
