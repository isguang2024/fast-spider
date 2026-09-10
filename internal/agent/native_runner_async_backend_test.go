package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type blockedNativeRunnerBackend struct {
	mu sync.Mutex

	dispatchCalls int
	observeCalls  int
	startCalls    int
	watchCalls    int
	notifyCalls   int

	release       chan struct{}
	started       chan struct{}
	canceled      chan struct{}
	watchResult   nativeRunnerCheckResult
	observeResult *nativeRunnerResult
}

func newBlockedNativeRunnerBackend() *blockedNativeRunnerBackend {
	return &blockedNativeRunnerBackend{
		release:  make(chan struct{}),
		started:  make(chan struct{}, 64),
		canceled: make(chan struct{}, 64),
	}
}

func (b *blockedNativeRunnerBackend) wait(ctx context.Context) error {
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		select {
		case b.canceled <- struct{}{}:
		default:
		}
		return ctx.Err()
	}
}

func (b *blockedNativeRunnerBackend) Dispatch(ctx context.Context, request nativeRunnerDispatch) (nativeRunnerReceipt, error) {
	b.mu.Lock()
	b.dispatchCalls++
	b.mu.Unlock()
	b.started <- struct{}{}
	if err := b.wait(ctx); err != nil {
		return nativeRunnerReceipt{}, err
	}
	return nativeRunnerReceipt{SessionID: "session-" + request.IdempotencyKey, TaskRef: "task-ref", Generation: int64(request.Round), ResultPath: request.ResultPath}, nil
}

func (b *blockedNativeRunnerBackend) Observe(ctx context.Context, task nativeRunnerTask) (*nativeRunnerResult, error) {
	b.mu.Lock()
	b.observeCalls++
	b.mu.Unlock()
	b.started <- struct{}{}
	if err := b.wait(ctx); err != nil {
		return nil, err
	}
	if b.observeResult != nil {
		result := *b.observeResult
		return &result, nil
	}
	return &nativeRunnerResult{EventID: "event-" + task.ID, Outcome: "completed", Summary: "done", Terminal: true}, nil
}

func (b *blockedNativeRunnerBackend) StartCheck(ctx context.Context, request nativeRunnerCheckRequest) (string, error) {
	b.mu.Lock()
	b.startCalls++
	b.mu.Unlock()
	b.started <- struct{}{}
	if err := b.wait(ctx); err != nil {
		return "", err
	}
	return "job-" + request.IdempotencyKey, nil
}

func (b *blockedNativeRunnerBackend) WatchCheck(ctx context.Context, jobID string) (nativeRunnerCheckResult, error) {
	b.mu.Lock()
	b.watchCalls++
	b.mu.Unlock()
	b.started <- struct{}{}
	if err := b.wait(ctx); err != nil {
		return nativeRunnerCheckResult{}, err
	}
	if b.watchResult.Evidence != "" {
		return b.watchResult, nil
	}
	return nativeRunnerCheckResult{State: "completed", ExitCode: 0, Evidence: jobID}, nil
}

func (b *blockedNativeRunnerBackend) Acknowledge(context.Context, nativeRunnerTask) error {
	return nil
}

func (b *blockedNativeRunnerBackend) Notify(ctx context.Context, notice nativeRunnerNotice) (string, error) {
	b.mu.Lock()
	b.notifyCalls++
	b.mu.Unlock()
	b.started <- struct{}{}
	if err := b.wait(ctx); err != nil {
		return "", err
	}
	return "turn-" + notice.Key, nil
}

func (b *blockedNativeRunnerBackend) calls(kind string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch kind {
	case "dispatch":
		return b.dispatchCalls
	case "observe":
		return b.observeCalls
	case "start":
		return b.startCalls
	case "watch":
		return b.watchCalls
	case "notify":
		return b.notifyCalls
	default:
		return 0
	}
}

func waitForNativeRunnerCalls(t *testing.T, backend *blockedNativeRunnerBackend, kind string, want int) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		if got := backend.calls(kind); got >= want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("%s calls=%d, want at least %d", kind, backend.calls(kind), want)
		case <-time.After(time.Millisecond):
		}
	}
}

func nativeRunnerAsyncDispatchRequest(key string) nativeRunnerDispatch {
	return nativeRunnerDispatch{
		ProjectID:           "project",
		TaskID:              "task",
		Round:               1,
		ControllerSessionID: "controller",
		WorkingDirectory:    ".",
		Prompt:              "run",
		IdempotencyKey:      key,
		ResultPath:          "result.md",
	}
}

func TestNativeRunnerAsyncBackendDispatchReturnsPromptlyAndDeduplicates(t *testing.T) {
	backend := newBlockedNativeRunnerBackend()
	adapter := newNativeRunnerAsyncBackend(backend, nil)
	defer adapter.Close(context.Background())
	request := nativeRunnerAsyncDispatchRequest("dispatch-key")

	started := time.Now()
	if _, err := adapter.Dispatch(context.Background(), request); !errors.Is(err, errNativeRunnerOperationPending) {
		t.Fatalf("first dispatch error=%v, want pending", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("first dispatch blocked for %s", elapsed)
	}
	if _, err := adapter.Dispatch(context.Background(), request); !errors.Is(err, errNativeRunnerOperationPending) {
		t.Fatalf("duplicate dispatch error=%v, want pending", err)
	}
	waitForNativeRunnerCalls(t, backend, "dispatch", 1)
	if got := backend.calls("dispatch"); got != 1 {
		t.Fatalf("provider dispatches=%d, want 1", got)
	}

	close(backend.release)
	var receipt nativeRunnerReceipt
	deadline := time.Now().Add(2 * time.Second)
	for {
		var err error
		receipt, err = adapter.Dispatch(context.Background(), request)
		if err == nil {
			break
		}
		if !errors.Is(err, errNativeRunnerOperationPending) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("completed dispatch result was not retained")
		}
		time.Sleep(time.Millisecond)
	}
	if receipt.SessionID != "session-dispatch-key" {
		t.Fatalf("receipt=%+v", receipt)
	}
	if got := backend.calls("dispatch"); got != 1 {
		t.Fatalf("provider dispatches after completion=%d, want 1", got)
	}
}

