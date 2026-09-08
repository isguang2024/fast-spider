package node

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type collaborationUpgradeTestAgent struct {
	mu     sync.Mutex
	routes []map[string]any
}

func (a *collaborationUpgradeTestAgent) Control(context.Context, string, map[string]any) (map[string]any, error) {
	return map[string]any{}, nil
}
func (a *collaborationUpgradeTestAgent) Close(context.Context) error { return nil }
func (a *collaborationUpgradeTestAgent) BindCollaborationInbox(_ context.Context, routes []map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.routes = routes
	return nil
}

func TestCollaborationUpgradeBacksUpPausedMissionAndBindsExactRoute(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	backupPath := filepath.Join(root, "backup.sqlite3")
	packet := collaborationTestPacket(root, "chat-target")
	createCollaborationTestLedger(t, dbPath, packet)
	agent := &collaborationUpgradeTestAgent{}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Version: "test-node", Agent: agent})

	claimed := callCollaborationTest(t, client, "claim", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "expectedRevision": int64(1), "itemId": "task-1",
	})
	tokenID := claimed["dispatchToken"].(string)
	token, err := client.readCollaborationToken(tokenID)
	if err != nil {
		t.Fatal(err)
	}
	missionID, taskID, generation := localCallbackIdentity(token)
	callCollaborationTest(t, client, "receipt", map[string]any{
		"dispatchToken": tokenID,
		"dispatchResult": map[string]any{
			"chatSessionId": "chat-target", "collaborationId": missionID, "taskRef": taskID,
			"callbackSessionId": "controller-1", "idempotencyKey": packet["idempotencyKey"],
		},
	})
	paused := map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "controller-1",
		"expectedRevision": int64(3), "mission": map[string]any{"status": "paused", "dispatch_enabled": false},
	}
	pausedResult := callCollaborationTest(t, client, "apply", paused)
	pausedRevision, ok := collaborationInt64(pausedResult["revision"])
	if !ok {
		t.Fatalf("paused revision=%#v", pausedResult)
	}
	upgraded, err := client.collaborationUpgrade(context.Background(), collaborationUpgradeParams{
		collaborationIdentityParams: collaborationIdentityParams{DBPath: dbPath, MissionID: "mission-1", ActorSessionID: "controller-1"},
		ExpectedRevision:            pausedRevision, BackupPath: backupPath, EvidenceRef: "upgrade:test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if upgraded["version"] != "test-node" || upgraded["revision"] != pausedRevision || upgraded["routeCount"] != 1 || upgraded["routesBound"] != true {
		t.Fatalf("upgrade=%#v", upgraded)
	}
	if _, err := filepath.Abs(backupPath); err != nil {
		t.Fatal(err)
	}
	agent.mu.Lock()
	routes := agent.routes
	agent.mu.Unlock()
	if len(routes) != 1 {
		t.Fatalf("routes=%#v", routes)
	}
	route := routes[0]
	if route["sourceSessionId"] != "chat-target" || route["targetSessionId"] != "controller-1" || route["missionId"] != missionID || route["taskId"] != taskID || route["generation"] != generation {
		t.Fatalf("route=%#v", route)
	}
	callbackRoute := route["callbackInboxRoute"].(map[string]any)
	resolvedDBPath, _ := validateCollaborationBaseIdentity(dbPath, "mission-1", "controller-1")
	if callbackRoute["dbPath"] != resolvedDBPath || callbackRoute["missionId"] != "mission-1" || callbackRoute["itemId"] != "task-1" || callbackRoute["claim"] != token.Claim {
		t.Fatalf("callback route=%#v", callbackRoute)
	}
	meta, err := readCollaborationUpgradeMission(context.Background(), backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Revision != pausedRevision || mapStringValue(meta.Mission, "id") != "mission-1" {
		t.Fatalf("backup meta=%#v", meta)
	}
	live, err := readCollaborationUpgradeMission(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if live.Revision != pausedRevision || mapStringValue(live.Mission, "status") != "paused" || mapBoolValue(live.Mission, "dispatch_enabled") {
		t.Fatalf("live meta=%#v", live)
	}
}

func TestCollaborationUpgradeRejectsUnauthorizedOrInvalidBackup(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	createCollaborationTestLedger(t, dbPath, collaborationTestPacket(root, "chat-target"))
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	response, err := client.collaborationUpgrade(context.Background(), collaborationUpgradeParams{
		collaborationIdentityParams: collaborationIdentityParams{DBPath: dbPath, MissionID: "mission-1", ActorSessionID: "coordinator-1"},
		ExpectedRevision:            1, BackupPath: filepath.Join(root, "unauthorized.sqlite3"), EvidenceRef: "upgrade:test",
	})
	if err == nil || response != nil {
		t.Fatalf("unauthorized upgrade succeeded: response=%#v err=%v", response, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "unauthorized.sqlite3")); !os.IsNotExist(statErr) {
		t.Fatalf("unauthorized upgrade created backup: %v", statErr)
	}
	response, err = client.collaborationUpgrade(context.Background(), collaborationUpgradeParams{
		collaborationIdentityParams: collaborationIdentityParams{DBPath: dbPath, MissionID: "mission-1", ActorSessionID: "controller-1"},
		ExpectedRevision:            1, BackupPath: dbPath, EvidenceRef: "upgrade:test",
	})
	if err == nil || response != nil {
		t.Fatalf("same-path upgrade succeeded: response=%#v err=%v", response, err)
	}
}

func TestCollaborationUpgradeAllowsUndispatchedCloudWithoutBinding(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	backupPath := filepath.Join(root, "backup.sqlite3")
	createCollaborationTestLedger(t, dbPath, collaborationTestPacket(root, "chat-target"))
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow("SELECT data FROM mission WHERE singleton=1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var mission map[string]any
	if err := json.Unmarshal([]byte(raw), &mission); err != nil {
		t.Fatal(err)
	}
	mission["status"], mission["dispatch_enabled"] = "paused", false
	updated, _ := json.Marshal(mission)
	if _, err := db.Exec("UPDATE mission SET data=? WHERE singleton=1", string(updated)); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	result, err := client.collaborationUpgrade(context.Background(), collaborationUpgradeParams{
		collaborationIdentityParams: collaborationIdentityParams{DBPath: dbPath, MissionID: "mission-1", ActorSessionID: "controller-1"},
		ExpectedRevision:            1, BackupPath: backupPath, EvidenceRef: "upgrade:planned-cloud",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["routeCount"] != 0 || result["routesBound"] != true {
		t.Fatalf("undispatched cloud route=%#v", result)
	}
}

func preparePausedUpgradeRoute(t *testing.T, root string, agent AgentController) (string, int64, collaborationToken, *Client) {
	t.Helper()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "chat-target")
	createCollaborationTestLedger(t, dbPath, packet)
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Agent: agent})
	claimed := callCollaborationTest(t, client, "claim", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "expectedRevision": int64(1), "itemId": "task-1",
	})
	tokenID := claimed["dispatchToken"].(string)
	token, err := client.readCollaborationToken(tokenID)
	if err != nil {
		t.Fatal(err)
	}
	missionID, taskID, _ := localCallbackIdentity(token)
	callCollaborationTest(t, client, "receipt", map[string]any{
		"dispatchToken": tokenID,
		"dispatchResult": map[string]any{
			"chatSessionId": "chat-target", "collaborationId": missionID, "taskRef": taskID,
			"callbackSessionId": "controller-1", "idempotencyKey": packet["idempotencyKey"],
		},
	})
	paused := callCollaborationTest(t, client, "apply", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "controller-1", "expectedRevision": int64(3),
		"mission": map[string]any{"status": "paused", "dispatch_enabled": false},
	})
	revision, ok := collaborationInt64(paused["revision"])
	if !ok {
		t.Fatalf("paused revision=%#v", paused)
	}
	return dbPath, revision, token, client
}

