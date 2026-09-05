//go:build !windows

package agent

import "io"

func dialCodexDesktopIPC() (io.ReadWriteCloser, error) {
	return nil, errCodexDesktopOwnerUnavailable
}
