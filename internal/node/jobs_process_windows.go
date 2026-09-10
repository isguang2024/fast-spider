//go:build windows

package node

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func persistedProcessIdentityForCommand(cmd *exec.Cmd) persistedProcessIdentity {
	if cmd == nil || cmd.Process == nil {
		return persistedProcessIdentity{}
	}
	identity := persistedProcessIdentity{PID: cmd.Process.Pid, Path: cmd.Path}
	if state, birth, path, err := inspectWindowsProcess(identity.PID); err == nil && state == persistedProcessRunning {
		identity.Birth = birth
		identity.Path = path
	}
	return identity
}

func inspectWindowsProcess(pid int) (persistedProcessState, string, string, error) {
	if pid <= 0 {
		return persistedProcessUnknown, "", "", errors.New("invalid process id")
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_FOUND) {
			return persistedProcessStopped, "", "", nil
		}
		return persistedProcessUnknown, "", "", err
	}
	defer windows.CloseHandle(h)
	buffer := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(h, 0, &buffer[0], &size); err != nil {
		return persistedProcessUnknown, "", "", err
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return persistedProcessUnknown, "", "", err
	}
	if exited.HighDateTime != 0 || exited.LowDateTime != 0 {
		return persistedProcessStopped, "", "", nil
	}
	birth := fmt.Sprintf("%d", uint64(created.HighDateTime)<<32|uint64(created.LowDateTime))
	return persistedProcessRunning, birth, filepath.Clean(windows.UTF16ToString(buffer[:size])), nil
}

func persistedProcessStateOf(identity persistedProcessIdentity) (persistedProcessState, error) {
	state, birth, path, err := inspectWindowsProcess(identity.PID)
	if state != persistedProcessRunning || err != nil {
		return state, err
	}
	want, err := filepath.Abs(identity.Path)
	if err != nil || strings.TrimSpace(identity.Birth) == "" {
		return persistedProcessUnknown, errors.New("persisted process identity is incomplete")
	}
	if birth != identity.Birth {
		return persistedProcessStopped, nil
	} // The PID now belongs to a different process; never terminate it.
	if !strings.EqualFold(filepath.Clean(path), filepath.Clean(want)) {
		return persistedProcessUnknown, fmt.Errorf("persisted process identity does not match path=%q want=%q birth=%q wantBirth=%q", path, want, birth, identity.Birth)
	}
	return persistedProcessRunning, nil
}

func stopPersistedProcess(identity persistedProcessIdentity) error {
	state, err := persistedProcessStateOf(identity)
	if state != persistedProcessRunning {
		if err == nil {
			err = errors.New("persisted process is not safely addressable")
		}
		return err
	}
	return killProcessTree(&exec.Cmd{Process: &os.Process{Pid: identity.PID}})
}
