package agent

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"
)

const (
	nativeRunnerAsyncMaxPending       = 32
	nativeRunnerAsyncOperationLimit   = time.Minute
	nativeRunnerAsyncNonterminalCache = 30 * time.Second
	nativeRunnerAsyncOutcomeRetention = 10 * time.Minute
)

// errNativeRunnerOperationPending means that the operation was accepted by the
// adapter and is still running, or that the bounded worker capacity is full.
// The runner keeps the durable request and retries the same immutable key on a
// later tick.
var errNativeRunnerOperationPending = errors.New("native runner operation pending")

var errNativeRunnerAsyncBackendClosed = errors.New("native runner async backend is closed")

type nativeRunnerAsyncOperationID struct {
	kind string
	key  string
}

type nativeRunnerAsyncOperation struct {
	done        bool
	completedAt time.Time
	replayable  bool
	replayUntil time.Time
	value       any
	err         error
}

// nativeRunnerAsyncBackend keeps provider and job calls out of nativeRunner's
// scheduler lock. Results stay in memory until the scheduler consumes them, so
// a completed operation is not replayed merely because a tick arrived early.
type nativeRunnerAsyncBackend struct {
	backend nativeRunnerBackend
	wake    func()

	ctx    context.Context
	cancel context.CancelFunc

	mu           sync.Mutex
	inflight     map[nativeRunnerAsyncOperationID]*nativeRunnerAsyncOperation
	pendingCount int
	closed       bool

	workers   sync.WaitGroup
	done      chan struct{}
	closeOnce sync.Once
	waitOnce  sync.Once
}

func newNativeRunnerAsyncBackend(backend nativeRunnerBackend, wake func()) *nativeRunnerAsyncBackend {
	ctx, cancel := context.WithCancel(context.Background())
	return &nativeRunnerAsyncBackend{
		backend:  backend,
		wake:     wake,
		ctx:      ctx,
		cancel:   cancel,
		inflight: make(map[nativeRunnerAsyncOperationID]*nativeRunnerAsyncOperation),
		done:     make(chan struct{}),
	}
}

// Close cancels all adapter-owned calls and waits for their bounded workers.
// If the caller's deadline expires first, the worker wait continues in the
// background and a later Close call can finish it without starting new work.
func (a *nativeRunnerAsyncBackend) Close(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		a.mu.Unlock()
		a.cancel()
		a.waitOnce.Do(func() {
			go func() {
				a.workers.Wait()
				close(a.done)
			}()
		})
	})
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *nativeRunnerAsyncBackend) Dispatch(_ context.Context, request nativeRunnerDispatch) (nativeRunnerReceipt, error) {
	value, err := a.start(nativeRunnerAsyncOperationID{kind: "dispatch", key: request.IdempotencyKey}, false, nil, func(ctx context.Context) (any, error) {
		if a.backend == nil {
			return nil, nodeRunnerUnavailableError()
		}
		return a.backend.Dispatch(ctx, request)
	})
	if err != nil {
		return nativeRunnerReceipt{}, err
	}
	receipt, ok := value.(nativeRunnerReceipt)
	if !ok {
		return nativeRunnerReceipt{}, errors.New("native runner dispatch returned an invalid async result")
	}
	return receipt, nil
}

func (a *nativeRunnerAsyncBackend) Observe(_ context.Context, task nativeRunnerTask) (*nativeRunnerResult, error) {
	value, err := a.start(nativeRunnerAsyncOperationID{kind: "observe", key: task.ID + "\x00" + strconv.Itoa(task.Round)}, true, func(value any, err error) time.Duration {
		if err != nil {
			return 0
		}
		result, ok := value.(*nativeRunnerResult)
		if !ok || result == nil || result.RecoveryOnly || !result.Terminal {
			return nativeRunnerAsyncNonterminalCache
		}
		return 0
	}, func(ctx context.Context) (any, error) {
		if a.backend == nil {
			return nil, nodeRunnerUnavailableError()
		}
		return a.backend.Observe(ctx, task)
	})
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	result, ok := value.(*nativeRunnerResult)
	if !ok {
		return nil, errors.New("native runner observation returned an invalid async result")
	}
	return result, nil
}

func (a *nativeRunnerAsyncBackend) StartCheck(_ context.Context, request nativeRunnerCheckRequest) (string, error) {
	value, err := a.start(nativeRunnerAsyncOperationID{kind: "start-check", key: request.IdempotencyKey}, false, nil, func(ctx context.Context) (any, error) {
		if a.backend == nil {
			return nil, nodeRunnerUnavailableError()
		}
		return a.backend.StartCheck(ctx, request)
	})
	if err != nil {
		return "", err
	}
	jobID, ok := value.(string)
	if !ok {
		return "", errors.New("native runner check start returned an invalid async result")
	}
	return jobID, nil
}

func (a *nativeRunnerAsyncBackend) WatchCheck(_ context.Context, jobID string) (nativeRunnerCheckResult, error) {
	value, err := a.start(nativeRunnerAsyncOperationID{kind: "watch-check", key: jobID}, false, func(value any, err error) time.Duration {
		if err != nil {
			return 0
		}
		result, ok := value.(nativeRunnerCheckResult)
		if !ok || (result.State != "completed" && result.State != "failed" && result.State != "canceled") {
			return nativeRunnerAsyncNonterminalCache
		}
		return 0
	}, func(ctx context.Context) (any, error) {
		if a.backend == nil {
			return nil, nodeRunnerUnavailableError()
		}
		return a.backend.WatchCheck(ctx, jobID)
	})
	if err != nil {
		return nativeRunnerCheckResult{}, err
	}
	result, ok := value.(nativeRunnerCheckResult)
	if !ok {
		return nativeRunnerCheckResult{}, errors.New("native runner check watch returned an invalid async result")
	}
	return result, nil
}

