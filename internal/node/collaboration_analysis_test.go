package node

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func analysisTestPolicy(root string) map[string]any {
	return map[string]any{
		"enabled":          true,
		"authorityRef":     "user:authorized-analysis",
		"model":            "gpt-analysis-model",
		"thinking":         "high",
		"machineId":        "analysis-machine",
		"workingDirectory": root,
		"instructions":     "Use only the frozen evidence and return a bounded decision brief.",
	}
}

func analysisTestSource(id, phase string) map[string]any {
	item := collaborationStateLocalItem(id, phase)
	item["source_ref"] = "test:verified-source-" + id
	item["next_action"] = "Source execution completed"
	item["result"] = "completed"
	item["evidence"] = []any{"test:source-evidence-" + id}
	item["terminal_ref"] = "test:terminal-" + id
	item["validation"] = "passed"
	item["integration"] = "done"
	return item
}

func analysisTestMission(t *testing.T, sourceItems []any, policy map[string]any, delivery bool) (*Client, *collaborationTestAgent, string) {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "mission.sqlite3")
	agent := &collaborationTestAgent{
		results: map[string]map[string]any{"session.create": {"sessionId": "analysis-chat-1"}},
		errors:  map[string]error{},
	}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node"), Agent: agent})
	initialized := callCollaborationTest(t, client, "init", collaborationStateInitParams(dbPath, sourceItems))
	if policy != nil {
		patch := collaborationStateIdentity(dbPath, "controller-1")
		patch["expectedRevision"] = initialized["revision"]
		patch["mission"] = map[string]any{"analysis_policy": policy}
		callCollaborationTest(t, client, "apply", patch)
	}
	if delivery {
		bindTestDelivery(t, client, dbPath)
	}
	return client, agent, dbPath
}

func analysisPrepareParams(dbPath, actor string, expected any, sourceIDs []string, reason string) map[string]any {
	return map[string]any{
		"dbPath":           dbPath,
		"missionId":        "mission-1",
		"actorSessionId":   actor,
		"expectedRevision": expected,
		"sourceItemIds":    sourceIDs,
		"reason":           reason,
		"question":         "Which bounded technical decision should the controller adopt?",
		"brief":            "The source execution returned evidence and needs a focused technical synthesis.",
	}
}

func analysisCurrentRevision(t *testing.T, c *Client, dbPath, actor string) int64 {
	t.Helper()
	brief := callCollaborationTest(t, c, "brief", collaborationStateIdentity(dbPath, actor))
	revision, ok := collaborationInt64(brief["revision"])
	if !ok {
		t.Fatalf("brief has no numeric revision: %#v", brief)
	}
	return revision
}

func analysisExpectError(t *testing.T, c *Client, action string, params map[string]any, contains string) {
	t.Helper()
	response := c.HandleLocalCapability(context.Background(), collaborationCapabilityRequest(action, params))
	if response.Error == nil {
		t.Fatalf("%s unexpectedly succeeded: %#v", action, response.Result)
	}
	if contains != "" && !strings.Contains(response.Error.Message, contains) {
		t.Fatalf("%s error=%q does not contain %q", action, response.Error.Message, contains)
	}
}

func analysisItem(t *testing.T, c *Client, dbPath, actor, itemID string) map[string]any {
	t.Helper()
	result := callCollaborationTest(t, c, "get", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": actor, "itemId": itemID,
	})
	item, ok := result["item"].(map[string]any)
	if !ok {
		t.Fatalf("item %s missing from get: %#v", itemID, result)
	}
	return item
}

