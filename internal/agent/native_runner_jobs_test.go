package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
)

type fakeNativeRunnerJobExecutor struct {
	mu       sync.Mutex
	starts   int
	lastSpec nativeRunnerJobSpec
	startErr error
	snapshot nativeRunnerJobSnapshot
	watch    nativeRunnerJobSnapshot
	watchErr error
}

func (f *fakeNativeRunnerJobExecutor) Start(_ context.Context, spec nativeRunnerJobSpec) (nativeRunnerJobSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	f.lastSpec = spec
	if f.startErr != nil {
		return nativeRunnerJobSnapshot{}, f.startErr
	}
	return f.snapshot, nil
}

func (f *fakeNativeRunnerJobExecutor) Watch(context.Context, string) (nativeRunnerJobSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.watchErr != nil {
		return nativeRunnerJobSnapshot{}, f.watchErr
	}
	return f.watch, nil
}

func testNativeRunnerCheck(key string) nativeRunnerCheckRequest {
	return nativeRunnerCheckRequest{
		ProjectID: "project-1", TaskID: "task-1", Round: 1, Name: "test",
		WorkingDirectory: ".", IdempotencyKey: key,
		Check: nativeRunnerCheck{Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 5},
	}
}

func TestNativeRunnerJobsDelegatesIdempotencyToNodeExecutor(t *testing.T) {
	executor := &fakeNativeRunnerJobExecutor{snapshot: nativeRunnerJobSnapshot{JobID: "job_delegate_001", State: "running"}}
	jobs, err := newNativeRunnerJobs(t.TempDir(), executor)
	if err != nil {
		t.Fatal(err)
	}
	request := testNativeRunnerCheck("nr-check-delegate-001")
	if got, err := jobs.StartCheck(context.Background(), request); err != nil || got != "job_delegate_001" {
		t.Fatalf("first start id=%q err=%v", got, err)
	}
	if got, err := jobs.StartCheck(context.Background(), request); err != nil || got != "job_delegate_001" {
		t.Fatalf("second start id=%q err=%v", got, err)
	}
	executor.mu.Lock()
	starts := executor.starts
	executor.mu.Unlock()
	if starts != 2 {
		t.Fatalf("agent must delegate idempotency to Node; starts=%d", starts)
	}
}

func TestNativeRunnerJobsWatchMapsTerminalStates(t *testing.T) {
	executor := &fakeNativeRunnerJobExecutor{
		snapshot: nativeRunnerJobSnapshot{JobID: "job_watch_001", State: "running"},
		watch:    nativeRunnerJobSnapshot{JobID: "job_watch_001", State: "expired", ExitCode: intPtr(124), Evidence: "timeout"},
	}
	jobs, err := newNativeRunnerJobs(t.TempDir(), executor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.StartCheck(context.Background(), testNativeRunnerCheck("nr-check-watch-001")); err != nil {
		t.Fatal(err)
	}
	result, err := jobs.WatchCheck(context.Background(), "job_watch_001")
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "failed" || result.ExitCode != 124 || result.Evidence != "timeout" {
		t.Fatalf("result=%+v", result)
	}
}

func TestNativeRunnerJobsResolvesRelativeCheckCwd(t *testing.T) {
	executor := &fakeNativeRunnerJobExecutor{snapshot: nativeRunnerJobSnapshot{JobID: "job_relative_cwd_001", State: "running"}}
	jobs, err := newNativeRunnerJobs(t.TempDir(), executor)
	if err != nil {
		t.Fatal(err)
	}
	request := testNativeRunnerCheck("nr-check-relative-cwd-001")
	request.WorkingDirectory = t.TempDir()
	request.Check.Cwd = "subdir"
	if _, err := jobs.StartCheck(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	executor.mu.Lock()
	got := executor.lastSpec.Cwd
	executor.mu.Unlock()
	want, _ := filepath.Abs(filepath.Join(request.WorkingDirectory, "subdir"))
	if got != filepath.Clean(want) {
		t.Fatalf("resolved cwd=%q want=%q", got, filepath.Clean(want))
	}
}

func TestNativeRunnerJobsDoesNotPassCompletedWithoutExitCode(t *testing.T) {
	executor := &fakeNativeRunnerJobExecutor{
		snapshot: nativeRunnerJobSnapshot{JobID: "job_missing_exit_001", State: "running"},
		watch:    nativeRunnerJobSnapshot{JobID: "job_missing_exit_001", State: "completed", Evidence: "output"},
	}
	jobs, err := newNativeRunnerJobs(t.TempDir(), executor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.StartCheck(context.Background(), testNativeRunnerCheck("nr-check-missing-exit-001")); err != nil {
		t.Fatal(err)
	}
	result, err := jobs.WatchCheck(context.Background(), "job_missing_exit_001")
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "failed" || result.ExitCode != -1 || !strings.Contains(result.Evidence, "exit code") {
		t.Fatalf("missing exit result=%+v", result)
	}
}

func TestNativeRunnerJobsUsesRealNodeExecutor(t *testing.T) {
	dir := t.TempDir()
	nodeJobs := node.NewJobManager(dir)
	jobs, err := newNativeRunnerJobs(dir, node.NewNativeRunnerJobExecutor(nodeJobs))
	if err != nil {
		t.Fatal(err)
	}
	request := testNativeRunnerCheck("nr-check-real-node-001")
	request.Check.Argv = []string{"go", "version"}
	jobID, err := jobs.StartCheck(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer nodeJobs.CancelAll(context.Background())
	for {
		result, watchErr := jobs.WatchCheck(ctx, jobID)
		if watchErr != nil {
			t.Fatal(watchErr)
		}
		if result.State != "running" {
			if result.State != "completed" || result.ExitCode != 0 {
				t.Fatalf("real node result=%+v", result)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestNativeRunnerJobsValidationAndExecutorErrors(t *testing.T) {
	executor := &fakeNativeRunnerJobExecutor{startErr: errors.New("uncertain transport response")}
	jobs, err := newNativeRunnerJobs(t.TempDir(), executor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.StartCheck(context.Background(), testNativeRunnerCheck("nr-check-error-001")); err == nil {
		t.Fatal("expected executor error")
	}
	for _, request := range []nativeRunnerCheckRequest{
		{IdempotencyKey: "short", WorkingDirectory: ".", Check: nativeRunnerCheck{Argv: []string{"go"}}},
		{IdempotencyKey: "nr-check-empty-argv", WorkingDirectory: "."},
		{IdempotencyKey: "nr-check-negative-time", WorkingDirectory: ".", Check: nativeRunnerCheck{Argv: []string{"go"}, TimeoutSeconds: -1}},
	} {
		if _, err := jobs.StartCheck(context.Background(), request); err == nil {
			t.Fatalf("invalid request accepted: %+v", request)
		}
	}
	if _, err := jobs.WatchCheck(context.Background(), ""); err == nil {
		t.Fatal("empty job ID accepted")
	}
}

func intPtr(v int) *int { return &v }
