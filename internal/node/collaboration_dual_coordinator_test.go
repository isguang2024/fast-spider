package node

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestCollaborationRoleWakesCoalesceAndWaitForResume(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "mission.sqlite3")
	a := &collaborationTestAgent{results: map[string]map[string]any{"session.send": {"turnId": "one-wake", "executionMode": "codex_app_server", "owner": "fast_spider_node"}}}
	c := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node"), Agent: a})
	init := callCollaborationTest(t, c, "init", collaborationStateInitParams(db, nil))
	p := collaborationStateIdentity(db, "controller-1")
	p["expectedRevision"], p["mission"] = init["revision"], map[string]any{"status": "paused", "dispatch_enabled": false}
	paused := callCollaborationTest(t, c, "apply", p)
	c.drainCollaborationRoleWakes(context.Background())
	if a.actionCount("session.send") != 0 {
		t.Fatal("paused mission woke a control task")
	}
	p["expectedRevision"], p["mission"] = paused["revision"], map[string]any{"status": "active", "dispatch_enabled": true}
	callCollaborationTest(t, c, "apply", p)
	c.drainCollaborationRoleWakes(context.Background())
	if a.actionCount("session.send") != 1 {
		t.Fatalf("expected one wake for pending revisions, got %d", a.actionCount("session.send"))
	}
	dbConn, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer dbConn.Close()
	var pending int
	if err := dbConn.QueryRow("SELECT count(*) FROM role_wakeup_outbox WHERE delivered_at IS NULL").Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatal("coalesced wake left duplicate pending notifications")
	}
}

func bindTestDelivery(t *testing.T, c *Client, db string) {
	t.Helper()
	brief := callCollaborationTest(t, c, "brief", collaborationStateIdentity(db, "controller-1"))
	p := collaborationStateIdentity(db, "controller-1")
	p["expectedRevision"], p["mission"] = brief["revision"], map[string]any{"status": "paused", "dispatch_enabled": false}
	paused := callCollaborationTest(t, c, "apply", p)
	p["expectedRevision"], p["mission"] = paused["revision"], map[string]any{"delivery_coordinator": "delivery-1", "coordination_ref": "test:peer settings verified", "status": "active", "dispatch_enabled": true}
	callCollaborationTest(t, c, "apply", p)
}

func TestCollaborationDualCoordinatorSeparatesAuthorityAndPreservesClaims(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "mission.sqlite3")
	c := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node")})
	old := collaborationStateLocalItem("old-validation", "verifying")
	fresh := collaborationStateLocalItem("fresh-validation", "verifying")
	for _, i := range []map[string]any{old, fresh} {
		i["source_ref"] = "test:returned"
		i["validation_owner"] = "validator"
		i["next_action"] = "Validate"
	}
	ready := collaborationStateCloudItem(root, "ready-task", "dual-ready-key-001", "cloud-ready", "src/ready")
	init := callCollaborationTest(t, c, "init", collaborationStateInitParams(db, []any{old, fresh, ready}))
	p := collaborationStateIdentity(db, "coordinator-1")
	p["expectedRevision"], p["itemId"], p["launchRef"] = init["revision"], "old-validation", "codex-agent:coordinator-1#/root/old"
	claim := callCollaborationTest(t, c, "validation_claim", p)
	bindTestDelivery(t, c, db)
	delivery := callCollaborationTest(t, c, "next_actions", collaborationStateIdentity(db, "delivery-1"))
	execution := callCollaborationTest(t, c, "next_actions", collaborationStateIdentity(db, "coordinator-1"))
	if !collaborationTestHasActionKind(delivery, "launch_validation") || collaborationTestHasActionKind(delivery, "dispatch_ready") || len(collaborationAnyList(delivery["refillInputs"])) != 0 {
		t.Fatalf("delivery actions=%#v", delivery)
	}
	if !collaborationTestHasActionKind(execution, "dispatch_ready") || !collaborationTestHasActionKind(execution, "recover_validation_binding") || collaborationTestHasActionKind(execution, "launch_validation") {
		t.Fatalf("execution actions=%#v", execution)
	}
	for _, action := range []string{"apply", "resolve", "decision_batch", "claim", "dispatch"} {
		q := collaborationStateIdentity(db, "delivery-1")
		q["expectedRevision"] = delivery["revision"]
		q["itemId"] = "ready-task"
		q["decisions"] = []any{map[string]any{"resultId": "x"}}
		if _, err := c.collaborationControl(context.Background(), action, q); err == nil {
			t.Fatalf("delivery allowed %s", action)
		}
	}
	p = collaborationStateIdentity(db, "coordinator-1")
	p["expectedRevision"], p["itemId"], p["validationClaim"], p["executionRef"] = delivery["revision"], "old-validation", claim["validationClaim"], "codex-thread:old-child"
	receipt := callCollaborationTest(t, c, "validation_receipt", p)
	p = collaborationStateIdentity(db, "delivery-1")
	p["expectedRevision"], p["itemId"], p["launchRef"] = receipt["revision"], "fresh-validation", "codex-agent:delivery-1#/root/new"
	callCollaborationTest(t, c, "validation_claim", p)
}

func TestCollaborationDecisionBatchRollsBackAndReplaysAfterAckFailure(t *testing.T) {
	c, a, db, event := collaborationInboxFixture(t)
	if err := c.PersistCollaborationCallback(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	box := callCollaborationTest(t, c, "inbox", collaborationStateIdentity(db, "controller-1"))
	id := collaborationTestInboxResults(box)[0]["resultId"]
	decision := map[string]any{"resultId": id, "decision": "accept", "evidenceRef": "test:batch verified", "validation": "passed", "integration": "not_required"}
	p := collaborationStateIdentity(db, "controller-1")
	p["expectedRevision"] = box["revision"]
	p["decisions"] = []any{decision, map[string]any{"resultId": "missing", "decision": "accept"}}
	if _, err := c.collaborationControl(context.Background(), "decision_batch", p); err == nil {
		t.Fatal("invalid batch accepted")
	}
	unchanged := callCollaborationTest(t, c, "inbox", collaborationStateIdentity(db, "controller-1"))
	if unchanged["revision"] != box["revision"] || len(collaborationTestInboxResults(unchanged)) != 1 {
		t.Fatal("partial batch committed")
	}
	if a.actionCount("session.callback.ack") != 0 {
		t.Fatal("ACK before atomic commit")
	}
	p["decisions"] = []any{decision}
	a.errors["session.callback.ack"] = errors.New("temporary transport failure")
	result := callCollaborationTest(t, c, "decision_batch", p)
	if result["resolved"] != true {
		t.Fatal(result)
	}
	delete(a.errors, "session.callback.ack")
	replay := callCollaborationTest(t, c, "decision_batch", p)
	r := collaborationAnyList(replay["results"])[0].(map[string]any)
	if r["duplicate"] != true || r["transportAcked"] != true {
		t.Fatal(replay)
	}
}
