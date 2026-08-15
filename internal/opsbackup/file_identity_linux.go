//go:build linux

package opsbackup

import (
	"errors"
	"os"
	"syscall"
)

type releaseBackupFileIdentity struct {
	device     uint64
	inode      uint64
	changeSec  int64
	changeNano int64
}

func releaseBackupFileIdentityForFile(_ *os.File, info os.FileInfo) (releaseBackupFileIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return releaseBackupFileIdentity{}, errors.New("release backup file identity is unavailable")
	}
	return releaseBackupFileIdentity{
		device:     uint64(stat.Dev),
		inode:      stat.Ino,
		changeSec:  stat.Ctim.Sec,
		changeNano: stat.Ctim.Nsec,
	}, nil
}
