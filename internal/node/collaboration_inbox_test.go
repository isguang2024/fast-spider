package node

import (
	"context"
	"errors"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"path/filepath"
	"testing"
)

func collaborationTestInboxResults(box map[string]any) []map[string]any {
	var out []map[string]any
	for _, value := range collaborationAnyList(box["results"]) {
		out = append(out, value.(map[string]any))
	}
	return out
}

func TestInboxResultNeedsReviewNotRecoveryAndReleasesCapacity(t *testing.T) {
	c, a, db, event := collaborationInboxFixture(t)
	before := callCollaborationTest(t, c, "brief", collaborationStateIdentity(db, "controller-1"))
	used := before["executionHeld"].(map[string]any)
	if collaborationIntDefault(used, "cloud", 0) != 1 {
		t.Fatalf("fixture not holding execution: %#v", before)
	}
	if err := c.PersistCollaborationCallback(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	due := callCollaborationTest(t, c, "next_actions", collaborationStateIdentity(db, "controller-1"))
	if !collaborationTestHasActionKind(due, "review_result") || collaborationTestHasActionKind(due, "recover_callback") {
		t.Fatalf("durable result misrouted to recovery: %#v", due)
	}
	brief := callCollaborationTest(t, c, "brief", collaborationStateIdentity(db, "controller-1"))
	if collaborationIntDefault(brief["counts"].(map[string]any), "returned", 0) != 1 || collaborationIntDefault(brief["executionHeld"].(map[string]any), "cloud", -1) != 0 {
		t.Fatalf("returned consumed execution capacity: %#v", brief)
	}
	cap := brief["mission"].(map[string]any)["capacity"].(map[string]any)
	if collaborationIntDefault(brief["executionAvailable"].(map[string]any), "cloud", -1) != collaborationIntDefault(cap, "cloud", 0) {
		t.Fatalf("available capacity differs from same snapshot: %#v", brief)
	}
	// Resolving while ACK fails still leaves a genuine transport obligation.
	box := callCollaborationTest(t, c, "inbox", collaborationStateIdentity(db, "controller-1"))
	p := collaborationStateIdentity(db, "controller-1")
	p["expectedRevision"], p["resultId"], p["decision"], p["evidenceRef"], p["validation"], p["integration"] = box["revision"], collaborationTestInboxResults(box)[0]["resultId"], "accept", "test:accepted", "passed", "not_required"
	a.errors["session.callback.ack"] = errors.New("transport unavailable")
	callCollaborationTest(t, c, "resolve", p)
	after := callCollaborationTest(t, c, "next_actions", collaborationStateIdentity(db, "controller-1"))
	if !collaborationTestHasActionKind(after, "recover_callback") {
		t.Fatalf("real ACK recovery hidden: %#v", after)
	}
	if a.actionCount("session.get") != 0 || a.actionCount("session.send") != 0 {
		t.Fatal("ledger views contacted provider")
	}
}

func collaborationInboxFixture(t *testing.T) (*Client, *collaborationTestAgent, string, map[string]any) {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "mission.sqlite3")
	a := &collaborationTestAgent{errors: map[string]error{}}
	c := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node"), Agent: a})
	item := collaborationStateCloudItem(root, "task-1", "mission-key-001", "cloud-1", "src/task")
	init := callCollaborationTest(t, c, "init", collaborationStateInitParams(dbPath, []any{item}))
	p := collaborationStateIdentity(dbPath, "coordinator-1")
	p["itemId"], p["expectedRevision"] = "task-1", init["revision"]
	claimed := callCollaborationTest(t, c, "claim", p)
	token, err := c.readCollaborationToken(claimed["dispatchToken"].(string))
	if err != nil {
		t.Fatal(err)
	}
	m, task, g := localCallbackIdentity(token)
	event := map[string]any{"callbackInboxRoute": map[string]any{"dbPath": token.DBPath, "missionId": token.MissionID, "itemId": token.ItemID, "claim": token.Claim}, "sourceSessionId": "cloud-1", "targetSessionId": "controller-1", "missionId": m, "taskId": task, "generation": g, "eventKey": "formal-1", "outcome": "completed", "resultStatus": "completed", "resultId": "result-manifest-1"}
	return c, a, dbPath, event
}

