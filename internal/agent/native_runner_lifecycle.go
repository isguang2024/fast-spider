package agent

import (
	"context"
	"errors"
	"time"
)

type nativeRunnerChange struct {
	Revision  int64  `json:"revision"`
	CreatedAt int64  `json:"createdAt"`
	Kind      string `json:"kind"`
	TaskID    string `json:"taskId,omitempty"`
	Evidence  string `json:"evidence,omitempty"`
}

type nativeRunnerTaskUpdate struct {
	Objective   string   `json:"objective"`
	Acceptance  string   `json:"acceptance"`
	Scope       string   `json:"scope"`
	GoalVersion string   `json:"goalVersion"`
	Context     []string `json:"context,omitempty"`
	After       []string `json:"after,omitempty"`
	Priority    int      `json:"priority,omitempty"`
}

type nativeRunnerCancellation struct {
	Reason      string                     `json:"reason"`
	Revision    int64                      `json:"revision"`
	RequestedAt int64                      `json:"requestedAt"`
	NextAt      int64                      `json:"nextAt,omitempty"`
	LastError   string                     `json:"lastError,omitempty"`
	Failures    int                        `json:"failures,omitempty"`
	Target      *nativeRunnerTaskUpdate    `json:"target,omitempty"`
	Proof       *nativeRunnerInactiveProof `json:"proof,omitempty"`
	WaitingJobs []string                   `json:"waitingJobs,omitempty"`
}

type nativeRunnerStopResult struct {
	Stopped     bool
	Proof       *nativeRunnerInactiveProof
	WaitingJobs []string
	Summary     string
}

type nativeRunnerStopBackend interface {
	Stop(context.Context, nativeRunnerTask) (nativeRunnerStopResult, error)
}

func (a *nativeRunnerAsyncBackend) Stop(ctx context.Context, t nativeRunnerTask) (nativeRunnerStopResult, error) {
	backend, ok := a.backend.(nativeRunnerStopBackend)
	if !ok {
		return nativeRunnerStopResult{}, errors.New("exact task stopping is unavailable")
	}
	return backend.Stop(ctx, t)
}

type nativeLifecycleCompletion struct {
	Task   nativeRunnerTask
	Result nativeRunnerStopResult
	Err    error
}

func nativeRequestCancel(t *nativeRunnerTask, reason string, revision, now int64, target *nativeRunnerTaskUpdate) bool {
	if t.State == "accepted" || t.State == "cancelled" {
		return false
	}
	if t.Cancellation != nil && nativeHash(t.Cancellation.Target) == nativeHash(target) {
		if target == nil || t.Cancellation.Revision == revision {
			return false
		}
	}
	t.Cancellation = &nativeRunnerCancellation{Reason: reason, Revision: revision, RequestedAt: now, Target: target}
	waitingJobs := t.Recovery != nil && len(t.Recovery.Checkpoint.WaitingJobs) > 0
	if t.Request == nil && t.Receipt == nil && !nativeChecking(*t) && !waitingJobs {
		t.State = "cancelled"
	} else {
		t.State = "canceling"
	}
	return true
}

func nativeLifecycleFingerprint(t nativeRunnerTask) string {
	var waiting []string
	if t.Recovery != nil {
		waiting = t.Recovery.Checkpoint.WaitingJobs
	}
	return nativeHash([]any{t.ID, t.Round, t.Request, t.Receipt, t.Cancellation, t.Validations, waiting})
}

// All provider cancellation and job inspection happens outside r.mu. The
// committed intent is the dispatch fence; results must match that exact intent.
func (r *nativeRunner) tickLifecycle(ctx context.Context, p nativeRunnerProject, tasks []nativeRunnerTask) error {
	if r.lifecycleInFlight == nil {
		r.lifecycleInFlight = map[string]bool{}
		r.lifecycleDone = make(chan nativeLifecycleCompletion, 8)
	}
	for {
		select {
		case out := <-r.lifecycleDone:
			delete(r.lifecycleInFlight, out.Task.ID)
			current, all, err := r.read(ctx, out.Task.ProjectID)
			if err != nil {
				return err
			}
			for _, t := range all {
				if t.ID != out.Task.ID || t.State != "canceling" || nativeLifecycleFingerprint(t) != nativeLifecycleFingerprint(out.Task) {
					continue
				}
				if err = r.applyLifecycle(ctx, current, &t, out.Result, out.Err); err != nil {
					return err
				}
			}
		default:
			goto drained
		}
	}
drained:
	var err error
	p, tasks, err = r.read(ctx, p.ID)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if (t.State == "cancelled" || t.State == "canceling") && t.Receipt != nil && t.Result == nil {
			late, e := r.backend.Observe(ctx, t)
			if errors.Is(e, errNativeRunnerOperationPending) {
				continue
			}
			if e != nil {
				continue
			}
			if late != nil && late.Terminal && !late.RecoveryOnly {
				t.ResultAcked = false
				if err = r.storeResult(ctx, &t, *late); err != nil {
					return err
				}
			}
		}
		if t.State != "canceling" || t.Cancellation == nil || t.Cancellation.NextAt > r.now().Unix() || r.lifecycleInFlight[t.ID] || r.recoveryInFlight[t.ID] || len(r.lifecycleInFlight) >= 8 {
			continue
		}
		// A frozen, ambiguously sent request must first reconcile its receipt.
		formalResult := t.Result != nil && t.Result.Terminal && !t.Result.RecoveryOnly
		if t.Request != nil && (t.Receipt == nil || t.Receipt.InDoubt) && !formalResult {
			continue
		}
		backend, ok := r.backend.(nativeRunnerStopBackend)
		if !ok {
			continue
		}
		cancel := *t.Cancellation
		cancel.NextAt = r.now().Add(time.Minute).Unix()
		t.Cancellation = &cancel
		if err = r.saveTask(ctx, t, ""); err != nil {
			return err
		}
		r.lifecycleInFlight[t.ID] = true
		r.lifecycleWG.Add(1)
		go func(task nativeRunnerTask) {
			defer r.lifecycleWG.Done()
			stopCtx, stopCancel := context.WithTimeout(ctx, 45*time.Second)
			defer stopCancel()
			result, e := backend.Stop(stopCtx, task)
			r.lifecycleDone <- nativeLifecycleCompletion{Task: task, Result: result, Err: e}
			r.Wake()
		}(t)
	}
	if p.State == "canceling" {
		stopped := true
		for _, t := range tasks {
			if t.State != "cancelled" && t.State != "accepted" {
				stopped = false
				break
			}
		}
		if stopped {
			p.State = "cancelled"
			p.Paused = true
			tx, e := r.db.BeginTx(ctx, nil)
			if e != nil {
				return e
			}
			defer tx.Rollback()
			if e = nativeSave(tx, "runner_projects", p.ID, "", p); e != nil {
				return e
			}
			if e = r.event(tx, p.ID, "project_cancelled", map[string]any{"revision": p.Revision}); e != nil {
				return e
			}
			return tx.Commit()
		}
	}
	return nil
}

