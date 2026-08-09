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
