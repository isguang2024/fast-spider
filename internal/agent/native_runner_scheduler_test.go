package agent

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func schedulerTestProject(id, goal string) nativeRunnerProject {
	return nativeRunnerProject{ID: id, GoalVersion: nativeHash(goal)}
}

func TestNativeRunnerSchedulingWeightsCompetingLongAndShortQueues(t *testing.T) {
	projects := []nativeRunnerProject{schedulerTestProject("long", "long"), schedulerTestProject("short", "short")}
	var tasks []nativeRunnerTask
	for i := 0; i < 8; i++ {
		for _, p := range projects {
			task := schedulerTestTask(p.ID, fmt.Sprintf("%s-%d", p.ID, i), p.ID)
			task.EstimatedMinutes = 5
			if p.ID == "long" {
				task.EstimatedMinutes = 30
			}
			tasks = append(tasks, task)
		}
	}
	s := nativeBuildSchedulingSnapshot(tasks, projects, 8)
	if s.Allocations["long"] <= s.Allocations["short"] || s.Allocations["short"] == 0 {
		t.Fatalf("long work has no larger fair share: %+v", s.Allocations)
	}
	for i := range tasks {
		tasks[i].EstimatedMinutes = 0
	}
	tasks = append(tasks, nativeRunnerTask{ID: "slow", ProjectID: "long", Kind: "work", State: "active", Request: &nativeRunnerDispatch{}, StartedAt: time.Now().Add(-20 * time.Minute).Unix()})
	s = nativeBuildSchedulingSnapshot(tasks, projects, 8)
	tasks[len(tasks)-1].StartedAt = 0
	unknown := nativeBuildSchedulingSnapshot(tasks, projects, 8)
	if s.Allocations["long"] <= unknown.Allocations["long"] || s.GlobalActive != 1 {
		t.Fatalf("slow active work invalid allocation: %+v", s)
	}
}

