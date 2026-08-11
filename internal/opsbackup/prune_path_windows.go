//go:build windows

package opsbackup

import (
	"os"

	"golang.org/x/sys/windows"
)

func releaseBackupPathIsReparse(path string, info os.FileInfo) (bool, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attributes, err := windows.GetFileAttributes(pathPtr)
	if err != nil {
		return false, err
	}
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}
