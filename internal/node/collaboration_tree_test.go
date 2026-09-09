package node

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func treeTestClient(t *testing.T, root string) *Client {
	t.Helper()
	return NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
}

func installTreeTestSchema(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	columns, err := collaborationTreeColumns(context.Background(), db, "items")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []struct {
		name, statement string
	}{
		{"title", "ALTER TABLE items ADD COLUMN title TEXT NOT NULL DEFAULT ''"},
		{"workstream_id", "ALTER TABLE items ADD COLUMN workstream_id TEXT NOT NULL DEFAULT ''"},
		{"archived", "ALTER TABLE items ADD COLUMN archived INTEGER NOT NULL DEFAULT 0"},
	} {
		if columns[field.name] {
			continue
		}
		if _, err := db.Exec(field.statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS workstreams(id TEXT PRIMARY KEY,title TEXT NOT NULL,objective TEXT NOT NULL DEFAULT '',owner TEXT NOT NULL DEFAULT '',archived INTEGER NOT NULL DEFAULT 0,revision INTEGER NOT NULL DEFAULT 0,data TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
}

func callTreeTest(t *testing.T, client *Client, action string, params map[string]any) map[string]any {
	t.Helper()
	result, err := client.collaborationTreeControl(context.Background(), action, params)
	if err != nil {
		t.Fatalf("tree %s failed: %v", action, err)
	}
	return result
}

func insertTreeTestItem(t *testing.T, dbPath, id string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	item := map[string]any{"id": id, "kind": "implement", "phase": "planned", "owner": "owner-1", "executor": "local", "next_action": "plan", "evidence": []any{}, "depends_on": []any{}, "contract_refs": []any{}, "callback": "none", "result": "none", "validation": "pending", "integration": "pending"}
	raw, _ := json.Marshal(item)
	if _, err := db.Exec("INSERT INTO items(id,phase,kind,revision,data) VALUES(?,?,?,?,?)", id, "planned", "implement", 1, string(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestCollaborationTreeRestartAndPagination(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	createCollaborationTestLedger(t, dbPath, collaborationTestPacket(root, "chat-target"))
	installTreeTestSchema(t, dbPath)
	insertTreeTestItem(t, dbPath, "task-2")
	insertTreeTestItem(t, dbPath, "task-3")
	client := treeTestClient(t, root)
	identity := map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "controller-1"}

	updated := callTreeTest(t, client, "tree_update", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "controller-1", "expectedRevision": int64(1),
		"mission":      map[string]any{"objective": "Ship the tree"},
		"authorityRef": "decision:tree-1",
		"workstreams": []any{
			map[string]any{"id": "stream-a", "title": "Core", "objective": "Implement core", "owner": "owner-1"},
			map[string]any{"id": "stream-b", "title": "UI", "objective": "Implement UI", "owner": "owner-1"},
		},
		"items": []any{map[string]any{"id": "task-1", "title": "First task", "workstream_id": "stream-a"}},
	})
	if updated["revision"] != int64(5) {
		t.Fatalf("unexpected revision=%#v", updated)
	}
	second := treeTestClient(t, root)
	identity["limit"] = int64(1)
	page := callTreeTest(t, second, "tree", identity)
	if page["revision"] != int64(5) || len(page["items"].([]map[string]any)) != 1 || page["nextAfter"] == nil {
		t.Fatalf("restart tree=%#v", page)
	}
	page2 := callTreeTest(t, second, "tree", map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "controller-1", "limit": int64(1), "after": page["nextAfter"]})
	if len(page2["items"].([]map[string]any)) != 1 {
		t.Fatalf("second page=%#v", page2)
	}
	streams := page["workstreams"].([]any)
	if len(streams) != 1 || streams[0].(map[string]any)["title"] != "Core" || page["nextWorkstreamAfter"] == nil {
		t.Fatalf("workstreams=%#v", page)
	}
	streamPage := callTreeTest(t, second, "tree", map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "controller-1", "limit": int64(1), "workstreamAfter": page["nextWorkstreamAfter"]})
	streamPageItems := streamPage["workstreams"].([]any)
	if len(streamPageItems) != 1 || streamPageItems[0].(map[string]any)["id"] != "stream-b" || streamPage["nextWorkstreamAfter"] != nil {
		t.Fatalf("second workstream page=%#v", streamPage)
	}
	mission := page["mission"].(map[string]any)
	if mission["objective"] != "Ship the tree" || mission["tree_update_authority_ref"] != "decision:tree-1" {
		t.Fatalf("mission=%#v", mission)
	}
}

func TestCollaborationTreeTaskIsolationAndUnauthorizedEdit(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.sqlite3")
	second := filepath.Join(root, "second.sqlite3")
	createCollaborationTestLedger(t, first, collaborationTestPacket(root, "chat-target"))
	createCollaborationTestLedger(t, second, collaborationTestPacket(root, "chat-target"))
	installTreeTestSchema(t, first)
	installTreeTestSchema(t, second)
	client := treeTestClient(t, root)
	params := map[string]any{"dbPath": first, "missionId": "mission-1", "actorSessionId": "coordinator-1", "expectedRevision": int64(1), "items": []any{map[string]any{"id": "task-1", "title": "should fail"}}}
	if _, err := client.collaborationTreeControl(context.Background(), "tree_update", params); err == nil {
		t.Fatal("coordinator was allowed to edit tree")
	}
	if _, err := client.collaborationTreeControl(context.Background(), "tree_update", map[string]any{"dbPath": first, "missionId": "mission-1", "actorSessionId": "controller-1", "expectedRevision": int64(1), "items": []any{map[string]any{"id": "task-1", "archived": true}}}); err == nil {
		t.Fatal("tree_update bypassed archive completion checks")
	}
	if _, err := client.collaborationTreeControl(context.Background(), "tree_update", map[string]any{"dbPath": first, "missionId": "mission-1", "actorSessionId": "controller-1", "expectedRevision": int64(1), "items": []any{map[string]any{"id": "task-1", "workstream_id": "missing"}}}); err == nil {
		t.Fatal("task accepted an unknown workstream")
	}
	callTreeTest(t, client, "tree_update", map[string]any{"dbPath": first, "missionId": "mission-1", "actorSessionId": "controller-1", "expectedRevision": int64(1), "items": []any{map[string]any{"id": "task-1", "title": "first only"}}})
	other := callTreeTest(t, client, "tree", map[string]any{"dbPath": second, "missionId": "mission-1", "actorSessionId": "controller-1"})
	item := other["items"].([]map[string]any)[0]
	if item["title"] != "" {
		t.Fatalf("cross mission mutation leaked: %#v", item)
	}
}

func TestCollaborationTreeArchiveIsIdempotentAndPreservesCounts(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "chat-target")
	createCollaborationTestLedger(t, dbPath, packet)
	installTreeTestSchema(t, dbPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow("SELECT data FROM items WHERE id='task-1'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var item map[string]any
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatal(err)
	}
	item["phase"] = "done"
	item["binding"] = map[string]any{"chatSessionId": "chat-target", "taskRef": "task-ref-1", "idempotencyKey": packet["idempotencyKey"]}
	item["callback"] = "acked"
	item["result"] = "completed"
	item["validation"] = "not_required"
	item["integration"] = "done"
	item["evidence"] = []any{"test:done"}
	item["terminal_ref"] = "test:done"
	updated, _ := json.Marshal(item)
	if _, err := db.Exec("UPDATE items SET phase='done',data=? WHERE id='task-1'", string(updated)); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	client := treeTestClient(t, root)
	archived := callTreeTest(t, client, "archive", map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "controller-1", "expectedRevision": int64(1), "itemId": "task-1", "archived": true})
	if archived["archived"] != true {
		t.Fatalf("archive=%#v", archived)
	}
	collapsed := callTreeTest(t, client, "tree", map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "controller-1"})
	if len(collapsed["items"].([]map[string]any)) != 0 {
		t.Fatalf("archived task was not folded: %#v", collapsed)
	}
	counts := collapsed["counts"].(map[string]any)
	pageCounts := counts["workstreams"].([]any)
	globalCounts := counts["global"].(map[string]any)
	if len(pageCounts) != 1 || globalCounts["archived"] != int64(1) {
		t.Fatalf("counts=%#v", counts)
	}
	replayed := callTreeTest(t, client, "archive", map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "controller-1", "expectedRevision": collapsed["revision"], "itemId": "task-1", "archived": true})
	if replayed["replayed"] != true {
		t.Fatalf("archive replay=%#v", replayed)
	}
}
