package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeRunnerViewEmptyDoesNotInitialize(t *testing.T) {
	root := t.TempDir()
	view, err := ReadNativeRunnerView(context.Background(), root, "", "", 0, false)
	if err != nil || len(view["projects"].([]any)) != 0 {
		t.Fatalf("view=%v err=%v", view, err)
	}
	if _, err := os.Stat(filepath.Join(root, "native-runner")); !os.IsNotExist(err) {
		t.Fatalf("read initialized runtime: %v", err)
	}
	if _, err := ReadNativeRunnerView(context.Background(), root, "unknown", "", 0, false); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}

func TestNativeRunnerViewSnapshotIsolationAndTaskOwnership(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "任务 # %")
	r, err := newNativeRunner(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close(ctx)
	p := nativeRunnerProject{ID: "project/一", Root: root, ControllerSessionID: "controller-a", Goal: "# 工作区一\n业务目标", GoalVersion: "v1", Concurrency: 3}
	q := nativeRunnerProject{ID: "project-b", Root: root, ControllerSessionID: "controller-b", Goal: "另一个目标", GoalVersion: "v1"}
	active := nativeRunnerTask{ID: "writer", ProjectID: p.ID, Kind: "work", Parent: "大任务", Title: "执行中", State: "active", Scope: filepath.Join(root, "source"), Request: &nativeRunnerDispatch{Prompt: "PRIVATE-FROZEN-PROMPT"}, History: []nativeRunnerAttempt{{Result: &nativeRunnerResult{Outcome: "blocked"}, Acked: false}}}
	queued := nativeRunnerTask{ID: "queued", ProjectID: p.ID, Kind: "work", Title: "同域排队", State: "queued", Scope: filepath.Join(root, "source", "child")}
	dependent := nativeRunnerTask{ID: "dependent", ProjectID: p.ID, Kind: "work", Title: "依赖排队", State: "queued", After: []string{"writer"}}
	foreign := nativeRunnerTask{ID: "foreign", ProjectID: q.ID, Kind: "work", Title: "不可串区", State: "queued"}
	tx, err := r.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range []nativeRunnerProject{p, q} {
		if err = nativeSave(tx, "runner_projects", project.ID, "", project); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []nativeRunnerTask{active, queued, dependent, foreign} {
		if err = nativeSave(tx, "runner_tasks", task.ID, task.ProjectID, task); err != nil {
			t.Fatal(err)
		}
	}
	if err = r.event(tx, p.ID, "dispatched", map[string]string{"taskId": "writer"}); err != nil {
		t.Fatal(err)
	}
	if err = r.event(tx, q.ID, "dispatched", map[string]string{"taskId": "foreign"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Simulate a scheduler blocked on a remote call and an uncommitted writer.
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, err = r.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	uncommitted := p
	uncommitted.Goal = "uncommitted"
	if err = nativeSave(tx, "runner_projects", p.ID, "", uncommitted); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	view, err := ReadNativeRunnerView(readCtx, root, p.ID, "", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if view["project"].(map[string]any)["title"] != "工作区一" {
		t.Fatal("read uncommitted data")
	}
	if view["pendingAcknowledgements"] != 1 {
		t.Fatalf("history ACK lost: %v", view)
	}
	tasks := view["tasks"].([]map[string]any)
	if len(tasks) != 3 || tasks[1]["waitingReason"] != "等待重叠写域释放" || tasks[2]["waitingReason"] != "等待前置任务验收" {
		t.Fatalf("wrong task view: %v", tasks)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "PRIVATE-FROZEN-PROMPT") || strings.Contains(string(encoded), "不可串区") {
		t.Fatal("view leaked another area or dispatch prompt")
	}
	if _, err = ReadNativeRunnerView(readCtx, root, p.ID, "foreign", 0, false); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-area detail err=%v", err)
	}
	events, err := ReadNativeRunnerView(readCtx, root, p.ID, "", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	list := events["events"].([]map[string]any)
	if len(list) != 1 || list[0]["taskId"] != "writer" {
		t.Fatalf("event isolation: %v", events)
	}
	empty, err := ReadNativeRunnerView(readCtx, root, p.ID, "", events["cursor"].(int64), true)
	if err != nil || len(empty["events"].([]map[string]any)) != 0 {
		t.Fatalf("cursor replay: %v %v", empty, err)
	}
	all, err := ReadNativeRunnerView(readCtx, root, "", "", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(all["projects"].([]any)) != 2 {
		t.Fatalf("missing controller area: %v", all)
	}
}