func snapshotUpgradeItems(t *testing.T, dbPath string) map[string]map[string]any {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT id,data FROM items ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]map[string]any{}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			t.Fatal(err)
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			t.Fatal(err)
		}
		snapshot := selectCollaborationFields(item, "id", "binding", "phase", "claim", "depends_on")
		snapshot["data"] = raw
		result[id] = snapshot
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func addUpgradeDoneItem(t *testing.T, dbPath string) {
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
	item["id"] = "done-1"
	item["phase"] = "done"
	item["callback"] = "acked"
	item["result"] = "completed"
	item["validation"] = "not_required"
	item["integration"] = "done"
	item["evidence"] = []any{"done:evidence"}
	item["terminal_ref"] = "done:evidence"
	encoded, _ := json.Marshal(item)
	if _, err := db.Exec("INSERT INTO items(id,phase,kind,revision,data) VALUES(?,?,?,?,?)", "done-1", "done", "implement", 1, string(encoded)); err != nil {
		t.Fatal(err)
	}
}

func TestCollaborationUpgradeRepeatWithNewBackupPreservesItems(t *testing.T) {
	root := t.TempDir()
	agent := &collaborationUpgradeTestAgent{}
	dbPath, revision, _, client := preparePausedUpgradeRoute(t, root, agent)
	addUpgradeDoneItem(t, dbPath)
	before := snapshotUpgradeItems(t, dbPath)
	first, err := client.collaborationUpgrade(context.Background(), collaborationUpgradeParams{
		collaborationIdentityParams: collaborationIdentityParams{DBPath: dbPath, MissionID: "mission-1", ActorSessionID: "controller-1"},
		ExpectedRevision:            revision, BackupPath: filepath.Join(root, "backup-first.sqlite3"), EvidenceRef: "upgrade:first",
	})
	if err != nil || first["routeCount"] != 1 {
		t.Fatalf("first upgrade=%#v err=%v", first, err)
	}
	afterFirst := snapshotUpgradeItems(t, dbPath)
	if !collaborationMapsEqual(map[string]any{"items": before}, map[string]any{"items": afterFirst}) {
		t.Fatalf("first upgrade changed item facts: before=%#v after=%#v", before, afterFirst)
	}
	second, err := client.collaborationUpgrade(context.Background(), collaborationUpgradeParams{
		collaborationIdentityParams: collaborationIdentityParams{DBPath: dbPath, MissionID: "mission-1", ActorSessionID: "controller-1"},
		ExpectedRevision:            revision, BackupPath: filepath.Join(root, "backup-second.sqlite3"), EvidenceRef: "upgrade:repeat",
	})
	if err != nil || second["routeCount"] != 1 || second["revision"] != revision {
		t.Fatalf("repeat upgrade=%#v err=%v", second, err)
	}
	afterSecond := snapshotUpgradeItems(t, dbPath)
	if !collaborationMapsEqual(map[string]any{"items": before}, map[string]any{"items": afterSecond}) {
		t.Fatalf("repeat upgrade changed item facts: before=%#v after=%#v", before, afterSecond)
	}
	if afterSecond["done-1"]["phase"] != "done" {
		t.Fatalf("done item reopened: %#v", afterSecond["done-1"])
	}
}

