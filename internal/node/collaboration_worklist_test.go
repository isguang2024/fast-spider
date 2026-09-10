package node

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func unattendedFixture(t *testing.T) (*Client, *collaborationTestAgent, string) {
	t.Helper()
	root := t.TempDir()
	db := filepath.Join(root, "mission.sqlite3")
	a := &collaborationTestAgent{results: map[string]map[string]any{"session.send": {"turnId": "wake-turn", "executionMode": "codex_desktop_ipc", "owner": "codex_desktop"}}}
	c := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node"), Agent: a})
	items := []any{}
	for i := 1; i <= 2; i++ {
		id := fmt.Sprintf("AE-SYSTEM-STORE-POOL-%d", i)
		key := fmt.Sprintf("active-cloud-key-%d", i)
		chat := fmt.Sprintf("cloud-%d", i)
		item := collaborationStateCloudItem(root, id, key, chat, fmt.Sprintf("src/active-%d", i))
		item["phase"], item["source_ref"], item["started_at"] = "active", "test:1691-verified-cloud-binding", int64(100)
		item["binding"] = map[string]any{"chatSessionId": chat, "collaborationId": "collaboration-" + id, "taskRef": "task-" + id, "callbackSessionId": "controller-1", "idempotencyKey": key}
		items = append(items, item)
	}
	for _, id := range []string{"AIASSIST-CATALOG-INTEGRATION-IMPLEMENT-01", "AIASSIST-ACCOUNTING-DURABILITY-IMPLEMENT-01", "AIASSIST-TURN-RECOVERY-IMPLEMENT-01", "AIASSIST-PROPOSAL-BINDING-IMPLEMENT-01"} {
		item := collaborationStateLocalItem(id, "planned")
		item["executor"] = "cloud"
		items = append(items, item)
	}
	for _, id := range []string{"AIASSIST-CF-UI-IMPLEMENT-01", "AIASSIST-PROVIDER-READINESS-GATE-IMPLEMENT-01"} {
		item := collaborationStateLocalItem(id, "integrating")
		item["source_ref"] = "test:1691-verified-terminal"
		item["result"], item["validation"], item["terminal_ref"], item["evidence"] = "completed", "passed", "native:done", []any{"native:done"}
		items = append(items, item)
	}
	local := collaborationStateLocalItem("AIASSIST-LOCAL-RUNTIME-CURRENT-SOURCE-EXECUTE-01", "active")
	local["source_ref"] = "test:1691-verified-native-binding"
	local["execution_ref"], local["started_at"], local["next_check_at"] = "codex-agent:controller-1#/root/aiassist_current_runtime", int64(100), int64(9999999999)
	items = append(items, local)
	p := collaborationStateInitParams(db, items)
	p["capacity"] = map[string]any{"cloud": int64(15), "local": int64(3)}
	callCollaborationTest(t, c, "init", p)
	bindTestDelivery(t, c, db)
	return c, a, db
}

