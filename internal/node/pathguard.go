package node

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrPathOutsideWorkspace = errors.New("path outside workspace")

// ResolveWorkspacePath exposes the existing workspace path guard to the
// provider adapter without duplicating its symlink and traversal checks.
func ResolveWorkspacePath(root, relative string) (string, error) {
	return resolveWorkspacePath(root, relative)
}

// PathWithin exposes the existing real-path containment check to the provider
// adapter without duplicating its security logic.
func PathWithin(root, target string) bool { return pathWithin(root, target) }

func resolveWorkspacePath(root, relative string) (string, error) {
	if strings.IndexByte(relative, 0) >= 0 || filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" {
		return "", ErrPathOutsideWorkspace
	}
	clean := filepath.Clean(relative)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrPathOutsideWorkspace
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	target := filepath.Join(rootReal, clean)
	targetReal, err := filepath.EvalSymlinks(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", os.ErrNotExist
		}
		return "", err
	}
	rel, err := filepath.Rel(rootReal, targetReal)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", ErrPathOutsideWorkspace
	}
	return targetReal, nil
}
