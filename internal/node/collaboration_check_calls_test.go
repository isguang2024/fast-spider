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
	rp["outcome"], rp["evidenceRef"], rp["now"] = "unchanged", "provider:chat-target/running", now
	result := callCollaborationTest(t, c, "record_check", rp)
	if collaborationIntDefault(result, "retryAt", 0) < now+1800 {
		t.Fatalf("Cloud check did not respect recovery interval: %#v", result)
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
