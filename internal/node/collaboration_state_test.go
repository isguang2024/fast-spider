package node

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestCollaborationStateLifecycleUsesStructuredParamsWithoutProviderCalls(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "mission", "collaboration.sqlite3")
	agent := &collaborationTestAgent{}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Agent: agent})

	initialized := callCollaborationTest(t, client, "init", collaborationStateInitParams(dbPath, []any{collaborationStateLocalItem("local-1", "planned")}))
	if revision, ok := collaborationInt64(initialized["revision"]); !ok || revision != 1 {
		t.Fatalf("init=%#v", initialized)
	}
	brief := callCollaborationTest(t, client, "brief", collaborationStateIdentity(dbPath, "controller-1"))
	if brief["role"] != "controller" || brief["revision"] != initialized["revision"] {
		t.Fatalf("brief=%#v", brief)
	}
	mission := brief["mission"].(map[string]any)
	if mission["goal"] != "Deliver the bounded mission" || mission["db_path"] == "" {
		t.Fatalf("mission=%#v", mission)
	}

	apply := collaborationStateIdentity(dbPath, "controller-1")
	apply["expectedRevision"] = initialized["revision"]
	apply["items"] = []any{map[string]any{"id": "local-1", "phase": "active", "next_action": "Run local work"}}
	updated := callCollaborationTest(t, client, "apply", apply)
	item := callCollaborationTest(t, client, "get", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "controller-1", "itemId": "local-1",
	})["item"].(map[string]any)
	if item["phase"] != "active" || item["started_at"] == nil || updated["revision"] == initialized["revision"] {
		t.Fatalf("updated=%#v item=%#v", updated, item)
	}
	if len(agent.actions) != 0 {
		t.Fatalf("ledger-only lifecycle contacted provider: %v", agent.actions)
	}
}

func TestCollaborationStateReadsLegacyPythonSchemaAndFields(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	createCollaborationTestLedger(t, dbPath, collaborationTestPacket(root, "chat-target"))
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})

	brief := callCollaborationTest(t, client, "brief", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
	})
	if revision, ok := collaborationInt64(brief["revision"]); brief["role"] != "coordinator" || !ok || revision != 1 {
		t.Fatalf("legacy brief=%#v", brief)
	}
	actions := callCollaborationTest(t, client, "next_actions", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "now": int64(0),
	})
	if !collaborationTestHasActionKind(actions, "dispatch_ready") {
		t.Fatalf("legacy actions=%#v", actions)
	}
	apply := collaborationStateIdentity(dbPath, "controller-1")
	apply["expectedRevision"] = int64(1)
	apply["items"] = []any{map[string]any{"id": "task-1", "phase": "planned", "next_action": "Refreeze later"}}
	callCollaborationTest(t, client, "apply", apply)
	if item := readCollaborationTestItem(t, dbPath); item["phase"] != "planned" {
		t.Fatalf("legacy item=%#v", item)
	}
}

func TestCollaborationStateDueActionsAndObservationStayBounded(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	blocked := collaborationStateLocalItem("blocked-1", "blocked")
	blocked["source_ref"] = "migration:blocked-1"
	blocked["next_check_at"] = int64(1000)
	blocked["blocker"] = map[string]any{
		"kind": "dependency", "owner": "owner-1", "reason": "waiting",
		"resume_when": "dependency completes", "next_check_at": int64(1000), "userActionThreadId": nil,
	}
	initialized := callCollaborationTest(t, client, "init", collaborationStateInitParams(dbPath, []any{blocked}))
	due := callCollaborationTest(t, client, "next_actions", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"since": initialized["revision"], "now": int64(1000),
	})
	if due["changed"] != false || !collaborationTestHasActionKind(due, "recheck_blocker") || !collaborationTestHasActionKind(due, "consistency_audit") {
		t.Fatalf("due=%#v", due)
	}
	actionID := collaborationTestActionID(due, "recheck_blocker")
	recorded := callCollaborationTest(t, client, "record_action", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"expectedRevision": initialized["revision"], "expectedObservationRevision": due["observationRevision"],
		"actionId": actionID, "retryAt": int64(1100), "evidenceRef": "check:unchanged", "notified": true, "now": int64(1000),
	})
	consistencyID := collaborationTestActionID(due, "consistency_audit")
	recorded = callCollaborationTest(t, client, "record_action", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"expectedRevision": initialized["revision"], "expectedObservationRevision": recorded["observationRevision"],
		"actionId": consistencyID, "retryAt": int64(2000), "evidenceRef": "check:consistency", "now": int64(1000),
	})
	quiet := callCollaborationTest(t, client, "next_actions", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "now": int64(1050),
	})
	nextDue, nextDueOK := collaborationInt64(quiet["nextDueAt"])
	if collaborationTestContainsActionID(quiet, actionID) || !nextDueOK || nextDue != 1100 {
		t.Fatalf("quiet=%#v", quiet)
	}
	observed := callCollaborationTest(t, client, "observe", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"expectedObservationRevision": recorded["observationRevision"], "checkedRevision": initialized["revision"],
		"full": false, "conflicts": []any{},
	})
	if revision, ok := collaborationInt64(observed["revision"]); !ok || revision != 3 {
		t.Fatalf("observation=%#v", observed)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var eventCount int64
	if err := db.QueryRow("SELECT count(*) FROM events").Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("observation created business events: %d", eventCount)
	}
}