func TestCollaborationAnalysisPrepareRequiresAuthorizedDeliveryAndFrozenSources(t *testing.T) {
	source := analysisTestSource("source-1", "done")
	c, _, dbPath := analysisTestMission(t, []any{source}, nil, true)
	params := analysisPrepareParams(dbPath, "delivery-1", analysisCurrentRevision(t, c, dbPath, "delivery-1"), []string{"source-1"}, "successor_planning")
	analysisExpectError(t, c, "analysis_prepare", params, "controller has not authorized Cloud analysis")

	policy := analysisTestPolicy(filepath.Dir(dbPath))
	c, _, dbPath = analysisTestMission(t, []any{analysisTestSource("source-1", "done")}, policy, true)
	roleCheckRevision := analysisCurrentRevision(t, c, dbPath, "delivery-1")
	for _, actor := range []string{"controller-1", "coordinator-1", "unbound-delivery-helper"} {
		p := analysisPrepareParams(dbPath, actor, roleCheckRevision, []string{"source-1"}, "successor_planning")
		want := "delivery coordinator prepares analysis"
		if actor == "unbound-delivery-helper" {
			want = "actor not bound to this task"
		}
		analysisExpectError(t, c, "analysis_prepare", p, want)
	}
	unknown := analysisPrepareParams(dbPath, "delivery-1", analysisCurrentRevision(t, c, dbPath, "delivery-1"), []string{"missing-source"}, "successor_planning")
	analysisExpectError(t, c, "analysis_prepare", unknown, "unknown item")

	valid := analysisPrepareParams(dbPath, "delivery-1", analysisCurrentRevision(t, c, dbPath, "delivery-1"), []string{"source-1"}, "successor_planning")
	prepared := callCollaborationTest(t, c, "analysis_prepare", valid)
	analysisID := prepared["itemId"].(string)
	recursive := analysisPrepareParams(dbPath, "delivery-1", analysisCurrentRevision(t, c, dbPath, "delivery-1"), []string{analysisID}, "conflicting_evidence")
	analysisExpectError(t, c, "analysis_prepare", recursive, "recursively")

	for _, field := range []string{"model", "thinking", "writeScope", "workingDirectory", "callbackSessionId"} {
		p := analysisPrepareParams(dbPath, "delivery-1", analysisCurrentRevision(t, c, dbPath, "delivery-1"), []string{"source-1"}, "cross_owner_design")
		p[field] = "caller-selected-value"
		analysisExpectError(t, c, "analysis_prepare", p, "unknown field")
	}
}

func TestCollaborationAnalysisPrepareIsIdempotentAndUsesRevisionCAS(t *testing.T) {
	root := t.TempDir()
	policy := analysisTestPolicy(root)
	sources := []any{analysisTestSource("source-1", "returned"), analysisTestSource("source-2", "done")}
	c, _, dbPath := analysisTestMission(t, sources, policy, true)
	firstParams := analysisPrepareParams(dbPath, "delivery-1", analysisCurrentRevision(t, c, dbPath, "delivery-1"), []string{"source-1"}, "successor_planning")
	first := callCollaborationTest(t, c, "analysis_prepare", firstParams)
	replay := cloneParams(firstParams)
	replay["expectedRevision"] = int64(0)
	replayed := callCollaborationTest(t, c, "analysis_prepare", replay)
	if replayed["replayed"] != true || replayed["itemId"] != first["itemId"] {
		t.Fatalf("analysis replay=%#v first=%#v", replayed, first)
	}
	if !collaborationValueEqual(replayed["phase"], first["phase"]) {
		t.Fatalf("replay changed phase: %#v", replayed)
	}

	current := analysisCurrentRevision(t, c, dbPath, "controller-1")
	mutation := collaborationStateIdentity(dbPath, "controller-1")
	mutation["expectedRevision"] = current
	mutation["mission"] = map[string]any{"next_action": "Evidence changed after the first analysis request"}
	updated := callCollaborationTest(t, c, "apply", mutation)
	stale := analysisPrepareParams(dbPath, "delivery-1", current, []string{"source-2"}, "conflicting_evidence")
	analysisExpectError(t, c, "analysis_prepare", stale, "revision conflict")
	updatedRevision, _ := collaborationInt64(updated["revision"])
	if currentAfter := analysisCurrentRevision(t, c, dbPath, "delivery-1"); currentAfter != updatedRevision {
		t.Fatalf("stale prepare changed revision: got=%d update=%#v", currentAfter, updated)
	}

	item := analysisItem(t, c, dbPath, "delivery-1", first["itemId"].(string))
	packet := item["packet"].(map[string]any)
	for key, want := range map[string]any{"model": policy["model"], "thinking": policy["thinking"], "machineId": policy["machineId"], "workingDirectory": policy["workingDirectory"], "accessMode": "read_only", "callbackType": "text", "callbackSessionId": "controller-1"} {
		if !collaborationValueEqual(packet[key], want) {
			t.Fatalf("frozen packet %s=%#v want=%#v item=%#v", key, packet[key], want, item)
		}
	}
	for _, forbidden := range []string{"writeScope", "targetSessionId", "deliverablePath"} {
		if _, exists := packet[forbidden]; exists {
			t.Fatalf("analysis packet selected %s: %#v", forbidden, packet)
		}
	}
}

