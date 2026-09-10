package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// These are observation deadlines, never execution failure deadlines.
const nativeProgressInterval = 15 * time.Minute
const nativeJobPollInterval = 30 * time.Second
const nativeContextHandoverBytes = 256 << 10 // bytes observed, not model token capacity

type nativeRunnerCheckpoint struct {
	Summary     string   `json:"summary"`
	NextStep    string   `json:"nextStep"`
	Stage       string   `json:"stage"`
	Evidence    []string `json:"evidence,omitempty"`
	WaitingJobs []string `json:"waitingJobs,omitempty"`
}

type nativeRunnerRecovery struct {
	Manual                   bool                   `json:"manual,omitempty"`
	LastContinuedProgressKey string                 `json:"lastContinuedProgressKey,omitempty"`
	PendingInterrupt         bool                   `json:"pendingInterrupt,omitempty"`
	ContextExhausted         bool                   `json:"contextExhausted,omitempty"`
	Phase                    string                 `json:"phase"`
	LastProgressAt           int64                  `json:"lastProgressAt"`
	NextProbeAt              int64                  `json:"nextProbeAt"`
	Attempts                 int                    `json:"attempts"`
	LastError                string                 `json:"lastError,omitempty"`
	Checkpoint               nativeRunnerCheckpoint `json:"checkpoint"`
	ProgressKey              string                 `json:"progressKey,omitempty"`
	PendingKey               string                 `json:"pendingKey,omitempty"`
	PendingPrompt            string                 `json:"pendingPrompt,omitempty"`
}

type nativeRunnerContextExhaustedError struct{ err error }

func (e *nativeRunnerContextExhaustedError) Error() string {
	return "cloud context exhausted: " + e.err.Error()
}
func (e *nativeRunnerContextExhaustedError) Unwrap() error { return e.err }

type nativeRunnerProbe struct {
	ProgressKey, Summary                string
	Running, Terminal, ContextExhausted bool
	ContextBytes                        int64
	Authoritative, ContextBytesKnown    bool
	ObservedAt                          int64
}

type nativeRunnerInactiveProof struct {
	SessionID   string `json:"sessionId"`
	Round       int    `json:"round"`
	ProgressKey string `json:"progressKey"`
	ObservedAt  int64  `json:"observedAt"`
	Terminal    bool   `json:"terminal"`
}

type nativeRunnerRecoveryBackend interface {
	Probe(context.Context, nativeRunnerTask) (nativeRunnerProbe, error)
	Continue(context.Context, nativeRunnerTask, string, string) error
}

type nativeRunnerInterruptBackend interface {
	Interrupt(context.Context, nativeRunnerTask) (nativeRunnerProbe, error)
}

type nativeRecoveryCompletion struct {
	Task      nativeRunnerTask
	Probe     nativeRunnerProbe
	Jobs      map[string]nativeRunnerCheckResult
	Continued bool
	Err       error
}

// The ledger retains full history; a new cloud turn receives compact references
// instead of recursively embedding prior prompts and accumulated transcripts.
func nativePacketTask(t nativeRunnerTask) nativeRunnerTask {
	t.Request = nil
	t.Basis = nil
	history := t.History
	if len(history) > 8 {
		history = history[len(history)-8:]
	}
	t.History = append([]nativeRunnerAttempt(nil), history...)
	for i := range t.History {
		t.History[i].Request = nil
	}
	if t.Recovery != nil {
		state := *t.Recovery
		state.PendingPrompt = ""
		state.PendingKey = ""
		t.Recovery = &state
	}
	return t
}

