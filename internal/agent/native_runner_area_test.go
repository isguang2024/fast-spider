package agent

import (
	"context"
	"strings"
	"testing"
)

func TestNativeRunnerPausedAreaFinishesCurrentChecksWithoutNewDispatch(t *testing.T) {
	r, b, p := newNativeRunnerForTest(t, "pause area", map[string]nativeRunnerCheck{"unit": {Argv: []string{"test"}}})
	ctx := context.Background()
	addNativeTask(t, r, p.ID, "current", "current-scope", "unit")
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	addNativeTask(t, r, p.ID, "next", "next-scope")
	if _, err := r.Handle(ctx, "runner.pause", map[string]any{"projectId": p.ID, "taskId": "current"}); err == nil {
		t.Fatal("block ID was allowed to pause the whole area")
	}
	if _, err := r.Handle(ctx, "runner.pause", map[string]any{"projectId": p.ID}); err != nil {
		t.Fatal(err)
	}
	b.observed["current"] = []nativeRunnerResult{{EventID: "finished", Outcome: "completed", Terminal: true}}
	b.jobResults["job-current-unit"] = nativeRunnerCheckResult{State: "completed", ExitCode: 0}
	for i := 0; i < 2; i++ {
		if err := r.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	current := loadNativeTask(t, r, p.ID, "current")
	if current.State != "returned" || current.Cancellation != nil || current.Validations["unit"].State != "passed" {
		t.Fatalf("paused area interrupted in-flight work: %+v", current)
	}
	if len(b.dispatches) != 1 || loadNativeTask(t, r, p.ID, "next").Request != nil {
		t.Fatal("paused area dispatched new work")
	}
	if _, err := r.Handle(ctx, "runner.resume", map[string]any{"projectId": p.ID}); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if loadNativeTask(t, r, p.ID, "next").Request == nil {
		t.Fatal("resumed area did not continue scheduling")
	}
}

func TestNativeRunnerContinuousAreaPlansNextCycleInsteadOfCompleting(t *testing.T) {
	r, _, _ := newNativeRunnerForTest(t, "other", nil)
	ctx := context.Background()
	result, err := r.Handle(ctx, "runner.init", map[string]any{"projectId": "exploration", "root": t.TempDir(), "goal": "Discover a bounded batch, implement and verify it, then repeat.", "controllerSessionId": "controller", "continuous": true})
	if err != nil {
		t.Fatal(err)
	}
	if !result["project"].(nativeRunnerProject).Continuous {
		t.Fatal("continuous intent was not saved")
	}
	p, planner := plannerForTest(t, r, "exploration")
	err = r.applyPlan(ctx, p.ID, planner.ID, nativeRunnerPlan{GoalVersion: p.GoalVersion, Revision: p.Revision, Summary: "stop", GoalComplete: true, CompletionEvidence: []string{"cycle finished"}})
	if err == nil || !strings.Contains(err.Error(), "continuous") {
		t.Fatalf("continuous area completed: %v", err)
	}
	err = r.applyPlan(ctx, p.ID, planner.ID, nativeRunnerPlan{GoalVersion: p.GoalVersion, Revision: p.Revision, Summary: "first batch", Blocks: []nativeRunnerTask{{Key: "cycle-1", Title: "First batch", Objective: "Confirm bounded findings", Acceptance: "Evidence for each finding"}}})
	if err != nil {
		t.Fatal(err)
	}
	saveNativeTasks(t, r, p.ID, func(task *nativeRunnerTask) {
		if task.Kind == "work" {
			task.State = "accepted"
			task.AcceptedVersion = p.GoalVersion
		}
	})
	if err = r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	_, tasks := loadNativeProject(t, r, p.ID)
	for _, task := range tasks {
		if task.Kind == "planner" && task.ID != planner.ID && task.State != "accepted" {
			return
		}
	}
	t.Fatal("continuous area did not create the next planning round")
}

func TestNativeRunnerCompletedAreaDoesNotCreateRedundantPlanners(t *testing.T) {
	r, b, p := newNativeRunnerForTest(t, "finite goal", nil)
	ctx := context.Background()
	addNativeTask(t, r, p.ID, "finished", "scope")
	saveNativeTasks(t, r, p.ID, func(task *nativeRunnerTask) { task.State = "accepted"; task.AcceptedVersion = p.GoalVersion })
	p, planner := plannerForTest(t, r, p.ID)
	if err := r.applyPlan(ctx, p.ID, planner.ID, nativeRunnerPlan{GoalVersion: p.GoalVersion, Revision: p.Revision, Summary: "All evidence accepted", GoalComplete: true, CompletionEvidence: []string{"integration checked"}}); err != nil {
		t.Fatal(err)
	}
	before := len(b.dispatches)
	for i := 0; i < 3; i++ {
		if err := r.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(b.dispatches) != before {
		t.Fatal("completed area kept dispatching planners")
	}
	current, tasks := loadNativeProject(t, r, p.ID)
	basis := map[string]string{}
	for _, task := range tasks {
		if task.Kind != "planner" {
			basis[task.ID] = nativeBasis(task)
		}
	}
	if current.PlanBasis != nativeHash([]any{current.GoalVersion, current.Revision, basis, current.PendingChanges}) {
		t.Fatal("applied plan fingerprint differs from planner creation fingerprint")
	}
	if _, err := r.Handle(ctx, "runner.change", map[string]any{"projectId": p.ID, "evidence": "A new confirmed issue requires follow-up"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(b.dispatches) == before {
		t.Fatal("explicit change did not reopen completed area planning")
	}
}