func TestCollaborationStateTransferRequiresPausedMissionAndChangesAuthorityAtomically(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	callCollaborationTest(t, client, "init", collaborationStateInitParams(dbPath, nil))

	paused := collaborationStateIdentity(dbPath, "controller-1")
	paused["expectedRevision"] = int64(0)
	paused["mission"] = map[string]any{"status": "paused", "dispatch_enabled": false}
	pausedResult := callCollaborationTest(t, client, "apply", paused)
	transfer := collaborationStateIdentity(dbPath, "controller-1")
	transfer["expectedRevision"] = pausedResult["revision"]
	transfer["newController"] = "controller-2"
	transfer["newCoordinator"] = "coordinator-2"
	transfer["evidenceRef"] = "native-task-handoff:1"
	transfer["automationsPaused"] = true
	transferred := callCollaborationTest(t, client, "transfer_control", transfer)
	if transferred["controller"] != "controller-2" || transferred["coordinator"] != "coordinator-2" {
		t.Fatalf("transfer=%#v", transferred)
	}
	old := client.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("brief", collaborationStateIdentity(dbPath, "controller-1")))
	if old.Error == nil {
		t.Fatalf("old controller retained authority: %#v", old.Result)
	}
	current := callCollaborationTest(t, client, "brief", collaborationStateIdentity(dbPath, "controller-2"))
	mission := current["mission"].(map[string]any)
	legacy := collaborationStringList(mission["legacy_callback_sessions"])
	if len(legacy) != 1 || legacy[0] != "controller-1" {
		t.Fatalf("transferred mission=%#v", mission)
	}
}

func TestCollaborationStateCloseCompactAndCleanupTouchOnlyExactDatabase(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "first.sqlite3")
	neighbor := filepath.Join(root, "second.sqlite3")
	sibling := filepath.Join(root, "preserve.txt")
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	done := collaborationStateLocalItem("done-1", "done")
	done["source_ref"] = "migration:done-1"
	done["evidence"] = []any{"test:done"}
	done["result"] = "completed"
	done["validation"] = "not_required"
	done["integration"] = "done"
	done["terminal_ref"] = "execution:done"
	initialized := callCollaborationTest(t, client, "init", collaborationStateInitParams(dbPath, []any{done}))
	callCollaborationTest(t, client, "init", collaborationStateInitParamsWithMission(neighbor, "mission-2", "controller-2", "coordinator-2", nil))
	if err := os.WriteFile(sibling, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	actions := callCollaborationTest(t, client, "next_actions", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "controller-1", "now": int64(1000),
	})
	if !collaborationTestHasActionKind(actions, "stop_automations_and_close") {
		t.Fatalf("close action=%#v", actions)
	}
	closed := callCollaborationTest(t, client, "close", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "controller-1",
		"expectedRevision": initialized["revision"], "automationStopped": true, "evidenceRef": "automation:stopped",
	})
	if closed["status"] != "closed" {
		t.Fatalf("close=%#v", closed)
	}
	callCollaborationTest(t, client, "compact", collaborationStateIdentity(dbPath, "controller-1"))
	preview := callCollaborationTest(t, client, "cleanup", collaborationStateIdentity(dbPath, "controller-1"))
	if preview["applied"] != false {
		t.Fatalf("preview=%#v", preview)
	}
	wrong := collaborationStateIdentity(dbPath, "controller-1")
	wrong["apply"] = true
	wrong["confirmMission"] = "wrong"
	if response := client.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("cleanup", wrong)); response.Error == nil {
		t.Fatalf("wrong cleanup confirmation succeeded: %#v", response.Result)
	}
	cleanup := collaborationStateIdentity(dbPath, "controller-1")
	cleanup["apply"] = true
	cleanup["confirmMission"] = "mission-1"
	callCollaborationTest(t, client, "cleanup", cleanup)
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("target database still exists: %v", err)
	}
	if _, err := os.Stat(neighbor); err != nil {
		t.Fatalf("neighbor database changed: %v", err)
	}
	if raw, err := os.ReadFile(sibling); err != nil || string(raw) != "preserve" {
		t.Fatalf("sibling changed: %q %v", raw, err)
	}
}

