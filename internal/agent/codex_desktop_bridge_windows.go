//go:build windows

package agent

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/windows"
)

const codexDesktopIPCPipe = `\\.\pipe\codex-ipc`

func dialCodexDesktopIPC() (io.ReadWriteCloser, error) {
	path, err := windows.UTF16PtrFromString(codexDesktopIPCPipe)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OVERLAPPED,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), codexDesktopIPCPipe)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("create Codex Desktop IPC file")
	}
	return file, nil
}
