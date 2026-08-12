//go:build windows

package node

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

// WSL is intentionally kept alive lazily: Fast Spider does not start WSL on
// Node startup, but once a shell/build job actually enters a WSL distribution
// we keep one tiny process in that distribution. Subsequent jobs reuse the
// already-running WSL VM instead of letting it stop between commands.
var processWSLKeepAlive = &wslKeepAliveManager{processes: map[string]*exec.Cmd{}}

type wslKeepAliveManager struct {
	mu        sync.Mutex
	processes map[string]*exec.Cmd
}

type wslKeepAliveSpec struct {
	executable   string
	distribution string
	key          string
}

func maybeEnsureWSLKeepAlive(argv []string) error {
	spec, ok := parseWSLKeepAliveSpec(argv)
	if !ok {
		return nil
	}
	return processWSLKeepAlive.ensure(spec)
}

func parseWSLKeepAliveSpec(argv []string) (wslKeepAliveSpec, bool) {
	if len(argv) == 0 {
		return wslKeepAliveSpec{}, false
	}
	executable := strings.TrimSpace(argv[0])
	base := strings.ToLower(filepath.Base(executable))
	if base != "wsl.exe" && base != "wsl" {
		return wslKeepAliveSpec{}, false
	}

	var distribution string
	for index := 1; index < len(argv); index++ {
		arg := strings.TrimSpace(argv[index])
		lower := strings.ToLower(arg)
		switch lower {
		case "--shutdown", "--terminate", "-t", "--unregister", "--install", "--update",
			"--list", "-l", "--status", "--version", "--set-default-version", "--set-version",
			"--set-default", "-s", "--mount", "--unmount", "--import", "--export":
			return wslKeepAliveSpec{}, false
		case "-d", "--distribution":
			if index+1 < len(argv) {
				distribution = strings.TrimSpace(argv[index+1])
				index++
			}
		default:
			if strings.HasPrefix(lower, "--distribution=") {
				distribution = strings.TrimSpace(arg[len("--distribution="):])
			}
		}
	}

	key := strings.ToLower(distribution)
	if key == "" {
		key = "<default>"
	}
	return wslKeepAliveSpec{executable: executable, distribution: distribution, key: key}, true
}

func (m *wslKeepAliveManager) ensure(spec wslKeepAliveSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current := m.processes[spec.key]; current != nil && current.Process != nil && current.ProcessState == nil {
		return nil
	}

	args := make([]string, 0, 8)
	if spec.distribution != "" {
		args = append(args, "--distribution", spec.distribution)
	}
	args = append(args, "--exec", "/bin/sh", "-c", "while :; do sleep 3600; done")
	cmd := exec.Command(spec.executable, args...)
	cmd.Env = safeShellEnvironment()
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start WSL keepalive for %s: %w", spec.key, err)
	}
	m.processes[spec.key] = cmd
	go m.reap(spec.key, cmd)
	return nil
}

func (m *wslKeepAliveManager) reap(key string, cmd *exec.Cmd) {
	_ = cmd.Wait()
	m.mu.Lock()
	if m.processes[key] == cmd {
		delete(m.processes, key)
	}
	m.mu.Unlock()
}