func TestCollaborationUpgradePausedControllerCASMismatch(t *testing.T) {
	root := t.TempDir()
	dbPath, revision, _, client := preparePausedUpgradeRoute(t, root, &collaborationUpgradeTestAgent{})
	backupPath := filepath.Join(root, "cas-mismatch.sqlite3")
	result, err := client.collaborationUpgrade(context.Background(), collaborationUpgradeParams{
		collaborationIdentityParams: collaborationIdentityParams{DBPath: dbPath, MissionID: "mission-1", ActorSessionID: "controller-1"},
		ExpectedRevision:            revision - 1, BackupPath: backupPath, EvidenceRef: "upgrade:stale",
	})
	if err == nil || result != nil {
		t.Fatalf("stale paused upgrade succeeded: result=%#v err=%v", result, err)
	}
	if _, statErr := os.Stat(backupPath); !os.IsNotExist(statErr) {
		t.Fatalf("stale upgrade created backup: %v", statErr)
	}
}

type collaborationUpgradeFailingAgent struct {
	collaborationUpgradeTestAgent
	fail bool
}

func (a *collaborationUpgradeFailingAgent) BindCollaborationInbox(ctx context.Context, routes []map[string]any) error {
	if a.fail {
		return errors.New("binder unavailable")
	}
	return a.collaborationUpgradeTestAgent.BindCollaborationInbox(ctx, routes)
}

func TestCollaborationUpgradeBinderFailureKeepsPausedAndCanRetry(t *testing.T) {
	root := t.TempDir()
	agent := &collaborationUpgradeFailingAgent{fail: true}
	dbPath, revision, _, client := preparePausedUpgradeRoute(t, root, agent)
	first, err := client.collaborationUpgrade(context.Background(), collaborationUpgradeParams{
		collaborationIdentityParams: collaborationIdentityParams{DBPath: dbPath, MissionID: "mission-1", ActorSessionID: "controller-1"},
		ExpectedRevision:            revision, BackupPath: filepath.Join(root, "binder-failure.sqlite3"), EvidenceRef: "upgrade:binder-failure",
	})
	if err == nil || first != nil {
		t.Fatalf("binder failure was reported as success: result=%#v err=%v", first, err)
	}
	live, err := readCollaborationUpgradeMission(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if live.Revision != revision || mapStringValue(live.Mission, "status") != "paused" || mapBoolValue(live.Mission, "dispatch_enabled") {
		t.Fatalf("binder failure changed mission state: %#v", live)
	}
	agent.fail = false
	second, err := client.collaborationUpgrade(context.Background(), collaborationUpgradeParams{
		collaborationIdentityParams: collaborationIdentityParams{DBPath: dbPath, MissionID: "mission-1", ActorSessionID: "controller-1"},
		ExpectedRevision:            revision, BackupPath: filepath.Join(root, "binder-retry.sqlite3"), EvidenceRef: "upgrade:binder-retry",
	})
	if err != nil || second["routeCount"] != 1 || second["routesBound"] != true {
		t.Fatalf("binder retry=%#v err=%v", second, err)
	}
}
