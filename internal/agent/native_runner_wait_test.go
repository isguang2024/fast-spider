package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeValidateWaitForNormalizesProjectTasksAndPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "evidence.txt"), []byte("evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	condition := &nativeRunnerWaitFor{TaskIDs: []string{"semantic-key", "task-id"}, Paths: []string{"evidence.txt"}}
	err := nativeValidateWaitFor(nativeRunnerProject{Root: root}, condition, map[string]string{"semantic-key": "task-id", "other": "other-id"})
	if err != nil {
		t.Fatal(err)
	}
	if condition.TaskIDs[0] != "task-id" || condition.TaskIDs[1] != "task-id" {
		t.Fatalf("task IDs were not normalized: %#v", condition.TaskIDs)
	}
	if filepath.Base(condition.Paths[0]) != "evidence.txt" {
		t.Fatalf("path was not normalized inside project: %#v", condition.Paths)
	}
	if _, err := os.Stat(condition.Paths[0]); err != nil {
		t.Fatalf("normalized wait path is not readable: %v", err)
	}
	tooMany := &nativeRunnerWaitFor{TaskIDs: make([]string, 17)}
	if err := nativeValidateWaitFor(nativeRunnerProject{Root: root}, tooMany, map[string]string{}); err == nil {
		t.Fatal("waitFor accepted more than 16 task IDs")
	}
	outside := &nativeRunnerWaitFor{Paths: []string{filepath.Join(filepath.Dir(root), "outside")}}
	if err := nativeValidateWaitFor(nativeRunnerProject{Root: root}, outside, nil); err == nil {
		t.Fatal("waitFor accepted a path outside the project")
	}
}

