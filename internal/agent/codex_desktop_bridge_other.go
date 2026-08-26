//go:build !windows

package agent

import (
	"errors"
	"io"
)

func dialCodexDesktopIPC() (io.ReadWriteCloser, error) {
	return nil, errors.New("Codex Desktop IPC bridge is only available on Windows")
}