func TestCollaborationAnalysisRequestFingerprintIncludesQuestionAndBrief(t *testing.T) {
	root := t.TempDir()
	c, _, dbPath := analysisTestMission(t, []any{analysisTestSource("source-1", "done")}, analysisTestPolicy(root), true)
	base := analysisPrepareParams(dbPath, "delivery-1", analysisCurrentRevision(t, c, dbPath, "delivery-1"), []string{"source-1"}, "successor_planning")
	first := callCollaborationTest(t, c, "analysis_prepare", base)
	replay := cloneParams(base)
	replay["expectedRevision"] = int64(0)
	replayed := callCollaborationTest(t, c, "analysis_prepare", replay)
	if replayed["replayed"] != true || replayed["itemId"] != first["itemId"] {
		t.Fatalf("complete request replay created a new analysis: first=%#v replay=%#v", first, replayed)
	}

	changedQuestion := cloneParams(base)
	changedQuestion["expectedRevision"] = analysisCurrentRevision(t, c, dbPath, "delivery-1")
	changedQuestion["question"] = "Which evidence conflict changes the controller's adoption decision?"
	second := callCollaborationTest(t, c, "analysis_prepare", changedQuestion)
	if second["itemId"] == first["itemId"] {
		t.Fatalf("question change reused original analysis: first=%#v second=%#v", first, second)
	}

	changedBrief := cloneParams(base)
	changedBrief["expectedRevision"] = analysisCurrentRevision(t, c, dbPath, "delivery-1")
	changedBrief["brief"] = "A materially different evidence brief requires a separate technical decision packet."
	third := callCollaborationTest(t, c, "analysis_prepare", changedBrief)
	if third["itemId"] == first["itemId"] || third["itemId"] == second["itemId"] {
		t.Fatalf("brief change reused an existing analysis: first=%#v second=%#v third=%#v", first, second, third)
	}

	firstItem := analysisItem(t, c, dbPath, "delivery-1", first["itemId"].(string))
	secondItem := analysisItem(t, c, dbPath, "delivery-1", second["itemId"].(string))
	thirdItem := analysisItem(t, c, dbPath, "delivery-1", third["itemId"].(string))
	firstPacket := firstItem["packet"].(map[string]any)
	secondPacket := secondItem["packet"].(map[string]any)
	thirdPacket := thirdItem["packet"].(map[string]any)
	if firstItem["phase"] != "ready" || secondItem["phase"] != "ready" || thirdItem["phase"] != "ready" {
		t.Fatalf("changed requests did not remain independently READY: first=%#v second=%#v third=%#v", firstItem, secondItem, thirdItem)
	}
	if firstPacket["idempotencyKey"] == secondPacket["idempotencyKey"] || firstPacket["idempotencyKey"] == thirdPacket["idempotencyKey"] || secondPacket["idempotencyKey"] == thirdPacket["idempotencyKey"] {
		t.Fatalf("changed requests shared idempotency keys: first=%#v second=%#v third=%#v", firstPacket, secondPacket, thirdPacket)
	}
}

