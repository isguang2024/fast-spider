//go:build !windows

package node

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func persistedProcessIdentityForCommand(cmd *exec.Cmd) persistedProcessIdentity {
	if cmd == nil || cmd.Process == nil {
		return persistedProcessIdentity{}
	}
	identity := persistedProcessIdentity{PID: cmd.Process.Pid, Path: cmd.Path}
	if state, birth, path, err := inspectUnixProcess(identity.PID); err == nil && state == persistedProcessRunning {
		identity.Birth = birth
		identity.Path = path
	}
	return identity
}

func inspectUnixProcess(pid int) (persistedProcessState, string, string, error) {
	if pid <= 0 {
		return persistedProcessUnknown, "", "", errors.New("invalid process id")
	}
	path, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return persistedProcessStopped, "", "", nil
		}
		return persistedProcessUnknown, "", "", err
	}
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return persistedProcessUnknown, "", "", err
	}
	closeName := strings.LastIndexByte(string(raw), ')')
	if closeName < 0 {
		return persistedProcessUnknown, "", "", errors.New("invalid process stat")
	}
	fields := strings.Fields(string(raw)[closeName+1:])
	if len(fields) <= 19 {
		return persistedProcessUnknown, "", "", errors.New("process birth time unavailable")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return persistedProcessUnknown, "", "", err
	}
	return persistedProcessRunning, strings.TrimSpace(string(boot)) + ":" + fields[19], filepath.Clean(path), nil
}

func persistedProcessStateOf(identity persistedProcessIdentity) (persistedProcessState, error) {
	state, birth, path, err := inspectUnixProcess(identity.PID)
	if state != persistedProcessRunning || err != nil {
		return state, err
	}
	want, err := filepath.Abs(identity.Path)
	if err != nil || strings.TrimSpace(identity.Birth) == "" {
		return persistedProcessUnknown, errors.New("persisted process identity is incomplete")
	}
	if birth != identity.Birth {
		return persistedProcessStopped, nil
	} // A reused PID is not the recorded job.
	if filepath.Clean(path) != filepath.Clean(want) {
		return persistedProcessUnknown, errors.New("persisted process identity does not match")
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