func TestUnattendedTibbs1691Worklist(t *testing.T) {
	c, a, db := unattendedFixture(t)
	for _, actor := range []string{"controller-1", "delivery-1", "coordinator-1"} {
		p := collaborationStateIdentity(db, actor)
		p["now"] = int64(1001)
		result := callCollaborationTest(t, c, "next_actions", p)
		diag := result["diagnostics"].(map[string]any)
		if collaborationIntDefault(result["dispatchCapacity"].(map[string]any), "cloudFree", 0) != 13 {
			t.Fatal("1691 fixture must hold exactly 2/15 cloud slots")
		}
		if collaborationIntDefault(diag, "planned_eligible_not_ready", 0) != 4 || diag["coordinator_idle_with_capacity"] != true || collaborationIntDefault(diag, "active_without_verified_progress", 0) != 1 {
			t.Fatalf("missing 1691 diagnostics: %#v", diag)
		}
		counts := map[string]int{}
		for _, action := range collaborationTestActions(result) {
			counts[mapStringValue(action, "kind")]++
		}
		switch actor {
		case "controller-1":
			if counts["prepare_task"] != 4 || counts["review_integration"] != 2 || counts["decide_stalled_check"] != 1 {
				t.Fatalf("controller work dropped: %#v", counts)
			}
		case "delivery-1":
			if counts["prepare_task_packet"] != 4 || counts["prepare_review_integration_decision"] != 2 {
				t.Fatalf("delivery lacks preparation: %#v", counts)
			}
			for _, action := range collaborationTestActions(result) {
				call := action["controllerCall"].(map[string]any)
				if call["params"].(map[string]any)["actorSessionId"] != "controller-1" || call["controllerOnly"] != true {
					t.Fatal("coordination gained decision authority")
				}
			}
		case "coordinator-1":
			if result["preparationHandoff"] == nil {
				t.Fatal("idle capacity gap was silent")
			}
			for _, action := range collaborationTestActions(result) {
				if action["kind"] == "check_execution" && (action["nativeExecutionCheck"] == nil || action["terminalHandoff"] == nil) {
					t.Fatal("local active lacks native plan")
				}
			}
		}
	}
	if len(a.actions) != 0 {
		t.Fatal("diagnostics polled provider")
	}
}

func TestUnattendedWorkRemindersSurviveRestartUntilDecision(t *testing.T) {
	c, _, db := unattendedFixture(t)
	canonical, err := validateCollaborationBaseIdentity(db, "mission-1", "controller-1")
	if err != nil {
		t.Fatal(err)
	}
	route := collaborationRoleWakeRoute{canonical, "mission-1"}
	now := time.Now().Unix()
	if err := c.ensureCollaborationWorkWakes(context.Background(), route, "controller-1", now); err != nil {
		t.Fatal(err)
	}
	c.drainCollaborationRoleWakes(context.Background())
	c.drainCollaborationRoleWakes(context.Background()) // Drain the next bounded page, not a duplicate send.
	conn, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var first int64
	if err := conn.QueryRow("SELECT min(created_at) FROM role_wakeup_outbox WHERE reason='work_due_prepare_task'").Scan(&first); err != nil {
		t.Fatal(err)
	}
	restarted := NewLocalCapabilityClient(c.cfg)
	if err := restarted.ensureCollaborationWorkWakes(context.Background(), route, "controller-1", now+3602); err != nil {
		t.Fatal(err)
	}
	if err := restarted.ensureCollaborationWorkWakes(context.Background(), route, "controller-1", now+3603); err != nil {
		t.Fatal(err)
	}
	var reminders int
	if err := conn.QueryRow("SELECT count(*) FROM role_wakeup_outbox WHERE reason='work_reminder_prepare_task'").Scan(&reminders); err != nil {
		t.Fatal(err)
	}
	if reminders != 4 {
		t.Fatalf("reminder lost/duplicated: %d", reminders)
	}
	p := collaborationStateIdentity(db, "controller-1")
	p["now"] = now + 3603
	result := callCollaborationTest(t, c, "next_actions", p)
	if collaborationIntDefault(result["diagnostics"].(map[string]any), "controller_due_action_age", 0) < 3600 {
		t.Fatal("delivered notification erased due age")
	}
	// Same action remains available to delivery; only a controller decision settles it.
	q := collaborationStateIdentity(db, "delivery-1")
	q["now"] = now + 3603
	if !collaborationTestHasActionKind(callCollaborationTest(t, c, "next_actions", q), "prepare_task_packet") {
		t.Fatal("delivery dedupe canceled unsettled work")
	}
	patch := collaborationStateIdentity(db, "controller-1")
	patch["expectedRevision"], patch["items"] = result["revision"], []any{map[string]any{"id": "AIASSIST-PROPOSAL-BINDING-IMPLEMENT-01", "phase": "blocked", "blocker": map[string]any{"kind": "dependency", "owner": "owner-1", "reason": "exact dependency", "resume_when": "dependency done", "next_check_at": now + 7200}}}
	callCollaborationTest(t, c, "apply", patch)
	if err := restarted.ensureCollaborationWorkWakes(context.Background(), route, "controller-1", now+3604); err != nil {
		t.Fatal(err)
	}
	var obsolete int
	conn.QueryRow("SELECT count(*) FROM role_wakeup_outbox WHERE item_id='AIASSIST-PROPOSAL-BINDING-IMPLEMENT-01' AND reason LIKE 'work_%' AND delivered_at IS NULL").Scan(&obsolete)
	if obsolete != 0 {
		t.Fatal("settled action retained pending reminders")
	}
	if r := c.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("apply", patch)); r.Error == nil {
		t.Fatal("stale controller CAS accepted")
	}
	patch["actorSessionId"] = "delivery-1"
	if r := c.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("apply", patch)); r.Error == nil {
		t.Fatal("delivery wrote a decision")
	}
}

