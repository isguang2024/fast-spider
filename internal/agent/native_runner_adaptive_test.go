package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeWaitFileWritesDoNotReplanUntilOwnerReleases(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "source.go")
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	p := nativeRunnerProject{ID: "p", Root: root}
	waiting := nativeRunnerTask{ID: "wait", ProjectID: "p", State: "deferred", Scope: root, WaitFor: &nativeRunnerWaitFor{Paths: []string{path}}}
	holder := nativeRunnerTask{ID: "writer", ProjectID: "p", State: "active", Scope: root, Request: &nativeRunnerDispatch{}}
	before := nativeDeferredWaitFingerprint(p, waiting, []nativeRunnerTask{waiting}, []nativeRunnerTask{holder})
	if err := os.WriteFile(path, []byte("writer is making more changes"), 0600); err != nil {
		t.Fatal(err)
	}
	during := nativeDeferredWaitFingerprint(p, waiting, []nativeRunnerTask{waiting}, []nativeRunnerTask{holder})
	if before != during {
		t.Fatal("ordinary writes by a known owner triggered replanning")
	}
	holder.State = "accepted"
	after := nativeDeferredWaitFingerprint(p, waiting, []nativeRunnerTask{waiting}, []nativeRunnerTask{holder})
	if after == before {
		t.Fatal("owner release was not observed")
	}
}

func TestNativeRunnerPreparationCanStartButFinalDependenciesRemain(t *testing.T) {
	r, _, p := newNativeRunnerForTest(t, "prepare independently then integrate against actual backend", nil)
	addNativeTask(t, r, p.ID, "backend", "backend")
	addNativeTask(t, r, p.ID, "prepare", "frontend")
	addNativeTask(t, r, p.ID, "integration", "")
	saveNativeTasks(t, r, p.ID, func(task *nativeRunnerTask) {
		switch task.ID {
		case "backend":
			task.State = "deferred"
			task.DeferredReason = "await actual backend contract"
		case "prepare":
			task.After = []string{"backend"}
		case "integration":
			task.After = []string{"backend", "prepare"}
		}
	})
	if _, err := r.Handle(context.Background(), "runner.change", map[string]any{"projectId": p.ID, "evidence": "separate independent preparation from final integration"}); err != nil {
		t.Fatal(err)
	}
	p, planner := plannerForTest(t, r, p.ID)
	empty := []string{}
	plan := nativeRunnerPlan{GoalVersion: p.GoalVersion, Revision: p.Revision, Summary: "only preparation can start before backend; final integration retains both prerequisites", Actions: []nativeRunnerPlanAction{
		{TaskID: "backend", Round: 1, Action: "defer", Reason: "backend contract not available", ResumeAt: r.now().Unix() + 3600},
		{TaskID: "prepare", Round: 1, Action: "revise", Reason: "independent layout and adapter contract preparation is executable", Objective: "Prepare independent UI and adapter contract only", Acceptance: "Preparation verified; real backend integration remains in integration block", After: &empty},
		{TaskID: "integration", Round: 1, Action: "keep", Reason: "final integration must use both actual deliverables"},
	}}
	if err := r.applyPlan(context.Background(), p.ID, planner.ID, plan); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if loadNativeTask(t, r, p.ID, "prepare").Receipt == nil {
		t.Fatal("independent preparation did not dispatch")
	}
	if loadNativeTask(t, r, p.ID, "integration").Request != nil {
		t.Fatal("final integration bypassed backend dependency")
	}
	saveNativeTasks(t, r, p.ID, func(task *nativeRunnerTask) {
		if task.ID == "prepare" {
			task.State = "accepted"
			task.AcceptedVersion = p.GoalVersion
		}
	})
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if loadNativeTask(t, r, p.ID, "integration").Request != nil {
		t.Fatal("preparation alone incorrectly satisfied final acceptance")
	}
	saveNativeTasks(t, r, p.ID, func(task *nativeRunnerTask) {
		if task.ID == "backend" {
			task.State = "accepted"
			task.AcceptedVersion = p.GoalVersion
		}
	})
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if loadNativeTask(t, r, p.ID, "integration").Receipt == nil {
		t.Fatal("final integration did not resume after both dependencies completed")
	}
}

func TestNativeRunnerActivitySeparatesReportRecoveryFromChecks(t *testing.T) {
	p := nativeRunnerProject{GoalVersion: "v"}
	for _, test := range []struct {
		name string
		task nativeRunnerTask
		want string
	}{
		{"missing report", nativeRunnerTask{Kind: "work", State: "returned", Result: &nativeRunnerResult{Outcome: "blocked", ErrorCode: "RUNNER_REPORT_UNAVAILABLE"}}, "needs_recovery"},
		{"returned awaiting acceptance", nativeRunnerTask{Kind: "work", State: "returned", Result: &nativeRunnerResult{Outcome: "completed"}}, "awaiting_review"},
		{"real check running", nativeRunnerTask{Kind: "work", State: "returned", Checks: []string{"build"}, Result: &nativeRunnerResult{Outcome: "completed"}}, "checking"},
		{"planner rate limited", nativeRunnerTask{Kind: "planner", State: "active", Request: &nativeRunnerDispatch{}, Recovery: &nativeRunnerRecovery{Phase: "uncertain"}}, "needs_recovery"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, _ := nativeProjectActivity(p, []nativeRunnerTask{test.task})
			if state != test.want {
				t.Fatalf("state=%s want=%s", state, test.want)
			}
		})
	}
}
