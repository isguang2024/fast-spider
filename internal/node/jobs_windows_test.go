//go:build windows

package node

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
	job, err := client.shellRun(context.Background(), map[string]any{
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
