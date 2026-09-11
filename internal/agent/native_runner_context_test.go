package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

func newContextTestRunner(t *testing.T) *nativeRunner {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "runner.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`CREATE TABLE runner_tasks(id TEXT PRIMARY KEY, project_id TEXT NOT NULL, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err = initNativeEvidenceSchema(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &nativeRunner{db: db}
}

func TestNativeRunnerQueryContextEvidenceScopeAndPaging(t *testing.T) {
	r := newContextTestRunner(t)
	task := nativeRunnerTask{ID: "task-1", ProjectID: "project-1", Kind: "work", Title: "Task", Objective: "Do it", Acceptance: "Done", Round: 2, State: "returned", Result: &nativeRunnerResult{EventID: "report-1", Outcome: "completed", Summary: "large report", Terminal: true}}
	raw, _ := json.Marshal(task)
	if _, err := r.db.Exec(`INSERT INTO runner_tasks(id,project_id,value) VALUES(?,?,?)`, task.ID, task.ProjectID, raw); err != nil {
		t.Fatal(err)
	}
	if err := r.indexEvidence(context.Background(), task, *task.Result, []byte("large report body")); err != nil {
		t.Fatal(err)
	}
	list, err := r.QueryContext(context.Background(), map[string]any{"projectId": "project-1", "section": "evidence", "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	items := list["evidence"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("evidence index=%#v", items)
	}
	if _, ok := items[0]["content"]; ok {
		t.Fatal("large report leaked into evidence index")
	}
	id := items[0]["evidenceId"].(int64)
	if _, err := r.QueryContext(context.Background(), map[string]any{"projectId": "project-2", "section": "evidence", "evidenceId": id}); err == nil {
		t.Fatal("cross-project evidence was accepted")
	}
	content, err := r.QueryContext(context.Background(), map[string]any{"projectId": "project-1", "section": "evidence", "evidenceId": id, "limit": 4})
	if err != nil {
		t.Fatal(err)
	}
	if content["evidence"].(map[string]any)["content"] != "larg" {
		t.Fatalf("content=%#v", content)
	}
}
