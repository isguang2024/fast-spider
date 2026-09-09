package node

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestCloudProgressChecksUseThirtyThenTenMinutes(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "mission.sqlite3")
	packet := collaborationTestPacket(root, "chat-target")
	createCollaborationTestLedger(t, db, packet)
	c := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node"), Agent: &collaborationTestAgent{}})
	p := collaborationStateIdentity(db, "coordinator-1")
	p["itemId"], p["expectedRevision"] = "task-1", int64(1)
	claim := callCollaborationTest(t, c, "claim", p)
	token, err := c.readCollaborationToken(claim["dispatchToken"].(string))
	if err != nil {
		t.Fatal(err)
	}
	mission, task, _ := localCallbackIdentity(token)
	callCollaborationTest(t, c, "receipt", map[string]any{"dispatchToken": claim["dispatchToken"], "dispatchResult": map[string]any{
		"chatSessionId": "chat-target", "collaborationId": mission, "taskRef": task, "callbackSessionId": "controller-1", "idempotencyKey": packet["idempotencyKey"],
	}})
	q := collaborationStateIdentity(db, "coordinator-1")
	now := time.Now().Unix()
	q["now"] = now + 1790
	if collaborationTestHasActionKind(callCollaborationTest(t, c, "next_actions", q), "check_execution") {
		t.Fatal("Cloud checked before 30 minutes")
	}
	now += 1810
	var last map[string]any
	var record map[string]any
	for i, token := range []string{"activity-a", "activity-b", "activity-c", "activity-d", "activity-d", "activity-d"} {
		q["now"] = now
		due := callCollaborationTest(t, c, "next_actions", q)
		var action map[string]any
		for _, a := range collaborationTestActions(due) {
			if a["kind"] == "check_execution" {
				action = a
			}
		}
		if action == nil {
			t.Fatalf("progress exhausted total-check budget at %d: %#v", i, due)
		}
		record = action["recordCheck"].(map[string]any)["params"].(map[string]any)
		record["outcome"], record["progressToken"], record["evidenceRef"], record["now"] = "observed", token, fmt.Sprintf("actual-message-%d", i), now
		last = callCollaborationTest(t, c, "record_check", record)
		if collaborationIntDefault(last, "retryAt", 0) != now+600 {
			t.Fatalf("not ten minutes: %#v", last)
		}
		if (last["stalled"] == true) != (i == 5) {
			t.Fatalf("incorrect stall decision at %d: %#v", i, last)
		}
		q["now"] = now + 599
		if collaborationTestHasActionKind(callCollaborationTest(t, c, "next_actions", q), "check_execution") {
			t.Fatal("early repeated check")
		}
		now += 600
	}
	cq := collaborationStateIdentity(db, "controller-1")
	cq["now"] = now
	decision := callCollaborationTest(t, c, "next_actions", cq)
	var stalled map[string]any
	for _, a := range collaborationTestActions(decision) {
		if a["kind"] == "decide_stalled_check" {
			stalled = a
		}
	}
	if stalled == nil || stalled["continuation"] == nil || stalled["terminalRecovery"] == nil || stalled["nativeBindingLookup"] != nil {
		t.Fatalf("no exact Cloud recovery: %#v", decision)
	}
	// Real progress resumes observation through the original coordinator, not
	// a description edit or pretending that running is sufficient evidence.
	recovery := stalled["progressRecoveryRecord"].(map[string]any)["params"].(map[string]any)
	recovery["progressToken"], recovery["evidenceRef"], recovery["now"] = "activity-e", "exact job advanced", now
	resumed := callCollaborationTest(t, c, "record_check", recovery)
	if resumed["progressState"] != "progressed" || resumed["stalled"] != false {
		t.Fatalf("could not resume actual progress: %#v", resumed)
	}
	// A real successful continuation has a new execution identity/window.
	edit := collaborationStateIdentity(db, "controller-1")
	edit["expectedRevision"], edit["items"] = resumed["revision"], []any{map[string]any{
		"id": "task-1", "execution_ref": "cloud-continuation:verified-message-id", "next_check_at": now + 1800,
	}}
	callCollaborationTest(t, c, "apply", edit)
	q["now"] = now + 1799
	if collaborationTestHasActionKind(callCollaborationTest(t, c, "next_actions", q), "check_execution") {
		t.Fatal("continuation lost its first 30 minute window")
	}
	q["now"] = now + 1800
	after := callCollaborationTest(t, c, "next_actions", q)
	if !collaborationTestHasActionKind(after, "check_execution") || collaborationTestActionID(after, "check_execution") == record["actionId"] {
		t.Fatalf("new execution inherited old budget: %#v", after)
	}
}