func TestNativeRunnerAsyncBackendObserveReturnsNilWhileRunningAndConsumesOnce(t *testing.T) {
	backend := newBlockedNativeRunnerBackend()
	adapter := newNativeRunnerAsyncBackend(backend, nil)
	defer adapter.Close(context.Background())
	task := nativeRunnerTask{ID: "task", Round: 3}

	if result, err := adapter.Observe(context.Background(), task); result != nil || err != nil {
		t.Fatalf("first observe result=%+v err=%v, want nil pending", result, err)
	}
	if result, err := adapter.Observe(context.Background(), task); result != nil || err != nil {
		t.Fatalf("duplicate observe result=%+v err=%v, want nil pending", result, err)
	}
	waitForNativeRunnerCalls(t, backend, "observe", 1)
	close(backend.release)

	deadline := time.Now().Add(2 * time.Second)
	for {
		result, err := adapter.Observe(context.Background(), task)
		if err == nil && result != nil {
			if result.EventID != "event-task" {
				t.Fatalf("consumed observation=%+v", result)
			}
			break
		}
		if err != nil && !errors.Is(err, errNativeRunnerOperationPending) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("completed observation result was not retained")
		}
		time.Sleep(time.Millisecond)
	}
	if got := backend.calls("observe"); got != 1 {
		t.Fatalf("provider observations=%d, want 1", got)
	}
}

func TestNativeRunnerAsyncBackendCachesNonterminalCheckResult(t *testing.T) {
	backend := newBlockedNativeRunnerBackend()
	backend.watchResult = nativeRunnerCheckResult{State: "running", Evidence: "still running"}
	adapter := newNativeRunnerAsyncBackend(backend, nil)
	defer adapter.Close(context.Background())

	if _, err := adapter.WatchCheck(context.Background(), "job-cache"); !errors.Is(err, errNativeRunnerOperationPending) {
		t.Fatalf("first watch error=%v, want pending", err)
	}
	waitForNativeRunnerCalls(t, backend, "watch", 1)
	close(backend.release)

	deadline := time.Now().Add(2 * time.Second)
	for {
		result, err := adapter.WatchCheck(context.Background(), "job-cache")
		if err == nil {
			if result.State != "running" || result.Evidence != "still running" {
				t.Fatalf("cached check result=%+v", result)
			}
			break
		}
		if !errors.Is(err, errNativeRunnerOperationPending) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("nonterminal check result was not retained")
		}
		time.Sleep(time.Millisecond)
	}
	if result, err := adapter.WatchCheck(context.Background(), "job-cache"); err != nil || result.State != "running" {
		t.Fatalf("cached replay result=%+v err=%v", result, err)
	}
	if got := backend.calls("watch"); got != 1 {
		t.Fatalf("provider watches=%d, want 1 during cache window", got)
	}
}

func TestNativeRunnerAsyncBackendCloseCancelsBlockedWorker(t *testing.T) {
	backend := newBlockedNativeRunnerBackend()
	adapter := newNativeRunnerAsyncBackend(backend, nil)
	if _, err := adapter.Dispatch(context.Background(), nativeRunnerAsyncDispatchRequest("close-key")); !errors.Is(err, errNativeRunnerOperationPending) {
		t.Fatalf("dispatch error=%v", err)
	}
	waitForNativeRunnerCalls(t, backend, "dispatch", 1)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := adapter.Close(ctx); err != nil {
		t.Fatalf("close error=%v", err)
	}
	select {
	case <-backend.canceled:
	default:
		t.Fatal("blocked provider call was not canceled")
	}
}

func TestNativeRunnerAsyncBackendLimitsProviderInflightCalls(t *testing.T) {
	backend := newBlockedNativeRunnerBackend()
	adapter := newNativeRunnerAsyncBackend(backend, nil)
	defer adapter.Close(context.Background())

	for i := 0; i < nativeRunnerAsyncMaxPending+8; i++ {
		request := nativeRunnerAsyncDispatchRequest(fmt.Sprintf("dispatch-key-%02d", i))
		if _, err := adapter.Dispatch(context.Background(), request); !errors.Is(err, errNativeRunnerOperationPending) {
			t.Fatalf("dispatch %d error=%v, want pending", i, err)
		}
	}
	waitForNativeRunnerCalls(t, backend, "dispatch", nativeRunnerAsyncMaxPending)
	if got := backend.calls("dispatch"); got > nativeRunnerAsyncMaxPending {
		t.Fatalf("provider dispatches=%d, max=%d", got, nativeRunnerAsyncMaxPending)
	}
	close(backend.release)
}
