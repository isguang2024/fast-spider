package node

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	_ "modernc.org/sqlite"
)

func TestCollaborationControlIsLocalOnlyAndClosesDispatchReceipt(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "chat-target")
	createCollaborationTestLedger(t, dbPath, packet)
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	params := map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"expectedRevision": 1, "itemId": "task-1",
	}

	remote := client.handleCapabilityRequest(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "remote-collaboration", Capability: "collaboration.control", Action: "claim", Params: params,
	})
	if remote.Error == nil || remote.Error.Code != "UNSUPPORTED_CAPABILITY" {
		t.Fatalf("remote collaboration control=%#v", remote)
	}

	claimed := callCollaborationTest(t, client, "claim", params)
	token, _ := claimed["dispatchToken"].(string)
	if len(token) != 64 || claimed["packetSHA256"] == "" {
		t.Fatalf("claim=%#v", claimed)
	}
	dispatchRequest, _ := claimed["dispatchRequest"].(map[string]any)
	if dispatchRequest["action"] != "dispatch" || !collaborationMapsEqual(dispatchRequest["params"].(map[string]any), packet) {
		t.Fatalf("dispatch request=%#v", dispatchRequest)
	}

	verified := callCollaborationTest(t, client, "verify", map[string]any{"dispatchToken": token})
	if !collaborationMapsEqual(verified["dispatchRequest"].(map[string]any), dispatchRequest) {
		t.Fatalf("verify=%#v", verified)
	}
	receipt := map[string]any{"structuredContent": map[string]any{"result": map[string]any{
		"chatSessionId": "chat-target", "collaborationId": "collaboration-1", "taskRef": "task-ref-1",
		"callbackSessionId": "controller-1",
	}}}
	completed := callCollaborationTest(t, client, "receipt", map[string]any{"dispatchToken": token, "dispatchResult": receipt})
	if completed["phase"] != "active" {
		t.Fatalf("receipt=%#v", completed)
	}
	replayed := callCollaborationTest(t, client, "receipt", map[string]any{"dispatchToken": token, "dispatchResult": map[string]any{"invalid": true}})
	if !collaborationMapsEqual(completed, replayed) {
		t.Fatalf("completed token was not idempotent: first=%#v second=%#v", completed, replayed)
	}
	item := readCollaborationTestItem(t, dbPath)
	if item["phase"] != "active" || item["claim"] == nil {
		t.Fatalf("stored item=%#v", item)
	}
	binding, _ := item["binding"].(map[string]any)
	if binding["chatSessionId"] != "chat-target" || binding["idempotencyKey"] != packet["idempotencyKey"] {
		t.Fatalf("binding=%#v", binding)
	}
}

func TestCollaborationControlSerializesClaimsAndPreservesUncertainPacket(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "chat-target")
	createCollaborationTestLedger(t, dbPath, packet)
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	params := map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"expectedRevision": 1, "itemId": "task-1",
	}

	var wg sync.WaitGroup
	responses := make(chan protocolv1.CapabilityResponse, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
				RequestId: "claim-race", Capability: "collaboration.control", Action: "claim", Params: params,
			})
		}()
	}
	wg.Wait()
	close(responses)
	var claimed map[string]any
	var success, rejected int
	for response := range responses {
		if response.Error != nil {
			rejected++
			continue
		}
		success++
		claimed = response.Result
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("claim race success=%d rejected=%d", success, rejected)
	}
	token := claimed["dispatchToken"].(string)
	uncertain := callCollaborationTest(t, client, "receipt", map[string]any{
		"dispatchToken":  token,
		"dispatchResult": map[string]any{"structuredContent": map[string]any{"result": map[string]any{"deliveryInDoubt": true}}},
	})
	if uncertain["phase"] != "in_doubt" || uncertain["dispatchToken"] != token {
		t.Fatalf("uncertain=%#v", uncertain)
	}
	recovered := callCollaborationTest(t, client, "recover", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1",
	})
	if recovered["dispatchToken"] != token || !collaborationMapsEqual(recovered["dispatchRequest"].(map[string]any), claimed["dispatchRequest"].(map[string]any)) {
		t.Fatalf("recover=%#v claimed=%#v", recovered, claimed)
	}
}

