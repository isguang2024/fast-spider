package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type nativeRunnerOwnedJobExecutor interface {
	CancelOwned(context.Context, string, string) (nativeRunnerJobSnapshot, error)
}

// nativeRunnerCheckIdempotencyKey is the immutable key used by StartCheck.
// Cancellation must derive the same key from the task generation rather than
// trusting a job ID reported by a Cloud checkpoint.
func nativeRunnerCheckIdempotencyKey(taskID string, round int, name string) string {
	return "nr-check-" + nativeHash([]any{taskID, round, name})[:48]
}

// Stop cancels the exact Cloud writer and waits for fresh terminal proof. It
// also cancels only validation jobs owned by their derived check key. Jobs
// named by a recovery checkpoint are observed to terminal but never cancelled
// because the checkpoint alone does not prove process ownership.
func (t *nativeRunnerTransport) Stop(ctx context.Context, task nativeRunnerTask) (nativeRunnerStopResult, error) {
	var out nativeRunnerStopResult
	if t == nil {
		return out, nodeRunnerUnavailableError()
	}
	stopCtx, cancel := context.WithTimeout(ctx, nativeRunnerProbeTimeout)
	defer cancel()

	var errs []error
	cloudStopped := true
	if task.Result != nil && task.Result.Terminal && !task.Result.RecoveryOnly {
		if task.Receipt != nil {
			out.Proof = &nativeRunnerInactiveProof{SessionID: task.Receipt.SessionID, Round: task.Round, ProgressKey: "formal-result:" + task.Result.EventID, ObservedAt: time.Now().UTC().Unix(), Terminal: true}
		}
	} else if task.Request != nil || task.Receipt != nil {
		cloudStopped = false
		if task.Request == nil || task.Receipt == nil {
			errs = append(errs, errors.New("runner stop has an incomplete immutable Cloud binding"))
		} else {
			probe, err := t.Interrupt(stopCtx, task)
			if err != nil {
				errs = append(errs, err)
			} else if !probe.Authoritative || !probe.Terminal {
				// Interrupt currently enforces this itself. Keep the check here so
				// a future interrupt implementation cannot turn an unknown read
				// (including a nil/403-shaped response) into proof.
				errs = append(errs, errors.New("Cloud cancellation has no fresh authoritative terminal proof"))
			} else {
				cloudStopped = true
				out.Proof = &nativeRunnerInactiveProof{SessionID: task.Receipt.SessionID, Round: task.Round, ProgressKey: probe.ProgressKey, ObservedAt: probe.ObservedAt, Terminal: true}
			}
		}
	}

	jobsToWatch := map[string]bool{}
	validationNames := make([]string, 0, len(task.Validations))
	for name := range task.Validations {
		validationNames = append(validationNames, name)
	}
	sort.Strings(validationNames)
	if len(validationNames) > 0 {
		jobs, err := t.ensureJobs()
		if err != nil {
			errs = append(errs, err)
		} else {
			executor, canCancel := jobs.executor.(nativeRunnerOwnedJobExecutor)
			for _, name := range validationNames {
				validation := task.Validations[name]
				jobID := strings.TrimSpace(validation.JobID)
				if jobID == "" {
					continue
				}
				key := nativeRunnerCheckIdempotencyKey(task.ID, task.Round, name)
				if !canCancel {
					errs = append(errs, fmt.Errorf("validation job %s cannot prove durable ownership", jobID))
					continue
				}
				if _, err = executor.CancelOwned(stopCtx, jobID, key); err != nil {
					errs = append(errs, fmt.Errorf("cancel validation job %s: %w", jobID, err))
					continue
				}
				jobsToWatch[jobID] = true
			}
		}
	}

	if task.Recovery != nil {
		for _, rawID := range task.Recovery.Checkpoint.WaitingJobs {
			jobID := strings.TrimSpace(rawID)
			if jobID != "" {
				jobsToWatch[jobID] = true
			}
		}
	}
	if len(jobsToWatch) > 0 {
		jobs, err := t.ensureJobs()
		if err != nil {
			errs = append(errs, err)
		} else {
			jobIDs := make([]string, 0, len(jobsToWatch))
			for jobID := range jobsToWatch {
				jobIDs = append(jobIDs, jobID)
			}
			sort.Strings(jobIDs)
			for _, jobID := range jobIDs {
				result, watchErr := jobs.WatchCheck(stopCtx, jobID)
				if watchErr != nil {
					out.WaitingJobs = append(out.WaitingJobs, jobID)
					errs = append(errs, fmt.Errorf("watch stopped job %s: %w", jobID, watchErr))
					continue
				}
				switch result.State {
				case "completed", "failed", "canceled":
				default:
					out.WaitingJobs = append(out.WaitingJobs, jobID)
				}
			}
		}
	}

	out.Stopped = cloudStopped && len(out.WaitingJobs) == 0 && len(errs) == 0
	switch {
	case out.Stopped && out.Proof != nil:
		out.Summary = "Cloud writer has fresh authoritative terminal proof"
	case out.Stopped:
		out.Summary = "Cloud writer was already inactive and owned jobs are terminal"
	case len(out.WaitingJobs) > 0:
		out.Summary = "Cancellation is waiting for jobs to reach a terminal state: " + strings.Join(out.WaitingJobs, ", ")
	default:
		out.Summary = "Cancellation did not obtain terminal proof; preserve the existing writer binding"
	}
	return out, errors.Join(errs...)
}