func TestCollaborationAnalysisOldTokenCannotDispatchAfterPolicyOrSourceChange(t *testing.T) {
	for _, tc := range []struct {
		name       string
		changeItem bool
	}{
		{name: "policy change"},
		{name: "source revision change", changeItem: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			policy := analysisTestPolicy(root)
			c, agent, dbPath := analysisTestMission(t, []any{analysisTestSource("source-1", "returned")}, policy, true)
			prepared := callCollaborationTest(t, c, "analysis_prepare", analysisPrepareParams(dbPath, "delivery-1", analysisCurrentRevision(t, c, dbPath, "delivery-1"), []string{"source-1"}, "successor_planning"))
			analysisID := prepared["itemId"].(string)
			claimed := callCollaborationTest(t, c, "claim", map[string]any{
				"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "expectedRevision": analysisCurrentRevision(t, c, dbPath, "coordinator-1"), "itemId": analysisID,
			})
			if claimed["dispatchToken"] == nil {
				t.Fatalf("analysis claim=%#v", claimed)
			}

			if tc.changeItem {
				patch := collaborationStateIdentity(dbPath, "controller-1")
				patch["expectedRevision"] = analysisCurrentRevision(t, c, dbPath, "controller-1")
				patch["items"] = []any{map[string]any{"id": "source-1", "next_action": "Source metadata revised"}}
				callCollaborationTest(t, c, "apply", patch)
			} else {
				changed := analysisTestPolicy(root)
				changed["thinking"] = "max"
				patch := collaborationStateIdentity(dbPath, "controller-1")
				patch["expectedRevision"] = analysisCurrentRevision(t, c, dbPath, "controller-1")
				patch["mission"] = map[string]any{"analysis_policy": changed}
				callCollaborationTest(t, c, "apply", patch)
			}
			analysisExpectError(t, c, "dispatch", map[string]any{"dispatchToken": claimed["dispatchToken"]}, "analysis")
			if agent.actionCount("session.create") != 0 {
				t.Fatalf("stale analysis token contacted Cloud: %v", agent.actions)
			}
			if item := analysisItem(t, c, dbPath, "controller-1", analysisID); item["phase"] != "dispatching" {
				t.Fatalf("stale token changed item phase: %#v", item)
			}
		})
	}
}

func TestCollaborationAnalysisAllowsOnlyOneActiveRoundUnderConcurrentClaims(t *testing.T) {
	root := t.TempDir()
	policy := analysisTestPolicy(root)
	c, agent, dbPath := analysisTestMission(t, []any{analysisTestSource("source-1", "done"), analysisTestSource("source-2", "done")}, policy, true)
	first := callCollaborationTest(t, c, "analysis_prepare", analysisPrepareParams(dbPath, "delivery-1", analysisCurrentRevision(t, c, dbPath, "delivery-1"), []string{"source-1"}, "successor_planning"))
	second := callCollaborationTest(t, c, "analysis_prepare", analysisPrepareParams(dbPath, "delivery-1", analysisCurrentRevision(t, c, dbPath, "delivery-1"), []string{"source-2"}, "cross_owner_design"))
	claimRevision := analysisCurrentRevision(t, c, dbPath, "coordinator-1")
	start := make(chan struct{})
	type claimResult struct {
		value map[string]any
		err   error
	}
	results := make(chan claimResult, 2)
	for _, itemID := range []string{first["itemId"].(string), second["itemId"].(string)} {
		itemID := itemID
		go func() {
			<-start
			value, err := c.collaborationControl(context.Background(), "claim", map[string]any{
				"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "expectedRevision": claimRevision, "itemId": itemID,
			})
			results <- claimResult{value: value, err: err}
		}()
	}
	close(start)
	var winner map[string]any
	for i := 0; i < 2; i++ {
		result := <-results
		if result.err == nil {
			if winner != nil {
				t.Fatalf("two analysis claims succeeded: first=%#v second=%#v", winner, result.value)
			}
			winner = result.value
		}
	}
	if winner == nil {
		t.Fatal("concurrent analysis claims both failed")
	}
	callCollaborationTest(t, c, "dispatch", map[string]any{"dispatchToken": winner["dispatchToken"]})
	if agent.actionCount("session.create") != 1 {
		t.Fatalf("one active analysis expected one Cloud create, actions=%v", agent.actions)
	}
	brief := callCollaborationTest(t, c, "brief", collaborationStateIdentity(dbPath, "controller-1"))
	counts := brief["counts"].(map[string]any)
	if collaborationIntDefault(counts, "active", 0) != 1 || collaborationIntDefault(counts, "dispatching", 0) != 0 {
		t.Fatalf("analysis active count=%#v", counts)
	}
	for _, id := range []string{first["itemId"].(string), second["itemId"].(string)} {
		item := analysisItem(t, c, dbPath, "controller-1", id)
		if collaborationStringDefault(item, "phase", "") != "active" && collaborationStringDefault(item, "phase", "") != "ready" {
			t.Fatalf("analysis item entered invalid phase: %#v", item)
		}
	}
}

