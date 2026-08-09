package node

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var ErrAbsolutePathRequired = errors.New("absolute path is required")

// ResolveMachinePath resolves an absolute path through the local filesystem.
// In personal-use mode, the operating-system account running Fast Spider Node
// is the filesystem permission boundary.
func ResolveMachinePath(path string) (string, error) {
	path = normalizeMachinePathInput(strings.TrimSpace(path))
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) {
		return "", ErrAbsolutePathRequired
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", os.ErrNotExist
		}
		return "", err
	}
	return filepath.Clean(real), nil
}

func samePath(a, b string) bool {
	ca, errA := filepath.Abs(a)
	cb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	if filepath.Separator == '\\' {
		return strings.EqualFold(filepath.Clean(ca), filepath.Clean(cb))
	}
	return filepath.Clean(ca) == filepath.Clean(cb)
}
