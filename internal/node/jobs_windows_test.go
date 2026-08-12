//go:build windows

package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeWSLKeepAliveProcess struct {
	mu      sync.Mutex
	running bool
	done    chan struct{}
}

func newFakeWSLKeepAliveProcess() *fakeWSLKeepAliveProcess {
	return &fakeWSLKeepAliveProcess{running: true, done: make(chan struct{})}
}
func (p *fakeWSLKeepAliveProcess) Running() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.running }
func (p *fakeWSLKeepAliveProcess) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		p.running = false
		close(p.done)
	}
	return nil
}
func (p *fakeWSLKeepAliveProcess) Wait() error { <-p.done; return nil }

func TestDecodeWindowsCodePage936(t *testing.T) {
	text, err := decodeWindowsCodePage([]byte{0xb2, 0xe2, 0xca, 0xd4}, 936)
	if err != nil {
		t.Fatal(err)
	}
	if text != "测试" {
		t.Fatalf("decoded text=%q, want 测试", text)
	}
}

func TestWindowsShellRunAcceptsBareDriveRootCwd(t *testing.T) {
	client, err := New(Config{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.jobs.CancelAll(context.Background()) }()
	volume := filepath.VolumeName(t.TempDir())
	if len(volume) != 2 || volume[1] != ':' {
		t.Fatalf("unexpected Windows temp volume %q", volume)
	}
	job, err := client.shellRun(context.Background(), "", "", map[string]any{
		"cwd":            volume,
		"argv":           []string{"cmd.exe", "/d", "/s", "/c", "echo", "FAST_SPIDER_DRIVE_ROOT_OK"},
		"timeoutSeconds": 10,
		"idempotencyKey": "idem_drive_root_001",
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitJobTerminal(t, client.jobs, job.JobID, 10*time.Second)
	if final.State != "completed" || final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("drive-root cwd job=%+v", final)
	}
}

func TestWindowsCmdStructuredArgvCreatesDirectory(t *testing.T) {
	jobs := NewJobManager()
	defer func() { _ = jobs.CancelAll(context.Background()) }()
	root := t.TempDir()
	target := filepath.Join(root, "round3")
	job, err := jobs.StartShell(root, []string{"cmd.exe", "/d", "/s", "/c", "mkdir", target}, 10*time.Second, "idem_windows_mkdir_001")
	if err != nil {
		t.Fatal(err)
	}
	final := waitJobTerminal(t, jobs, job.JobID, 10*time.Second)
	if final.State != "completed" || final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("cmd mkdir job=%+v", final)
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("created directory stat=%+v err=%v", info, err)
	}
}

func TestParseWSLKeepAliveSpecUsesRequestedDistribution(t *testing.T) {
	spec, ok := parseWSLKeepAliveSpec([]string{
		`C:\Windows\System32\wsl.exe`, "--distribution", "Ubuntu-24.04", "--", "bash", "-lc", "docker ps",
	})
	if !ok {
		t.Fatal("WSL execution should enable keepalive")
	}
	if spec.distribution != "Ubuntu-24.04" || spec.key != "ubuntu-24.04" {
		t.Fatalf("unexpected WSL keepalive spec: %+v", spec)
	}
}

func TestParseWSLKeepAliveSpecUsesDefaultDistribution(t *testing.T) {
	spec, ok := parseWSLKeepAliveSpec([]string{"wsl.exe", "--exec", "/bin/sh", "-lc", "docker ps"})
	if !ok || spec.key != "<default>" || spec.distribution != "" {
		t.Fatalf("unexpected default WSL keepalive spec: %+v ok=%v", spec, ok)
	}
}

func TestParseWSLKeepAliveSpecSkipsManagementCommands(t *testing.T) {
	for _, argv := range [][]string{
		{"wsl.exe", "--status"},
		{"wsl.exe", "--shutdown"},
		{"wsl.exe", "--terminate", "Ubuntu-24.04"},
		{"wsl.exe", "--list", "--running"},
		{"cmd.exe", "/c", "echo", "wsl"},
	} {
		if spec, ok := parseWSLKeepAliveSpec(argv); ok {
			t.Fatalf("management/non-WSL command unexpectedly enabled keepalive: argv=%v spec=%+v", argv, spec)
		}
	}
}

func TestWSLKeepAliveManagerReusesCapsAndStopsOnlyOwnedProcesses(t *testing.T) {
	manager := newWSLKeepAliveManager()
	started := make([]*fakeWSLKeepAliveProcess, 0, maxWSLKeepAliveDistributions)
	manager.start = func(spec wslKeepAliveSpec) (wslKeepAliveProcess, error) {
		if spec.executable != "wsl.exe" || spec.key == "" {
			return nil, errors.New("unexpected keepalive spec")
		}
		process := newFakeWSLKeepAliveProcess()
		started = append(started, process)
		return process, nil
	}
	first := wslKeepAliveSpec{executable: "wsl.exe", distribution: "Ubuntu-0", key: "ubuntu-0"}
	if err := manager.ensure(first); err != nil {
		t.Fatal(err)
	}
	if err := manager.ensure(first); err != nil || len(started) != 1 {
		t.Fatalf("reuse err=%v starts=%d", err, len(started))
	}
	for index := 1; index < maxWSLKeepAliveDistributions; index++ {
		spec := wslKeepAliveSpec{executable: "wsl.exe", distribution: fmt.Sprintf("Ubuntu-%d", index), key: fmt.Sprintf("ubuntu-%d", index)}
		if err := manager.ensure(spec); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.ensure(wslKeepAliveSpec{executable: "wsl.exe", distribution: "Overflow", key: "overflow"}); !errors.Is(err, ErrJobLimit) {
		t.Fatalf("ninth distribution error=%v", err)
	}
	manager.stopAll()
	if len(started) != maxWSLKeepAliveDistributions {
		t.Fatalf("started=%d", len(started))
	}
	for index, process := range started {
		if process.Running() {
			t.Fatalf("owned keepalive %d still running after stopAll", index)
		}
	}
}