func TestCollaborationControlRejectsReceiptIdentityDrift(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "chat-target")
	createCollaborationTestLedger(t, dbPath, packet)
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	claimed := callCollaborationTest(t, client, "claim", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"expectedRevision": 1, "itemId": "task-1",
	})
	token := claimed["dispatchToken"].(string)
	response := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "receipt-drift", Capability: "collaboration.control", Action: "receipt",
		Params: map[string]any{"dispatchToken": token, "dispatchResult": map[string]any{
			"chatSessionId": "wrong-chat", "collaborationId": "collaboration-1", "taskRef": "task-ref-1",
		}},
	})
	if response.Error == nil {
		t.Fatalf("drifted receipt succeeded: %#v", response.Result)
	}
	item := readCollaborationTestItem(t, dbPath)
	if item["phase"] != "dispatching" || item["binding"] != nil {
		t.Fatalf("drifted receipt changed item=%#v", item)
	}

	missingRevision := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "missing-revision", Capability: "collaboration.control", Action: "claim",
		Params: map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"},
	})
	if missingRevision.Error == nil {
		t.Fatal("missing expectedRevision was accepted")
	}
}

func TestCollaborationControlRecordsConfirmedNoCreate(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "chat-target")
	createCollaborationTestLedger(t, dbPath, packet)
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	claimed := callCollaborationTest(t, client, "claim", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"expectedRevision": 1, "itemId": "task-1",
	})
	token := claimed["dispatchToken"].(string)

	rejected := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "not-created-without-proof", Capability: "collaboration.control", Action: "not_created",
		Params: map[string]any{"dispatchToken": token, "noTaskCreated": false, "evidenceRef": "fs:request-rejected"},
	})
	if rejected.Error == nil {
		t.Fatal("not_created without an explicit confirmation was accepted")
	}
	if item := readCollaborationTestItem(t, dbPath); item["phase"] != "dispatching" {
		t.Fatalf("rejected not_created changed item=%#v", item)
	}

	completed := callCollaborationTest(t, client, "not_created", map[string]any{
		"dispatchToken": token, "noTaskCreated": true, "evidenceRef": "fs:request-rejected",
	})
	if completed["phase"] != "dispatch_rejected" || completed["evidenceRef"] != "fs:request-rejected" {
		t.Fatalf("not_created=%#v", completed)
	}
	item := readCollaborationTestItem(t, dbPath)
	if item["phase"] != "dispatch_rejected" || item["terminal_ref"] != "fs:request-rejected" {
		t.Fatalf("stored rejected item=%#v", item)
	}
	evidence := collaborationStringList(item["evidence"])
	if len(evidence) != 1 || evidence[0] != "fs:request-rejected" {
		t.Fatalf("stored evidence=%#v", evidence)
	}
	replayed := callCollaborationTest(t, client, "not_created", map[string]any{
		"dispatchToken": token, "noTaskCreated": true, "evidenceRef": "fs:request-rejected",
	})
	if !collaborationMapsEqual(completed, replayed) {
		t.Fatalf("completed not_created token was not idempotent: first=%#v second=%#v", completed, replayed)
	}
}

func TestCollaborationControlRecoverRequiresActiveBoundCoordinator(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "chat-target")
	createCollaborationTestLedger(t, dbPath, packet)
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	claimed := callCollaborationTest(t, client, "claim", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"expectedRevision": 1, "itemId": "task-1",
	})
	if claimed["dispatchToken"] == "" {
		t.Fatalf("claim=%#v", claimed)
	}

	foreign := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "recover-as-controller", Capability: "collaboration.control", Action: "recover",
		Params: map[string]any{
			"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "controller-1", "itemId": "task-1",
		},
	})
	if foreign.Error == nil {
		t.Fatalf("controller recovered coordinator dispatch=%#v", foreign.Result)
	}

	setCollaborationTestMissionDispatch(t, dbPath, "paused", false)
	paused := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "recover-while-paused", Capability: "collaboration.control", Action: "recover",
		Params: map[string]any{
			"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1",
		},
	})
	if paused.Error == nil {
		t.Fatalf("paused mission returned a dispatch request=%#v", paused.Result)
	}
}

