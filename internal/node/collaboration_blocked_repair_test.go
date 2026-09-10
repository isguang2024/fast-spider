package node

import (
	"path/filepath"
	"testing"
)

func TestBlockedRepairAndReleasedWriterDependencyBecomeWork(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "mission.sqlite3")
	c := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node")})
	blocked := func(id string) map[string]any {
		item := collaborationStateLocalItem(id, "blocked")
		item["source_ref"], item["result"], item["terminal_ref"], item["evidence"] = "test:1730-terminal", "completed", "native:terminal", []any{"native:terminal"}
		item["blocker"] = map[string]any{"kind": "dependency", "owner": "backend-owner", "reason": "missing server consumer and failed validation", "resume_when": "owner fixes implementation", "next_check_at": int64(9999999999)}
		return item
	}
	backend := blocked("AE-SYSTEM-STORE-POOL-BACKEND-01")
	backend["validation"] = "failed"
	frontend := blocked("AE-SYSTEM-STORE-POOL-ADMIN-FRONTEND-01")
	proposal := collaborationStateLocalItem("AIASSIST-PROPOSAL-BINDING-IMPLEMENT-01", "planned")
	proposal["executor"] = "cloud"
	proposal["depends_on"] = []any{"AE-SYSTEM-STORE-POOL-ADMIN-FRONTEND-01"}
	proposal["next_action"] = "Freeze after the frontend writer releases its scope"
	callCollaborationTest(t, c, "init", collaborationStateInitParams(db, []any{backend, frontend, proposal}))
	bindTestDelivery(t, c, db)
	current := callCollaborationTest(t, c, "next_actions", collaborationStateIdentity(db, "controller-1"))
	for _, kind := range []string{"prepare_blocker_repair", "link_blocker_dependency", "review_blocked_dependency"} {
		if !collaborationTestHasActionKind(current, kind) {
			t.Fatalf("internal wait swallowed %s", kind)
		}
	}
	if current["boundedRefillInvariant"].(map[string]any)["required"] != true {
		t.Fatal("no ready incorrectly ended repair obligations")
	}
	for _, action := range collaborationTestActions(current) {
		if action["kind"] == "review_blocked_dependency" {
			facts := collaborationAnyList(action["dependencyFacts"])
			if len(facts) != 1 || facts[0].(map[string]any)["holdsWriter"] != false || facts[0].(map[string]any)["executionEnded"] != true {
				t.Fatalf("writer release confused with acceptance: %#v", facts)
			}
		}
	}
	delivery := callCollaborationTest(t, c, "next_actions", collaborationStateIdentity(db, "delivery-1"))
	for _, kind := range []string{"prepare_blocker_repair_packet", "link_blocker_dependency_packet", "prepare_dependency_review_packet"} {
		if !collaborationTestHasActionKind(delivery, kind) {
			t.Fatalf("delivery has no concrete %s package", kind)
		}
	}
	execution := callCollaborationTest(t, c, "next_actions", collaborationStateIdentity(db, "coordinator-1"))
	if execution["preparationHandoff"] == nil {
		t.Fatal("execution reported only no READY")
	}
	// Diagnosis never removes a real business dependency automatically.
	get := collaborationStateIdentity(db, "controller-1")
	get["itemId"] = proposal["id"]
	item := callCollaborationTest(t, c, "get", get)["item"].(map[string]any)
	if len(collaborationStringList(item["depends_on"])) != 1 {
		t.Fatal("dependency mutated without controller")
	}
	// Only the controller may confirm it was writer ordering and freeze the packet.
	packet := collaborationStateCloudItem(root, proposal["id"].(string), "proposal-after-writer-release-001", "", "src/proposal")["packet"]
	p := collaborationStateIdentity(db, "controller-1")
	p["expectedRevision"], p["items"] = current["revision"], []any{map[string]any{"id": proposal["id"], "depends_on": []any{}, "packet": packet, "phase": "ready", "evidence": []any{"controller:independent-contract-confirmed"}}}
	callCollaborationTest(t, c, "apply", p)
	execution = callCollaborationTest(t, c, "next_actions", collaborationStateIdentity(db, "coordinator-1"))
	if len(collaborationAnyList(execution["refillInputs"])) != 1 {
		t.Fatal("ended predecessor business blocker still froze independent work")
	}
}

func TestBlockedRepairHonorsLinkedRepairAndExternalWait(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "mission.sqlite3")
	c := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node")})
	dep := collaborationStateLocalItem("assigned-repair", "planned")
	item := collaborationStateLocalItem("failed-with-assigned-repair", "blocked")
	item["source_ref"], item["result"], item["terminal_ref"], item["validation"], item["evidence"] = "test:terminal", "completed", "native:terminal", "failed", []any{"native:terminal"}
	item["depends_on"] = []any{"assigned-repair"}
	item["blocker"] = map[string]any{"kind": "dependency", "owner": "repair-owner", "reason": "assigned repair required", "resume_when": "assigned-repair accepted", "next_check_at": int64(9999999999)}
	external := cloneParams(item)
	external["id"] = "external-wait"
	external["depends_on"] = []any{}
	external["blocker"] = map[string]any{"kind": "external", "owner": "provider-owner", "reason": "provider unavailable", "resume_when": "provider restores service", "next_check_at": int64(9999999999)}
	callCollaborationTest(t, c, "init", collaborationStateInitParams(db, []any{dep, item, external}))
	r := callCollaborationTest(t, c, "next_actions", collaborationStateIdentity(db, "controller-1"))
	for _, action := range collaborationTestActions(r) {
		if action["kind"] == "prepare_blocker_repair" || action["kind"] == "link_blocker_dependency" {
			t.Fatalf("duplicated assigned repair or converted external wait: %#v", action)
		}
	}
}
