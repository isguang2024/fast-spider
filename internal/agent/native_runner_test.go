package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeNativeRunnerBackend struct {
	mu          sync.Mutex
	dispatches  []nativeRunnerDispatch
	dispatchErr map[string]error
	inDoubt     map[string]int
	observed    map[string][]nativeRunnerResult
	checks      []nativeRunnerCheckRequest
	jobs        map[string][]nativeRunnerCheckResult
	jobResults  map[string]nativeRunnerCheckResult
	acks        []nativeRunnerTask
	ackErr      error
}

func newFakeNativeRunnerBackend() *fakeNativeRunnerBackend {
	return &fakeNativeRunnerBackend{
		dispatchErr: map[string]error{},
		inDoubt:     map[string]int{},
		observed:    map[string][]nativeRunnerResult{},
		jobs:        map[string][]nativeRunnerCheckResult{},
		jobResults:  map[string]nativeRunnerCheckResult{},
	}
}

func (b *fakeNativeRunnerBackend) Dispatch(_ context.Context, request nativeRunnerDispatch) (nativeRunnerReceipt, error) {
	b.mu.Lock()
	b.dispatches = append(b.dispatches, request)
	err := b.dispatchErr[request.TaskID]
	uncertain := b.inDoubt[request.TaskID] > 0
	if uncertain {
		b.inDoubt[request.TaskID]--
	}
	b.mu.Unlock()
	if err != nil {
		return nativeRunnerReceipt{}, err
	}
	if err := os.MkdirAll(filepath.Dir(request.ResultPath), 0o700); err != nil {
		return nativeRunnerReceipt{}, err
	}
	if err := os.WriteFile(request.ResultPath, []byte("worker report"), 0o600); err != nil {
		return nativeRunnerReceipt{}, err
	}
	return nativeRunnerReceipt{
		SessionID:  "session-" + request.TaskID,
		TaskRef:    "ref-" + request.TaskID,
		Generation: int64(request.Round),
		ResultPath: request.ResultPath,
		InDoubt:    uncertain,
	}, nil
}

func (b *fakeNativeRunnerBackend) Observe(_ context.Context, task nativeRunnerTask) (*nativeRunnerResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	items := b.observed[task.ID]
	if len(items) == 0 {
		return nil, nil
	}
	result := items[0]
	b.observed[task.ID] = items[1:]
	if result.Path == "" && task.Receipt != nil {
		result.Path = task.Receipt.ResultPath
	}
	return &result, nil
}

func (b *fakeNativeRunnerBackend) StartCheck(_ context.Context, request nativeRunnerCheckRequest) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.checks = append(b.checks, request)
	jobID := "job-" + request.TaskID + "-" + request.Name
	if _, ok := b.jobResults[jobID]; !ok {
		b.jobResults[jobID] = nativeRunnerCheckResult{State: "running"}
	}
	return jobID, nil
}

func (b *fakeNativeRunnerBackend) WatchCheck(_ context.Context, jobID string) (nativeRunnerCheckResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if queued := b.jobs[jobID]; len(queued) > 0 {
		result := queued[0]
		b.jobs[jobID] = queued[1:]
		return result, nil
	}
	return b.jobResults[jobID], nil
}

func (b *fakeNativeRunnerBackend) Acknowledge(_ context.Context, task nativeRunnerTask) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.acks = append(b.acks, task)
	return b.ackErr
}

func (b *fakeNativeRunnerBackend) Notify(context.Context, nativeRunnerNotice) (string, error) {
	return "confirmed-notice-turn", nil
}

