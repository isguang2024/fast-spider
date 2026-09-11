package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeStageArtifactUnlocksConsumerNotFinalIntegration(t *testing.T) {
	r, _, p := newNativeRunnerForTest(t, "stage outputs", nil)
	path := filepath.Join(p.Root, "interface.json")
	if err := os.WriteFile(path, []byte(`{"field":"id"}`), 0600); err != nil {
		t.Fatal(err)
	}
	producer := nativeRunnerTask{ID: "producer", ProjectID: p.ID, GoalVersion: p.GoalVersion, Round: 1, Kind: "work", State: "active", Scope: filepath.Join(p.Root, "backend"), Request: &nativeRunnerDispatch{}}
	consumer := nativeRunnerTask{ID: "consumer", ProjectID: p.ID, GoalVersion: p.GoalVersion, Kind: "work", Round: 1, State: "queued", Scope: filepath.Join(p.Root, "frontend"), Requires: []nativeRunnerRequirement{{TaskID: "producer", Key: "api", Version: "1"}}}
	final := consumer
	final.ID = "final"
	final.After = []string{"producer", "consumer"}
	tasks := []nativeRunnerTask{producer, consumer, final}
	if nativeBuildSchedulingSnapshot(tasks, []nativeRunnerProject{p}, 8).Selected[consumer.ID] {
		t.Fatal("missing output dispatched")
	}
	if err := r.publishOutputs(p, &producer, []nativeRunnerOutput{{Key: "api", Version: "1", Summary: "stable interface", Path: path}}); err != nil {
		t.Fatal(err)
	}
	tasks[0] = producer
	s := nativeBuildSchedulingSnapshot(tasks, []nativeRunnerProject{p}, 8)
	if !s.Selected[consumer.ID] || s.Selected[final.ID] {
		t.Fatalf("stage did not unblock only consumer: %+v", s.Selected)
	}
	if producer.State != "active" {
		t.Fatal("stage accepted the producer")
	}
	if err := os.WriteFile(path, []byte(`{"field":"changed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(producer.Outputs[0].Path)
	if err != nil || string(raw) != `{"field":"id"}` {
		t.Fatal("published snapshot was mutable")
	}
	if err := r.publishOutputs(p, &producer, []nativeRunnerOutput{{Key: "api", Version: "1", Summary: "overwrite", Path: path}}); err == nil {
		t.Fatal("immutable version overwrite accepted")
	}
	producer.Round++
	tasks[0] = producer
	if nativeBuildSchedulingSnapshot(tasks, []nativeRunnerProject{p}, 8).Selected[consumer.ID] {
		t.Fatal("stale generation artifact dispatched")
	}
}

func TestNativeIsolatedTaskCanRunBesideSharedWriter(t *testing.T) {
	p := nativeRunnerProject{ID: "p", GoalVersion: "v"}
	writer := nativeRunnerTask{ID: "writer", ProjectID: "p", Kind: "work", Scope: "/repo/ai", State: "active", GoalVersion: "v", Request: &nativeRunnerDispatch{}}
	shared := nativeRunnerTask{ID: "shared", ProjectID: "p", Kind: "work", Scope: "/repo/ai", State: "queued", GoalVersion: "v"}
	isolated := shared
	isolated.ID = "isolated"
	isolated.Workspace = &nativeRunnerWorkspace{Mode: "worktree"}
	s := nativeBuildSchedulingSnapshot([]nativeRunnerTask{writer, shared, isolated}, []nativeRunnerProject{p}, 8)
	if s.Selected[shared.ID] || !s.Selected[isolated.ID] {
		t.Fatalf("scope isolation incorrect: %+v", s.Selected)
	}
	if s.QueueReasons[shared.ID].Code != "write_scope" {
		t.Fatal("missing writer queue reason")
	}
}

func TestNativeFullCapacityStillExplainsQueueReasons(t *testing.T) {
	p := nativeRunnerProject{ID: "p", GoalVersion: "v"}
	active := nativeRunnerTask{ID: "active", ProjectID: "p", Kind: "work", State: "active", GoalVersion: "v", Scope: "/repo/backend", Request: &nativeRunnerDispatch{}}
	queued := nativeRunnerTask{ID: "queued", ProjectID: "p", Kind: "work", State: "queued", GoalVersion: "v", Scope: "/repo/frontend"}
	s := nativeBuildSchedulingSnapshot([]nativeRunnerTask{active, queued}, []nativeRunnerProject{p}, 1)
	if s.QueueReasons[queued.ID] == nil || s.QueueReasons[queued.ID].Code != "global_capacity" {
		t.Fatalf("full scheduler lost queue explanation: %+v", s)
	}
	queued.After = []string{active.ID}
	s = nativeBuildSchedulingSnapshot([]nativeRunnerTask{active, queued}, []nativeRunnerProject{p}, 1)
	if s.QueueReasons[queued.ID] == nil || s.QueueReasons[queued.ID].Code != "dependency" {
		t.Fatal("capacity masked the actual prerequisite")
	}
}

func TestNativeIdleQueueReviewDoesNotRepeatUnchangedPlan(t *testing.T) {
	r, _, p := newNativeRunnerForTest(t, "parallel review", nil)
	addNativeTask(t, r, p.ID, "upstream", "backend")
	addNativeTask(t, r, p.ID, "downstream", "frontend")
	saveNativeTasks(t, r, p.ID, func(task *nativeRunnerTask) {
		if task.ID == "upstream" {
			task.State = "deferred"
			task.ResumeAt = r.now().Add(time.Hour).Unix()
		} else {
			task.After = []string{"upstream"}
		}
	})
	p, tasks, err := r.read(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.ensurePlanner(context.Background(), &p, tasks); err != nil {
		t.Fatal(err)
	}
	_, tasks, _ = r.read(context.Background(), p.ID)
	var planner nativeRunnerTask
	for _, task := range tasks {
		if task.Kind == "planner" {
			planner = task
		}
	}
	if planner.ID == "" {
		t.Fatal("idle blocked queue was not reviewed")
	}
	plan := nativeRunnerPlan{GoalVersion: p.GoalVersion, Revision: p.Revision, Summary: "hard dependency remains; do not split a tiny task", Actions: []nativeRunnerPlanAction{{TaskID: "downstream", Round: 1, Action: "keep", Reason: "actual integration prerequisite"}}}
	if err = r.applyPlan(context.Background(), p.ID, planner.ID, plan); err != nil {
		t.Fatal(err)
	}
	p, tasks, _ = r.read(context.Background(), p.ID)
	if err = r.ensurePlanner(context.Background(), &p, tasks); err != nil {
		t.Fatal(err)
	}
	_, tasks, _ = r.read(context.Background(), p.ID)
	n := 0
	for _, task := range tasks {
		if task.Kind == "planner" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("unchanged dependency repeatedly replanned: %d", n)
	}
}