func TestCollaborationStateFinalItemDropsPromptButKeepsDispatchIdentity(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "chat-target")
	createCollaborationTestLedger(t, dbPath, packet)
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	claimed := callCollaborationTest(t, client, "claim", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "expectedRevision": int64(1), "itemId": "task-1",
	})
	binding := map[string]any{
		"chatSessionId": "chat-target", "collaborationId": "collaboration-1", "taskRef": "task-ref-1",
		"callbackSessionId": "controller-1", "idempotencyKey": packet["idempotencyKey"],
	}
	callCollaborationTest(t, client, "receipt", map[string]any{
		"dispatchToken": claimed["dispatchToken"], "dispatchResult": binding,
	})
	returned := collaborationStateIdentity(dbPath, "controller-1")
	returned["expectedRevision"] = int64(3)
	returned["items"] = []any{map[string]any{
		"id": "task-1", "phase": "returned", "next_action": "Accept result", "callback": "acked",
		"result": "completed", "evidence": []any{"callback:done"}, "terminal_ref": "callback:done",
	}}
	returnedResult := callCollaborationTest(t, client, "apply", returned)
	done := collaborationStateIdentity(dbPath, "controller-1")
	done["expectedRevision"] = returnedResult["revision"]
	done["items"] = []any{map[string]any{
		"id": "task-1", "phase": "done", "next_action": "Done", "validation": "passed", "integration": "done",
	}}
	callCollaborationTest(t, client, "apply", done)
	item := readCollaborationTestItem(t, dbPath)
	if item["packet"] != nil || item["dispatch_key"] != packet["idempotencyKey"] {
		t.Fatalf("final item retained prompt or lost key: %#v", item)
	}
}

func TestCollaborationStateEnforcesDependenciesCapacityScopeAndDatabaseIdentity(t *testing.T) {
	root := t.TempDir()
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	firstDB := filepath.Join(root, "first.sqlite3")
	local := collaborationStateLocalItem("local-1", "planned")
	local["local_scope"] = map[string]any{
		"machineId": "machine-1", "workingDirectory": root,
		"accessMode": "write", "writeScope": []any{"src/shared"},
	}
	initialized := callCollaborationTest(t, client, "init", collaborationStateInitParams(firstDB, []any{local}))
	start := collaborationStateIdentity(firstDB, "controller-1")
	start["expectedRevision"] = initialized["revision"]
	start["items"] = []any{map[string]any{"id": "local-1", "phase": "active", "next_action": "Run local work"}}
	active := callCollaborationTest(t, client, "apply", start)

	overlap := collaborationStateCloudItem(root, "cloud-overlap", "cloud-overlap-key-1", "chat-overlap", "src/shared/sub")
	overlapApply := collaborationStateIdentity(firstDB, "controller-1")
	overlapApply["expectedRevision"] = active["revision"]
	overlapApply["items"] = []any{overlap}
	if response := client.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("apply", overlapApply)); response.Error == nil {
		t.Fatalf("overlapping Cloud write scope was accepted: %#v", response.Result)
	}

	secondDB := filepath.Join(root, "second.sqlite3")
	secondInit := collaborationStateInitParamsWithMission(secondDB, "mission-2", "controller-2", "coordinator-2", nil)
	callCollaborationTest(t, client, "init", secondInit)
	independent := collaborationStateCloudItem(root, "cloud-independent", "cloud-independent-key-1", "chat-independent", "src/shared/sub")
	independent["packet"].(map[string]any)["callbackSessionId"] = "controller-2"
	secondApply := map[string]any{
		"dbPath": secondDB, "missionId": "mission-2", "actorSessionId": "controller-2",
		"expectedRevision": int64(0), "items": []any{independent},
	}
	callCollaborationTest(t, client, "apply", secondApply)
	wrongMission := map[string]any{
		"dbPath": secondDB, "missionId": "mission-1", "actorSessionId": "controller-1",
	}
	if response := client.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("brief", wrongMission)); response.Error == nil {
		t.Fatalf("wrong mission opened another task database: %#v", response.Result)
	}

	dependencyDB := filepath.Join(root, "dependency.sqlite3")
	callCollaborationTest(t, client, "init", collaborationStateInitParams(dependencyDB, []any{collaborationStateLocalItem("upstream", "planned")}))
	dependent := collaborationStateCloudItem(root, "dependent", "dependent-task-key-1", "chat-dependent", "src/dependent")
	dependent["depends_on"] = []any{"upstream"}
	dependencyApply := collaborationStateIdentity(dependencyDB, "controller-1")
	dependencyApply["expectedRevision"] = int64(1)
	dependencyApply["items"] = []any{dependent}
	if response := client.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("apply", dependencyApply)); response.Error == nil {
		t.Fatalf("unfinished dependency was accepted: %#v", response.Result)
	}

	capacityDB := filepath.Join(root, "capacity.sqlite3")
	capacityInit := collaborationStateInitParams(capacityDB, []any{
		collaborationStateCloudItem(root, "cloud-1", "capacity-task-key-1", "chat-1", "src/one"),
		collaborationStateCloudItem(root, "cloud-2", "capacity-task-key-2", "chat-2", "src/two"),
	})
	capacityInit["capacity"] = map[string]any{"cloud": int64(1), "local": int64(2)}
	capacityState := callCollaborationTest(t, client, "init", capacityInit)
	firstClaim := callCollaborationTest(t, client, "claim", map[string]any{
		"dbPath": capacityDB, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"expectedRevision": capacityState["revision"], "itemId": "cloud-1",
	})
	secondClaim := map[string]any{
		"dbPath": capacityDB, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"expectedRevision": firstClaim["ledgerRevision"], "itemId": "cloud-2",
	}
	if response := client.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("claim", secondClaim)); response.Error == nil {
		t.Fatalf("Cloud capacity limit was bypassed: %#v", response.Result)
	}
}

