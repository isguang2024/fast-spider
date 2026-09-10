package node

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrNativeRunnerJobOwnership is returned when a native runner task presents
// a job ID that is not durably owned by its expected check idempotency key.
// The caller must preserve the runner binding instead of cancelling an
// unrelated process.
var ErrNativeRunnerJobOwnership = errors.New("native runner job ownership mismatch")

// CancelOwned cancels a native runner validation job only after checking both
// the in-memory job record and the durable idempotency index. A job ID by
// itself is not sufficient authority to stop a process.
func (e *NativeRunnerJobExecutor) CancelOwned(ctx context.Context, jobID, idempotencyKey string) (NativeRunnerJobSnapshot, error) {
	if e == nil || e.manager == nil {
		return NativeRunnerJobSnapshot{}, errors.New("native runner job manager is unavailable")
	}
	jobID = strings.TrimSpace(jobID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if jobID == "" || idempotencyKey == "" {
		return NativeRunnerJobSnapshot{}, fmt.Errorf("native runner owned cancellation requires job ID and idempotency key")
	}

	e.manager.mu.RLock()
	job := e.manager.jobs[jobID]
	record, indexed := e.manager.idempotency[idempotencyKey]
	e.manager.mu.RUnlock()
	if job == nil || !indexed || record.JobID != jobID {
		return NativeRunnerJobSnapshot{}, fmt.Errorf("%w: job %s", ErrNativeRunnerJobOwnership, jobID)
	}
	job.mu.Lock()
	owned := job.idempotencyKey == idempotencyKey
	job.mu.Unlock()
	if !owned {
		return NativeRunnerJobSnapshot{}, fmt.Errorf("%w: job %s", ErrNativeRunnerJobOwnership, jobID)
	}

	snapshot, err := e.manager.Cancel(ctx, jobID)
	if err != nil {
		return NativeRunnerJobSnapshot{}, err
	}
	return nativeRunnerJobSnapshotFromJob(snapshot), nil
}