func TestCollaborationInboxDurableBeforeAckAndAtomicResolve(t *testing.T) {
	c, a, dbPath, event := collaborationInboxFixture(t)
	if err := c.PersistCollaborationCallback(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := c.PersistCollaborationCallback(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	callCollaborationTest(t, c, "callback_ack", map[string]any{"callbackTargetSessionId": "controller-1", "callbackClaimId": "claim-1"})
	// Construct a fresh Node client against the same on-disk mission after ACK.
	c = NewLocalCapabilityClient(c.cfg)
	box := callCollaborationTest(t, c, "inbox", collaborationStateIdentity(dbPath, "controller-1"))
	results := collaborationTestInboxResults(box)
	if len(results) != 1 {
		t.Fatalf("inbox=%#v", box)
	}
	p := collaborationStateIdentity(dbPath, "controller-1")
	p["expectedRevision"], p["resultId"], p["decision"], p["evidenceRef"], p["validation"], p["integration"] = box["revision"], results[0]["resultId"], "accept", "test:verified", "passed", "not_required"
	a.errors["session.callback.ack"] = errors.New("interrupted before transport ACK")
	resolved := callCollaborationTest(t, c, "resolve", p)
	if resolved["resolved"] != true || resolved["ackPending"] != true {
		t.Fatalf("resolve=%#v", resolved)
	}
	box = callCollaborationTest(t, c, "inbox", collaborationStateIdentity(dbPath, "controller-1"))
	if len(collaborationTestInboxResults(box)) != 0 {
		t.Fatal("decision not durable")
	}
	item := readCollaborationTestItem(t, dbPath)
	if item["phase"] != "accepted" {
		t.Fatalf("item=%#v", item)
	}
	delete(a.errors, "session.callback.ack")
	c = NewLocalCapabilityClient(c.cfg)
	replay := callCollaborationTest(t, c, "resolve", p)
	if replay["duplicate"] != true || replay["transportAcked"] != true {
		t.Fatalf("replay=%#v", replay)
	}
	if item := readCollaborationTestItem(t, dbPath); item["phase"] != "done" {
		t.Fatalf("item=%#v", item)
	}
	if err := c.PersistCollaborationCallback(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if a.actionCount("session.create") != 0 || a.actionCount("session.send") != 0 {
		t.Fatal("result resolution contacted provider")
	}
	// A stale controller cannot reuse the already-resolved event to change intent.
	p["decision"] = "rework"
	if got := c.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("resolve", p)); got.Error == nil {
		t.Fatal("conflicting decision accepted")
	}
}

func TestCollaborationInboxFailureCancellationAndIdentityIsolation(t *testing.T) {
	c, _, dbPath, event := collaborationInboxFixture(t)
	wrong := cloneParams(event)
	wrong["taskId"] = "other-attempt"
	if err := c.PersistCollaborationCallback(context.Background(), wrong); err == nil {
		t.Fatal("wrong attempt accepted")
	}
	event["outcome"], event["callbackErrorCode"] = "failed", "CLOUD_CHAT_CANCELED"
	if err := c.PersistCollaborationCallback(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	box := callCollaborationTest(t, c, "inbox", collaborationStateIdentity(dbPath, "controller-1"))
	results := collaborationTestInboxResults(box)
	if len(results) != 1 || results[0]["callbackErrorCode"] != "CLOUD_CHAT_CANCELED" {
		t.Fatalf("box=%#v", box)
	}
	p := collaborationStateIdentity(dbPath, "controller-1")
	p["expectedRevision"], p["resultId"], p["decision"], p["evidenceRef"], p["validation"], p["integration"] = box["revision"], results[0]["resultId"], "accept", "invalid:accept-cancel", "passed", "not_required"
	if r := c.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("resolve", p)); r.Error == nil {
		t.Fatal("canceled execution accepted")
	}
	p["actorSessionId"] = "coordinator-1"
	p["decision"] = "rework"
	if r := c.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("resolve", p)); r.Error == nil {
		t.Fatal("coordinator made business decision")
	}
	setCollaborationTestMissionDispatch(t, dbPath, "paused", false)
	due := callCollaborationTest(t, c, "next_actions", collaborationStateIdentity(dbPath, "coordinator-1"))
	if len(collaborationTestActions(due)) != 0 {
		t.Fatalf("paused mission generated work: %#v", due)
	}
}

func TestCollaborationRecordCheckFinishesAuditAndExhaustsUnchangedBudget(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "mission.sqlite3")
	c := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node")})
	item := collaborationStateLocalItem("local-1", "planned")
	init := callCollaborationTest(t, c, "init", collaborationStateInitParams(dbPath, []any{item}))
	p := collaborationStateIdentity(dbPath, "controller-1")
	p["expectedRevision"], p["items"] = init["revision"], []any{map[string]any{"id": "local-1", "phase": "active", "next_check_at": int64(1000)}}
	callCollaborationTest(t, c, "apply", p)
	readDue := func(actor string, now int64) map[string]any {
		q := collaborationStateIdentity(dbPath, actor)
		q["now"] = now
		return callCollaborationTest(t, c, "next_actions", q)
	}
	due := readDue("coordinator-1", 1000)
	check := collaborationStateIdentity(dbPath, "coordinator-1")
	check["expectedRevision"], check["expectedObservationRevision"], check["actionId"], check["evidenceRef"], check["outcome"], check["now"] = due["revision"], due["observationRevision"], collaborationTestActionID(due, "consistency_audit"), "audit:1", "completed", int64(1000)
	callCollaborationTest(t, c, "record_check", check)
	if collaborationTestHasActionKind(readDue("coordinator-1", 1001), "consistency_audit") {
		t.Fatal("completed audit remained due")
	}
	for _, now := range []int64{1000, 1900, 3700} {
		due = readDue("coordinator-1", now)
		check["expectedRevision"], check["expectedObservationRevision"], check["actionId"], check["evidenceRef"], check["outcome"], check["now"] = due["revision"], due["observationRevision"], collaborationTestActionID(due, "check_execution"), "execution:unchanged", "unchanged", now
		if check["actionId"] == "" {
			t.Fatalf("missing check %#v", due)
		}
		callCollaborationTest(t, c, "record_check", check)
	}
	if collaborationTestHasActionKind(readDue("coordinator-1", 10000), "check_execution") {
		t.Fatal("exhausted check kept polling")
	}
	if !collaborationTestHasActionKind(readDue("controller-1", 10000), "decide_stalled_check") {
		t.Fatal("stalled check lost controller handoff")
	}
}

