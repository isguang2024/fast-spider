package node

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollaborationNextActionsProvidesExecutableCheckAndRecordParams(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "mission.sqlite3")
	packet := collaborationTestPacket(root, "chat-target")
	createCollaborationTestLedger(t, dbPath, packet)
	a := &collaborationTestAgent{}
	c := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node"), Agent: a})
	p := collaborationStateIdentity(dbPath, "coordinator-1")
	p["itemId"], p["expectedRevision"] = "task-1", int64(1)
	claim := callCollaborationTest(t, c, "claim", p)
	token, err := c.readCollaborationToken(claim["dispatchToken"].(string))
	if err != nil {
		t.Fatal(err)
	}
	m, task, _ := localCallbackIdentity(token)
	callCollaborationTest(t, c, "receipt", map[string]any{"dispatchToken": claim["dispatchToken"], "dispatchResult": map[string]any{
		"chatSessionId": "chat-target", "collaborationId": m, "taskRef": task, "callbackSessionId": "controller-1", "idempotencyKey": packet["idempotencyKey"],
	}})
	q := collaborationStateIdentity(dbPath, "coordinator-1")
	now := time.Now().Unix() + 3600
	q["now"] = now
	due := callCollaborationTest(t, c, "next_actions", q)
	var action map[string]any
	for _, value := range collaborationAnyList(due["actions"]) {
		raw := value.(map[string]any)
		if raw["kind"] == "check_execution" {
			action = raw
		}
	}
	if action == nil {
		t.Fatalf("no execution check: %#v", due)
	}
	check := action["executionCheck"].(map[string]any)
	args := check["params"].(map[string]any)
	if check["action"] != "session.get" || args["sessionId"] != "chat-target" || args["metadataOnly"] != true || action["evidenceRule"] == nil {
		t.Fatalf("unsafe execution check: %#v", action)
	}
	recovery := action["terminalRecovery"].(map[string]any)["params"].(map[string]any)
	if recovery["callbackMissionId"] != m || recovery["callbackTaskId"] != task || recovery["callbackTargetSessionId"] != "controller-1" {
		t.Fatalf("wrong recovery binding: %#v", recovery)
	}
	if len(a.actions) != 0 {
		t.Fatal("next_actions contacted provider instead of returning a call")
	}
	record := action["recordCheck"].(map[string]any)
	rp := record["params"].(map[string]any)
	if rp["expectedRevision"] != due["revision"] || rp["expectedObservationRevision"] != due["observationRevision"] || rp["itemId"] != nil {
		t.Fatalf("record args require reconstruction: %#v", rp)
	}
	rp["outcome"], rp["evidenceRef"], rp["now"], rp["notified"] = "unchanged", "provider:chat-target/running", now, true
	result := callCollaborationTest(t, c, "record_check", rp)
	if collaborationIntDefault(result, "retryAt", 0) != now+600 {
		t.Fatalf("Cloud check did not respect recovery interval: %#v", result)
	}
	edit := collaborationStateIdentity(dbPath, "controller-1")
	edit["expectedRevision"], edit["items"] = result["revision"], []any{map[string]any{"id": "task-1", "next_action": "Waiting for same Cloud round", "next_check_at": now + 1}}
	callCollaborationTest(t, c, "apply", edit)
	q["now"] = now + 1
	if collaborationTestHasActionKind(callCollaborationTest(t, c, "next_actions", q), "check_execution") {
		t.Fatal("Cloud description edit bypassed backoff")
	}
	q["now"] = result["retryAt"]
	due = callCollaborationTest(t, c, "next_actions", q)
	if collaborationTestActionID(due, "check_execution") != action["actionId"] {
		t.Fatal("Cloud execution identity changed on scheduling edit")
	}
}

func TestCollaborationRecordCheckReportsWholeParameterContract(t *testing.T) {
	c := NewLocalCapabilityClient(Config{DataDir: t.TempDir()})
	for _, p := range []map[string]any{{"itemId": "bad", "evidence": "bad"}, {
		"dbPath": "bad", "missionId": "m", "actorSessionId": "c", "expectedRevision": 1, "expectedObservationRevision": 0,
		"actionId": "a", "outcome": "unchanged", "evidenceRef": "e", "itemId": "bad",
	}} {
		_, err := c.collaborationControl(context.Background(), "record_check", p)
		if err == nil || !strings.Contains(err.Error(), "expectedObservationRevision") || !strings.Contains(err.Error(), "Do not pass itemId or evidence") {
			t.Fatalf("incomplete parameter hint: %v", err)
		}
	}
}

func TestCollaborationPlannedDependencyDoesNotGeneratePreparationNoise(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "mission.sqlite3")
	c := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node")})
	first, after := collaborationStateLocalItem("first", "planned"), collaborationStateLocalItem("after", "planned")
	after["depends_on"] = []any{"first"}
	callCollaborationTest(t, c, "init", collaborationStateInitParams(db, []any{first, after}))
	due := callCollaborationTest(t, c, "next_actions", collaborationStateIdentity(db, "controller-1"))
	for _, value := range collaborationAnyList(due["actions"]) {
		action := value.(map[string]any)
		if action["kind"] == "prepare_task" && action["itemId"] == "after" {
			t.Fatal("dependent task incorrectly wakes controller to prepare")
		}
	}
}