func (r *nativeRunner) applyLifecycle(ctx context.Context, p nativeRunnerProject, t *nativeRunnerTask, result nativeRunnerStopResult, stopErr error) error {
	cancel := *t.Cancellation
	if stopErr != nil || !result.Stopped {
		cancel.Failures++
		cancel.NextAt = r.now().Add(time.Duration(30*(1<<min(cancel.Failures-1, 5))) * time.Second).Unix()
		cancel.LastError = result.Summary
		if stopErr != nil {
			cancel.LastError = stopErr.Error()
		}
		cancel.WaitingJobs = result.WaitingJobs
		t.Cancellation = &cancel
		return r.saveTask(ctx, *t, "cancellation_pending")
	}
	if t.Request != nil && t.Result == nil {
		proof := result.Proof
		if proof == nil || !proof.Terminal || t.Receipt == nil || proof.SessionID != t.Receipt.SessionID || proof.Round != t.Round || proof.ObservedAt <= 0 || r.now().Unix()-proof.ObservedAt > 60 {
			return r.applyLifecycle(ctx, p, t, nativeRunnerStopResult{}, errors.New("exact current generation stop proof is unavailable"))
		}
	}
	cancel.Proof = result.Proof
	cancel.LastError = ""
	cancel.NextAt = 0
	cancel.WaitingJobs = nil
	t.Cancellation = &cancel
	if cancel.Target == nil {
		t.State = "cancelled"
		if t.Recovery != nil {
			state := *t.Recovery
			state.PendingKey = ""
			state.PendingPrompt = ""
			state.PendingInterrupt = false
			state.Manual = false
			t.Recovery = &state
		}
		return r.saveTask(ctx, *t, "task_cancelled")
	}
	// Redirect is a new generation of the SAME logical block, retaining every
	// old callback binding. Late old-generation results cannot complete this one.
	t.History = append(t.History, nativeRunnerAttempt{CloseoutJobs: nativeCloseoutJobIDs(*t), Round: t.Round, GoalVersion: t.GoalVersion, Request: t.Request, Receipt: t.Receipt, Result: t.Result, Correction: t.Correction, Acked: t.ResultAcked, Superseded: true, InactiveProof: result.Proof})
	target := cancel.Target
	t.Round++
	t.StartedAt = 0
	t.Request = nil
	t.Receipt = nil
	t.Result = nil
	t.ResultAcked = false
	t.Validations = nil
	t.NextAt = 0
	t.LastError = ""
	t.DeferredReason = ""
	t.ResumeAt = 0
	t.Objective = target.Objective
	t.Acceptance = target.Acceptance
	t.Scope = target.Scope
	t.Context = target.Context
	t.After = target.After
	t.Priority = target.Priority
	t.GoalVersion = target.GoalVersion
	t.Correction = "Continue from preserved files and checkpoint under the newly approved plan: " + cancel.Reason
	t.Rotate = true
	t.Cancellation = nil
	t.State = "queued"
	if p.Revision != cancel.Revision {
		t.State = "pending_plan"
	}
	if t.Recovery != nil {
		state := *t.Recovery
		state.Phase = "handover"
		state.PendingKey = ""
		state.PendingPrompt = ""
		state.PendingInterrupt = false
		state.Manual = false
		state.Checkpoint.WaitingJobs = nil
		state.Attempts = 0
		t.Recovery = &state
	}
	return r.saveTask(ctx, *t, "task_redirected")
}
