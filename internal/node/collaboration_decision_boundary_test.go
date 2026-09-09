package node

import (
	"context"
	"testing"
)

// Coordinators prepare evidence; only the controller can make the business
// decision. Reading an inbox must neither consume it nor contact the provider.
func TestCollaborationDecisionCoordinatorCanPrepareButNotDecide(t *testing.T) {
	c, agent, db, event := collaborationInboxFixture(t)
	if err := c.PersistCollaborationCallback(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	identity := collaborationStateIdentity(db, "coordinator-1")
	box := callCollaborationTest(t, c, "inbox", identity)
	results := collaborationTestInboxResults(box)
	if len(results) != 1 {
		t.Fatalf("coordinator cannot prepare inbox evidence: %#v", box)
	}
	query := cloneParams(identity)
	query["resultId"] = results[0]["resultId"]
	detail := callCollaborationTest(t, c, "inbox", query)
	if detail["resolved"] != false || detail["result"].(map[string]any)["resultId"] != event["resultId"] {
		t.Fatalf("wrong evidence returned: %#v", detail)
	}
	for _, actor := range []string{"coordinator-1", "unbound-delivery-helper"} {
		for _, action := range []string{"apply", "resolve", "retry"} {
			p := collaborationStateIdentity(db, actor)
			p["expectedRevision"] = box["revision"]
			switch action {
			case "apply":
				p["items"] = []any{map[string]any{"id": "task-1", "next_action": "unauthorized decision"}}
			case "resolve":
				p["resultId"], p["decision"], p["evidenceRef"] = results[0]["resultId"], "rework", "test:proposal-only"
			case "retry":
				p["itemId"], p["evidenceRef"], p["item"] = "task-1", "test:proposal-only", map[string]any{}
			}
			if r := c.HandleLocalCapability(context.Background(), collaborationCapabilityRequest(action, p)); r.Error == nil {
				t.Fatalf("%s performed %s: %#v", actor, action, r.Result)
			}
		}
	}
	if r := c.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("inbox", collaborationStateIdentity(db, "unbound-delivery-helper"))); r.Error == nil {
		t.Fatal("unbound helper read mission evidence")
	}
	after := callCollaborationTest(t, c, "inbox", identity)
	if !collaborationValueEqual(after["revision"], box["revision"]) || len(collaborationTestInboxResults(after)) != 1 {
		t.Fatalf("preparation consumed or mutated result: %#v", after)
	}
	if readCollaborationTestItem(t, db)["phase"] != "returned" {
		t.Fatal("preparation changed business phase")
	}
	for _, action := range []string{"session.get", "session.send", "session.create", "session.callback.ack"} {
		if agent.actionCount(action) != 0 {
			t.Fatalf("preparation invoked %s", action)
		}
	}
}

func TestCollaborationDecisionRejectsStaleAndIncompleteAcceptance(t *testing.T) {
	c, agent, db, event := collaborationInboxFixture(t)
	if err := c.PersistCollaborationCallback(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	box := callCollaborationTest(t, c, "inbox", collaborationStateIdentity(db, "coordinator-1"))
	proposal := collaborationStateIdentity(db, "controller-1")
	proposal["expectedRevision"], proposal["resultId"], proposal["decision"], proposal["evidenceRef"] = box["revision"], collaborationTestInboxResults(box)[0]["resultId"], "accept", "test:evidence-v1"
	proposal["validation"], proposal["integration"] = "passed", "not_required"
	change := collaborationStateIdentity(db, "controller-1")
	change["expectedRevision"], change["items"] = box["revision"], []any{map[string]any{"id": "task-1", "next_action": "New evidence requires review"}}
	updated := callCollaborationTest(t, c, "apply", change)
	if r := c.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("resolve", proposal)); r.Error == nil {
		t.Fatal("stale proposal accepted")
	}
	proposal["expectedRevision"] = updated["revision"]
	for _, validation := range []string{"pending", "failed", "skipped", ""} {
		proposal["validation"] = validation
		if r := c.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("resolve", proposal)); r.Error == nil {
			t.Fatalf("accepted unverified result: %q", validation)
		}
	}
	proposal["validation"], proposal["integration"] = "passed", "pending"
	if r := c.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("resolve", proposal)); r.Error == nil {
		t.Fatal("accepted incomplete integration")
	}
	after := callCollaborationTest(t, c, "inbox", collaborationStateIdentity(db, "coordinator-1"))
	if len(collaborationTestInboxResults(after)) != 1 || !collaborationValueEqual(after["revision"], updated["revision"]) || agent.actionCount("session.callback.ack") != 0 {
		t.Fatalf("failed decision consumed evidence or ACKed: %#v", after)
	}
	// A reviewed controller decision uses one existing atomic action; replay
	// settles transport and never performs another business acceptance.
	proposal["validation"], proposal["integration"] = "passed", "not_required"
	accepted := callCollaborationTest(t, c, "resolve", proposal)
	if accepted["resolved"] != true || accepted["transportAcked"] != true {
		t.Fatalf("decision not settled: %#v", accepted)
	}
	replayed := callCollaborationTest(t, c, "resolve", proposal)
	if replayed["duplicate"] != true || !collaborationValueEqual(replayed["revision"], accepted["revision"]) {
		t.Fatalf("decision replay changed business state: %#v", replayed)
	}
	if readCollaborationTestItem(t, db)["phase"] != "done" {
		t.Fatal("approved result did not settle")
	}
}
