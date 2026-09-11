package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeRunnerPacketBoundsGrowingBusinessHistory(t *testing.T) {
	p := nativeRunnerProject{Goal: "continue bounded cycles", GoalVersion: "v"}
	planner := nativeRunnerTask{ID: "planner", Kind: "planner", Round: 1}
	tasks := make([]nativeRunnerTask, 0, 1001)
	for i := 0; i < 1000; i++ {
		tasks = append(tasks, nativeRunnerTask{ID: fmt.Sprintf("old-%d", i), Kind: "work", Title: "accepted work", State: "accepted", Round: 1, GoalVersion: "v", AcceptedVersion: "v"})
	}
	tasks = append(tasks, nativeRunnerTask{ID: "current", Kind: "work", State: "returned", Title: "current findings", Objective: "plan current correction"})
	packet := nativeCompilePacket(p, planner, tasks, "result")
	blocks := packet["blocks"].([]map[string]any)
	if len(blocks) != 24 || blocks[0]["id"] != "current" || packet["omittedBusinessBlocks"] != 977 {
		t.Fatalf("unexpected bounded index: len=%d omitted=%v", len(blocks), packet["omittedBusinessBlocks"])
	}
	raw, err := json.Marshal(packet)
	if err != nil || len(raw) > 24000 {
		t.Fatalf("history grew packet to %d bytes err=%v", len(raw), err)
	}
}

func TestNativeRunnerExternalQuestionStopsUnchangedPlanning(t *testing.T) {
	r, _, p := newNativeRunnerForTest(t, "finish migration", nil)
	addNativeTask(t, r, p.ID, "done", "")
	saveNativeTasks(t, r, p.ID, func(t *nativeRunnerTask) { t.State = "accepted"; t.AcceptedVersion = p.GoalVersion })
	p, planner := plannerForTest(t, r, p.ID)
	if err := r.applyPlan(context.Background(), p.ID, planner.ID, nativeRunnerPlan{GoalVersion: p.GoalVersion, Revision: p.Revision, Summary: "business done; ask once", UserQuestions: []string{"Which archive contains the preserved ledger?"}}); err != nil {
		t.Fatal(err)
	}
	p, tasks := loadNativeProject(t, r, p.ID)
	if !p.QuestionReviewPending || p.Notice != nil {
		t.Fatal("first unresolved question must schedule self-review before notifying user")
	}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, tasks = loadNativeProject(t, r, p.ID)
	var review nativeRunnerTask
	for _, candidate := range tasks {
		if candidate.Kind == "planner" && candidate.ID != planner.ID {
			review = candidate
		}
	}
	if review.ID == "" {
		t.Fatal("bounded self-review not scheduled")
	}
	if err := r.applyPlan(context.Background(), p.ID, review.ID, nativeRunnerPlan{GoalVersion: p.GoalVersion, Revision: p.Revision, Summary: "checked alternatives; a real external fact is still needed", UserQuestions: []string{"Archive location remains unknown after checking handoff and retained inputs"}}); err != nil {
		t.Fatal(err)
	}
	p, tasks = loadNativeProject(t, r, p.ID)
	if p.PlanBasis == "" || p.QuestionReviewPending {
		t.Fatal("second unresolved question must wait without repeating planning")
	}
	initial := len(tasks)
	for i := 0; i < 10; i++ {
		if err := r.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	p, tasks = loadNativeProject(t, r, p.ID)
	if len(tasks) != initial {
		t.Fatalf("unchanged question spawned %d extra planners", len(tasks)-initial)
	}
	if state, done := nativeProjectActivity(p, tasks); state != "waiting_input" || !done {
		t.Fatalf("state=%s done=%v", state, done)
	}
	if _, err := r.Handle(context.Background(), "runner.signal", map[string]any{"projectId": p.ID, "taskId": "done", "evidence": "retained archive found and verified"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, tasks = loadNativeProject(t, r, p.ID)
	if len(tasks) != initial+1 {
		t.Fatal("new fact did not resume planning exactly once")
	}
}

func TestNativeRunnerCancelledMissingReportClosesACKWithoutRevival(t *testing.T) {
	r, b, p := newNativeRunnerForTest(t, "cancel old plan", nil)
	task := addNativeTask(t, r, p.ID, "cancelled", "")
	path := filepath.Join(r.dir, "cancelled.result")
	task.State = "cancelled"
	task.Request = &nativeRunnerDispatch{ProjectID: p.ID, TaskID: task.ID, Round: 1, ControllerSessionID: p.ControllerSessionID, ResultPath: path}
	task.Receipt = &nativeRunnerReceipt{SessionID: "old-session", Generation: 1, ResultPath: path}
	task.Result = &nativeRunnerResult{EventID: "old-event", Outcome: "completed", Terminal: true, Path: path}
	if err := r.saveTask(context.Background(), task, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Handle(context.Background(), "runner.pause", map[string]any{"projectId": p.ID}); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	task = loadNativeTask(t, r, p.ID, task.ID)
	if task.State != "cancelled" || !task.ResultAcked || len(b.acks) != 1 {
		t.Fatalf("state=%s acked=%v ackCount=%d", task.State, task.ResultAcked, len(b.acks))
	}
	if task.Result.ErrorCode != "RUNNER_REPORT_UNAVAILABLE" || task.Result.Outcome != "blocked" {
		t.Fatal("missing report was misrepresented as success")
	}
	if _, err := os.Stat(task.Result.Path); err != nil {
		t.Fatal(err)
	}
	if err := nativeVerifyReport(task.Result); err != nil {
		t.Fatal(err)
	}
}

func TestNativeRunnerActivityDoesNotTreatHistoryAsExecution(t *testing.T) {
	p := nativeRunnerProject{GoalVersion: "v"}
	tasks := []nativeRunnerTask{{ID: "work", Kind: "work", State: "accepted", AcceptedVersion: "v"}, {ID: "plan", Kind: "planner", State: "accepted", Result: &nativeRunnerResult{Terminal: true}}}
	state, done := nativeProjectActivity(p, tasks)
	if state != "idle" || !done {
		t.Fatalf("state=%s done=%v", state, done)
	}
	p.CompleteVersion = "v"
	state, _ = nativeProjectActivity(p, tasks)
	if state != "completed" {
		t.Fatal(state)
	}
}
