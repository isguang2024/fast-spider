//go:build !windows

package opsbackup

import "os"

func releaseBackupPathIsReparse(_ string, info os.FileInfo) (bool, error) {
	return info != nil && info.Mode()&os.ModeSymlink != 0, nil
}