func TestNativeRunnerSchedulingDispatchesSelectedNonconflictingTasks(t *testing.T) {
	r, _, p := newNativeRunnerForTest(t, "one", nil)
	ctx := context.Background()
	if _, err := r.Handle(ctx, "runner.configure", map[string]any{"concurrency": 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Handle(ctx, "runner.init", map[string]any{"projectId": "aaa", "root": p.Root, "goal": "two", "controllerSessionId": "controller"}); err != nil {
		t.Fatal(err)
	}
	addNativeTask(t, r, p.ID, "a-conflict", "shared")
	addNativeTask(t, r, p.ID, "z-independent", "independent")
	addNativeTask(t, r, "aaa", "b-shared", "shared")
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if nativeHolds(loadNativeTask(t, r, p.ID, "a-conflict")) || !nativeHolds(loadNativeTask(t, r, p.ID, "z-independent")) || !nativeHolds(loadNativeTask(t, r, "aaa", "b-shared")) {
		t.Fatal("dispatch did not honor globally selected write scopes")
	}
}

func TestNativeRunnerSchedulingTickHonorsGlobalCapAndReclaimsSlots(t *testing.T) {
	r, _, p := newNativeRunnerForTest(t, "one", nil)
	ctx := context.Background()
	if _, err := r.Handle(ctx, "runner.configure", map[string]any{"projectId": p.ID, "concurrency": 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Handle(ctx, "runner.init", map[string]any{"projectId": "two", "root": t.TempDir(), "goal": "two", "controllerSessionId": "controller"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		for _, id := range []string{p.ID, "two"} {
			addNativeTask(t, r, id, fmt.Sprintf("%s-%d", id, i), fmt.Sprintf("scope-%d", i))
		}
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, err := r.readAllTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, task := range tasks {
		if nativeHolds(task) {
			counts[task.ProjectID]++
		}
	}
	if counts[p.ID]+counts["two"] != 8 || counts[p.ID] == 0 || counts["two"] == 0 {
		t.Fatalf("global runtime counts: %v", counts)
	}
	if _, err = r.Handle(ctx, "runner.configure", map[string]any{"concurrency": 3}); err != nil {
		t.Fatal(err)
	}
	if err = r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := r.readAllTasks(ctx)
	active := 0
	for _, task := range after {
		if nativeHolds(task) {
			active++
		}
	}
	if active != 8 {
		t.Fatalf("lowering cap interrupted existing work: %d", active)
	}
	for _, task := range after {
		if nativeHolds(task) && active > 2 {
			task.State = "cancelled"
			if err = r.saveTask(ctx, task, ""); err != nil {
				t.Fatal(err)
			}
			active--
		}
	}
	if err = r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ = r.readAllTasks(ctx)
	active = 0
	for _, task := range after {
		if nativeHolds(task) {
			active++
		}
	}
	if active != 3 {
		t.Fatalf("released slot was not reclaimed within global cap: %d", active)
	}
}

func schedulerTestTask(projectID, id, goal string) nativeRunnerTask {
	return nativeRunnerTask{ProjectID: projectID, ID: id, Kind: "work", Key: id, GoalVersion: nativeHash(goal), State: "queued"}
}

func TestNativeRunnerSchedulingSnapshotCapsAtEightAndSharesAreas(t *testing.T) {
	projects := []nativeRunnerProject{schedulerTestProject("one", "one"), schedulerTestProject("two", "two")}
	tasks := make([]nativeRunnerTask, 0, 12)
	for i := 0; i < 6; i++ {
		tasks = append(tasks, schedulerTestTask("one", "one-"+string(rune('a'+i)), "one"))
		tasks = append(tasks, schedulerTestTask("two", "two-"+string(rune('a'+i)), "two"))
	}
	snapshot := nativeBuildSchedulingSnapshot(tasks, projects, 8)
	if snapshot.GlobalActive != 0 {
		t.Fatalf("active=%d", snapshot.GlobalActive)
	}
	if got := snapshot.Allocations["one"] + snapshot.Allocations["two"]; got != 8 {
		t.Fatalf("allocated=%d want 8: %+v", got, snapshot)
	}
	if snapshot.Allocations["one"] == 0 || snapshot.Allocations["two"] == 0 {
		t.Fatalf("one area consumed all slots: %+v", snapshot.Allocations)
	}
}

func TestNativeRunnerSchedulingSnapshotWeightsLongDemandWhenContended(t *testing.T) {
	projects := []nativeRunnerProject{schedulerTestProject("long", "long"), schedulerTestProject("short", "short")}
	tasks := []nativeRunnerTask{{ProjectID: "long", ID: "long-1", Kind: "work", GoalVersion: nativeHash("long"), State: "queued", EstimatedMinutes: 30}}
	for i := 0; i < 8; i++ {
		tasks = append(tasks, nativeRunnerTask{ProjectID: "short", ID: "short-" + string(rune('a'+i)), Kind: "work", GoalVersion: nativeHash("short"), State: "queued", EstimatedMinutes: 5})
	}
	snapshot := nativeBuildSchedulingSnapshot(tasks, projects, 3)
	if snapshot.Allocations["long"] < 1 || snapshot.Allocations["short"] < 1 {
		t.Fatalf("weighted fair allocation starved an area: %+v", snapshot.Allocations)
	}
	if snapshot.Allocations["long"]+snapshot.Allocations["short"] != 3 {
		t.Fatalf("allocated=%+v", snapshot.Allocations)
	}
}

func TestNativeRunnerSchedulingSnapshotCountsPlannerAndCancelingHolds(t *testing.T) {
	projects := []nativeRunnerProject{schedulerTestProject("one", "one"), schedulerTestProject("two", "two")}
	tasks := []nativeRunnerTask{
		{ProjectID: "one", ID: "planner", Kind: "planner", GoalVersion: nativeHash("one"), State: "active", Request: &nativeRunnerDispatch{}},
		{ProjectID: "two", ID: "canceling", Kind: "work", GoalVersion: nativeHash("two"), State: "canceling", Request: &nativeRunnerDispatch{}},
	}
	for i := 0; i < 8; i++ {
		tasks = append(tasks, schedulerTestTask("one", "queued-"+string(rune('a'+i)), "one"))
	}
	snapshot := nativeBuildSchedulingSnapshot(tasks, projects, 8)
	if snapshot.GlobalActive != 2 {
		t.Fatalf("global active=%d want 2", snapshot.GlobalActive)
	}
	if got := snapshot.Allocations["one"] + snapshot.Allocations["two"]; got != 6 {
		t.Fatalf("allocated=%d want 6: %+v", got, snapshot)
	}
}

func TestNativeRunnerSchedulingSnapshotRotatesEqualWeightDemand(t *testing.T) {
	projects := []nativeRunnerProject{schedulerTestProject("one", "one"), schedulerTestProject("two", "two")}
	tasks := []nativeRunnerTask{schedulerTestTask("one", "one-work", "one"), schedulerTestTask("two", "two-work", "two")}
	first := nativeBuildSchedulingSnapshot(tasks, projects, 1)
	if first.Allocations["one"] != 1 || first.Allocations["two"] != 0 {
		t.Fatalf("initial stable allocation=%+v", first.Allocations)
	}
	tasks[0].DispatchOrdinal = 1
	second := nativeBuildSchedulingSnapshot(tasks, projects, 1)
	if second.Allocations["one"] != 0 || second.Allocations["two"] != 1 {
		t.Fatalf("equal weight demand did not rotate=%+v", second.Allocations)
	}
}

func TestNativeRunnerSchedulingSnapshotSimulatesScopeHolds(t *testing.T) {
	projects := []nativeRunnerProject{schedulerTestProject("one", "one")}
	a := schedulerTestTask("one", "a", "one")
	a.Scope = "shared"
	b := schedulerTestTask("one", "b", "one")
	b.Scope = "shared"
	snapshot := nativeBuildSchedulingSnapshot([]nativeRunnerTask{a, b}, projects, 2)
	if snapshot.Allocations["one"] != 1 {
		t.Fatalf("overlapping ready tasks were allocated together: %+v", snapshot)
	}
}

func TestNativeRunnerSchedulingSnapshotReportsActiveWhenFull(t *testing.T) {
	project := schedulerTestProject("one", "one")
	project.MaxConcurrency = 2
	tasks := []nativeRunnerTask{
		{ProjectID: "one", ID: "a", Kind: "work", GoalVersion: project.GoalVersion, State: "active", Request: &nativeRunnerDispatch{}},
		{ProjectID: "one", ID: "b", Kind: "planner", GoalVersion: project.GoalVersion, State: "prepared", Request: &nativeRunnerDispatch{}},
		schedulerTestTask("one", "queued", "one"),
	}
	snapshot := nativeBuildSchedulingSnapshot(tasks, []nativeRunnerProject{project}, 2)
	schedule := snapshot.Projects["one"]
	if snapshot.GlobalActive != 2 || schedule.ProjectLimit != 2 || schedule.ProjectActive != 2 || schedule.Allocation != 0 {
		t.Fatalf("full project status lost active/cap fields: global=%d schedule=%+v", snapshot.GlobalActive, schedule)
	}
}

func TestNativeRunnerSchedulingAreaCapIncludesPlanner(t *testing.T) {
	p := schedulerTestProject("one", "one")
	p.MaxConcurrency = 2
	tasks := []nativeRunnerTask{{ID: "planning", ProjectID: p.ID, Kind: "planner", State: "active", Request: &nativeRunnerDispatch{}}}
	for i := 0; i < 5; i++ {
		tasks = append(tasks, schedulerTestTask(p.ID, fmt.Sprint(i), "one"))
	}
	s := nativeBuildSchedulingSnapshot(tasks, []nativeRunnerProject{p}, 8)
	if s.Allocations[p.ID] != 1 {
		t.Fatalf("area cap failed with active planner: %+v", s)
	}
	p.MaxConcurrency = 0
	s = nativeBuildSchedulingSnapshot(tasks, []nativeRunnerProject{p}, 8)
	if s.Allocations[p.ID] != 5 {
		t.Fatalf("uncapped area did not borrow free slots: %+v", s)
	}
}

func TestNativeRunnerConfigurePersistsGlobalAndAreaLimits(t *testing.T) {
	runner, _, project := newNativeRunnerForTest(t, "configure", nil)
	if _, err := runner.Handle(context.Background(), "runner.configure", map[string]any{"projectId": project.ID}); err == nil {
		t.Fatal("project configure without explicit concurrency unexpectedly cleared the area limit")
	}
	if _, err := runner.Handle(context.Background(), "runner.configure", map[string]any{"concurrency": 0}); err == nil {
		t.Fatal("zero global concurrency unexpectedly accepted")
	}
	if _, err := runner.Handle(context.Background(), "runner.configure", map[string]any{"concurrency": 3}); err != nil {
		t.Fatal(err)
	}
	if runner.globalConcurrency != 3 {
		t.Fatalf("global limit=%d want 3", runner.globalConcurrency)
	}
	if _, err := runner.Handle(context.Background(), "runner.configure", map[string]any{"projectId": project.ID, "concurrency": 2}); err != nil {
		t.Fatal(err)
	}
	updated, _ := loadNativeProject(t, runner, project.ID)
	if updated.MaxConcurrency != 2 {
		t.Fatalf("area limit=%d want 2", updated.MaxConcurrency)
	}
	if _, err := runner.Handle(context.Background(), "runner.configure", map[string]any{"projectId": project.ID, "concurrency": 0}); err != nil {
		t.Fatal(err)
	}
	updated, _ = loadNativeProject(t, runner, project.ID)
	if updated.MaxConcurrency != 0 {
		t.Fatalf("area limit was not cleared: %d", updated.MaxConcurrency)
	}
	status, err := runner.Handle(context.Background(), "runner.status", map[string]any{"projectId": project.ID})
	if err != nil {
		t.Fatal(err)
	}
	scheduling, ok := status["scheduling"].(map[string]any)
	if !ok {
		t.Fatalf("status scheduling=%T: %#v", status["scheduling"], status)
	}
	if scheduling["globalLimit"] != 3 || scheduling["globalActive"] != 0 || scheduling["projectLimit"] != 0 || scheduling["projectActive"] != 0 || scheduling["allocation"] != 0 {
		t.Fatalf("unexpected status scheduling=%#v", scheduling)
	}
}