func (a *nativeRunnerAsyncBackend) Acknowledge(ctx context.Context, task nativeRunnerTask) error {
	if a == nil || a.backend == nil {
		return nodeRunnerUnavailableError()
	}
	return a.backend.Acknowledge(ctx, task)
}

func (a *nativeRunnerAsyncBackend) Notify(_ context.Context, notice nativeRunnerNotice) (string, error) {
	value, err := a.start(nativeRunnerAsyncOperationID{kind: "notify", key: notice.Key}, false, nil, func(ctx context.Context) (any, error) {
		if a.backend == nil {
			return nil, nodeRunnerUnavailableError()
		}
		return a.backend.Notify(ctx, notice)
	})
	if err != nil {
		return "", err
	}
	turnID, ok := value.(string)
	if !ok {
		return "", errors.New("native runner notification returned an invalid async result")
	}
	return turnID, nil
}

// Recovery and interruption already have their own scheduler workers. Keep
// those operations direct so this adapter does not create a second operation
// ledger or alter their existing deadlines and cancellation semantics.
func (a *nativeRunnerAsyncBackend) Probe(ctx context.Context, task nativeRunnerTask) (nativeRunnerProbe, error) {
	if a == nil || a.backend == nil {
		return nativeRunnerProbe{}, nodeRunnerUnavailableError()
	}
	backend, ok := a.backend.(nativeRunnerRecoveryBackend)
	if !ok {
		return nativeRunnerProbe{}, errors.New("native runner recovery probe is unavailable")
	}
	return backend.Probe(ctx, task)
}

func (a *nativeRunnerAsyncBackend) Continue(ctx context.Context, task nativeRunnerTask, prompt, idempotencyKey string) error {
	if a == nil || a.backend == nil {
		return nodeRunnerUnavailableError()
	}
	backend, ok := a.backend.(nativeRunnerRecoveryBackend)
	if !ok {
		return errors.New("native runner recovery continuation is unavailable")
	}
	return backend.Continue(ctx, task, prompt, idempotencyKey)
}

func (a *nativeRunnerAsyncBackend) Interrupt(ctx context.Context, task nativeRunnerTask) (nativeRunnerProbe, error) {
	if a == nil || a.backend == nil {
		return nativeRunnerProbe{}, nodeRunnerUnavailableError()
	}
	backend, ok := a.backend.(nativeRunnerInterruptBackend)
	if !ok {
		return nativeRunnerProbe{}, errors.New("native runner interruption is unavailable")
	}
	return backend.Interrupt(ctx, task)
}

func (a *nativeRunnerAsyncBackend) start(id nativeRunnerAsyncOperationID, observePending bool, replay func(any, error) time.Duration, run func(context.Context) (any, error)) (any, error) {
	if a == nil {
		return nil, errNativeRunnerAsyncBackendClosed
	}
	now := time.Now()
	a.mu.Lock()
	a.sweepLocked(now)
	if operation, ok := a.inflight[id]; ok {
		if !operation.done {
			a.mu.Unlock()
			if observePending {
				return nil, nil
			}
			return nil, errNativeRunnerOperationPending
		}
		if operation.replayable && now.Before(operation.replayUntil) {
			value, err := operation.value, operation.err
			a.mu.Unlock()
			return value, err
		}
		delete(a.inflight, id)
		value, err := operation.value, operation.err
		a.mu.Unlock()
		return value, err
	}
	if a.closed || a.ctx.Err() != nil {
		a.mu.Unlock()
		return nil, errNativeRunnerAsyncBackendClosed
	}
	if a.pendingCount >= nativeRunnerAsyncMaxPending {
		a.mu.Unlock()
		if observePending {
			return nil, nil
		}
		return nil, errNativeRunnerOperationPending
	}
	operation := &nativeRunnerAsyncOperation{}
	a.inflight[id] = operation
	a.pendingCount++
	a.workers.Add(1)
	a.mu.Unlock()

	go a.run(id, operation, replay, run)
	if observePending {
		return nil, nil
	}
	return nil, errNativeRunnerOperationPending
}

func (a *nativeRunnerAsyncBackend) run(id nativeRunnerAsyncOperationID, operation *nativeRunnerAsyncOperation, replay func(any, error) time.Duration, run func(context.Context) (any, error)) {
	defer a.workers.Done()
	callCtx, cancel := context.WithTimeout(a.ctx, nativeRunnerAsyncOperationLimit)
	value, err := run(callCtx)
	cancel()

	a.mu.Lock()
	if current, ok := a.inflight[id]; ok && current == operation {
		operation.value = value
		operation.err = err
		operation.done = true
		operation.completedAt = time.Now()
		if replay != nil {
			if duration := replay(value, err); duration > 0 {
				operation.replayable = true
				operation.replayUntil = operation.completedAt.Add(duration)
			}
		}
		a.pendingCount--
	}
	a.mu.Unlock()
	if a.wake != nil {
		a.wake()
	}
}

func (a *nativeRunnerAsyncBackend) sweepLocked(now time.Time) {
	for id, operation := range a.inflight {
		if operation.done && operation.replayable && !now.Before(operation.replayUntil) {
			delete(a.inflight, id)
			continue
		}
		if operation.done && !operation.completedAt.IsZero() && now.Sub(operation.completedAt) >= nativeRunnerAsyncOutcomeRetention {
			delete(a.inflight, id)
		}
	}
}