func TestCollaborationTokenCleanupRemovesOnlyExpiredRecords(t *testing.T) {
	root := t.TempDir()
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	expiredToken := strings.Repeat("1", 64)
	currentToken := strings.Repeat("2", 64)
	expiredPath, err := client.collaborationTokenPath(expiredToken)
	if err != nil {
		t.Fatal(err)
	}
	expiredRaw, _ := json.Marshal(collaborationToken{Version: collaborationTokenVersion, Completed: map[string]any{"phase": "active"}, ExpiresAt: time.Now().Add(-time.Minute).Unix()})
	if err := os.WriteFile(expiredPath, expiredRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	current := collaborationToken{Version: collaborationTokenVersion, Completed: map[string]any{"phase": "active"}, ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if err := client.writeCollaborationToken(currentToken, current); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(expiredPath); !os.IsNotExist(err) {
		t.Fatalf("expired token still exists: %v", err)
	}
	currentPath, err := client.collaborationTokenPath(currentToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(currentPath); err != nil {
		t.Fatalf("current token was removed: %v", err)
	}
}

func callCollaborationTest(t *testing.T, client *Client, action string, params map[string]any) map[string]any {
	t.Helper()
	response := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "local-collaboration-" + action, Capability: "collaboration.control", Action: action, Params: params,
	})
	if response.Error != nil {
		t.Fatalf("%s failed: %#v", action, response.Error)
	}
	delete(response.Result, "timing")
	return response.Result
}

func collaborationTestPacket(root, target string) map[string]any {
	return map[string]any{
		"machineId": "machine-1", "callbackSessionId": "controller-1", "workingDirectory": root,
		"prompt": "Implement and test the bounded task.", "idempotencyKey": "mission-task-key-001",
		"targetSessionId": target, "accessMode": "write", "writeScope": "src/task",
		"callbackType": "text",
	}
}

func createCollaborationTestLedger(t *testing.T, dbPath string, packet map[string]any) {
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
	resolved, err := ResolveMachinePath(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	mission := map[string]any{
		"id": "mission-1", "controller": "controller-1", "coordinator": "coordinator-1", "db_path": resolved,
		"status": "active", "dispatch_enabled": true, "capacity": map[string]any{"cloud": 2, "local": 1},
		"legacy_callback_sessions": []any{}, "schema": 1,
	}
	item := map[string]any{
		"id": "task-1", "kind": "implement", "phase": "ready", "owner": "owner-1", "executor": "cloud",
		"next_action": "dispatch", "evidence": []any{}, "depends_on": []any{}, "contract_refs": []any{},
		"packet": packet, "callback": "none", "result": "none", "validation": "pending", "integration": "pending",
		"blocker": nil, "claim": nil, "source_ref": nil,
	}
	missionRaw, _ := json.Marshal(mission)
	itemRaw, _ := json.Marshal(item)
	if _, err := db.Exec("INSERT INTO mission VALUES(1,?,1)", string(missionRaw)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO items VALUES(?,?,?,?,?,?,?)", "task-1", "ready", "implement", 1, string(itemRaw), packet["idempotencyKey"], nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO events VALUES(1,'task-1','ready')"); err != nil {
		t.Fatal(err)
	}
}

func readCollaborationTestItem(t *testing.T, dbPath string) map[string]any {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw string
	if err := db.QueryRow("SELECT data FROM items WHERE id='task-1'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var item map[string]any
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatal(err)
	}
	return item
}

func setCollaborationTestMissionDispatch(t *testing.T, dbPath, status string, enabled bool) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw string
	var revision int64
	if err := db.QueryRow("SELECT data, revision FROM mission WHERE singleton=1").Scan(&raw, &revision); err != nil {
		t.Fatal(err)
	}
	var mission map[string]any
	if err := json.Unmarshal([]byte(raw), &mission); err != nil {
		t.Fatal(err)
	}
	mission["status"] = status
	mission["dispatch_enabled"] = enabled
	updated, err := json.Marshal(mission)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE mission SET data=? WHERE singleton=1", string(updated)); err != nil {
		t.Fatal(err)
	}
}
