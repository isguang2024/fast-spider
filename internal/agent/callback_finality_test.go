package agent

import (
	"strings"
	"testing"
	"time"
)

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
	if _, exists, err := s.registrationFor("source"); err != nil || !exists {
		t.Fatalf("CHAT route was released: %v %v", exists, err)
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
