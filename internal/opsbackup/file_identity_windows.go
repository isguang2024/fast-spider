//go:build windows

package opsbackup

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type releaseBackupFileIdentity struct {
	volumeSerial uint32
	fileIndexHi  uint32
	fileIndexLo  uint32
	creationTime int64
	changeTime   int64
}

type releaseBackupFileBasicInfo struct {
	creationTime   int64
	lastAccessTime int64
	lastWriteTime  int64
	changeTime     int64
	attributes     uint32
	_              uint32
}

func releaseBackupFileIdentityForFile(file *os.File, _ os.FileInfo) (releaseBackupFileIdentity, error) {
	handle := windows.Handle(file.Fd())
	var handleInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &handleInfo); err != nil {
		return releaseBackupFileIdentity{}, err
	}
	var basic releaseBackupFileBasicInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileBasicInfo,
		(*byte)(unsafe.Pointer(&basic)),
		uint32(unsafe.Sizeof(basic)),
	); err != nil {
		return releaseBackupFileIdentity{}, err
	}
	return releaseBackupFileIdentity{
		volumeSerial: handleInfo.VolumeSerialNumber,
		fileIndexHi:  handleInfo.FileIndexHigh,
		fileIndexLo:  handleInfo.FileIndexLow,
		creationTime: basic.creationTime,
		changeTime:   basic.changeTime,
	}, nil
}