func TestNativeReviewDeferredWaitsTriggersOnDependencyAndFileFacts(t *testing.T) {
	runner, _, project := newNativeRunnerForTest(t, "wait review", nil)
	dependency := addNativeTask(t, runner, project.ID, "dependency", "src/dependency")
	waiting := addNativeTask(t, runner, project.ID, "waiting", "src/waiting")
	path := filepath.Join(project.Root, "wait.txt")
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	condition := &nativeRunnerWaitFor{TaskIDs: []string{dependency.ID}, Paths: []string{"wait.txt"}}
	saveNativeTasks(t, runner, project.ID, func(task *nativeRunnerTask) {
		switch task.ID {
		case dependency.ID:
			task.State = "queued"
			task.Round = 1
		case waiting.ID:
			task.State = "deferred"
			task.WaitFor = condition
			task.After = []string{dependency.ID}
		}
	})
	project, tasks := loadNativeProject(t, runner, project.ID)
	project.PlanBasis = "baseline"
	project.Questions = []string{"retain question until evidence changes"}
	project.NextPlanAt = 123
	now := time.Unix(1700000000, 0)
	runner.now = func() time.Time { return now }
	if err := runner.reviewDeferredWaits(context.Background(), &project, tasks, tasks); err != nil {
		t.Fatal(err)
	}
	first := loadNativeTask(t, runner, project.ID, waiting.ID)
	if first.WaitReview == nil || first.WaitReview.Reviewed || first.WaitReview.NextAt != now.Add(nativeRunnerWaitReviewDelay).Unix() {
		t.Fatalf("first wait baseline was not saved: %+v", first.WaitReview)
	}
	if project.PlanBasis != "baseline" || len(project.Questions) != 1 || project.NextPlanAt != 123 {
		t.Fatalf("baseline unexpectedly requested replanning: %+v", project)
	}

	for i := range tasks {
		if tasks[i].ID == dependency.ID {
			tasks[i].State = "returned"
			tasks[i].Round = 2
			tasks[i].Result = &nativeRunnerResult{EventID: "dependency-result", Outcome: "completed", Terminal: true}
		}
	}
	if err := runner.reviewDeferredWaits(context.Background(), &project, tasks, tasks); err != nil {
		t.Fatal(err)
	}
	updated := loadNativeTask(t, runner, project.ID, waiting.ID)
	if updated.WaitReview == nil || !updated.WaitReview.Reviewed || updated.WaitReview.NextAt != 0 {
		t.Fatalf("dependency fact did not trigger one review: %+v", updated.WaitReview)
	}
	if project.PlanBasis != "" || project.Questions != nil || project.NextPlanAt != 0 {
		t.Fatalf("project planning state was not cleared on new fact: %+v", project)
	}
	if updated.State != "deferred" || len(updated.After) != 1 || updated.After[0] != dependency.ID {
		t.Fatalf("wait review changed task scheduling contract: %+v", updated)
	}

	// A changed watched file is a separate new fact and can request another
	// review after the previous one has been consumed.
	project.PlanBasis = "second-baseline"
	updated.WaitReview.Reviewed = false
	updated.WaitReview.NextAt = now.Add(nativeRunnerWaitReviewDelay).Unix()
	updated.WaitReview.Fingerprint = nativeDeferredWaitFingerprint(project, updated, tasks, tasks)
	saveNativeTasks(t, runner, project.ID, func(task *nativeRunnerTask) {
		if task.ID == waiting.ID {
			*task = updated
		}
	})
	if err := os.WriteFile(path, []byte("second file content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runner.reviewDeferredWaits(context.Background(), &project, []nativeRunnerTask{updated, tasks[0]}, []nativeRunnerTask{updated, tasks[0]}); err != nil {
		t.Fatal(err)
	}
	fileChanged := loadNativeTask(t, runner, project.ID, waiting.ID)
	if fileChanged.WaitReview == nil || !fileChanged.WaitReview.Reviewed || project.PlanBasis != "" {
		t.Fatalf("file fact did not trigger review: task=%+v project=%+v", fileChanged.WaitReview, project)
	}
}

func TestNativeReviewDeferredWaitsDoesNotRepeatStableReviewOrReleaseHolderConflict(t *testing.T) {
	runner, _, project := newNativeRunnerForTest(t, "stable wait", nil)
	waiting := addNativeTask(t, runner, project.ID, "waiting", "shared")
	holder := nativeRunnerTask{ID: "holder", ProjectID: project.ID, Kind: "work", Scope: "shared", State: "active", Round: 4, Request: &nativeRunnerDispatch{TargetSessionID: "holder-session"}}
	saveNativeTasks(t, runner, project.ID, func(task *nativeRunnerTask) {
		if task.ID == waiting.ID {
			task.State = "deferred"
		}
	})
	project, tasks := loadNativeProject(t, runner, project.ID)
	now := time.Unix(1700000000, 0)
	runner.now = func() time.Time { return now }
	global := append(append([]nativeRunnerTask{}, tasks...), holder)
	if err := runner.reviewDeferredWaits(context.Background(), &project, tasks, global); err != nil {
		t.Fatal(err)
	}
	first := loadNativeTask(t, runner, project.ID, waiting.ID)
	if first.WaitReview == nil || first.WaitReview.Reviewed {
		t.Fatalf("stable wait did not record an unreveiwed baseline: %+v", first.WaitReview)
	}
	project.PlanBasis = "stable"
	if err := runner.reviewDeferredWaits(context.Background(), &project, tasks, global); err != nil {
		t.Fatal(err)
	}
	second := loadNativeTask(t, runner, project.ID, waiting.ID)
	if second.WaitReview == nil || second.WaitReview.Reviewed || project.PlanBasis != "stable" {
		t.Fatalf("unchanged wait repeated review or released holder conflict: %+v project=%+v", second.WaitReview, project)
	}
	if second.State != "deferred" {
		t.Fatalf("active overlapping holder changed deferred state: %s", second.State)
	}
}

func TestNativeReviewDeferredWaitsSkipsPausedProject(t *testing.T) {
	runner, _, project := newNativeRunnerForTest(t, "paused wait", nil)
	waiting := addNativeTask(t, runner, project.ID, "waiting", "scope")
	saveNativeTasks(t, runner, project.ID, func(task *nativeRunnerTask) {
		if task.ID == waiting.ID {
			task.State = "deferred"
		}
	})
	project.Paused = true
	_, tasks := loadNativeProject(t, runner, project.ID)
	if err := runner.reviewDeferredWaits(context.Background(), &project, tasks, tasks); err != nil {
		t.Fatal(err)
	}
	unchanged := loadNativeTask(t, runner, project.ID, waiting.ID)
	if unchanged.WaitReview != nil {
		t.Fatalf("paused project created a wait review: %+v", unchanged.WaitReview)
	}
}