// Checkpoint is called only after transport resolves the immutable task binding.
func (r *nativeRunner) checkpoint(ctx context.Context, projectID, taskID string, round int, sessionID string, checkpoint nativeRunnerCheckpoint) error {
	if len(checkpoint.Summary) > 4096 || len(checkpoint.NextStep) > 4096 || len(checkpoint.Stage) > 128 || len(checkpoint.Evidence) > 24 || len(checkpoint.WaitingJobs) > 8 {
		return errors.New("checkpoint exceeds bounded handover limits")
	}
	if strings.TrimSpace(checkpoint.Summary) == "" {
		return errors.New("checkpoint requires a progress summary")
	}
	for _, s := range append(append([]string{}, checkpoint.Evidence...), checkpoint.WaitingJobs...) {
		if len(s) > 2048 {
			return errors.New("checkpoint reference is too long")
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, tasks, err := r.read(ctx, projectID)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.ID != taskID {
			continue
		}
		if t.Round != round || t.Receipt == nil || t.Receipt.SessionID != sessionID || !nativeHolds(t) {
			return errors.New("checkpoint no longer owns the active task generation")
		}
		recovery := r.recoveryState(t)
		if nativeHash(recovery.Checkpoint) == nativeHash(checkpoint) {
			return nil
		}
		recovery.Checkpoint = checkpoint
		recovery.LastProgressAt = r.now().Unix()
		recovery.Attempts = 0
		recovery.Phase = "watching"
		recovery.NextProbeAt = r.now().Add(nativeProgressInterval).Unix()
		if len(checkpoint.WaitingJobs) > 0 {
			recovery.Phase = "waiting_job"
			recovery.NextProbeAt = r.now().Unix()
		}
		t.Recovery = &recovery
		return r.saveTask(ctx, t, "checkpoint_saved")
	}
	return errors.New("checkpoint task not found")
}

func (r *nativeRunner) recoveryState(t nativeRunnerTask) nativeRunnerRecovery {
	if t.Recovery != nil {
		return *t.Recovery
	}
	return nativeRunnerRecovery{Phase: "watching", LastProgressAt: r.now().Unix(), NextProbeAt: r.now().Add(nativeProgressInterval).Unix()}
}

// External provider and job reads run outside the scheduler lock. A stalled
// conversation cannot hold the tick loop, callback ingestion or other projects.
func (r *nativeRunner) scheduleRecovery(ctx context.Context, p nativeRunnerProject, tasks []nativeRunnerTask) error {
	backend, ok := r.backend.(nativeRunnerRecoveryBackend)
	if !ok || p.Paused || p.Archived || p.State == "canceling" || p.State == "cancelled" || r.cooldownUntil > r.now().Unix() {
		return nil
	}
	if r.recoveryInFlight == nil {
		r.recoveryInFlight = map[string]bool{}
		r.recoveryDone = make(chan nativeRecoveryCompletion, 8)
	}
	for _, t := range tasks {
		if !nativeHolds(t) || t.Cancellation != nil || t.Archived || t.Receipt == nil || t.Receipt.InDoubt || t.Request == nil {
			continue
		}
		if t.Recovery == nil {
			state := r.recoveryState(t)
			t.Recovery = &state
			if err := r.saveTask(ctx, t, "recovery_registered"); err != nil {
				return err
			}
			continue
		}
		if r.recoveryInFlight[t.ID] || len(r.recoveryInFlight) >= 8 || t.Recovery.NextProbeAt > r.now().Unix() {
			continue
		}
		state := *t.Recovery
		if state.PendingKey == "" {
			state.Phase = "probing"
		} else {
			state.Phase = "continuing"
		}
		state.NextProbeAt = r.now().Add(nativeProgressInterval).Unix()
		t.Recovery = &state
		if err := r.saveTask(ctx, t, ""); err != nil {
			return err
		}
		r.recoveryInFlight[t.ID] = true
		r.recoveryWG.Add(1)
		go func(t nativeRunnerTask) {
			defer r.recoveryWG.Done()
			callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			out := nativeRecoveryCompletion{Task: t, Jobs: map[string]nativeRunnerCheckResult{}}
			if t.Recovery.PendingInterrupt {
				if interrupt, ok := r.backend.(nativeRunnerInterruptBackend); ok {
					out.Probe, out.Err = interrupt.Interrupt(callCtx, t)
				} else {
					out.Err = errors.New("cloud interruption unavailable; preserving writer")
				}
			} else if t.Recovery.PendingKey != "" {
				out.Continued = true
				out.Err = backend.Continue(callCtx, t, t.Recovery.PendingPrompt, t.Recovery.PendingKey)
			} else {
				jobsTerminal := true
				for _, job := range t.Recovery.Checkpoint.WaitingJobs {
					v, err := r.backend.WatchCheck(callCtx, job)
					if err != nil {
						out.Err = err
						break
					}
					out.Jobs[job] = v
					if v.State != "completed" && v.State != "failed" && v.State != "canceled" {
						jobsTerminal = false
					}
				}
				if out.Err == nil && jobsTerminal {
					out.Probe, out.Err = backend.Probe(callCtx, t)
				}
			}
			r.recoveryDone <- out
			r.Wake()
		}(t)
	}
	return nil
}

func (r *nativeRunner) drainRecovery(ctx context.Context) error {
	for {
		select {
		case out := <-r.recoveryDone:
			delete(r.recoveryInFlight, out.Task.ID)
			_, tasks, err := r.read(ctx, out.Task.ProjectID)
			if err != nil {
				return err
			}
			for _, t := range tasks {
				if t.ID != out.Task.ID || t.Round != out.Task.Round || t.Cancellation != nil || !nativeHolds(t) || t.Receipt == nil || t.Receipt.SessionID != out.Task.Receipt.SessionID {
					continue
				}
				// A checkpoint received during a probe is newer authority.
				if nativeHash(t.Recovery) != nativeHash(out.Task.Recovery) {
					continue
				}
				if err = r.applyRecovery(ctx, &t, out); err != nil {
					return err
				}
			}
		default:
			return nil
		}
	}
}

func (r *nativeRunner) applyRecovery(ctx context.Context, t *nativeRunnerTask, out nativeRecoveryCompletion) error {
	state := *t.Recovery
	state.LastError = ""
	state.Phase = "watching"
	state.NextProbeAt = r.now().Add(nativeProgressInterval).Unix()
	if out.Err != nil {
		if errors.Is(out.Err, errNativeRunnerOperationPending) {
			state.Phase = "waiting_job"
			state.NextProbeAt = r.now().Add(nativeJobPollInterval).Unix()
			t.Recovery = &state
			return r.saveTask(ctx, *t, "")
		}
		var exhausted *nativeRunnerContextExhaustedError
		if errors.As(out.Err, &exhausted) {
			state.ContextExhausted = true
			state.PendingKey = ""
			state.PendingPrompt = ""
			state.PendingInterrupt = false
			state.NextProbeAt = r.now().Unix()
			state.Phase = "probing"
			state.LastError = out.Err.Error()
			t.Recovery = &state
			return r.saveTask(ctx, *t, "context_handover_requested")
		}
		state.LastError = out.Err.Error()
		state.Phase = "uncertain"
		// Preserve a frozen continuation key after an ambiguous send.
		t.Recovery = &state
		return r.saveTask(ctx, *t, "recovery_error")
	}
	if out.Continued {
		state.Attempts++
		state.LastContinuedProgressKey = state.ProgressKey
		state.Manual = false
		state.PendingKey = ""
		state.PendingPrompt = ""
		state.Checkpoint.WaitingJobs = nil
		t.Recovery = &state
		return r.saveTask(ctx, *t, "task_continued")
	}
	var jobEvidence []string
	for _, id := range state.Checkpoint.WaitingJobs {
		v := out.Jobs[id]
		if v.State != "completed" && v.State != "failed" && v.State != "canceled" {
			state.Phase = "waiting_job"
			if v.State == "unknown" {
				state.Phase = "uncertain"
				state.LastError = "Job execution identity remains unknown: " + id
			}
			state.NextProbeAt = r.now().Add(nativeJobPollInterval).Unix()
			t.Recovery = &state
			return r.saveTask(ctx, *t, "")
		}
		jobEvidence = append(jobEvidence, fmt.Sprintf("%s: %s exit=%d %s", id, v.State, v.ExitCode, v.Evidence))
	}
	probe := out.Probe
	state.PendingInterrupt = false
	if probe.ProgressKey != "" && probe.ProgressKey != state.ProgressKey {
		state.ProgressKey = probe.ProgressKey
		state.LastProgressAt = r.now().Unix()
	}
	staleUnknown := !probe.Authoritative && probe.ProgressKey != "" && r.now().Unix()-state.LastProgressAt >= int64((2*nativeProgressInterval)/time.Second) && state.Attempts < 2 && state.LastContinuedProgressKey != probe.ProgressKey
	// Same-session continuation retains the writer and uses the provider's
	// existing idempotent turn submission. Unknown never authorizes rotation.
	continueSameSession := !state.ContextExhausted && (state.Manual || staleUnknown || (len(jobEvidence) > 0 && probe.ProgressKey != "" && !probe.Running))
	if (!probe.Authoritative || !probe.Terminal) && !continueSameSession {
		if probe.Authoritative && probe.Running && state.ProgressKey != "" && r.now().Unix()-state.LastProgressAt >= int64((2*nativeProgressInterval)/time.Second) && len(state.Checkpoint.WaitingJobs) == 0 {
			state.PendingInterrupt = true
			state.Phase = "probing"
			state.NextProbeAt = r.now().Unix()
			state.LastError = "Cloud activity unchanged across the recovery window; verifying cancellation before continuation"
		}
		if !probe.Running {
			state.Phase = "uncertain"
			state.LastError = "Cloud execution state is not authoritative; preserving the current writer binding"
		}
		t.Recovery = &state
		return r.saveTask(ctx, *t, "")
	}
	// An ended turn alone is not a business result. Preserve original task and
	// callback identity until a submitted report is consumed by Observe.
	if probe.ObservedAt <= 0 || r.now().Unix()-probe.ObservedAt > 60 {
		state.Phase = "uncertain"
		state.LastError = "Terminal observation expired before handover/continuation"
		t.Recovery = &state
		return r.saveTask(ctx, *t, "recovery_error")
	}
	if probe.Authoritative && probe.Terminal && (state.ContextExhausted || probe.ContextExhausted || (probe.ContextBytesKnown && probe.ContextBytes >= nativeContextHandoverBytes) || state.Attempts >= 2 || state.Checkpoint.Stage == "context_handover") {
		state.Phase = "handover"
		t.History = append(t.History, nativeRunnerAttempt{Round: t.Round, GoalVersion: t.GoalVersion, Request: t.Request, Receipt: t.Receipt, Result: t.Result, Correction: t.Correction, Acked: t.ResultAcked, Superseded: true, InactiveProof: &nativeRunnerInactiveProof{SessionID: t.Receipt.SessionID, Round: t.Round, ProgressKey: probe.ProgressKey, ObservedAt: probe.ObservedAt, Terminal: true}})
		t.Round++
		t.StartedAt = 0
		t.State = "queued"
		t.Rotate = true
		t.Request = nil
		t.Receipt = nil
		t.Result = nil
		t.ResultAcked = false
		t.NextAt = 0
		t.Validations = nil
		t.Correction = "Continue the same authorized task from its persisted checkpoint and prior files. The previous cloud turn is terminal. Do not repeat completed work. " + state.Checkpoint.Summary + " Next: " + state.Checkpoint.NextStep
		state.Attempts = 0
		state.ContextExhausted = false
		state.ProgressKey = ""
		state.NextProbeAt = r.now().Add(nativeProgressInterval).Unix()
		state.Checkpoint.Stage = "handover_ready"
		t.Recovery = &state
		return r.saveTask(ctx, *t, "task_handover")
	}
	prompt := "This task has no submitted terminal result and a targeted progress check requires continuation. Continue this SAME task block within its original scope. First inspect existing files and jobs, avoid repeating completed builds or writes. Persist a concise checkpoint after each meaningful milestone. If the result is already ready, submit the ORIGINAL bound taskRef. Do not create another CHAT.\nCheckpoint: " + state.Checkpoint.Summary + "\nNext: " + state.Checkpoint.NextStep + "\nProbe: " + probe.Summary + "\nCompleted jobs:\n" + strings.Join(jobEvidence, "\n")
	if len(jobEvidence) > 0 {
		state.Checkpoint.Evidence = append(state.Checkpoint.Evidence, jobEvidence...)
		if len(state.Checkpoint.Evidence) > 24 {
			state.Checkpoint.Evidence = state.Checkpoint.Evidence[len(state.Checkpoint.Evidence)-24:]
		}
	}
	state.PendingKey = "nr-resume-" + nativeHash([]any{t.ID, t.Round, state.Attempts, state.ProgressKey, state.Checkpoint})[:48]
	state.PendingPrompt = prompt
	state.Phase = "continuing"
	state.NextProbeAt = r.now().Unix()
	t.Recovery = &state
	return r.saveTask(ctx, *t, "continuation_scheduled")
}
