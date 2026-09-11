package agent

import (
	"context"
	"path/filepath"
	"testing"
)

func TestNativeRunnerContextUsesAssignedProjectAndCurrentGeneration(t *testing.T) {
	r, _, p := newNativeRunnerForTest(t, "context", nil)
	task := addNativeTask(t, r, p.ID, "planner-reader", "")
	other := addNativeTask(t, r, p.ID, "related-work", "")
	task.Request = &nativeRunnerDispatch{ProjectID: p.ID, TaskID: task.ID, Round: task.Round, ControllerSessionID: "controller", ResultPath: filepath.Join(r.dir, "result.md")}
	task.Receipt = &nativeRunnerReceipt{SessionID: "cloud-context", TaskRef: nativeRunnerTaskRef(*task.Request), Generation: int64(task.Round), ResultPath: task.Request.ResultPath}
	task.State = "active"
	if err := r.saveTask(context.Background(), task, ""); err != nil {
		t.Fatal(err)
	}
	store := newSessionCallbackStore(t.TempDir())
	registerNativeRecoveryRoute(t, store, p.ID, task.ID, task.Receipt.SessionID, int64(task.Round))
	m := &AgentManager{callbackStore: store, nativeRunner: r}
	transport := newNativeRunnerTransport(m, t.TempDir())
	m.nativeRunnerTransport = transport
	query := map[string]any{"taskRef": task.Receipt.TaskRef, "section": "task", "taskId": other.ID}
	if out, err := m.Control(context.Background(), "runner.context", map[string]any{"responseContent": query}); err != nil || out["projectId"] != p.ID {
		t.Fatalf("out=%v err=%v", out, err)
	}

	if out, err := m.Control(context.Background(), "runner.context", map[string]any{"projectId": p.ID, "section": "task", "taskId": other.ID, "fields": []string{"scope", "checks"}}); err != nil || out["task"] == nil {
		t.Fatalf("local query out=%v err=%v", out, err)
	}
	query["projectId"] = "foreign"
	if _, err := transport.Context(context.Background(), query); err == nil {
		t.Fatal("cross-project context accepted")
	}
	delete(query, "projectId")
	task.Round++
	if err := r.saveTask(context.Background(), task, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Context(context.Background(), query); err == nil {
		t.Fatal("stale generation context accepted")
	}
}