func newNativeRunnerForTest(t *testing.T, goal string, checks map[string]nativeRunnerCheck) (*nativeRunner, *fakeNativeRunnerBackend, nativeRunnerProject) {
	t.Helper()
	backend := newFakeNativeRunnerBackend()
	runner, err := newNativeRunner(t.TempDir(), backend, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	result, err := runner.Handle(context.Background(), "runner.init", map[string]any{
		"projectId": "project-test", "root": t.TempDir(), "goal": goal,
		"controllerSessionId": "controller", "concurrency": 4, "checks": checks,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner, backend, result["project"].(nativeRunnerProject)
}

func addNativeTask(t *testing.T, runner *nativeRunner, projectID, id, scope string, checks ...string) nativeRunnerTask {
	t.Helper()
	task := nativeRunnerTask{
		ID: id, Key: id, Title: id, Objective: "Implement " + id,
		Acceptance: "The complete block is evidenced", Scope: scope, Checks: checks,
	}
	tx, err := runner.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	p, tasks, err := nativeLoad(tx, projectID)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = nativeValidateTask(p, &task, tasks); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	// Existing scheduling tests model post-planning validated work. Intake
	// behavior is covered separately through runner.add.
	task.State = "queued"
	task.PlanRevision = 0
	if err = nativeSave(tx, "runner_tasks", task.ID, projectID, task); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return loadNativeTask(t, runner, projectID, id)
}

func loadNativeTask(t *testing.T, runner *nativeRunner, projectID, taskID string) nativeRunnerTask {
	t.Helper()
	_, tasks, err := runner.read(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.ID == taskID {
			return task
		}
	}
	t.Fatalf("task %q not found", taskID)
	return nativeRunnerTask{}
}

func loadNativeProject(t *testing.T, runner *nativeRunner, projectID string) (nativeRunnerProject, []nativeRunnerTask) {
	t.Helper()
	project, tasks, err := runner.read(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	return project, tasks
}

func saveNativeTasks(t *testing.T, runner *nativeRunner, projectID string, update func(*nativeRunnerTask)) {
	t.Helper()
	tx, err := runner.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	_, tasks, err := nativeLoad(tx, projectID)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	for i := range tasks {
		update(&tasks[i])
		if err := nativeSave(tx, "runner_tasks", tasks[i].ID, projectID, tasks[i]); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func plannerForTest(t *testing.T, runner *nativeRunner, projectID string) (nativeRunnerProject, nativeRunnerTask) {
	t.Helper()
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	project, tasks := loadNativeProject(t, runner, projectID)
	for _, task := range tasks {
		if task.Kind == "planner" {
			return project, task
		}
	}
	t.Fatal("planner was not created")
	return nativeRunnerProject{}, nativeRunnerTask{}
}

func TestNativeRunnerParallelBlocksAndLocalFailure(t *testing.T) {
	runner, backend, project := newNativeRunnerForTest(t, "parallel goal", nil)
	addNativeTask(t, runner, project.ID, "a", "src/a")
	addNativeTask(t, runner, project.ID, "b", "src/b")
	saveNativeTasks(t, runner, project.ID, func(block *nativeRunnerTask) { block.Parent = "large-feature" })
	backend.dispatchErr["a"] = errors.New("A failed to dispatch")
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, tasks := loadNativeProject(t, runner, project.ID)
	byID := map[string]nativeRunnerTask{}
	for _, task := range tasks {
		byID[task.ID] = task
	}
	if byID["a"].LastError == "" {
		t.Fatal("failed A did not retain an individual error")
	}
	if byID["b"].Receipt == nil {
		t.Fatal("independent B was blocked by A")
	}
	if byID["a"].Parent != "large-feature" || byID["b"].Parent != "large-feature" {
		t.Fatal("parallel blocks lost their parent task")
	}
	backend.mu.Lock()
	dispatchCount := len(backend.dispatches)
	backend.mu.Unlock()
	if dispatchCount != 2 {
		t.Fatalf("dispatch count=%d, want both blocks attempted", dispatchCount)
	}
}

func TestNativeRunnerConfirmedPreflightRejectionReleasesOnlyItsSlot(t *testing.T) {
	runner, backend, p := newNativeRunnerForTest(t, "preflight repair", nil)
	addNativeTask(t, runner, p.ID, "rejected", "src/one")
	backend.dispatchErr["rejected"] = &nativeRunnerRejectedError{err: errors.New("invalid bound input before provider call")}
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := loadNativeTask(t, runner, p.ID, "rejected")
	if nativeHolds(got) || got.Result == nil || !got.ResultAcked {
		t.Fatalf("definitively rejected block still holds execution: %+v", got)
	}
}

func TestNativeRunnerReturnedResultReleasesWriterScope(t *testing.T) {
	runner, backend, project := newNativeRunnerForTest(t, "scope goal", nil)
	addNativeTask(t, runner, project.ID, "a", "src/shared")
	addNativeTask(t, runner, project.ID, "b", "src/shared")
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend.observed["a"] = []nativeRunnerResult{{EventID: "a-1", Outcome: "completed", Summary: "A done", Terminal: true}}
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := loadNativeTask(t, runner, project.ID, "b")
	if b.Receipt == nil {
		t.Fatal("same-scope B did not dispatch after A returned")
	}
	backend.mu.Lock()
	ackCount := len(backend.acks)
	backend.mu.Unlock()
	if ackCount == 0 {
		t.Fatal("returned result was dispatched without durable callback acknowledgement")
	}
}

func TestNativeRunnerTerminalMissingReportDoesNotHoldOtherBlocks(t *testing.T) {
	runner, backend, project := newNativeRunnerForTest(t, "recover evidence", nil)
	addNativeTask(t, runner, project.ID, "missing", "src/shared")
	addNativeTask(t, runner, project.ID, "next", "src/shared")
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := loadNativeTask(t, runner, project.ID, "missing")
	if err := os.Remove(first.Receipt.ResultPath); err != nil {
		t.Fatal(err)
	}
	backend.observed["missing"] = []nativeRunnerResult{{EventID: "terminal-missing", Outcome: "completed", Terminal: true}}
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	first = loadNativeTask(t, runner, project.ID, "missing")
	if first.Result == nil || first.Result.Outcome != "blocked" || first.Result.ErrorCode != "RUNNER_REPORT_UNAVAILABLE" {
		t.Fatalf("missing report not recorded as repairable: %+v", first.Result)
	}
	if first.Result.Path == "" || first.Result.Path != first.Request.ResultPath {
		t.Fatalf("missing report lost its immutable binding: result=%+v request=%+v", first.Result, first.Request)
	}
	if loadNativeTask(t, runner, project.ID, "next").Receipt == nil {
		t.Fatal("terminal missing report held independent writer")
	}
}

func TestNativeRunnerPlannerMissingReportSchedulesRepairWithoutEmptyPath(t *testing.T) {
	runner, _, project := newNativeRunnerForTest(t, "planner report recovery", nil)
	project, planner := plannerForTest(t, runner, project.ID)
	missingPath := filepath.Join(runner.dir, "planner-missing.result")
	saveNativeTasks(t, runner, project.ID, func(task *nativeRunnerTask) {
		if task.ID != planner.ID {
			return
		}
		task.State = "returned"
		task.Result = &nativeRunnerResult{
			EventID: "planner-missing-report", Outcome: "blocked", ExecutionOutcome: "completed",
			ErrorCode: "RUNNER_REPORT_UNAVAILABLE", Path: missingPath,
			Summary: "Execution is terminal; recover the missing report",
		}
	})
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	updated := loadNativeTask(t, runner, project.ID, planner.ID)
	if updated.State != "queued" || !strings.Contains(updated.Correction, "RUNNER_REPORT_UNAVAILABLE") {
		t.Fatalf("planner was not scheduled for focused repair: %+v", updated)
	}
	if strings.Contains(updated.LastError, "open :") || strings.Contains(updated.Correction, "open :") {
		t.Fatalf("planner attempted to read an empty report path: %+v", updated)
	}
}

func TestNativeRunnerHistoryFenceAllowsOnlyReportRecoveryRounds(t *testing.T) {
	receipt := &nativeRunnerReceipt{SessionID: "chat", TaskRef: "task", Generation: 1, ResultPath: "result"}
	for _, tc := range []struct {
		name    string
		attempt nativeRunnerAttempt
		blocks  bool
	}{
		{name: "ordinary unacked result", attempt: nativeRunnerAttempt{Receipt: receipt, Result: &nativeRunnerResult{ErrorCode: "WORKER_FAILED"}}, blocks: true},
		{name: "missing report recovery", attempt: nativeRunnerAttempt{Receipt: receipt, Result: &nativeRunnerResult{ErrorCode: "RUNNER_REPORT_UNAVAILABLE"}}, blocks: false},
		{name: "changed report recovery", attempt: nativeRunnerAttempt{Receipt: receipt, Result: &nativeRunnerResult{ErrorCode: "RUNNER_REPORT_CHANGED"}}, blocks: false},
		{name: "unacked attempt without result", attempt: nativeRunnerAttempt{Receipt: receipt}, blocks: true},
		{name: "acknowledged result", attempt: nativeRunnerAttempt{Receipt: receipt, Result: &nativeRunnerResult{ErrorCode: "WORKER_FAILED"}, Acked: true}, blocks: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nativeHistoryBlocksDispatch(tc.attempt); got != tc.blocks {
				t.Fatalf("nativeHistoryBlocksDispatch=%v, want %v", got, tc.blocks)
			}
		})
	}
}

func TestNativeRunnerAcceptsNodePrefixedReportDigest(t *testing.T) {
	runner, backend, p := newNativeRunnerForTest(t, "native digest", nil)
	addNativeTask(t, runner, p.ID, "digest", "src/digest")
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend.observed["digest"] = []nativeRunnerResult{{EventID: "digest-final", Outcome: "completed", Terminal: true, SHA256: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("worker report")))}}
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := loadNativeTask(t, runner, p.ID, "digest"); got.Result == nil || got.Result.Outcome != "completed" {
		t.Fatalf("Node digest rejected: %+v", got.Result)
	}
}

func TestNativeRunnerUnknownCheckRequiresRecoveryBeforeWriterRetry(t *testing.T) {
	runner, backend, p := newNativeRunnerForTest(t, "recover check", map[string]nativeRunnerCheck{"test": {Argv: []string{"go", "version"}}})
	addNativeTask(t, runner, p.ID, "owner", "src/owner", "test")
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend.observed["owner"] = []nativeRunnerResult{{EventID: "owner-done", Outcome: "completed", Terminal: true}}
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend.jobResults["job-owner-test"] = nativeRunnerCheckResult{State: "unknown", Evidence: "process identity cannot yet be confirmed"}
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, tasks := loadNativeProject(t, runner, p.ID)
	var planner nativeRunnerTask
	for _, task := range tasks {
		if task.Kind == "planner" {
			planner = task
		}
	}
	if planner.ID == "" {
		t.Fatal("unknown check did not produce a diagnostic planning opportunity")
	}
	err := runner.applyPlan(context.Background(), p.ID, planner.ID, nativeRunnerPlan{GoalVersion: p.GoalVersion, Summary: "retry", Actions: []nativeRunnerPlanAction{{TaskID: "owner", Round: 1, Action: "retry", Reason: "rerun implementation"}}})
	if err == nil {
		t.Fatal("unknown check was allowed to release/restart its writer")
	}
	if !nativeChecking(loadNativeTask(t, runner, p.ID, "owner")) {
		t.Fatal("unknown check lost its scope protection")
	}
}

func TestNativeRunnerAcknowledgesUnackedHistoricalResultBeforeRetryProgress(t *testing.T) {
	runner, backend, project := newNativeRunnerForTest(t, "ack history goal", nil)
	addNativeTask(t, runner, project.ID, "retryable", "src/retryable")
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend.observed["retryable"] = []nativeRunnerResult{{EventID: "retryable-1", Outcome: "completed", Summary: "done", Terminal: true}}
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	completed := loadNativeTask(t, runner, project.ID, "retryable")
	if completed.Result == nil || completed.Receipt == nil {
		t.Fatal("test setup did not persist a returned result")
	}
	saveNativeTasks(t, runner, project.ID, func(task *nativeRunnerTask) {
		if task.ID != "retryable" {
			return
		}
		task.History = []nativeRunnerAttempt{{
			Round: completed.Round, GoalVersion: completed.GoalVersion,
			Receipt: completed.Receipt, Result: completed.Result, Acked: false,
		}}
		task.ResultAcked = true
	})
	backend.mu.Lock()
	backend.acks = nil
	backend.mu.Unlock()
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	updated := loadNativeTask(t, runner, project.ID, "retryable")
	if len(updated.History) != 1 || !updated.History[0].Acked {
		t.Fatalf("historical result was not acknowledged: %#v", updated.History)
	}
	backend.mu.Lock()
	ackCount := len(backend.acks)
	backend.mu.Unlock()
	if ackCount == 0 {
		t.Fatal("historical ACK was not sent")
	}
}

func TestNativeRunnerAsyncCheckHoldsSameScopeOnlyUntilTerminal(t *testing.T) {
	runner, backend, project := newNativeRunnerForTest(t, "check goal", map[string]nativeRunnerCheck{
		"compile": {Argv: []string{"compile"}},
	})
	addNativeTask(t, runner, project.ID, "checked", "src/shared", "compile")
	addNativeTask(t, runner, project.ID, "followup", "src/shared")
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend.observed["checked"] = []nativeRunnerResult{{EventID: "checked-1", Outcome: "completed", Summary: "done", Terminal: true}}
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if task := loadNativeTask(t, runner, project.ID, "followup"); task.Receipt != nil {
		t.Fatal("same-scope followup dispatched while check was running")
	}
	if len(backend.checks) != 1 {
		t.Fatalf("started checks=%d, want 1", len(backend.checks))
	}
	jobID := "job-checked-compile"
	backend.jobs[jobID] = []nativeRunnerCheckResult{{State: "completed", ExitCode: 0, Evidence: "compile passed"}}
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if task := loadNativeTask(t, runner, project.ID, "followup"); task.Receipt == nil {
		t.Fatal("same-scope followup remained blocked after terminal check")
	}
}

func TestNativeRunnerRejectsEmptyPlannerWithoutStrandingProject(t *testing.T) {
	runner, _, project := newNativeRunnerForTest(t, "planner goal", nil)
	project, planner := plannerForTest(t, runner, project.ID)
	err := runner.applyPlan(context.Background(), project.ID, planner.ID, nativeRunnerPlan{
		GoalVersion: project.GoalVersion, Summary: "nothing to do",
	})
	if err == nil {
		t.Fatal("empty unfinished plan was accepted")
	}
	_, tasks := loadNativeProject(t, runner, project.ID)
	for _, task := range tasks {
		if task.ID == planner.ID && task.State == "accepted" {
			t.Fatal("rejected empty planner became accepted")
		}
	}
}

func TestNativeRunnerRejectsCyclicBlocksAtomically(t *testing.T) {
	runner, _, project := newNativeRunnerForTest(t, "cycle goal", nil)
	project, planner := plannerForTest(t, runner, project.ID)
	err := runner.applyPlan(context.Background(), project.ID, planner.ID, nativeRunnerPlan{
		GoalVersion: project.GoalVersion, Summary: "cycle",
		Blocks: []nativeRunnerTask{
			{Key: "x", Title: "X", Objective: "X", Acceptance: "X", After: []string{"y"}},
			{Key: "y", Title: "Y", Objective: "Y", Acceptance: "Y", After: []string{"x"}},
		},
	})
	if err == nil {
		t.Fatal("cyclic plan was accepted")
	}
	_, tasks := loadNativeProject(t, runner, project.ID)
	if len(tasks) != 1 || tasks[0].Kind != "planner" {
		t.Fatalf("cycle rejection was not atomic: %#v", tasks)
	}
}

func TestNativeRunnerGoalVersionRejectsOldAcceptedEvidence(t *testing.T) {
	runner, _, project := newNativeRunnerForTest(t, "old goal", nil)
	addNativeTask(t, runner, project.ID, "old", "src/old")
	oldVersion := project.GoalVersion
	saveNativeTasks(t, runner, project.ID, func(task *nativeRunnerTask) {
		if task.ID == "old" {
			task.State = "accepted"
			task.GoalVersion = oldVersion
			task.AcceptedVersion = oldVersion
		}
	})
	newGoal := "new goal"
	if _, err := runner.Handle(context.Background(), "runner.goal", map[string]any{"projectId": project.ID, "goal": newGoal}); err != nil {
		t.Fatal(err)
	}
	project, planner := plannerForTest(t, runner, project.ID)
	err := runner.applyPlan(context.Background(), project.ID, planner.ID, nativeRunnerPlan{
		GoalVersion: project.GoalVersion, Summary: "old evidence completes new goal",
		GoalComplete: true, CompletionEvidence: []string{"old report"},
	})
	if err == nil {
		t.Fatal("old accepted evidence completed a new goal version")
	}
	status, _ := loadNativeProject(t, runner, project.ID)
	if status.CompleteVersion != "" {
		t.Fatal("new goal was marked complete after old evidence")
	}
}

func TestNativeRunnerSameOwnerContinuesPastThreeRounds(t *testing.T) {
	runner, backend, project := newNativeRunnerForTest(t, "same owner goal", nil)
	addNativeTask(t, runner, project.ID, "long", "src/long")
	saveNativeTasks(t, runner, project.ID, func(task *nativeRunnerTask) {
		if task.ID != "long" {
			return
		}
		task.Round = 4
		task.History = []nativeRunnerAttempt{
			{Round: 3, GoalVersion: project.GoalVersion, Receipt: &nativeRunnerReceipt{SessionID: "same-chat"}, Acked: true},
		}
	})
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	found := false
	for _, request := range backend.dispatches {
		if request.TaskID == "long" {
			found = true
		}
		if request.TaskID == "long" && request.TargetSessionID != "same-chat" {
			t.Fatalf("round %d unexpectedly rotated owner: %q", request.Round, request.TargetSessionID)
		}
	}
	if !found {
		t.Fatal("continuation was not dispatched")
	}
}

func TestNativeRunnerPreparedInDoubtReplayKeepsIdempotencyKey(t *testing.T) {
	runner, backend, project := newNativeRunnerForTest(t, "replay goal", nil)
	addNativeTask(t, runner, project.ID, "uncertain", "src/uncertain")
	backend.inDoubt["uncertain"] = 1
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock := time.Now().Add(2 * time.Minute)
	runner.now = func() time.Time { return clock }
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	var keys []string
	for _, request := range backend.dispatches {
		if request.TaskID == "uncertain" {
			keys = append(keys, request.IdempotencyKey)
		}
	}
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("uncertain replay keys=%v", keys)
	}
	task := loadNativeTask(t, runner, project.ID, "uncertain")
	if task.Receipt == nil || task.Receipt.InDoubt {
		t.Fatalf("uncertain replay did not reconcile receipt: %#v", task.Receipt)
	}
}
