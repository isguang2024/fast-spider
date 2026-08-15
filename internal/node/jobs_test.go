package node

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestJobCaptureDrainsLongUnterminatedOutput(t *testing.T) {
	if len(os.Args) > 0 && os.Args[len(os.Args)-1] == "emit-long-unterminated-output" {
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), 512<<10))
		return
	}
	jobs := NewJobManager()
	defer func() { _ = jobs.CancelAll(context.Background()) }()
	job, err := jobs.StartShell(t.TempDir(), []string{os.Args[0], "-test.run=^TestJobCaptureDrainsLongUnterminatedOutput$", "--", "emit-long-unterminated-output"}, 5*time.Second, "idem_long_output_drain_001")
	if err != nil {
		t.Fatal(err)
	}
	final := waitJobTerminal(t, jobs, job.JobID, 10*time.Second)
	if final.State != "completed" || final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("long-output job=%+v", final)
	}
	stdoutSeen := false
	for _, event := range final.Events {
		if event.Type == "stdout" {
			stdoutSeen = true
		}
		if event.Type == "warning" && strings.Contains(event.Text, "capture stopped") {
			t.Fatalf("long output stopped capture: %+v", event)
		}
	}
	if !stdoutSeen {
		t.Fatal("long output produced no retained stdout event")
	}
}

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

func TestJobCancelAllCancelsStartingReservationAndPreventsLateStart(t *testing.T) {
	if len(os.Args) > 1 && os.Args[len(os.Args)-1] == "job-start-after-cancel-helper" {
		if err := os.WriteFile(os.Args[len(os.Args)-2], []byte("started"), 0o600); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}

	jobs := NewJobManager()
	entered := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	forceCleanup := make(chan struct{})
	marker := filepath.Join(t.TempDir(), "unexpected-start")
	jobs.prepare = func(ctx context.Context, cwd string, _ []string, _ executionRuntime) (string, []string, string, error) {
		close(entered)
		select {
		case <-ctx.Done():
			close(canceled)
			<-release
			return cwd, []string{os.Args[0], "-test.run=^TestJobCancelAllCancelsStartingReservationAndPreventsLateStart$", "--", marker, "job-start-after-cancel-helper"}, "host", nil
		case <-forceCleanup:
			return "", nil, "", errors.New("forced test cleanup")
		}
	}

	startDone := make(chan error, 1)
	go func() {
		_, err := jobs.StartExecution(context.Background(), t.TempDir(), []string{"unused"}, executionRuntime{Kind: "host"}, 20*time.Second, "idem_shutdown_start_001", "", "")
		startDone <- err
	}()
	<-entered
	cancelAllDone := make(chan error, 1)
	go func() { cancelAllDone <- jobs.CancelAll(context.Background()) }()

	select {
	case <-canceled:
	case err := <-cancelAllDone:
		close(forceCleanup)
		<-startDone
		t.Fatalf("CancelAll returned before canceling the starting reservation: %v", err)
	case <-time.After(time.Second):
		close(forceCleanup)
		<-startDone
		t.Fatal("CancelAll did not cancel the starting reservation")
	}
	select {
	case err := <-cancelAllDone:
		close(release)
		<-startDone
		t.Fatalf("CancelAll returned before the starting reservation finished: %v", err)
	default:
	}
	close(release)
	if err := <-startDone; !errors.Is(err, ErrJobManagerClosed) {
		t.Fatalf("late start error=%v want %v", err, ErrJobManagerClosed)
	}
	if err := <-cancelAllDone; err != nil {
		t.Fatalf("CancelAll error=%v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("process started after shutdown, marker stat error=%v", err)
	}
	jobs.mu.RLock()
	jobCount := len(jobs.jobs)
	startingCount := len(jobs.starting)
	jobs.mu.RUnlock()
	if jobCount != 0 || startingCount != 0 || len(jobs.semaphore) != 0 {
		t.Fatalf("shutdown state jobs=%d starting=%d semaphore=%d", jobCount, startingCount, len(jobs.semaphore))
	}
	if _, err := jobs.StartShell(t.TempDir(), shellSleepArgv(), time.Second, "idem_shutdown_block_001"); !errors.Is(err, ErrJobManagerClosed) {
		t.Fatalf("start after shutdown error=%v want %v", err, ErrJobManagerClosed)
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