func TestCollaborationAnalysisDispatchPassesModelAndThinkingToCreateAndSend(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		action string
	}{
		{name: "create", action: "session.create"},
		{name: "send", target: "existing-chat", action: "session.send"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dbPath := filepath.Join(root, "mission.sqlite3")
			packet := collaborationTestPacket(root, tc.target)
			packet["model"] = "gpt-forwarded-model"
			packet["thinking"] = "xhigh"
			createCollaborationTestLedger(t, dbPath, packet)
			agent := &collaborationTestAgent{results: map[string]map[string]any{"session.create": {"sessionId": "created-chat"}}, errors: map[string]error{}}
			c := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node"), Agent: agent})
			callCollaborationTest(t, c, "dispatch", map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"})
			var got map[string]any
			for _, call := range agent.calls {
				if call.action == tc.action {
					got = call.params
					break
				}
			}
			if got == nil || got["model"] != packet["model"] || got["thinking"] != packet["thinking"] || got["configurationMode"] != "advanced" {
				t.Fatalf("%s model forwarding=%#v calls=%v", tc.action, got, agent.calls)
			}
		})
	}
}

func TestCollaborationFollowupIsAtomicIdempotentDeliveryOwnedAndControllerDecides(t *testing.T) {
	c, agent, dbPath, event := collaborationInboxFixture(t)
	bindTestDelivery(t, c, dbPath)
	if err := c.PersistCollaborationCallback(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	box := callCollaborationTest(t, c, "inbox", collaborationStateIdentity(dbPath, "controller-1"))
	resultID := collaborationTestInboxResults(box)[0]["resultId"]
	params := collaborationStateIdentity(dbPath, "controller-1")
	params["expectedRevision"], params["resultId"], params["decision"], params["evidenceRef"] = box["revision"], resultID, "accept", "test:accept-with-followup"
	params["validation"], params["integration"], params["followup"] = "passed", "not_required", "prepare"
	resolved := callCollaborationTest(t, c, "resolve", params)
	followupID, ok := resolved["followupItemId"].(string)
	if !ok || followupID == "" || resolved["resolved"] != true {
		t.Fatalf("followup resolve=%#v", resolved)
	}
	followup := analysisItem(t, c, dbPath, "delivery-1", followupID)
	if followup["phase"] != "planned" || followup["executor"] != "local" || followup["owner"] != "delivery-1" || followup["source_ref"] != "followup:"+fmt.Sprint(resultID) {
		t.Fatalf("followup item=%#v", followup)
	}
	localScope := followup["local_scope"].(map[string]any)
	if localScope["accessMode"] != "read_only" || localScope["machineId"] == nil || localScope["workingDirectory"] == nil {
		t.Fatalf("followup scope=%#v", localScope)
	}
	deliveryActions := callCollaborationTest(t, c, "next_actions", collaborationStateIdentity(dbPath, "delivery-1"))
	if !collaborationTestHasActionKind(deliveryActions, "prepare_followup") {
		t.Fatalf("delivery followup action missing: %#v", deliveryActions)
	}
	deliveryResolve := cloneParams(params)
	deliveryResolve["actorSessionId"] = "delivery-1"
	deliveryResolve["expectedRevision"] = analysisCurrentRevision(t, c, dbPath, "delivery-1")
	analysisExpectError(t, c, "resolve", deliveryResolve, "controller-only result resolution")
	apply := collaborationStateIdentity(dbPath, "delivery-1")
	apply["expectedRevision"], apply["items"] = deliveryActions["revision"], []any{map[string]any{"id": followupID, "next_action": "delivery attempted final decision"}}
	analysisExpectError(t, c, "apply", apply, "controller-only mutation")

	replay := callCollaborationTest(t, c, "resolve", params)
	if replay["duplicate"] != true || replay["followupItemId"] != followupID {
		t.Fatalf("followup replay=%#v", replay)
	}
	var followupCount int
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow("SELECT count(*) FROM items WHERE id=?", followupID).Scan(&followupCount); err != nil {
		t.Fatal(err)
	}
	if followupCount != 1 || agent.actionCount("session.create") != 0 || agent.actionCount("session.send") != 0 {
		t.Fatalf("followup replay duplicated item or contacted Cloud: count=%d actions=%v", followupCount, agent.actions)
	}
}

func TestCollaborationFollowupRollbackLeavesNoPartialDecision(t *testing.T) {
	c, _, dbPath, event := collaborationInboxFixture(t)
	bindTestDelivery(t, c, dbPath)
	if err := c.PersistCollaborationCallback(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	box := callCollaborationTest(t, c, "inbox", collaborationStateIdentity(dbPath, "controller-1"))
	resultID := collaborationTestInboxResults(box)[0]["resultId"]
	analysisMutateMission(t, dbPath, func(mission map[string]any) {
		mission["delivery_coordinator"] = "invalid delivery role"
	})
	params := collaborationStateIdentity(dbPath, "controller-1")
	params["expectedRevision"], params["resultId"], params["decision"], params["evidenceRef"] = box["revision"], resultID, "accept", "test:rollback-followup"
	params["validation"], params["integration"], params["followup"] = "passed", "not_required", "prepare"
	analysisExpectError(t, c, "resolve", params, "delivery_coordinator session")

	item := readCollaborationTestItem(t, dbPath)
	if item["phase"] != "returned" {
		t.Fatalf("rollback changed source item=%#v", item)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var followups, unresolved int
	if err := db.QueryRow("SELECT count(*) FROM items WHERE id LIKE 'followup-%'").Scan(&followups); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM callback_inbox WHERE result_id=? AND resolution IS NULL", resultID).Scan(&unresolved); err != nil {
		t.Fatal(err)
	}
	if followups != 0 || unresolved != 1 {
		t.Fatalf("partial followup commit followups=%d unresolved=%d", followups, unresolved)
	}
}

func analysisMutateMission(t *testing.T, dbPath string, mutate func(map[string]any)) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw string
	if err := db.QueryRow("SELECT data FROM mission WHERE singleton=1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	mission := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &mission); err != nil {
		t.Fatal(err)
	}
	mutate(mission)
	updated, err := json.Marshal(mission)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE mission SET data=? WHERE singleton=1", string(updated)); err != nil {
		t.Fatal(err)
	}
}