func TestCollaborationLocalCheckUsesNativeBindingWithoutProviderCalls(t *testing.T) {
	for _, ref := range []string{"codex-thread:child-1", "codex-agent:parent-1#/root/validator", "", "codex-thread:parent-1#/root/validator"} {
		t.Run(ref, func(t *testing.T) {
			root := t.TempDir()
			db := filepath.Join(root, "mission.sqlite3")
			a := &collaborationTestAgent{}
			c := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node"), Agent: a})
			item := collaborationStateLocalItem("local-1", "planned")
			if ref != "" {
				item["execution_ref"] = ref
			}
			init := callCollaborationTest(t, c, "init", collaborationStateInitParams(db, []any{item}))
			p := collaborationStateIdentity(db, "controller-1")
			p["expectedRevision"], p["items"] = init["revision"], []any{map[string]any{"id": "local-1", "phase": "active", "next_check_at": int64(1000)}}
			callCollaborationTest(t, c, "apply", p)
			q := collaborationStateIdentity(db, "coordinator-1")
			q["now"] = int64(1000)
			due := callCollaborationTest(t, c, "next_actions", q)
			var check map[string]any
			for _, action := range collaborationTestActions(due) {
				if action["kind"] == "check_execution" {
					check = action
				}
			}
			if check == nil || check["executionCheck"] != nil || len(a.actions) != 0 {
				t.Fatalf("native inspection crossed FS provider boundary: %#v", check)
			}
			switch ref {
			case "codex-thread:child-1":
				call := check["nativeExecutionCheck"].(map[string]any)
				if call["tool"] != "read_thread" || call["params"].(map[string]any)["threadId"] != "child-1" {
					t.Fatalf("wrong child binding: %#v", call)
				}
			case "codex-agent:parent-1#/root/validator":
				call := check["nativeBindingLookup"].(map[string]any)
				if call["params"].(map[string]any)["threadId"] != "parent-1" || check["nativeExecutionCheck"] != nil {
					t.Fatalf("invented child binding: %#v", check)
				}
			default:
				if check["checkUnavailable"] == nil || check["nativeExecutionCheck"] != nil {
					t.Fatalf("invalid binding treated as readable: %#v", check)
				}
			}
			if check["terminalHandoff"].(map[string]any)["params"].(map[string]any)["threadId"] != "controller-1" {
				t.Fatal("terminal handoff lost original controller")
			}
		})
	}
}

func TestCollaborationValidationDueProvidesExactNativeCheck(t *testing.T) {
	c, _, db, event := collaborationInboxFixture(t)
	if err := c.PersistCollaborationCallback(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	box := callCollaborationTest(t, c, "inbox", collaborationStateIdentity(db, "controller-1"))
	p := collaborationStateIdentity(db, "controller-1")
	p["expectedRevision"], p["resultId"] = box["revision"], collaborationAnyList(box["results"])[0].(map[string]any)["resultId"]
	p["decision"], p["evidenceRef"], p["validationOwner"] = "verify", "test:verify", "codex-thread:validator-1"
	callCollaborationTest(t, c, "resolve", p)
	for _, actor := range []string{"controller-1", "coordinator-1"} {
		q := collaborationStateIdentity(db, actor)
		q["now"] = time.Now().Unix() + 3600
		due := callCollaborationTest(t, c, "next_actions", q)
		found := false
		for _, action := range collaborationTestActions(due) {
			if action["kind"] == "check_validation" || action["kind"] == "notify_validation_due" {
				found = true
				call := action["nativeExecutionCheck"].(map[string]any)
				if call["params"].(map[string]any)["threadId"] != "validator-1" || action["completionRule"] == nil {
					t.Fatalf("validation action lost handoff: %#v", action)
				}
				edit := collaborationStateIdentity(db, "controller-1")
				edit["expectedRevision"], edit["items"] = due["revision"], []any{map[string]any{"id": "task-1", "next_action": "Waiting for native validation", "next_check_at": int64(1000)}}
				callCollaborationTest(t, c, "apply", edit)
				refreshed := callCollaborationTest(t, c, "next_actions", q)
				if collaborationTestActionID(refreshed, mapStringValue(action, "kind")) != action["actionId"] {
					t.Fatal("validation check identity changed on scheduling edit")
				}
			}
		}
		if !found {
			t.Fatalf("missing validation action for %s", actor)
		}
	}
}

func TestCollaborationObserveExplainsAuditAlreadyClosed(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "mission.sqlite3")
	c := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node")})
	callCollaborationTest(t, c, "init", collaborationStateInitParams(db, []any{collaborationStateLocalItem("local-1", "planned")}))
	q := collaborationStateIdentity(db, "coordinator-1")
	due := callCollaborationTest(t, c, "next_actions", q)
	var audit map[string]any
	for _, action := range collaborationTestActions(due) {
		if action["kind"] == "consistency_audit" {
			audit = action
		}
	}
	if audit == nil || audit["completionRule"] == nil {
		t.Fatal("audit does not explain single-step completion")
	}
	p := collaborationStateIdentity(db, "coordinator-1")
	p["expectedObservationRevision"], p["checkedRevision"], p["full"], p["conflicts"] = due["observationRevision"], due["revision"], true, []any{}
	observed := callCollaborationTest(t, c, "observe", p)
	if observed["auditRecorded"] != true || observed["recordCheckRequired"] != false {
		t.Fatalf("ambiguous observe response: %#v", observed)
	}
	rp := audit["recordCheck"].(map[string]any)["params"].(map[string]any)
	rp["expectedObservationRevision"], rp["outcome"], rp["evidenceRef"] = observed["revision"], "completed", "test:audit"
	_, err := c.collaborationControl(context.Background(), "record_check", rp)
	if err == nil || !strings.Contains(err.Error(), "refresh local next_actions once") {
		t.Fatalf("stale action invites blind retry: %v", err)
	}
	if collaborationTestHasActionKind(callCollaborationTest(t, c, "next_actions", q), "consistency_audit") {
		t.Fatal("completed audit remains due")
	}
}
