package localmcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/localbridge"
	"github.com/isguang2024/fast-spider/internal/node"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	_ "modernc.org/sqlite"
)

func TestCollaborationControlMCPLocalBridgeNodeFakeAgentE2E(t *testing.T) {
	dataDir := t.TempDir()
	workingDirectory := t.TempDir()
	dbPath := filepath.Join(workingDirectory, "collaboration.sqlite3")
	packet := map[string]any{
		"machineId":         "machine-e2e",
		"callbackSessionId": "controller-e2e",
		"workingDirectory":  workingDirectory,
		"prompt":            "Run the bounded local collaboration test.",
		"idempotencyKey":    "local-mcp-e2e-idempotency-001",
		"accessMode":        "write",
		"writeScope":        "src/task",
		"callbackType":      "text",
	}
	createLocalCollaborationLedger(t, dbPath, packet)

	agent := &localCollaborationE2EAgent{
		results: map[string]map[string]any{
			"session.create": {"sessionId": "cloud-local-mcp-e2e"},
		},
	}
	nodeClient := node.NewLocalCapabilityClient(node.Config{DataDir: dataDir, Agent: agent})

	ctx, cancel := context.WithCancel(context.Background())
	bridgeDone := make(chan error, 1)
	go func() {
		bridgeDone <- localbridge.Run(ctx, dataDir, func(callCtx context.Context, req protocolv1.CapabilityRequest) protocolv1.CapabilityResponse {
			return nodeClient.HandleLocalCapability(callCtx, req)
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-bridgeDone:
			if err != nil {
				t.Errorf("local bridge stopped with error: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("local bridge did not stop after cancellation")
		}
	})

	waitForLocalBridgeE2E(t, dataDir)
	server := newServer(dataDir, "e2e", slog.New(slog.NewTextHandler(io.Discard, nil)), localbridge.Call)
	client := connectTestClient(t, server)

	tools, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	if got, want := names, []string{"collaboration_control", "local_capability", "local_machine"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("tools=%v", got)
	}

	callResult, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "collaboration_control",
		Arguments: map[string]any{
			"action": "dispatch",
			"params": map[string]any{
				"dbPath":         dbPath,
				"missionId":      "mission-e2e",
				"actorSessionId": "coordinator-e2e",
				"itemId":         "task-e2e",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if callResult.IsError {
		if len(callResult.Content) > 0 {
			if text, ok := callResult.Content[0].(*mcp.TextContent); ok {
				t.Fatalf("collaboration_control returned MCP error: %s", text.Text)
			}
		}
		t.Fatalf("collaboration_control returned MCP error: content=%#v", callResult.Content)
	}
	structured, ok := callResult.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured result=%T %#v", callResult.StructuredContent, callResult.StructuredContent)
	}
	result, ok := structured["result"].(map[string]any)
	if !ok {
		t.Fatalf("structured result payload=%#v", structured)
	}
	if result["phase"] != "active" || result["callerShouldYield"] != true || result["activePollingAllowed"] != false {
		t.Fatalf("dispatch result=%#v", result)
	}

	if got, want := agent.actionsSnapshot(), []string{"session.callback.prepare", "provider.readiness", "session.create", "session.callback.register", "session.callback.arm"}; !equalStrings(got, want) {
		t.Fatalf("provider actions=%v want=%v", got, want)
	}
	createParams := agent.paramsFor("session.create")
	if createParams["idempotencyKey"] != packet["idempotencyKey"] {
		t.Fatalf("create idempotency key=%v want=%v", createParams["idempotencyKey"], packet["idempotencyKey"])
	}
	resolvedWorkingDirectory, err := node.ResolveMachinePath(workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if createParams["workingDirectory"] != resolvedWorkingDirectory {
		t.Fatalf("create working directory=%v want=%v", createParams["workingDirectory"], resolvedWorkingDirectory)
	}
	prompt, _ := createParams["prompt"].(string)
	for _, required := range []string{"FAST_SPIDER_LOCAL_COLLABORATION_V1", packet["prompt"].(string)} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("create prompt missing %q: %q", required, prompt)
		}
	}

	item := readLocalCollaborationItem(t, dbPath)
	if item["phase"] != "active" {
		t.Fatalf("ledger item=%#v", item)
	}
}

type localCollaborationE2EAgent struct {
	mu      sync.Mutex
	actions []string
	params  map[string]map[string]any
	results map[string]map[string]any
}

func (a *localCollaborationE2EAgent) Control(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.actions = append(a.actions, action)
	if a.params == nil {
		a.params = map[string]map[string]any{}
	}
	a.params[action] = params
	if result := a.results[action]; result != nil {
		return result, nil
	}
	return map[string]any{"prepared": true}, nil
}

func (a *localCollaborationE2EAgent) Close(context.Context) error { return nil }

func (a *localCollaborationE2EAgent) actionsSnapshot() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.actions...)
}

func (a *localCollaborationE2EAgent) paramsFor(action string) map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.params[action]
}

func waitForLocalBridgeE2E(t *testing.T, dataDir string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		_, err := localbridge.Call(ctx, dataDir, protocolv1.CapabilityRequest{
			Capability: "collaboration.control",
			Action:     "verify",
			Params:     map[string]any{"dispatchToken": "invalid"},
		})
		cancel()
		if err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("local bridge did not become available")
}

func createLocalCollaborationLedger(t *testing.T, dbPath string, packet map[string]any) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE mission(singleton INTEGER PRIMARY KEY CHECK(singleton=1), data TEXT NOT NULL, revision INTEGER NOT NULL);
		CREATE TABLE items(id TEXT PRIMARY KEY, phase TEXT NOT NULL, kind TEXT NOT NULL, revision INTEGER NOT NULL, data TEXT NOT NULL, dispatch_key TEXT UNIQUE, task_ref TEXT UNIQUE);
		CREATE TABLE events(revision INTEGER PRIMARY KEY, object_id TEXT NOT NULL, phase TEXT NOT NULL);
		CREATE TABLE observation(singleton INTEGER PRIMARY KEY CHECK(singleton=1), data TEXT NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	resolvedDBPath, err := node.ResolveMachinePath(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	mission := map[string]any{
		"id": "mission-e2e", "controller": "controller-e2e", "coordinator": "coordinator-e2e", "db_path": resolvedDBPath,
		"status": "active", "dispatch_enabled": true, "capacity": map[string]any{"cloud": 2, "local": 1},
		"legacy_callback_sessions": []any{}, "schema": 1,
	}
	item := map[string]any{
		"id": "task-e2e", "kind": "implement", "phase": "ready", "owner": "owner-e2e", "executor": "cloud",
		"next_action": "dispatch", "evidence": []any{}, "depends_on": []any{}, "contract_refs": []any{},
		"packet": packet, "callback": "none", "result": "none", "validation": "pending", "integration": "pending",
		"blocker": nil, "claim": nil, "source_ref": nil,
	}
	missionRaw, _ := json.Marshal(mission)
	itemRaw, _ := json.Marshal(item)
	if _, err := db.Exec("INSERT INTO mission VALUES(1,?,1)", string(missionRaw)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO items VALUES(?,?,?,?,?,?,?)", "task-e2e", "ready", "implement", 1, string(itemRaw), packet["idempotencyKey"], nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO events VALUES(1,'task-e2e','ready')"); err != nil {
		t.Fatal(err)
	}
}

func readLocalCollaborationItem(t *testing.T, dbPath string) map[string]any {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw string
	if err := db.QueryRow("SELECT data FROM items WHERE id='task-e2e'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var item map[string]any
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatal(err)
	}
	return item
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
