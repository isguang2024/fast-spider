package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func nativeReviewTestResult(t *testing.T) *nativeRunnerResult {
	t.Helper()
	raw := []byte("completed implementation and focused verification")
	path := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return &nativeRunnerResult{EventID: "event", Outcome: "completed", Terminal: true, Path: path, SHA256: hex.EncodeToString(sum[:])}
}

func nativeReviewBatchFixture(t *testing.T) (*nativeRunner, nativeRunnerProject, nativeRunnerTask) {
	t.Helper()
	r, _, p := newNativeRunnerForTest(t, "review completed work", nil)
	addNativeTask(t, r, p.ID, "a", "a")
	addNativeTask(t, r, p.ID, "b", "b")
	saveNativeTasks(t, r, p.ID, func(task *nativeRunnerTask) {
		task.State = "returned"
		task.Result = nativeReviewTestResult(t)
		task.ResultAcked = true
	})
	p, planner := plannerForTest(t, r, p.ID)
	return r, p, planner
}

func TestNativeReviewBatchAppliesIndependentActionsWhenOneSnapshotMoves(t *testing.T) {
	r, p, planner := nativeReviewBatchFixture(t)
	saveNativeTasks(t, r, p.ID, func(task *nativeRunnerTask) {
		if task.ID == "b" {
			task.Result.Summary = "new result evidence"
		}
	})
	plan := nativeRunnerPlan{GoalVersion: p.GoalVersion, Revision: p.Revision, Summary: "accept current completed evidence", Actions: []nativeRunnerPlanAction{
		{TaskID: "a", Round: 1, Action: "accept", Reason: "verified report", Evidence: []string{"report"}},
		{TaskID: "b", Round: 1, Action: "accept", Reason: "verified report", Evidence: []string{"report"}},
	}}
	if err := r.applyPlan(context.Background(), p.ID, planner.ID, plan); err != nil {
		t.Fatal(err)
	}
	if loadNativeTask(t, r, p.ID, "a").State != "accepted" {
		t.Fatal("unchanged valid acceptance was discarded")
	}
	if loadNativeTask(t, r, p.ID, "b").State != "returned" {
		t.Fatal("stale action accepted updated evidence")
	}
	project, _, _ := r.read(context.Background(), p.ID)
	if project.PlanBasis != "" {
		t.Fatal("changed result was not queued for fresh review")
	}
	if loadNativeTask(t, r, p.ID, planner.ID).State != "accepted" {
		t.Fatal("entire acceptance batch was retried")
	}
}

func TestNativeReviewFreshTokenSupportsDynamicReadAfterDispatch(t *testing.T) {
	r, p, planner := nativeReviewBatchFixture(t)
	saveNativeTasks(t, r, p.ID, func(task *nativeRunnerTask) {
		if task.ID == "b" {
			task.Result.Summary = "fresh evidence read by cloud"
		}
	})
	item, err := r.queryTask(context.Background(), p.ID, "b")
	if err != nil {
		t.Fatal(err)
	}
	token, _ := item["reviewToken"].(string)
	if token == "" {
		t.Fatal("context did not return current review token")
	}
	plan := nativeRunnerPlan{GoalVersion: p.GoalVersion, Revision: p.Revision, Summary: "reviewed latest result", Actions: []nativeRunnerPlanAction{{TaskID: "b", Round: 1, Action: "accept", Reason: "latest report read", Evidence: []string{"report"}, ReviewToken: token}}}
	if err = r.applyPlan(context.Background(), p.ID, planner.ID, plan); err != nil {
		t.Fatal(err)
	}
	if loadNativeTask(t, r, p.ID, "b").State != "accepted" {
		t.Fatal("fresh observed result was rejected because original packet was old")
	}
}

func TestNativeReviewMovedSnapshotsDoNotEscalateOldUserQuestions(t *testing.T) {
	r, p, planner := nativeReviewBatchFixture(t)
	saveNativeTasks(t, r, p.ID, func(task *nativeRunnerTask) {
		if task.Kind != "planner" {
			task.Observation = "new fact"
		}
	})
	plan := nativeRunnerPlan{GoalVersion: p.GoalVersion, Revision: p.Revision, Summary: "old snapshot", UserQuestions: []string{"old unresolved question"}, Actions: []nativeRunnerPlanAction{{TaskID: "a", Round: 1, Action: "keep", Reason: "old facts"}, {TaskID: "b", Round: 1, Action: "keep", Reason: "old facts"}}}
	if err := r.applyPlan(context.Background(), p.ID, planner.ID, plan); err != nil {
		t.Fatal(err)
	}
	current, _, _ := r.read(context.Background(), p.ID)
	if current.Notice != nil || current.QuestionReviewPending || len(current.Questions) > 0 {
		t.Fatal("normal snapshot movement escalated an obsolete question")
	}
}

