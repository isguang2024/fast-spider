package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeRunnerAddAndChangeCreateRevisionedPlanInput(t *testing.T) {
	runner, _, project := newNativeRunnerForTest(t, "revisioned intake", nil)
	result, err := runner.Handle(context.Background(), "runner.add", map[string]any{
		"projectId": project.ID,
		"task": nativeRunnerTask{
			ID: "new-work", Key: "new-work", Title: "New work",
			Objective: "implement the change", Acceptance: "the change is evidenced",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["taskId"] != "new-work" {
		t.Fatalf("created task=%v", result["taskId"])
	}
	p, tasks := loadNativeProject(t, runner, project.ID)
	if p.Revision != 1 || len(p.PendingChanges) != 1 || p.PendingChanges[0].Kind != "add" {
		t.Fatalf("add change ledger=%+v", p)
	}
	if tasks[0].State != "pending_plan" || tasks[0].PlanRevision != p.Revision {
		t.Fatalf("new task was dispatchable before planning: %+v", tasks[0])
	}
	if _, err = runner.Handle(context.Background(), "runner.change", map[string]any{
		"projectId": project.ID, "taskId": "new-work", "evidence": "the requested scope changed",
	}); err != nil {
		t.Fatal(err)
	}
	p, _ = loadNativeProject(t, runner, project.ID)
	if p.Revision != 2 || len(p.PendingChanges) != 2 || p.PendingChanges[1].Kind != "change" || p.PendingChanges[1].Evidence == "" {
		t.Fatalf("change was not revisioned: %+v", p)
	}
}

func TestNativeRunnerCancelUnsentIsIdempotentAndPreservesTombstone(t *testing.T) {
	runner, _, project := newNativeRunnerForTest(t, "cancel unsent", nil)
	if _, err := runner.Handle(context.Background(), "runner.add", map[string]any{
		"projectId": project.ID,
		"task":      nativeRunnerTask{ID: "cancel-me", Key: "cancel-me", Title: "Cancel me", Objective: "unused", Acceptance: "unused"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Handle(context.Background(), "runner.cancel", map[string]any{
		"projectId": project.ID, "taskId": "cancel-me", "reason": "user withdrew the request",
	}); err != nil {
		t.Fatal(err)
	}
	first := loadNativeTask(t, runner, project.ID, "cancel-me")
	if first.State != "cancelled" || first.Request != nil || first.Cancellation == nil {
		t.Fatalf("unsent cancellation did not become a tombstone: %+v", first)
	}
	if _, err := runner.Handle(context.Background(), "runner.cancel", map[string]any{
		"projectId": project.ID, "taskId": "cancel-me", "reason": "same request repeated",
	}); err != nil {
		t.Fatal(err)
	}
	second := loadNativeTask(t, runner, project.ID, "cancel-me")
	if second.State != "cancelled" || second.Request != nil || second.Cancellation == nil {
		t.Fatalf("repeated cancellation changed the tombstone: %+v", second)
	}
}

func TestNativeRunnerProjectCancelArchiveAndResumeBoundaries(t *testing.T) {
	runner, _, project := newNativeRunnerForTest(t, "cancel project", nil)
	if _, err := runner.Handle(context.Background(), "runner.add", map[string]any{
		"projectId": project.ID,
		"task":      nativeRunnerTask{ID: "queued", Key: "queued", Title: "Queued", Objective: "queued", Acceptance: "queued"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Handle(context.Background(), "runner.archive", map[string]any{"projectId": project.ID}); err == nil {
		t.Fatal("active project was archived")
	}
	if _, err := runner.Handle(context.Background(), "runner.cancel", map[string]any{"projectId": project.ID, "reason": "stop project"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, _ := loadNativeProject(t, runner, project.ID)
	if p.State != "cancelled" {
		t.Fatalf("project did not finalize cancellation: %+v", p)
	}
	if _, err := runner.Handle(context.Background(), "runner.archive", map[string]any{"projectId": project.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Handle(context.Background(), "runner.archive", map[string]any{"projectId": project.ID, "taskId": "queued"}); err != nil {
		t.Fatal(err)
	}
	if task := loadNativeTask(t, runner, project.ID, "queued"); !task.Archived || task.State != "cancelled" {
		t.Fatalf("task archive changed execution state: %+v", task)
	}
	if _, err := runner.Handle(context.Background(), "runner.unarchive", map[string]any{"projectId": project.ID, "taskId": "queued"}); err != nil {
		t.Fatal(err)
	}
	if task := loadNativeTask(t, runner, project.ID, "queued"); task.Archived || task.State != "cancelled" {
		t.Fatalf("task unarchive resumed execution: %+v", task)
	}
	p, _ = loadNativeProject(t, runner, project.ID)
	if !p.Archived {
		t.Fatal("cancelled project was not archived")
	}
	if _, err := runner.Handle(context.Background(), "runner.unarchive", map[string]any{"projectId": project.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Handle(context.Background(), "runner.resume", map[string]any{"projectId": project.ID}); err == nil {
		t.Fatal("cancelled project resumed")
	}
}

func TestNativeRunnerDispatchUsesGlobalWriterScopeAndKeepsPriorityOrder(t *testing.T) {
	runner, backend, p1 := newNativeRunnerForTest(t, "global scope", nil)
	root := p1.Root
	if _, err := runner.Handle(context.Background(), "runner.init", map[string]any{
		"projectId": "project-two", "root": root, "goal": "global scope two",
		"controllerSessionId": "controller-two", "concurrency": 4,
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"project-test", "project-two"} {
		tx, err := runner.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		p, _, err := nativeLoad(tx, id)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		p.Revision, p.PlannedRevision = 1, 1
		p.PendingChanges = nil
		if err = nativeSave(tx, "runner_projects", p.ID, "", p); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	addNativeTask(t, runner, "project-test", "scope-one", "shared")
	addNativeTask(t, runner, "project-two", "scope-two", "shared")
	if err := runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := loadNativeTask(t, runner, "project-test", "scope-one")
	second := loadNativeTask(t, runner, "project-two", "scope-two")
	if (first.Receipt == nil) == (second.Receipt == nil) {
		t.Fatalf("overlapping writers both dispatched or both blocked: first=%+v second=%+v", first.Receipt, second.Receipt)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	for _, request := range backend.dispatches {
		if request.TaskID == "scope-one" || request.TaskID == "scope-two" {
			return
		}
	}
	t.Fatal("no business task dispatch was recorded")
}

func TestNativeRunnerArchivedProjectStillAcknowledgesLateResult(t *testing.T) {
	runner, backend, project := newNativeRunnerForTest(t, "archive transport", nil)
	addNativeTask(t, runner, project.ID, "archived-result", "archive-scope")
	completed := loadNativeTask(t, runner, project.ID, "archived-result")
	completed.State = "returned"
	completed.Request = &nativeRunnerDispatch{ProjectID: project.ID, TaskID: completed.ID, Round: 1, ResultPath: "archived.result"}
	completed.Receipt = &nativeRunnerReceipt{SessionID: "archived-session", TaskRef: "archived-ref", Generation: 1, ResultPath: "archived.result"}
	completed.Result = &nativeRunnerResult{EventID: "archived-event", Outcome: "completed", Summary: "done", Path: ""}
	saveNativeTasks(t, runner, project.ID, func(task *nativeRunnerTask) {
		if task.ID == completed.ID {
			*task = completed
		}
	})
	tx, err := runner.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := nativeLoad(tx, project.ID)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	p.CompleteVersion = p.GoalVersion
	if err = nativeSave(tx, "runner_projects", p.ID, "", p); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Handle(context.Background(), "runner.archive", map[string]any{"projectId": project.ID}); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	backend.acks = nil
	backend.mu.Unlock()
	if err = runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	ackCount := len(backend.acks)
	backend.mu.Unlock()
	if ackCount != 1 {
		t.Fatalf("archived project did not ACK late result, count=%d", ackCount)
	}
}

func TestNativeRunnerPlanKeepUnlocksPendingPlan(t *testing.T) {
	runner, _, project := newNativeRunnerForTest(t, "keep pending", nil)
	if _, err := runner.Handle(context.Background(), "runner.add", map[string]any{
		"projectId": project.ID,
		"task":      nativeRunnerTask{ID: "pending", Key: "pending", Title: "Pending", Objective: "work", Acceptance: "evidence"},
	}); err != nil {
		t.Fatal(err)
	}
	p, planner := plannerForTest(t, runner, project.ID)
	plan := nativeRunnerPlan{GoalVersion: p.GoalVersion, Revision: p.Revision, Summary: "retain the pending block", Actions: []nativeRunnerPlanAction{{TaskID: "pending", Round: 1, Action: "keep", Reason: "the new block remains compatible"}}}
	if err := runner.applyPlan(context.Background(), project.ID, planner.ID, plan); err != nil {
		t.Fatal(err)
	}
	task := loadNativeTask(t, runner, project.ID, "pending")
	if task.State != "queued" || task.PlanRevision != p.Revision {
		t.Fatalf("keep did not unlock pending plan: %+v", task)
	}
}

func TestNativeRunnerPlanReviseRefreshesRevisionWithoutAfter(t *testing.T) {
	runner, _, project := newNativeRunnerForTest(t, "revise without after", nil)
	if _, err := runner.Handle(context.Background(), "runner.add", map[string]any{
		"projectId": project.ID,
		"task":      nativeRunnerTask{ID: "revise", Key: "revise", Title: "Revise", Objective: "old", Acceptance: "old evidence"},
	}); err != nil {
		t.Fatal(err)
	}
	p, planner := plannerForTest(t, runner, project.ID)
	plan := nativeRunnerPlan{GoalVersion: p.GoalVersion, Revision: p.Revision, Summary: "revise the unsent block", Actions: []nativeRunnerPlanAction{{TaskID: "revise", Round: 1, Action: "revise", Reason: "new evidence changes the implementation", Objective: "new", Acceptance: "new evidence"}}}
	if err := runner.applyPlan(context.Background(), project.ID, planner.ID, plan); err != nil {
		t.Fatal(err)
	}
	task := loadNativeTask(t, runner, project.ID, "revise")
	if task.Objective != "new" || task.PlanRevision != p.Revision || task.State != "queued" {
		t.Fatalf("revise without after did not refresh revision: %+v", task)
	}
}

func TestNativeRunnerPlanRejectsRevivalOfCancelingTask(t *testing.T) {
	runner, _, project := newNativeRunnerForTest(t, "canceling plan", nil)
	addNativeTask(t, runner, project.ID, "canceling", "canceling-scope")
	if _, err := runner.Handle(context.Background(), "runner.change", map[string]any{
		"projectId": project.ID, "taskId": "canceling", "evidence": "replan the cancellation boundary",
	}); err != nil {
		t.Fatal(err)
	}
	current, _ := loadNativeProject(t, runner, project.ID)
	saveNativeTasks(t, runner, project.ID, func(task *nativeRunnerTask) {
		if task.ID == "canceling" {
			task.State = "canceling"
			task.Cancellation = &nativeRunnerCancellation{Reason: "stop", Revision: current.Revision, RequestedAt: 1}
		}
	})
	p, planner := plannerForTest(t, runner, project.ID)
	err := runner.applyPlan(context.Background(), project.ID, planner.ID, nativeRunnerPlan{
		GoalVersion: p.GoalVersion, Revision: p.Revision, Summary: "attempted revival",
		Actions: []nativeRunnerPlanAction{{TaskID: "canceling", Round: 1, Action: "retry", Reason: "try again"}},
	})
	if err == nil {
		t.Fatal("canceling task was revived by a plan")
	}
}

func seedCancelingPreparedTask(t *testing.T, runner *nativeRunner, projectID, taskID string) (nativeRunnerProject, nativeRunnerTask) {
	t.Helper()
	addNativeTask(t, runner, projectID, taskID, "prepared-scope")
	request := &nativeRunnerDispatch{ProjectID: projectID, TaskID: taskID, Round: 1, ResultPath: filepath.Join(runner.dir, taskID+".result"), IdempotencyKey: "cancel-reconcile-" + taskID}
	saveNativeTasks(t, runner, projectID, func(task *nativeRunnerTask) {
		if task.ID == taskID {
			task.Request = request
			task.State = "canceling"
			task.Cancellation = &nativeRunnerCancellation{Reason: "stop", Revision: 1, RequestedAt: 1}
		}
	})
	p, tasks := loadNativeProject(t, runner, projectID)
	for _, task := range tasks {
		if task.ID == taskID {
			return p, task
		}
	}
	t.Fatalf("task %q not found", taskID)
	return nativeRunnerProject{}, nativeRunnerTask{}
}

func TestNativeRunnerCancelingPreparedDispatchOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantState   string
		wantReceipt bool
	}{
		{name: "pending", err: errNativeRunnerOperationPending, wantState: "canceling"},
		{name: "rejected", err: &nativeRunnerRejectedError{err: errors.New("preflight")}, wantState: "cancelled"},
		{name: "transport", err: errors.New("transport down"), wantState: "canceling"},
		{name: "success", wantState: "canceling", wantReceipt: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, backend, project := newNativeRunnerForTest(t, "cancel prepared", nil)
			p, task := seedCancelingPreparedTask(t, runner, project.ID, "prepared-"+test.name)
			if test.err != nil {
				backend.dispatchErr[task.ID] = test.err
			}
			if err := runner.dispatch(context.Background(), p, []nativeRunnerTask{task}, nil); err != nil {
				t.Fatal(err)
			}
			got := loadNativeTask(t, runner, project.ID, task.ID)
			if got.State != test.wantState {
				t.Fatalf("state=%q want %q task=%+v", got.State, test.wantState, got)
			}
			if (got.Receipt != nil) != test.wantReceipt {
				t.Fatalf("receipt=%+v want=%v", got.Receipt, test.wantReceipt)
			}
			if test.name == "rejected" && got.State == "returned" {
				t.Fatal("canceling preflight rejection became ordinary returned work")
			}
		})
	}
}

func TestNativeRunnerCloseDeadlineDoesNotCloseBusyDatabase(t *testing.T) {
	runner, _, _ := newNativeRunnerForTest(t, "close deadline", nil)
	runner.recoveryWG.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := runner.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close error=%v, want deadline", err)
	}
	if err := runner.db.Ping(); err != nil {
		t.Fatalf("busy Close closed database: %v", err)
	}
	runner.recoveryWG.Done()
	if err := runner.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
