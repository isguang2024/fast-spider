package node

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestShellConcurrentIdempotentStart(t *testing.T) {
	jobs := NewJobManager()
	defer func() { _ = jobs.CancelAll(context.Background()) }()
	cwd := t.TempDir()
	argv := shellSleepArgv()
	const key = "idem_concurrent_shell_001"

	const callers = 8
	results := make(chan JobSnapshot, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := jobs.StartShell("ws_test", cwd, argv, 20*time.Second, key, nil)
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent StartShell error=%v", err)
		}
	}
	var jobID string
	for result := range results {
		if jobID == "" {
			jobID = result.JobID
		}
		if result.JobID != jobID {
			t.Fatalf("same idempotency key started multiple jobs: %q != %q", result.JobID, jobID)
		}
	}
	if jobID == "" {
		t.Fatal("no job returned")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	final, err := jobs.Cancel(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != "canceled" {
		t.Fatalf("cancel state=%q", final.State)
	}
}

func TestJobWorkspaceScopeRejectsCrossWorkspaceWatchAndCancel(t *testing.T) {
	jobs := NewJobManager()
	defer func() { _ = jobs.CancelAll(context.Background()) }()
	job, err := jobs.StartShell("workspace-a", t.TempDir(), shellSleepArgv(), 20*time.Second, "idem_workspace_scope_001", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.WatchWorkspace(context.Background(), "workspace-b", job.JobID, 0, 0); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("cross-workspace watch error=%v", err)
	}
	if _, err := jobs.CancelWorkspace(context.Background(), "workspace-b", job.JobID); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("cross-workspace cancel error=%v", err)
	}
	if _, err := jobs.WatchWorkspace(context.Background(), "workspace-a", job.JobID, 0, 0); err != nil {
		t.Fatalf("authorized workspace watch error=%v", err)
	}
}
