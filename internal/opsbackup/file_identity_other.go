//go:build !windows && !linux

package opsbackup

import (
	"errors"
	"os"
)

type releaseBackupFileIdentity struct{}

func releaseBackupFileIdentityForFile(_ *os.File, _ os.FileInfo) (releaseBackupFileIdentity, error) {
	return releaseBackupFileIdentity{}, errors.New("release backup file identity is unsupported on this platform")
}
