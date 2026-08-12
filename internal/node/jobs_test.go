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
			result, err := jobs.StartShell(cwd, argv, 20*time.Second, key)
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

func TestJobWatchIsNotBlockedByRuntimePreparation(t *testing.T) {
	jobs := NewJobManager()
	defer func() { _ = jobs.CancelAll(context.Background()) }()
	existing, err := jobs.StartShell(t.TempDir(), shellSleepArgv(), 20*time.Second, "idem_existing_job_001")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	jobs.prepare = func(context.Context, string, []string, executionRuntime) (string, []string, string, error) {
		close(entered)
		<-release
		return "", nil, "", errors.New("synthetic preparation failure")
	}
	startDone := make(chan error, 1)
	go func() {
		_, startErr := jobs.StartExecution(context.Background(), t.TempDir(), shellSleepArgv(), executionRuntime{Kind: "wsl"}, 20*time.Second, "idem_blocked_start_01", "", "")
		startDone <- startErr
	}()
	<-entered
	watchStarted := time.Now()
	if _, err := jobs.Watch(context.Background(), existing.JobID, 0, 0); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(watchStarted); elapsed > 200*time.Millisecond {
		t.Fatalf("watch blocked behind runtime preparation for %s", elapsed)
	}
	close(release)
	if err := <-startDone; err == nil {
		t.Fatal("synthetic preparation unexpectedly succeeded")
	}
}

func TestJobWatchAndCancelUseJobIDOnly(t *testing.T) {
	jobs := NewJobManager()
	defer func() { _ = jobs.CancelAll(context.Background()) }()
	job, err := jobs.StartExecution(context.Background(), t.TempDir(), shellSleepArgv(), executionRuntime{Kind: "host"}, 20*time.Second, "idem_job_scope_001", "request_job_start", "trace_job_start")
	if err != nil {
		t.Fatal(err)
	}
	watched, err := jobs.Watch(context.Background(), job.JobID, 0, 0)
	if err != nil {
		t.Fatalf("watch error=%v", err)
	}
	if watched.RequestID != "request_job_start" || watched.TraceID != "trace_job_start" {
		t.Fatalf("watch changed job origin IDs: %+v", watched)
	}
	canceled, err := jobs.Cancel(context.Background(), job.JobID)
	if err != nil {
		t.Fatalf("cancel error=%v", err)
	}
	if canceled.RequestID != "request_job_start" || canceled.TraceID != "trace_job_start" {
		t.Fatalf("cancel changed job origin IDs: %+v", canceled)
	}
}