func TestCollaborationAnalysisPrepareRejectsDuplicateSourceIDsAndBoundsSources(t *testing.T) {
	root := t.TempDir()
	c, _, dbPath := analysisTestMission(t, []any{analysisTestSource("source-1", "done")}, analysisTestPolicy(root), true)
	duplicate := analysisPrepareParams(dbPath, "delivery-1", analysisCurrentRevision(t, c, dbPath, "delivery-1"), []string{"source-1", "source-1"}, "successor_planning")
	analysisExpectError(t, c, "analysis_prepare", duplicate, "duplicate analysis source")
	tooMany := make([]string, 9)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("source-%d", i)
	}
	bounded := analysisPrepareParams(dbPath, "delivery-1", analysisCurrentRevision(t, c, dbPath, "delivery-1"), tooMany, "successor_planning")
	analysisExpectError(t, c, "analysis_prepare", bounded, "1..8")
}

func TestCollaborationAnalysisPrepareRejectsProviderCallsBeforeCommit(t *testing.T) {
	root := t.TempDir()
	c, actualAgent, dbPath := analysisTestMission(t, []any{analysisTestSource("source-1", "done")}, analysisTestPolicy(root), true)
	params := analysisPrepareParams(dbPath, "delivery-1", analysisCurrentRevision(t, c, dbPath, "delivery-1"), []string{"source-1"}, "repeated_rework")
	callCollaborationTest(t, c, "analysis_prepare", params)
	if actualAgent.actionCount("session.create") != 0 || actualAgent.actionCount("session.send") != 0 {
		t.Fatalf("analysis_prepare contacted provider: %v", actualAgent.actions)
	}
}