func collaborationStateIdentity(dbPath, actor string) map[string]any {
	return map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": actor}
}

func collaborationStateInitParams(dbPath string, items []any) map[string]any {
	return collaborationStateInitParamsWithMission(dbPath, "mission-1", "controller-1", "coordinator-1", items)
}

func collaborationStateInitParamsWithMission(dbPath, mission, controller, coordinator string, items []any) map[string]any {
	return map[string]any{
		"dbPath": dbPath, "missionId": mission, "actorSessionId": controller,
		"coordinator": coordinator, "authorityRef": "user-scope-1", "goal": "Deliver the bounded mission",
		"strategyRef": "strategy-1", "nextAction": "Process current work", "dispatchEnabled": true,
		"continuation": map[string]any{"enabled": false}, "capacity": map[string]any{"cloud": int64(4), "local": int64(2)},
		"items": items,
	}
}

func collaborationStateLocalItem(id, phase string) map[string]any {
	return map[string]any{
		"id": id, "kind": "implement", "phase": phase, "owner": "owner-1", "executor": "local",
		"next_action": "Wait", "evidence": []any{}, "depends_on": []any{}, "packet": nil, "binding": nil,
		"callback": "none", "result": "none", "validation": "pending", "integration": "pending",
		"blocker": nil, "claim": nil, "source_ref": nil, "dispatch_key": nil, "terminal_ref": nil,
		"validation_owner": nil, "validation_started_at": nil, "next_check_at": nil, "started_at": nil,
		"priority": int64(100), "contract_refs": []any{}, "acceptance_ref": nil, "execution_ref": nil, "local_scope": nil,
	}
}

func collaborationStateCloudItem(root, id, key, target, scope string) map[string]any {
	item := collaborationStateLocalItem(id, "ready")
	item["executor"] = "cloud"
	item["packet"] = map[string]any{
		"machineId": "machine-1", "callbackSessionId": "controller-1", "workingDirectory": root,
		"prompt": "Implement and validate the bounded task.", "idempotencyKey": key,
		"targetSessionId": target, "accessMode": "write", "writeScope": scope, "callbackType": "text",
	}
	return item
}

func collaborationTestHasActionKind(result map[string]any, kind string) bool {
	return collaborationTestActionID(result, kind) != ""
}

func collaborationTestActionID(result map[string]any, kind string) string {
	for _, action := range collaborationTestActions(result) {
		if action["kind"] == kind {
			value, _ := action["actionId"].(string)
			return value
		}
	}
	return ""
}

func collaborationTestContainsActionID(result map[string]any, id string) bool {
	for _, action := range collaborationTestActions(result) {
		if action["actionId"] == id {
			return true
		}
	}
	return false
}

func collaborationTestActions(result map[string]any) []map[string]any {
	switch actions := result["actions"].(type) {
	case []map[string]any:
		return actions
	case []any:
		result := make([]map[string]any, 0, len(actions))
		for _, raw := range actions {
			if action, ok := raw.(map[string]any); ok {
				result = append(result, action)
			}
		}
		return result
	default:
		return nil
	}
}

func collaborationCapabilityRequest(action string, params map[string]any) protocolv1.CapabilityRequest {
	return protocolv1.CapabilityRequest{RequestId: "state-" + action, Capability: "collaboration.control", Action: action, Params: params}
}
