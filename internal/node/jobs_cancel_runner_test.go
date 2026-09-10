package node

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNativeRunnerCancelOwnedRejectsMismatchedPersistentKey(t *testing.T) {
	manager := NewJobManager(t.TempDir())
	defer func() { _ = manager.CancelAll(context.Background()) }()
	executor := NewNativeRunnerJobExecutor(manager)
	started, err := executor.Start(context.Background(), NativeRunnerJobSpec{
		Cwd: t.TempDir(), Argv: shellSleepArgv(), Runtime: "host", Timeout: 20 * time.Second,
		IdempotencyKey: "nr-check-owned-mismatch-001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = executor.CancelOwned(context.Background(), started.JobID, "nr-check-other-owner-001"); !errors.Is(err, ErrNativeRunnerJobOwnership) {
		t.Fatalf("mismatched owner error=%v", err)
	}
	snapshot, err := executor.Watch(context.Background(), started.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != "running" {
		t.Fatalf("mismatched owner stopped job: %+v", snapshot)
	}
}

func TestNativeRunnerCancelOwnedCancelsMatchingJobAndIsIdempotent(t *testing.T) {
	manager := NewJobManager(t.TempDir())
	defer func() { _ = manager.CancelAll(context.Background()) }()
	executor := NewNativeRunnerJobExecutor(manager)
	const key = "nr-check-owned-cancel-001"
	started, err := executor.Start(context.Background(), NativeRunnerJobSpec{
		Cwd: t.TempDir(), Argv: shellSleepArgv(), Runtime: "host", Timeout: 20 * time.Second,
		IdempotencyKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := executor.CancelOwned(context.Background(), started.JobID, key)
	if err != nil || first.State != "canceled" {
		t.Fatalf("first owned cancel=%+v err=%v", first, err)
	}
	second, err := executor.CancelOwned(context.Background(), started.JobID, key)
	if err != nil || second.State != "canceled" {
		t.Fatalf("repeated owned cancel=%+v err=%v", second, err)
	}
}

func TestNativeRunnerCancelOwnedAllowsCompletedJob(t *testing.T) {
	manager := NewJobManager(t.TempDir())
	defer func() { _ = manager.CancelAll(context.Background()) }()
	executor := NewNativeRunnerJobExecutor(manager)
	const key = "nr-check-owned-complete-001"
	started, err := executor.Start(context.Background(), NativeRunnerJobSpec{
		Cwd: t.TempDir(), Argv: shellEchoArgv("done"), Runtime: "host", Timeout: 5 * time.Second,
		IdempotencyKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitJobTerminal(t, manager, started.JobID, 5*time.Second)
	if final.State != "completed" {
		t.Fatalf("setup state=%+v", final)
	}
	observed, err := executor.CancelOwned(context.Background(), started.JobID, key)
	if err != nil || observed.State != "completed" {
		t.Fatalf("completed owned cancel=%+v err=%v", observed, err)
	}
}

func TestNativeRunnerCancelOwnedUsesRestoredPersistentIdempotencyKey(t *testing.T) {
	dir := t.TempDir()
	first := NewJobManager(dir)
	defer func() { _ = first.CancelAll(context.Background()) }()
	started, err := first.StartShell(t.TempDir(), shellSleepArgv(), 20*time.Second, "nr-check-persisted-owner-001")
	if err != nil {
		t.Fatal(err)
	}
	second := NewJobManager(dir)
	defer func() { _ = second.CancelAll(context.Background()) }()
	executor := NewNativeRunnerJobExecutor(second)
	observed, err := executor.CancelOwned(context.Background(), started.JobID, "nr-check-persisted-owner-001")
	if err != nil || observed.State != "interrupted" {
		t.Fatalf("restored owned cancellation=%+v err=%v", observed, err)
	}
}
