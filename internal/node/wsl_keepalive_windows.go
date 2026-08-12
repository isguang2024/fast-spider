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
var processWSLKeepAlive = newWSLKeepAliveManager()

type wslKeepAliveProcess interface {
	Running() bool
	Kill() error
	Wait() error
}

type execWSLKeepAliveProcess struct{ cmd *exec.Cmd }

func (p *execWSLKeepAliveProcess) Running() bool {
	return p != nil && p.cmd != nil && p.cmd.Process != nil && p.cmd.ProcessState == nil
}
func (p *execWSLKeepAliveProcess) Kill() error { return p.cmd.Process.Kill() }
func (p *execWSLKeepAliveProcess) Wait() error { return p.cmd.Wait() }

type wslKeepAliveManager struct {
	mu        sync.Mutex
	processes map[string]wslKeepAliveProcess
	start     func(wslKeepAliveSpec) (wslKeepAliveProcess, error)
}

const maxWSLKeepAliveDistributions = 8

type wslKeepAliveSpec struct {
	executable   string
	distribution string
	key          string
}

func newWSLKeepAliveManager() *wslKeepAliveManager {
	manager := &wslKeepAliveManager{processes: map[string]wslKeepAliveProcess{}}
	manager.start = startWSLKeepAliveProcess
	return manager
}

func startWSLKeepAliveProcess(spec wslKeepAliveSpec) (wslKeepAliveProcess, error) {
	args := make([]string, 0, 8)
	if spec.distribution != "" {
		args = append(args, "--distribution", spec.distribution)
	}
	args = append(args, "--exec", "/bin/sh", "-c", "while :; do sleep 3600; done")
	cmd := exec.Command(spec.executable, args...)
	cmd.Env = safeShellEnvironment()
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start WSL keepalive for %s: %w", spec.key, err)
	}
	return &execWSLKeepAliveProcess{cmd: cmd}, nil
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
parseArgs:
	for index := 1; index < len(argv); index++ {
		arg := strings.TrimSpace(argv[index])
		lower := strings.ToLower(arg)
		switch lower {
		case "--":
			break parseArgs
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
	if current := m.processes[spec.key]; current != nil && current.Running() {
		return nil
	}
	delete(m.processes, spec.key)
	if len(m.processes) >= maxWSLKeepAliveDistributions {
		return ErrJobLimit
	}
	process, err := m.start(spec)
	if err != nil {
		return err
	}
	m.processes[spec.key] = process
	go m.reap(spec.key, process)
	return nil
}

func (m *wslKeepAliveManager) stopAll() {
	m.mu.Lock()
	commands := make([]wslKeepAliveProcess, 0, len(m.processes))
	for _, command := range m.processes {
		commands = append(commands, command)
	}
	m.processes = map[string]wslKeepAliveProcess{}
	m.mu.Unlock()
	for _, command := range commands {
		if command != nil && command.Running() {
			_ = command.Kill()
		}
	}
}

func stopWSLKeepAlives() { processWSLKeepAlive.stopAll() }

func (m *wslKeepAliveManager) reap(key string, process wslKeepAliveProcess) {
	_ = process.Wait()
	m.mu.Lock()
	if m.processes[key] == process {
		delete(m.processes, key)
	}
	m.mu.Unlock()
}
