//go:build windows

package agent

import (
	"golang.org/x/sys/windows"
	"io"
	"os"
)

func dialCodexDesktopIPC() (io.ReadWriteCloser, error) {
	const pipe = `\\.\pipe\codex-ipc`
	path, err := windows.UTF16PtrFromString(pipe)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), pipe), nil
}
