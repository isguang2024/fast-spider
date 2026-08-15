//go:build linux

package server

import (
	"errors"
	"os"
	"syscall"
)

type releaseFileIdentity struct {
	device     uint64
	inode      uint64
	changeSec  int64
	changeNano int64
}

func releaseFileIdentityForFile(_ *os.File, info os.FileInfo) (releaseFileIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return releaseFileIdentity{}, errors.New("release file identity is unavailable")
	}
	return releaseFileIdentity{
		device:     uint64(stat.Dev),
		inode:      stat.Ino,
		changeSec:  stat.Ctim.Sec,
		changeNano: stat.Ctim.Nsec,
	}, nil
}