func TestCollaborationRetryKeepsTaskAndDependencyIdentityAndRejectsLateAttempt(t *testing.T) {
	c, _, dbPath, event := collaborationInboxFixture(t)
	if err := c.PersistCollaborationCallback(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	box := callCollaborationTest(t, c, "inbox", collaborationStateIdentity(dbPath, "controller-1"))
	p := collaborationStateIdentity(dbPath, "controller-1")
	p["expectedRevision"], p["resultId"], p["decision"], p["evidenceRef"] = box["revision"], collaborationTestInboxResults(box)[0]["resultId"], "rework", "review:fix-required"
	callCollaborationTest(t, c, "resolve", p)
	brief := callCollaborationTest(t, c, "brief", collaborationStateIdentity(dbPath, "controller-1"))
	dep := collaborationStateLocalItem("after-task", "planned")
	dep["depends_on"] = []any{"task-1"}
	q := collaborationStateIdentity(dbPath, "controller-1")
	q["expectedRevision"], q["items"] = brief["revision"], []any{dep}
	updated := callCollaborationTest(t, c, "apply", q)
	packet := collaborationTestPacket(filepath.Dir(dbPath), "cloud-1")
	packet["idempotencyKey"] = "new-attempt-key-002"
	r := collaborationStateIdentity(dbPath, "controller-1")
	r["expectedRevision"], r["itemId"], r["evidenceRef"], r["item"] = updated["revision"], "task-1", "decision:attempt2", map[string]any{"packet": packet, "next_action": "Dispatch corrected implementation"}
	retried := callCollaborationTest(t, c, "retry", r)
	if retried["itemId"] != "task-1" || collaborationIntDefault(retried, "attempt", 0) != 2 {
		t.Fatalf("retry=%#v", retried)
	}
	dependent := callCollaborationTest(t, c, "get", map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "controller-1", "itemId": "after-task"})["item"].(map[string]any)
	if collaborationStringList(dependent["depends_on"])[0] != "task-1" {
		t.Fatal("dependency had to be rewritten")
	}
	// A new event from the previous attempt remains historical and cannot end attempt 2.
	late := cloneParams(event)
	late["eventKey"] = "late-old-attempt"
	if err := c.PersistCollaborationCallback(context.Background(), late); err != nil {
		t.Fatal(err)
	}
	item := readCollaborationTestItem(t, dbPath)
	if item["phase"] != "ready" || item["callback"] != "none" {
		t.Fatalf("late callback corrupted task: %#v", item)
	}
	callCollaborationTest(t, c, "resolve", p) // Replay an old decision after attempt 2 exists.
	if item := readCollaborationTestItem(t, dbPath); item["callback"] != "none" {
		t.Fatal("old resolve ACK overwrote new attempt")
	}
	if len(collaborationTestInboxResults(callCollaborationTest(t, c, "inbox", collaborationStateIdentity(dbPath, "controller-1")))) != 0 {
		t.Fatal("late callback created new business obligation")
	}
	// Historical dispatch keys remain reserved after replacing the current row.
	l, err := openCollaborationLedger(context.Background(), mapStringValue(event["callbackInboxRoute"].(map[string]any), "dbPath"), "mission-1", "controller-1", false)
	if err != nil {
		t.Fatal(err)
	}
	defer l.rollback()
	oldKeyItem := collaborationStateCloudItem(filepath.Dir(dbPath), "another-task", "mission-key-001", "cloud-2", "other")
	if err := l.checkUnique(context.Background(), "another-task", oldKeyItem); err == nil {
		t.Fatal("historical key reused")
	}
}