func TestNativeReviewStructuralPlanRetainsAtomicSnapshotFence(t *testing.T) {
	r, p, planner := nativeReviewBatchFixture(t)
	saveNativeTasks(t, r, p.ID, func(task *nativeRunnerTask) {
		if task.ID == "b" {
			task.Result.Summary = "changed"
		}
	})
	plan := nativeRunnerPlan{GoalVersion: p.GoalVersion, Revision: p.Revision, Summary: "restructure tasks", Actions: []nativeRunnerPlanAction{
		{TaskID: "a", Round: 1, Action: "accept", Reason: "report", Evidence: []string{"report"}},
		{TaskID: "b", Round: 1, Action: "cancel", Reason: "replacement"},
	}}
	if err := r.applyPlan(context.Background(), p.ID, planner.ID, plan); !errors.Is(err, errNativePlanEvidenceChanged) {
		t.Fatalf("structural stale plan accepted: %v", err)
	}
	if loadNativeTask(t, r, p.ID, "a").State != "returned" {
		t.Fatal("structural transaction partly applied")
	}
}

func TestNativeReviewQueuedLegacySnapshotRetryResumesWithoutDelay(t *testing.T) {
	r, p, planner := nativeReviewBatchFixture(t)
	planner.State = "queued"
	planner.Request = nil
	planner.Receipt = nil
	planner.LastError = nativeStalePlanMessage
	planner.NextAt = r.now().Add(20 * time.Minute).Unix()
	if err := r.saveTask(context.Background(), planner, ""); err != nil {
		t.Fatal(err)
	}
	_, tasks, _ := r.read(context.Background(), p.ID)
	if err := r.refreshStalePlannerQueue(context.Background(), p, tasks); err != nil {
		t.Fatal(err)
	}
	got := loadNativeTask(t, r, p.ID, planner.ID)
	if got.NextAt != 0 || got.LastError != "" || got.Round != planner.Round || len(got.ReviewTargets) != 2 {
		t.Fatalf("legacy normal concurrency delay was not recovered: %+v", got)
	}
}

func TestNativePresentationDistinguishesReviewOwnershipAndChecks(t *testing.T) {
	now := time.Now().Unix()
	p := nativeRunnerProject{ID: "p", GoalVersion: "g", Revision: 2}
	task := nativeRunnerTask{ID: "work", ProjectID: "p", Kind: "work", State: "returned", Round: 1, GoalVersion: "g", Result: &nativeRunnerResult{Outcome: "completed"}, Recovery: &nativeRunnerRecovery{Phase: "watching", LastError: "old read error"}}
	owner := nativeRunnerTask{ID: "review", ProjectID: "p", Kind: "planner", State: "active", PlanRevision: 2, GoalVersion: "g", Receipt: &nativeRunnerReceipt{SessionID: "cloud"}, ReviewTargets: []string{"work"}, Basis: map[string]string{"work": nativeBasis(task)}}
	if got := nativeTaskPresentation(p, task, nil, nil, now); got.Code != "review_queued" {
		t.Fatalf("unassigned result became active review: %+v", got)
	}
	if got := nativeTaskPresentation(p, task, []nativeRunnerTask{owner}, nil, now); got.Code != "reviewing" || got.OwnerTaskID != "review" || got.OwnerSessionID != "cloud" {
		t.Fatalf("real review owner not shown: %+v", got)
	}
	owner.State = "queued"
	owner.NextAt = now + 120
	if got := nativeTaskPresentation(p, task, []nativeRunnerTask{owner}, nil, now); got.Code != "review_retry" {
		t.Fatal(got)
	}
	owner.State = "returned"
	owner.NextAt = 0
	if got := nativeTaskPresentation(p, task, []nativeRunnerTask{owner}, nil, now); got.Code != "review_applying" {
		t.Fatal(got)
	}
	task.Checks = []string{"unit"}
	task.Validations = map[string]nativeRunnerValidation{}
	if got := nativeTaskPresentation(p, task, nil, nil, now); got.Code != "checks_queued" {
		t.Fatal(got)
	}
	task.Validations["unit"] = nativeRunnerValidation{JobID: "job", State: "running"}
	if got := nativeTaskPresentation(p, task, nil, nil, now); got.Code != "checks_running" {
		t.Fatal(got)
	}
	task.Validations["unit"] = nativeRunnerValidation{JobID: "job", State: "failed"}
	if got := nativeTaskPresentation(p, task, nil, nil, now); got.Code != "checks_failed" {
		t.Fatal(got)
	}
	task.Result.ErrorCode = "RUNNER_REPORT_UNAVAILABLE"
	if got := nativeTaskPresentation(p, task, nil, nil, now); got.Code != "report_missing" {
		t.Fatal(got)
	}
}