func TestUnattendedIndependentReadySurvivesBlocker(t *testing.T) {
	c, _, db := unattendedFixture(t)
	p := collaborationStateIdentity(db, "controller-1")
	state := callCollaborationTest(t, c, "brief", p)
	ready := collaborationStateCloudItem(filepath.Dir(db), "independent-ready", "independent-key-001", "", "src/independent")
	blocked := collaborationStateLocalItem("waiting-dependent", "planned")
	blocked["depends_on"] = []any{"AIASSIST-LOCAL-RUNTIME-CURRENT-SOURCE-EXECUTE-01"}
	p["expectedRevision"], p["items"] = state["revision"], []any{ready, blocked}
	callCollaborationTest(t, c, "apply", p)
	result := callCollaborationTest(t, c, "next_actions", collaborationStateIdentity(db, "coordinator-1"))
	if len(collaborationAnyList(result["refillInputs"])) != 1 {
		t.Fatal("one blocked dependency froze independent READY")
	}
	reasons := result["plannedPreparation"].(map[string]any)["blocked"]
	if len(collaborationAnyList(reasons)) != 1 {
		t.Fatalf("missing exact dependency reason: %#v", reasons)
	}
}

func TestUnattendedLocalProgressBudgetAndNoTimeOnlyDeferral(t *testing.T) {
	c, _, db := unattendedFixture(t)
	q := collaborationStateIdentity(db, "coordinator-1")
	q["now"] = int64(1001)
	for i, at := range []int64{1001, 1601, 2201} {
		q["now"] = at
		result := callCollaborationTest(t, c, "next_actions", q)
		var action map[string]any
		for _, a := range collaborationTestActions(result) {
			if a["kind"] == "check_execution" && a["itemId"] == "AIASSIST-LOCAL-RUNTIME-CURRENT-SOURCE-EXECUTE-01" {
				action = a
			}
		}
		if action == nil {
			t.Fatal("native check unavailable before budget exhausted")
		}
		p := cloneParams(action["recordCheck"].(map[string]any)["params"].(map[string]any))
		p["outcome"], p["progressToken"], p["evidenceRef"], p["now"] = "observed", "native-tool-checkpoint-1", fmt.Sprintf("native:%d", i), at
		callCollaborationTest(t, c, "record_check", p)
	}
	p := collaborationStateIdentity(db, "controller-1")
	p["now"] = int64(2202)
	r := callCollaborationTest(t, c, "next_actions", p)
	if !collaborationTestHasActionKind(r, "decide_stalled_check") {
		t.Fatal("two no-progress observations did not escalate")
	}
	p["expectedRevision"], p["expectedObservationRevision"], p["actionId"], p["retryAt"], p["evidenceRef"] = r["revision"], r["observationRevision"], collaborationTestActionID(r, "decide_stalled_check"), int64(999999), "time-only"
	if got := c.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("record_action", p)); got.Error == nil {
		t.Fatal("stalled obligation suppressed by time alone")
	}
}
