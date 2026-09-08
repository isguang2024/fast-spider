package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCallbackStoreSchema3DefaultsTransportToHubAndSchema4PersistsLocal(t *testing.T) {
	dir := t.TempDir()
	seed := newSessionCallbackStore(dir)
	reg := testCallbackRegistration("legacy-source", "legacy-target", "legacy-task", 1)
	registered, _, err := seed.register(reg)
	if err != nil {
		t.Fatal(err)
	}
	registered.CallbackClaimTransport = ""
	legacy, err := json.Marshal(sessionCallbackIndex{SchemaVersion: 3, Registrations: []sessionCallbackRegistration{registered}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent", "session-callbacks.json")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := newSessionCallbackStore(dir)
	legacyLoaded, exists, err := loaded.registrationFor("legacy-source")
	if err != nil || !exists || callbackTransportForRegistration(legacyLoaded) != callbackClaimTransportHub {
		t.Fatalf("schema3 legacy transport=%q exists=%v err=%v", legacyLoaded.CallbackClaimTransport, exists, err)
	}
	local := testCallbackRegistration("local-schema4", "local-target", "local-task", 1)
	local.CallbackClaimTransport = callbackClaimTransportLocal
	if _, _, err := loaded.register(local); err != nil {
		t.Fatal(err)
	}
	reloaded := newSessionCallbackStore(dir)
	localLoaded, exists, err := reloaded.registrationFor("local-schema4")
	if err != nil || !exists || callbackTransportForRegistration(localLoaded) != callbackClaimTransportLocal {
		t.Fatalf("schema4 local transport=%q exists=%v err=%v", localLoaded.CallbackClaimTransport, exists, err)
	}
}

func TestSubmittedCallbackNudgeHasOneDirectReceiveCall(t *testing.T) {
	event := sessionCallbackEvent{CompletionSource: "submission"}
	prompt := buildSessionCallbackNudge("codex-target", "callback-envelope", event)
	want := `Call FastSpider_FS codex_cloud_collaboration({"action":"completion.claim","params":{"actorSessionId":"codex-target","claimId":"callback-envelope"}}).`
	if prompt != want {
		t.Fatalf("normal callback must contain only the receive call: %s", prompt)
	}
	event.CompletionSource = "recovery"
	if prompt := buildSessionCallbackNudge("codex-target", "callback-envelope", event); !strings.Contains(prompt, "cloud-callback-recovery") {
		t.Fatalf("missing on-demand recovery entry: %s", prompt)
	}
}

func TestLocalCallbackNudgeUsesLocalControlPlane(t *testing.T) {
	event := sessionCallbackEvent{CompletionSource: "submission"}
	prompt := buildSessionCallbackNudgeForTransport("codex-target", "callback-envelope", callbackClaimTransportLocal, event)
	if !strings.Contains(prompt, "FastSpider_Local collaboration_control") || !strings.Contains(prompt, "callback_claim") || !strings.Contains(prompt, "callback_ack") || strings.Contains(prompt, "FastSpider_FS") {
		t.Fatalf("local callback must stay on the local control plane: %s", prompt)
	}
}

func TestLocalCallbackNudgeJSONDrivesClaimAndAck(t *testing.T) {
	s := newSessionCallbackStore(t.TempDir())
	reg := testCallbackRegistration("local-json-source", "local-json-target", "local-json-task", 1)
	reg.CallbackClaimTransport = callbackClaimTransportLocal
	if _, _, err := s.register(reg); err != nil {
		t.Fatal(err)
	}
	if queued, err := s.enqueue(testCallbackEvent("local-json-source", 1)); err != nil || !queued {
		t.Fatalf("enqueue queued=%v err=%v", queued, err)
	}
	grouped, err := s.pendingForNudgeByTransport()
	if err != nil {
		t.Fatal(err)
	}
	group := sessionCallbackNudgeGroup{TargetSessionID: "local-json-target", Transport: callbackClaimTransportLocal}
	envelope := sessionCallbackEnvelopeIDForTransport(group.TargetSessionID, group.Transport, grouped[group])
	prompt := buildSessionCallbackNudgeForTransport(group.TargetSessionID, envelope, group.Transport, grouped[group]...)
	parts := strings.Split(prompt, "FastSpider_Local collaboration_control(")
	if len(parts) != 3 {
		t.Fatalf("expected claim and ack calls, prompt=%q", prompt)
	}
	manager := &AgentManager{callbackStore: s}
	for i, raw := range parts[1:] {
		end := strings.Index(raw, "})")
		if end < 0 {
			t.Fatalf("call %d has no JSON terminator: %q", i, raw)
		}
		var call struct {
			Action string         `json:"action"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal([]byte(raw[:end+1]), &call); err != nil {
			t.Fatalf("call %d JSON decode: %v", i, err)
		}
		if call.Action != []string{"callback_claim", "callback_ack"}[i] {
			t.Fatalf("call %d action=%q", i, call.Action)
		}
		if call.Params["callbackTargetSessionId"] != group.TargetSessionID || call.Params["callbackClaimId"] != envelope || call.Params["callbackClaimTransport"] != callbackClaimTransportLocal {
			t.Fatalf("call %d params=%#v", i, call.Params)
		}
		paramsJSON, err := json.Marshal(call.Params)
		if err != nil {
			t.Fatalf("call %d params encode: %v", i, err)
		}
		var decoded agentControlParams
		if err := json.Unmarshal(paramsJSON, &decoded); err != nil {
			t.Fatalf("call %d params decode: %v", i, err)
		}
		if i == 0 {
			if _, err := manager.sessionCallbackClaim(decoded); err != nil {
				t.Fatalf("claim from decoded JSON failed: %v", err)
			}
		} else {
			if _, err := manager.sessionCallbackAck(decoded); err != nil {
				t.Fatalf("ack from decoded JSON failed: %v", err)
			}
		}
	}
	if pending, _ := s.pendingSnapshot("local-json-source", group.TargetSessionID); len(pending) != 0 {
		t.Fatalf("local callback remained pending after decoded claim/ack: %+v", pending)
	}
	if _, exists, _ := s.registrationFor("local-json-source"); exists {
		t.Fatal("local callback route remained after decoded ack")
	}
}

func TestCallbackClaimTransportIsolatedAndLocalAckRetiresRoute(t *testing.T) {
	s := newSessionCallbackStore(t.TempDir())
	hub := testCallbackRegistration("hub-source", "same-target", "hub-task", 1)
	local := testCallbackRegistration("local-source", "same-target", "local-task", 1)
	local.CallbackClaimTransport = callbackClaimTransportLocal
	if _, _, err := s.register(hub); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.register(local); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"hub-source", "local-source"} {
		if queued, err := s.enqueue(testCallbackEvent(source, 1)); err != nil || !queued {
			t.Fatalf("enqueue %s queued=%v err=%v", source, queued, err)
		}
	}
	localClaim, localEvents, err := s.claim("same-target", "local-claim", 10, time.Now(), callbackClaimTransportLocal)
	if err != nil || localClaim != "local-claim" || len(localEvents) != 1 || localEvents[0].SourceSessionID != "local-source" {
		t.Fatalf("local claim=%q events=%+v err=%v", localClaim, localEvents, err)
	}
	hubClaim, hubEvents, err := s.claim("same-target", "hub-claim", 10, time.Now(), callbackClaimTransportHub)
	if err != nil || hubClaim != "hub-claim" || len(hubEvents) != 1 || hubEvents[0].SourceSessionID != "hub-source" {
		t.Fatalf("hub claim=%q events=%+v err=%v", hubClaim, hubEvents, err)
	}
	acked, retired, err := s.acknowledgeClaimAndRetire("same-target", localClaim, time.Now(), callbackClaimTransportLocal)
	if err != nil || acked != 1 || len(retired) != 1 || retired[0].SourceSessionID != "local-source" {
		t.Fatalf("local ack=%d retired=%+v err=%v", acked, retired, err)
	}
	if _, exists, err := s.registrationFor("local-source"); err != nil || exists {
		t.Fatalf("local route remained after local ack: exists=%v err=%v", exists, err)
	}
	if _, exists, err := s.registrationFor("hub-source"); err != nil || !exists {
		t.Fatalf("hub route was affected by local ack: exists=%v err=%v", exists, err)
	}
	if retryCount, _, retryErr := s.acknowledgeClaimAndRetire("same-target", localClaim, time.Now(), callbackClaimTransportLocal); retryErr != nil || retryCount != 0 {
		t.Fatalf("local ack retry was not idempotent: count=%d err=%v", retryCount, retryErr)
	}
	reloaded := newSessionCallbackStore(filepath.Dir(filepath.Dir(s.path)))
	if retryCount, _, retryErr := reloaded.acknowledgeClaimAndRetire("same-target", localClaim, time.Now(), callbackClaimTransportLocal); retryErr != nil || retryCount != 0 {
		t.Fatalf("local ack retry after restart was not idempotent: count=%d err=%v", retryCount, retryErr)
	}
}

func TestLocalAckValidatesWholeClaimBeforeMutation(t *testing.T) {
	s := newSessionCallbackStore(t.TempDir())
	for _, source := range []string{"local-atomic-a", "local-atomic-b"} {
		reg := testCallbackRegistration(source, "local-atomic-target", source, 1)
		reg.CallbackClaimTransport = callbackClaimTransportLocal
		if _, _, err := s.register(reg); err != nil {
			t.Fatal(err)
		}
		if queued, err := s.enqueue(testCallbackEvent(source, 1)); err != nil || !queued {
			t.Fatalf("enqueue %s queued=%v err=%v", source, queued, err)
		}
	}
	claimID, events, err := s.claim("local-atomic-target", "local-atomic-claim", 2, time.Now(), callbackClaimTransportLocal)
	if err != nil || claimID != "local-atomic-claim" || len(events) != 2 {
		t.Fatalf("claim=%q events=%d err=%v", claimID, len(events), err)
	}
	// Simulate a corrupted/missing route discovered during the all-or-nothing
	// validation pass. The other claimed item must remain untouched.
	s.mu.Lock()
	delete(s.registrations, "local-atomic-b")
	s.mu.Unlock()
	if _, _, err := s.acknowledgeClaimAndRetire("local-atomic-target", claimID, time.Now(), callbackClaimTransportLocal); err == nil {
		t.Fatal("accepted a claim with a missing route")
	}
	if pending, _ := s.pendingSnapshot("local-atomic-a", "local-atomic-target"); len(pending) != 1 || pending[0].ClaimID != claimID {
		t.Fatalf("first claimed item was partially removed: %+v", pending)
	}
}

func TestLocalProviderCompletionIsFormalWhileHubRecoveryRemainsRecovery(t *testing.T) {
	s := newSessionCallbackStore(t.TempDir())
	local := testCallbackRegistration("local-provider-source", "local-provider-target", "local-provider-task", 1)
	local.CallbackClaimTransport = callbackClaimTransportLocal
	if _, _, err := s.register(local); err != nil {
		t.Fatal(err)
	}
	if queued, err := s.enqueue(testCallbackEvent("local-provider-source", 1)); err != nil || !queued {
		t.Fatalf("local enqueue queued=%v err=%v", queued, err)
	}
	localPending, err := s.pendingSnapshot("local-provider-source", "local-provider-target")
	if err != nil || len(localPending) != 1 || localPending[0].CompletionSource != "local-submission" {
		t.Fatalf("local provider source=%+v err=%v", localPending, err)
	}
	if mapped := sessionCallbackEventMap(localPending[0], time.Now().UTC(), false); mapped["recoveryOnly"] != false {
		t.Fatalf("local provider completion incorrectly marked recovery-only: %#v", mapped)
	}
	hub := testCallbackRegistration("hub-provider-source", "hub-provider-target", "hub-provider-task", 1)
	if _, _, err := s.register(hub); err != nil {
		t.Fatal(err)
	}
	if queued, err := s.enqueue(testCallbackEvent("hub-provider-source", 1)); err != nil || !queued {
		t.Fatalf("hub enqueue queued=%v err=%v", queued, err)
	}
	hubPending, err := s.pendingSnapshot("hub-provider-source", "hub-provider-target")
	if err != nil || len(hubPending) != 1 || hubPending[0].CompletionSource != "recovery" {
		t.Fatalf("hub provider source=%+v err=%v", hubPending, err)
	}
}

func TestHubCompletionAcknowledgesOnlyBoundNodeGeneration(t *testing.T) {
	dir := t.TempDir()
	s := newSessionCallbackStore(dir)
	reg := testCallbackRegistration("source", "target", "task", 1)
	if _, _, err := s.register(reg); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.register(testCallbackRegistration("other-source", "target", "other-task", 1)); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"source", "other-source"} {
		event := testCallbackEvent(source, 1)
		event.EventType = "hub-completion-notify"
		if queued, err := s.enqueue(event); err != nil || !queued {
			t.Fatalf("enqueue=%v %v", queued, err)
		}
	}
	// Also handles the previously observed case: a Node claim was never acked.
	if _, _, err := s.claim("target", "old-node-claim", 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	wrong := reg
	wrong.TargetSessionID = "wrong-target"
	if _, err := s.acknowledgeCompletion(wrong, time.Now()); err == nil {
		t.Fatal("accepted a mismatched callback owner")
	}
	manager := &AgentManager{callbackStore: s}
	out, err := manager.sessionCallbackAck(agentControlParams{Mode: "completion", SessionID: "source", CallbackTargetSessionID: reg.TargetSessionID, CallbackMissionID: reg.MissionID, CallbackTaskID: reg.TaskID, CallbackGeneration: reg.Generation})
	if err != nil || out["acked"] != true || out["ackedCount"] != 1 {
		t.Fatalf("ack=%v %v", out, err)
	}
	s = newSessionCallbackStore(dir)
	pending, err := s.pendingSnapshot("", "target")
	if err != nil || len(pending) != 1 || pending[0].SourceSessionID != "other-source" {
		t.Fatalf("wrong callback cleared: %+v %v", pending, err)
	}
	if _, exists, err := s.registrationFor("source"); err != nil || exists {
		t.Fatalf("acknowledged route remained active: %v %v", exists, err)
	}
	if count, err := s.acknowledgeCompletion(reg, time.Now()); err != nil || count != 0 {
		t.Fatalf("retry=%d %v", count, err)
	}
	if queued, err := s.enqueue(testCallbackEvent("source", 2)); err != nil || queued {
		t.Fatalf("recovery requeued an acknowledged submission: %v %v", queued, err)
	}
	newReg := reg
	newReg.Generation++
	if _, _, err := s.register(newReg); err != nil {
		t.Fatal(err)
	}
	if queued, err := s.enqueue(testCallbackEvent("source", 3)); err != nil || !queued {
		t.Fatalf("new generation=%v %v", queued, err)
	}
	if count, err := s.acknowledgeCompletion(reg, time.Now()); err != nil || count != 0 {
		t.Fatalf("old generation affected new result: %d %v", count, err)
	}
	if pending, err := s.pendingSnapshot("source", "target"); err != nil || len(pending) != 1 || pending[0].Generation != 2 {
		t.Fatalf("new result lost: %+v %v", pending, err)
	}
}

func TestCloudCallbackFinalityRejectsIntermediateMessages(t *testing.T) {
	for _, tc := range []struct {
		name, role, channel, recipient, status, async string
		end                                           bool
		want                                          string
	}{
		{"final", "assistant", "final", "all", "finished_successfully", "", true, "completed"},
		{"old async completed with progress", "assistant", "commentary", "all", "finished_successfully", "completed", false, "unknown"},
		{"tool", "tool", "", "all", "finished_successfully", "", true, "unknown"},
		{"tool call", "assistant", "analysis", "python", "finished_successfully", "", false, "unknown"},
		{"analysis end", "assistant", "analysis", "all", "finished_successfully", "", true, "unknown"},
		{"user", "user", "", "all", "finished_successfully", "", true, "unknown"},
		{"async running wins", "assistant", "final", "all", "finished_successfully", "running", true, "running"},
		{"streaming final", "assistant", "final", "all", "in_progress", "completed", true, "running"},
		{"failed async beats old final", "assistant", "final", "all", "finished_successfully", "failed", true, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := map[string]any{"author": map[string]any{"role": tc.role}, "channel": tc.channel, "recipient": tc.recipient, "status": tc.status, "end_turn": tc.end}
			old := map[string]any{"author": map[string]any{"role": "assistant"}, "channel": "final", "status": "finished_successfully", "end_turn": true}
			detail := map[string]any{"async_status": tc.async, "current_node": "current", "mapping": map[string]any{"current": map[string]any{"message": message, "parent": "old"}, "old": map[string]any{"message": old}}}
			if got := chatgptCloudConversationStatus(detail); got != tc.want {
				t.Fatalf("status=%s want=%s", got, tc.want)
			}
		})
	}
}

func TestCallbackSubmissionSupersedesClaimedRecoveryAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s := newSessionCallbackStore(dir)
	if _, _, err := s.register(testCallbackRegistration("source", "target", "task", 1)); err != nil {
		t.Fatal(err)
	}
	e := testCallbackEvent("source", 1)
	if _, err := s.enqueue(e); err != nil {
		t.Fatal(err)
	}
	claim, _, err := s.claim("target", "", 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.Sequence = 2
	e.EventType = "hub-completion-notify"
	if queued, err := s.enqueue(e); err != nil || !queued {
		t.Fatalf("formal queued=%v err=%v", queued, err)
	}
	s = newSessionCallbackStore(dir)
	pending, err := s.pendingSnapshot("source", "target")
	if err != nil || len(pending) != 1 || pending[0].CompletionSource != "submission" || pending[0].ClaimID != "" {
		t.Fatalf("replacement=%+v err=%v", pending, err)
	}
	if _, err := s.acknowledgeClaim("target", claim, time.Now()); err == nil {
		t.Fatal("old claim acknowledged replacement")
	}
	e.Sequence = 3
	e.EventType = "conversation-turn-complete"
	if queued, err := s.enqueue(e); err != nil || queued {
		t.Fatalf("recovery overwrote formal: %v %v", queued, err)
	}
}

func TestRecoveryNudgeOnceButRemainsClaimable(t *testing.T) {
	s := newSessionCallbackStore(t.TempDir())
	if _, _, err := s.register(testCallbackRegistration("source", "target", "task", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.enqueue(testCallbackEvent("source", 1)); err != nil {
		t.Fatal(err)
	}
	grouped, _ := s.pendingForNudge()
	if err := s.recordNudge("target", sessionCallbackEnvelopeID("target", grouped["target"]), testAppServerCallbackDelivery(), time.Now()); err != nil {
		t.Fatal(err)
	}
	grouped, _ = s.pendingForNudge()
	if len(grouped) != 0 {
		t.Fatal("recovery will nudge again")
	}
	_, items, err := s.claim("target", "", 1, time.Now())
	if err != nil || len(items) != 1 {
		t.Fatalf("recovery receipt lost: %v %v", items, err)
	}
	e := testCallbackEvent("source", 2)
	e.EventType = "hub-completion-notify"
	if queued, err := s.enqueue(e); err != nil || !queued {
		t.Fatalf("submitted=%v %v", queued, err)
	}
	if due, _, err := s.nudgeSchedule("target", time.Now(), time.Hour); err != nil || !due {
		t.Fatalf("formal result delayed by recovery nudge: %v %v", due, err)
	}
}

func TestLegacyCallbackDedupCannotSwallowFormalSubmission(t *testing.T) {
	s := newSessionCallbackStore(t.TempDir())
	reg, _, err := s.register(testCallbackRegistration("source", "target", "task", 1))
	if err != nil {
		t.Fatal(err)
	}
	// Old versions used this same key for already-consumed failed observations.
	reg.LastEventKey = sessionCallbackCompletionEventKey(reg)
	reg.RecentEventKeys = []string{reg.LastEventKey}
	reg.LastEventSequence = 10
	s.registrations["source"] = reg
	e := testCallbackEvent("source", 11)
	e.EventType = "hub-completion-notify"
	if queued, err := s.enqueue(e); err != nil || !queued {
		t.Fatalf("formal swallowed by legacy key: %v %v", queued, err)
	}
	e.Sequence = 12
	e.CallbackOutcome = "failed"
	if _, err := s.enqueue(e); err == nil {
		t.Fatal("different formal result swallowed")
	}
}

func TestRecoveryClaimDuringDeliveryDoesNotRepeatNudge(t *testing.T) {
	s := newSessionCallbackStore(t.TempDir())
	if _, _, err := s.register(testCallbackRegistration("source", "target", "task", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.enqueue(testCallbackEvent("source", 1)); err != nil {
		t.Fatal(err)
	}
	grouped, _ := s.pendingForNudge()
	sent := grouped["target"]
	now := time.Now()
	if _, _, err := s.claim("target", "", 1, now); err != nil {
		t.Fatal(err)
	}
	if err := s.recordNudge("target", sessionCallbackEnvelopeID("target", sent), testAppServerCallbackDelivery(), now, sent...); err != nil {
		t.Fatal(err)
	}
	if _, err := s.releaseExpiredClaims(now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	grouped, _ = s.pendingForNudge()
	if len(grouped) != 0 {
		t.Fatal("concurrent claim caused a repeat recovery nudge")
	}
}
