//go:build !windows

package nodeupdate

import "errors"

func StartApply(stagedPath, targetPath, dataDir string, background bool) error {
	return errors.New("self update apply is currently supported on Windows only")
}

func ApplyHelper(targetPath, dataDir string, oldPID int, background bool) error {
	return errors.New("self update apply is currently supported on Windows only")
}
