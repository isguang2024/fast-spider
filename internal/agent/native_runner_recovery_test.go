package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

type recoveryTestBackend struct {
	*fakeNativeRunnerBackend
	gate chan struct{}
}

type blockedObserveBackend struct {
	*fakeNativeRunnerBackend
	gate chan struct{}
}

func (b *blockedObserveBackend) Observe(ctx context.Context, t nativeRunnerTask) (*nativeRunnerResult, error) {
	select {
	case <-b.gate:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestNativeRecoveryActualTickDoesNotHoldLockDuringProviderWait(t *testing.T) {
	r, _, _ := recoveryFixture(t)
	b := &blockedObserveBackend{fakeNativeRunnerBackend: newFakeNativeRunnerBackend(), gate: make(chan struct{})}
	t.Cleanup(func() { close(b.gate) })
	r.asyncBackend = newNativeRunnerAsyncBackend(b, r.Wake)
	r.backend = r.asyncBackend
	started := time.Now()
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("tick waited for provider")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.checkpoint(ctx, "project", "task", 1, "chat", nativeRunnerCheckpoint{Summary: "independent progress"}); err != nil {
		t.Fatal(err)
	}
}

func (b *recoveryTestBackend) Probe(ctx context.Context, _ nativeRunnerTask) (nativeRunnerProbe, error) {
	if b.gate != nil {
		select {
		case <-b.gate:
		case <-ctx.Done():
			return nativeRunnerProbe{}, ctx.Err()
		}
	}
	return nativeRunnerProbe{Authoritative: true, Terminal: true, ObservedAt: time.Now().Unix(), ProgressKey: "terminal"}, nil
}
func (b *recoveryTestBackend) Continue(context.Context, nativeRunnerTask, string, string) error {
	return nil
}

func recoveryFixture(t *testing.T) (*nativeRunner, nativeRunnerTask, *recoveryTestBackend) {
	t.Helper()
	ctx := context.Background()
	b := &recoveryTestBackend{fakeNativeRunnerBackend: newFakeNativeRunnerBackend()}
	r, err := newNativeRunner(t.TempDir(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	p := nativeRunnerProject{ID: "project", GoalVersion: "v1", Root: t.TempDir()}
	state := nativeRunnerRecovery{Phase: "watching", LastProgressAt: time.Now().Add(-time.Hour).Unix()}
	task := nativeRunnerTask{ID: "task", ProjectID: p.ID, Kind: "work", Round: 1, GoalVersion: "v1", State: "active", Recovery: &state, Request: &nativeRunnerDispatch{ProjectID: p.ID, TaskID: "task", Round: 1}, Receipt: &nativeRunnerReceipt{SessionID: "chat", TaskRef: "binding", Generation: 1, ResultPath: "result"}}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = nativeSave(tx, "runner_projects", p.ID, "", p); err != nil {
		t.Fatal(err)
	}
	if err = nativeSave(tx, "runner_tasks", task.ID, p.ID, task); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return r, task, b
}

func TestNativeRecoveryPreservesUnknownWriterAndDeduplicatesContinuation(t *testing.T) {
	r, task, _ := recoveryFixture(t)
	ctx := context.Background()
	if err := r.applyRecovery(ctx, &task, nativeRecoveryCompletion{Probe: nativeRunnerProbe{Terminal: true}}); err != nil {
		t.Fatal(err)
	}
	if task.Recovery.PendingKey != "" || task.Round != 1 {
		t.Fatal("non-authoritative observation released writer")
	}
	probe := nativeRunnerProbe{Authoritative: true, Terminal: true, ObservedAt: r.now().Unix(), ProgressKey: "end"}
	if err := r.applyRecovery(ctx, &task, nativeRecoveryCompletion{Probe: probe}); err != nil {
		t.Fatal(err)
	}
	key := task.Recovery.PendingKey
	prompt := task.Recovery.PendingPrompt
	if key == "" {
		t.Fatal("no durable continuation intent")
	}
	if err := r.applyRecovery(ctx, &task, nativeRecoveryCompletion{Continued: true, Err: errors.New("ambiguous network result")}); err != nil {
		t.Fatal(err)
	}
	if task.Recovery.PendingKey != key || task.Recovery.PendingPrompt != prompt {
		t.Fatal("uncertain send changed frozen intent")
	}
	_, saved, err := r.read(ctx, "project")
	if err != nil || saved[0].Recovery.PendingKey != key {
		t.Fatalf("intent not durable: %v", err)
	}
}

func TestNativeRecoveryHandoverPreservesOldBindingAndRequiresTerminal(t *testing.T) {
	r, task, _ := recoveryFixture(t)
	ctx := context.Background()
	task.Recovery.Attempts = 2
	if err := r.applyRecovery(ctx, &task, nativeRecoveryCompletion{Probe: nativeRunnerProbe{Authoritative: true, Running: true}}); err != nil {
		t.Fatal(err)
	}
	if task.Round != 1 {
		t.Fatal("rotated a running writer")
	}
	if err := r.applyRecovery(ctx, &task, nativeRecoveryCompletion{Probe: nativeRunnerProbe{Authoritative: true, Terminal: true, ObservedAt: r.now().Unix()}}); err != nil {
		t.Fatal(err)
	}
	if task.Round != 2 || !task.Rotate || task.State != "queued" || task.Receipt != nil || len(task.History) != 1 {
		t.Fatalf("bad handover: %+v", task)
	}
	h := task.History[0]
	if h.Receipt.SessionID != "chat" || h.InactiveProof.SessionID != "chat" || h.Acked || nativeHistoryBlocksDispatch(h) {
		t.Fatal("old binding/proof not retained or falsely acknowledged")
	}
}

func TestNativeRecoveryWaitsForJobsWithoutContinuation(t *testing.T) {
	r, task, _ := recoveryFixture(t)
	task.Recovery.Checkpoint.WaitingJobs = []string{"build"}
	err := r.applyRecovery(context.Background(), &task, nativeRecoveryCompletion{Jobs: map[string]nativeRunnerCheckResult{"build": {State: "running"}}, Probe: nativeRunnerProbe{Authoritative: true, Terminal: true, ObservedAt: r.now().Unix()}})
	if err != nil || task.Recovery.Phase != "waiting_job" || task.Recovery.PendingKey != "" {
		t.Fatalf("job was not awaited: %v %+v", err, task.Recovery)
	}
}

func TestNativeRecoveryStalledRunningTurnRequiresVerifiedInterruption(t *testing.T) {
	r, task, _ := recoveryFixture(t)
	task.Recovery.ProgressKey = "unchanged"
	task.Recovery.LastProgressAt = r.now().Add(-time.Hour).Unix()
	probe := nativeRunnerProbe{Authoritative: true, Running: true, ProgressKey: "unchanged", ObservedAt: r.now().Unix()}
	if err := r.applyRecovery(context.Background(), &task, nativeRecoveryCompletion{Probe: probe}); err != nil {
		t.Fatal(err)
	}
	if !task.Recovery.PendingInterrupt || task.Recovery.PendingKey != "" || task.Round != 1 {
		t.Fatal("stalled turn was continued/rotated without stopping its writer")
	}
	if err := r.applyRecovery(context.Background(), &task, nativeRecoveryCompletion{Err: errors.New("cancel uncertain")}); err != nil {
		t.Fatal(err)
	}
	if !task.Recovery.PendingInterrupt || task.Round != 1 {
		t.Fatal("uncertain cancellation released writer")
	}
}

func TestNativeRecoveryContextRejectionRequestsFreshProbe(t *testing.T) {
	r, task, _ := recoveryFixture(t)
	task.Recovery.PendingKey = "original-key"
	task.Recovery.PendingPrompt = "continue"
	err := r.applyRecovery(context.Background(), &task, nativeRecoveryCompletion{Continued: true, Err: &nativeRunnerContextExhaustedError{err: errors.New("context_length_exceeded")}})
	if err != nil || task.Recovery.PendingKey != "" || !task.Recovery.ContextExhausted || task.Round != 1 {
		t.Fatalf("context rejection did not preserve writer for fresh probe: %v", err)
	}
	if err = r.applyRecovery(context.Background(), &task, nativeRecoveryCompletion{Probe: nativeRunnerProbe{ObservedAt: r.now().Unix(), ProgressKey: "unknown"}}); err != nil {
		t.Fatal(err)
	}
	if task.Round != 1 || task.Recovery.PendingKey != "" {
		t.Fatal("unknown state allowed handover or another rejected send")
	}
	if err = r.applyRecovery(context.Background(), &task, nativeRecoveryCompletion{Probe: nativeRunnerProbe{Authoritative: true, Terminal: true, ObservedAt: r.now().Unix()}}); err != nil {
		t.Fatal(err)
	}
	if task.Round != 2 || !task.Rotate {
		t.Fatal("fresh terminal proof did not enable context handover")
	}
}

func TestNativeRecoverySignalCanContinueUnknownButCannotRotate(t *testing.T) {
	r, task, _ := recoveryFixture(t)
	if _, err := r.Handle(context.Background(), "runner.signal", map[string]any{"projectId": "project", "taskId": "task", "evidence": "User verified this exact conversation is stalled"}); err != nil {
		t.Fatal(err)
	}
	_, tasks, err := r.read(context.Background(), "project")
	if err != nil {
		t.Fatal(err)
	}
	task = tasks[0]
	if err = r.applyRecovery(context.Background(), &task, nativeRecoveryCompletion{Probe: nativeRunnerProbe{ProgressKey: "stalled-tool", ObservedAt: r.now().Unix()}}); err != nil {
		t.Fatal(err)
	}
	if task.Recovery.PendingKey == "" || task.Round != 1 || task.Receipt.SessionID != "chat" {
		t.Fatal("signal did not schedule same-session continuation with original writer")
	}
}

func TestNativeRecoveryDoesNotRaceUncertainDispatch(t *testing.T) {
	r, task, b := recoveryFixture(t)
	task.Receipt.InDoubt = true
	b.gate = make(chan struct{})
	if err := r.scheduleRecovery(context.Background(), nativeRunnerProject{ID: "project"}, []nativeRunnerTask{task}); err != nil {
		t.Fatal(err)
	}
	if len(r.recoveryInFlight) != 0 {
		close(b.gate)
		t.Fatal("recovery raced uncertain dispatch replay")
	}
	close(b.gate)
}

func TestNativeRecoveryProbeDoesNotBlockCheckpointAndIgnoresStaleCompletion(t *testing.T) {
	r, task, b := recoveryFixture(t)
	b.gate = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.scheduleRecovery(ctx, nativeRunnerProject{ID: "project"}, []nativeRunnerTask{task}); err != nil {
		t.Fatal(err)
	}
	checkpoint := nativeRunnerCheckpoint{Summary: "build started", NextStep: "browser validation", WaitingJobs: []string{"build"}}
	if err := r.checkpoint(ctx, "project", "task", 1, "wrong-session", checkpoint); err == nil {
		t.Fatal("wrong owner accepted")
	}
	if err := r.checkpoint(ctx, "project", "task", 1, "chat", checkpoint); err != nil {
		t.Fatal(err)
	}
	close(b.gate)
	r.recoveryWG.Wait()
	if err := r.drainRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	_, tasks, err := r.read(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	if tasks[0].Recovery.Checkpoint.Summary != "build started" || tasks[0].Recovery.PendingKey != "" {
		t.Fatal("stale probe overrode newer checkpoint")
	}
}

func TestNativeRecoveryPacketDoesNotRecursivelyCopyPriorPrompts(t *testing.T) {
	task := nativeRunnerTask{Request: &nativeRunnerDispatch{Prompt: "frozen credential prompt"}, History: []nativeRunnerAttempt{{Request: &nativeRunnerDispatch{Prompt: "old prompt"}, Receipt: &nativeRunnerReceipt{SessionID: "old"}}}}
	packet := nativePacketTask(task)
	if packet.Request != nil || packet.History[0].Request != nil || packet.History[0].Receipt.SessionID != "old" || task.History[0].Request == nil {
		t.Fatal("packet projection lost references or changed ledger history")
	}
}