func TestCollaborationCanceledRetryNeedsExplicitUserDecision(t *testing.T) {
	c, _, dbPath, event := collaborationInboxFixture(t)
	event["outcome"], event["callbackErrorCode"] = "failed", "CLOUD_CHAT_CANCELED"
	if err := c.PersistCollaborationCallback(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	box := callCollaborationTest(t, c, "inbox", collaborationStateIdentity(dbPath, "controller-1"))
	p := collaborationStateIdentity(dbPath, "controller-1")
	p["expectedRevision"], p["resultId"], p["decision"], p["evidenceRef"] = box["revision"], collaborationTestInboxResults(box)[0]["resultId"], "rework", "review:canceled"
	callCollaborationTest(t, c, "resolve", p)
	brief := callCollaborationTest(t, c, "brief", collaborationStateIdentity(dbPath, "controller-1"))
	packet := collaborationTestPacket(filepath.Dir(dbPath), "cloud-1")
	packet["idempotencyKey"] = "canceled-new-key-002"
	r := collaborationStateIdentity(dbPath, "controller-1")
	r["expectedRevision"], r["itemId"], r["evidenceRef"], r["item"] = brief["revision"], "task-1", "decision:retry", map[string]any{"packet": packet}
	if got := c.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("retry", r)); got.Error == nil {
		t.Fatal("manual cancellation automatically resumed")
	}
	r["userDecisionRef"] = "user:explicit-resume-this-attempt"
	callCollaborationTest(t, c, "retry", r)
}

func TestCollaborationLocalAgentReferenceAndRepeatedAttempts(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "mission.sqlite3")
	c := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node")})
	item := collaborationStateLocalItem("local-1", "planned")
	item["execution_ref"] = "codex-agent:parent-task#/root/validator"
	item["local_scope"] = map[string]any{"machineId": "machine-1", "workingDirectory": root, "accessMode": "write", "writeScope": []any{"src/local"}}
	current := callCollaborationTest(t, c, "init", collaborationStateInitParams(dbPath, []any{item}))
	for attempt := 1; attempt <= 2; attempt++ {
		p := collaborationStateIdentity(dbPath, "controller-1")
		p["expectedRevision"] = current["revision"]
		p["items"] = []any{map[string]any{"id": "local-1", "phase": "active"}}
		current = callCollaborationTest(t, c, "apply", p)
		p["expectedRevision"] = current["revision"]
		p["items"] = []any{map[string]any{"id": "local-1", "phase": "returned", "result": "completed", "terminal_ref": "test:local-exit", "evidence": []any{"test:local-exit"}}}
		current = callCollaborationTest(t, c, "apply", p)
		p["expectedRevision"] = current["revision"]
		p["items"] = []any{map[string]any{"id": "local-1", "phase": "rework"}}
		current = callCollaborationTest(t, c, "apply", p)
		r := collaborationStateIdentity(dbPath, "controller-1")
		r["expectedRevision"], r["itemId"], r["evidenceRef"], r["item"] = current["revision"], "local-1", "test:local-retry", map[string]any{"execution_ref": item["execution_ref"], "local_scope": item["local_scope"]}
		current = callCollaborationTest(t, c, "retry", r)
	}
	if collaborationIntDefault(current, "attempt", 0) != 3 {
		t.Fatalf("local attempt=%#v", current)
	}
}

func TestCollaborationV3AndInboxRouteCannotBeInvokedFromHub(t *testing.T) {
	a := &collaborationTestAgent{}
	c := NewLocalCapabilityClient(Config{DataDir: t.TempDir(), Agent: a})
	for _, action := range []string{"tree", "tree_update", "archive", "inbox", "resolve", "retry", "record_check", "upgrade"} {
		r := c.handleCapabilityRequest(context.Background(), protocolv1.CapabilityRequest{RequestId: "remote-" + action, Capability: "collaboration.control", Action: action, Params: map[string]any{}})
		if r.Error == nil || r.Error.Code != "UNSUPPORTED_CAPABILITY" {
			t.Fatalf("remote action %s: %#v", action, r.Error)
		}
	}
	r := c.handleCapabilityRequest(context.Background(), protocolv1.CapabilityRequest{RequestId: "remote-callback-route", Capability: "agent.control", Action: "session.callback.register", Params: map[string]any{"callbackInboxRoute": map[string]any{"dbPath": "private"}}})
	if r.Error == nil || r.Error.Code != "UNSUPPORTED_CAPABILITY" || len(a.actions) != 0 {
		t.Fatalf("remote route accepted: %#v", r)
	}
}
