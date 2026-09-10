package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJobManagerPersistsTerminalRunnerSnapshot(t *testing.T) {
	dir := t.TempDir()
	first := NewJobManager(dir)
	cwd := t.TempDir()
	job, err := first.StartShell(cwd, shellEchoArgv("persisted"), 5*time.Second, "idem_persist_terminal_001")
	if err != nil {
		t.Fatal(err)
	}
	final := waitJobTerminal(t, first, job.JobID, 5*time.Second)
	if final.State != "completed" || final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("first terminal=%+v", final)
	}

	second := NewJobManager(dir)
	defer func() { _ = first.CancelAll(context.Background()) }()
	defer func() { _ = second.CancelAll(context.Background()) }()
	restored, err := second.Watch(context.Background(), job.JobID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if restored.State != "completed" || restored.ExitCode == nil || *restored.ExitCode != 0 {
		t.Fatalf("restored terminal=%+v", restored)
	}
	replayed, err := second.StartShell(cwd, shellEchoArgv("persisted"), 5*time.Second, "idem_persist_terminal_001")
	if err != nil {
		t.Fatal(err)
	}
	if replayed.JobID != job.JobID || replayed.State != "completed" {
		t.Fatalf("idempotent replay=%+v original=%+v", replayed, job)
	}
}

func TestJobManagerMarksRunningRunnerAsInterruptedAfterRestart(t *testing.T) {
	dir := t.TempDir()
	first := NewJobManager(dir)
	cwd := t.TempDir()
	job, err := first.StartShell(cwd, shellSleepArgv(), 20*time.Second, "idem_persist_running_001")
	if err != nil {
		t.Fatal(err)
	}
	second := NewJobManager(dir)
	restored, err := second.Watch(context.Background(), job.JobID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if restored.State != "interrupted" || restored.ExitCode != nil {
		t.Fatalf("restored interrupted=%+v", restored)
	}
	replayed, err := second.StartShell(cwd, shellSleepArgv(), 20*time.Second, "idem_persist_running_001")
	if err != nil {
		t.Fatal(err)
	}
	if replayed.JobID != job.JobID || replayed.State != "interrupted" {
		t.Fatalf("interrupted idempotent replay=%+v original=%+v", replayed, job)
	}
	_ = first.CancelAll(context.Background())
	_ = second.CancelAll(context.Background())
}

func TestNativeRunnerJobExecutorUsesPersistentJobManager(t *testing.T) {
	dir := t.TempDir()
	manager := NewJobManager(dir)
	executor := NewNativeRunnerJobExecutor(manager)
	if executor == nil {
		t.Fatal("executor is nil")
	}
	started, err := executor.Start(context.Background(), NativeRunnerJobSpec{
		Cwd: t.TempDir(), Argv: shellEchoArgv("adapter"), Runtime: "host", Timeout: 5 * time.Second,
		IdempotencyKey: "idem_native_adapter_001",
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitJobTerminal(t, manager, started.JobID, 5*time.Second)
	watched, err := executor.Watch(context.Background(), started.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if watched.State != final.State || watched.ExitCode == nil || *watched.ExitCode != 0 {
		t.Fatalf("adapter watched=%+v final=%+v", watched, final)
	}
	_ = manager.CancelAll(context.Background())
}

func TestJobManagerFailsClosedWhenPersistedLedgerCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	jobsDir := filepath.Join(dir, "jobs")
	if err := os.MkdirAll(jobsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobsDir, "records.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewJobManager(dir)
	_, err := manager.StartShell(t.TempDir(), shellEchoArgv("must-not-start"), 5*time.Second, "idem_persist_corrupt_001")
	if !errors.Is(err, ErrJobPersistence) {
		t.Fatalf("corrupt ledger error=%v", err)
	}
}

func TestJobManagerFailsClosedBeforeProcessWhenLedgerWriteFails(t *testing.T) {
	manager := NewJobManager(t.TempDir())
	manager.store.path = filepath.Join(t.TempDir(), "missing", "records.json")
	_, err := manager.StartShell(t.TempDir(), shellEchoArgv("must-not-start"), 5*time.Second, "idem_persist_write_001")
	if !errors.Is(err, ErrJobPersistence) {
		t.Fatalf("write ledger error=%v", err)
	}
}
