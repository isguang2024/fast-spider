package agent

import (
	"context"
	"errors"
	"testing"
)

type lifecycleTestBackend struct {
	*fakeNativeRunnerBackend
	stopResult nativeRunnerStopResult
	stopErr    error
	stopCalls  int
}

func (b *lifecycleTestBackend) Stop(context.Context, nativeRunnerTask) (nativeRunnerStopResult, error) {
	b.stopCalls++
	return b.stopResult, b.stopErr
}

func lifecycleTaskFixture(t *testing.T) (*nativeRunner, *fakeNativeRunnerBackend, nativeRunnerProject, nativeRunnerTask) {
	t.Helper()
	runner, backend, project := newNativeRunnerForTest(t, "lifecycle goal", nil)
	task := addNativeTask(t, runner, project.ID, "lifecycle-task", "src/lifecycle")
	task.Request = &nativeRunnerDispatch{
		ProjectID: project.ID, TaskID: task.ID, Round: task.Round,
		ControllerSessionID: project.ControllerSessionID, ResultPath: "result",
	}
	task.Receipt = &nativeRunnerReceipt{
		SessionID: "lifecycle-chat", TaskRef: nativeRunnerTaskRef(*task.Request),
		Generation: int64(task.Round), ResultPath: task.Request.ResultPath,
	}
	task.State = "active"
	saveNativeTasks(t, runner, project.ID, func(current *nativeRunnerTask) {
		if current.ID == task.ID {
			*current = task
		}
	})
	return runner, backend, project, task
}

func lifecycleCancelTask(task *nativeRunnerTask, revision int64, now int64, target *nativeRunnerTaskUpdate) {
	task.Cancellation = &nativeRunnerCancellation{Reason: "user requested cancellation", Revision: revision, RequestedAt: now, Target: target}
	task.State = "canceling"
}

func lifecycleProof(task nativeRunnerTask, observedAt int64) *nativeRunnerInactiveProof {
	return &nativeRunnerInactiveProof{SessionID: task.Receipt.SessionID, Round: task.Round, ObservedAt: observedAt, Terminal: true}
}

func TestNativeRunnerLifecycleActiveCancelWithoutProofKeepsCanceling(t *testing.T) {
	runner, _, project, task := lifecycleTaskFixture(t)
	now := runner.now().Unix()
	lifecycleCancelTask(&task, project.Revision, now, nil)
	task.Recovery = &nativeRunnerRecovery{Checkpoint: nativeRunnerCheckpoint{WaitingJobs: []string{"job-running"}}}
	if err := runner.applyLifecycle(context.Background(), project, &task, nativeRunnerStopResult{Summary: "job still running", WaitingJobs: []string{"job-running"}}, nil); err != nil {
		t.Fatal(err)
	}
	if task.State != "canceling" || task.Cancellation.Failures != 1 || len(task.Cancellation.WaitingJobs) != 1 || task.Cancellation.LastError != "job still running" {
		t.Fatalf("cancellation released without proof: %+v", task)
	}
}

func TestNativeRunnerLifecycleExactProofTransitionsToCancelled(t *testing.T) {
	runner, _, project, task := lifecycleTaskFixture(t)
	now := runner.now().Unix()
	lifecycleCancelTask(&task, project.Revision, now, nil)
	if err := runner.applyLifecycle(context.Background(), project, &task, nativeRunnerStopResult{Stopped: true, Proof: lifecycleProof(task, now)}, nil); err != nil {
		t.Fatal(err)
	}
	if task.State != "cancelled" || task.Cancellation.Proof == nil || !task.Cancellation.Proof.Terminal || task.Cancellation.LastError != "" {
		t.Fatalf("exact proof did not cancel task: %+v", task)
	}
}

func TestNativeRunnerLifecycleLateResultDoesNotReviveCancelledTask(t *testing.T) {
	runner, backend, project, task := lifecycleTaskFixture(t)
	now := runner.now().Unix()
	lifecycleCancelTask(&task, project.Revision, now, nil)
	task.State = "cancelled"
	backend.observed[task.ID] = []nativeRunnerResult{{EventID: "late-result", Outcome: "completed", Terminal: true}}
	if err := runner.saveTask(context.Background(), task, "test_cancelled"); err != nil {
		t.Fatal(err)
	}
	if err := runner.tickLifecycle(context.Background(), project, nil); err != nil {
		t.Fatal(err)
	}
	saved := loadNativeTask(t, runner, project.ID, task.ID)
	if saved.State != "cancelled" || saved.Result == nil || saved.Result.EventID != "late-result" {
		t.Fatalf("late result revived or disappeared: %+v", saved)
	}
}

func TestNativeRunnerLifecycleRedirectPreservesHistoryAndChangesRound(t *testing.T) {
	runner, _, project, task := lifecycleTaskFixture(t)
	now := runner.now().Unix()
	target := &nativeRunnerTaskUpdate{Objective: "continue with revised objective", Acceptance: "new evidence", Scope: "src/new", Context: []string{"README.md"}, After: []string{"dependency"}, Priority: 4, GoalVersion: "goal-v2"}
	lifecycleCancelTask(&task, project.Revision, now, target)
	proof := lifecycleProof(task, now)
	oldRound, oldSession := task.Round, task.Receipt.SessionID
	if err := runner.applyLifecycle(context.Background(), project, &task, nativeRunnerStopResult{Stopped: true, Proof: proof}, nil); err != nil {
		t.Fatal(err)
	}
	if task.State != "queued" || task.Round != oldRound+1 || task.Request != nil || task.Receipt != nil || task.Cancellation != nil || !task.Rotate || len(task.History) != 1 {
		t.Fatalf("redirect did not create a new generation: %+v", task)
	}
	if history := task.History[0]; history.Round != oldRound || history.Receipt == nil || history.Receipt.SessionID != oldSession || history.InactiveProof == nil || !history.Superseded {
		t.Fatalf("old binding was not retained in history: %+v", history)
	}
	if task.Objective != target.Objective || task.Acceptance != target.Acceptance || task.Scope != target.Scope || task.GoalVersion != target.GoalVersion || task.Priority != target.Priority {
		t.Fatalf("redirect target was not applied: %+v", task)
	}
}

func TestNativeRunnerLifecycleRevisionChangeWaitsForPendingPlan(t *testing.T) {
	runner, _, project, task := lifecycleTaskFixture(t)
	now := runner.now().Unix()
	target := &nativeRunnerTaskUpdate{Objective: "new objective", Acceptance: "new acceptance", GoalVersion: "goal-v2"}
	lifecycleCancelTask(&task, project.Revision-1, now, target)
	if err := runner.applyLifecycle(context.Background(), project, &task, nativeRunnerStopResult{Stopped: true, Proof: lifecycleProof(task, now)}, nil); err != nil {
		t.Fatal(err)
	}
	if task.State != "pending_plan" || task.Round != 2 || task.Cancellation != nil || task.Request != nil || task.Receipt != nil {
		t.Fatalf("revision mismatch did not fence redirect: %+v", task)
	}
}

func TestNativeRunnerLifecycleRecoveryInFlightBlocksStop(t *testing.T) {
	runner, backend, project, task := lifecycleTaskFixture(t)
	stopBackend := &lifecycleTestBackend{fakeNativeRunnerBackend: backend, stopResult: nativeRunnerStopResult{Stopped: true, Proof: lifecycleProof(task, runner.now().Unix())}}
	runner.backend = stopBackend
	now := runner.now().Unix()
	lifecycleCancelTask(&task, project.Revision, now, nil)
	if err := runner.saveTask(context.Background(), task, "test_canceling"); err != nil {
		t.Fatal(err)
	}
	runner.recoveryInFlight = map[string]bool{task.ID: true}
	if err := runner.tickLifecycle(context.Background(), project, nil); err != nil {
		t.Fatal(err)
	}
	if stopBackend.stopCalls != 0 || len(runner.lifecycleInFlight) != 0 {
		t.Fatalf("recovery in flight did not block Stop: calls=%d lifecycle=%v", stopBackend.stopCalls, runner.lifecycleInFlight)
	}
}

func TestNativeRunnerLifecycleRejectsStaleProof(t *testing.T) {
	runner, _, project, task := lifecycleTaskFixture(t)
	now := runner.now().Unix()
	lifecycleCancelTask(&task, project.Revision, now, nil)
	stale := lifecycleProof(task, now-61)
	if err := runner.applyLifecycle(context.Background(), project, &task, nativeRunnerStopResult{Stopped: true, Proof: stale}, nil); err != nil {
		t.Fatal(err)
	}
	if task.State != "canceling" || task.Cancellation.Failures != 1 || task.Cancellation.LastError == "" {
		t.Fatalf("stale proof was accepted: %+v", task)
	}
}

func TestNativeRunnerLifecycleStopErrorRetainsBinding(t *testing.T) {
	runner, _, project, task := lifecycleTaskFixture(t)
	now := runner.now().Unix()
	lifecycleCancelTask(&task, project.Revision, now, nil)
	stopErr := errors.New("cancel transport unavailable")
	if err := runner.applyLifecycle(context.Background(), project, &task, nativeRunnerStopResult{}, stopErr); err != nil {
		t.Fatal(err)
	}
	if task.State != "canceling" || task.Request == nil || task.Receipt == nil || task.Cancellation.LastError != stopErr.Error() {
		t.Fatalf("Stop error lost active binding: %+v", task)
	}
}
