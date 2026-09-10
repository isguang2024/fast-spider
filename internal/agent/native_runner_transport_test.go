package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func nativeCallbackFixture(t *testing.T, store *sessionCallbackStore, source, task string, native bool) nativeRunnerTask {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(path, []byte("complete report"), 0600); err != nil {
		t.Fatal(err)
	}
	registration := sessionCallbackRegistration{SourceSessionID: source, TargetSessionID: "controller", MissionID: "project", TaskID: task, Generation: 1, CallbackType: "local_file", CallbackClaimTransport: callbackClaimTransportLocal, DeliverablePath: path, NativeRunner: native, Armed: true}
	if _, _, err := store.register(registration); err != nil {
		t.Fatal(err)
	}
	status, size, digest := inspectCallbackDeliverable(path)
	if _, err := store.enqueue(chatgptCloudEvent{Sequence: store.maxEventSequence() + 1, EventKey: "event-" + source, Type: "conversation.turn.complete", ConversationID: source, EventType: "native-runner-submit", Timestamp: time.Now().Add(-time.Hour), CallbackType: "local_file", CallbackOutcome: "completed", DeliverablePath: path, DeliverableStatus: status, ResultStatus: status, ResultBytes: size, ResultSHA256: digest}); err != nil {
		t.Fatal(err)
	}
	req := nativeRunnerDispatch{ProjectID: "project", TaskID: task, Round: 1, ControllerSessionID: "controller", ResultPath: path}
	return nativeRunnerTask{ID: task, ProjectID: "project", Round: 1, Request: &req, Receipt: &nativeRunnerReceipt{SessionID: source, TaskRef: nativeRunnerTaskRef(req), Generation: 1, ResultPath: path}}
}

func TestNativeRunnerCallbackCoexistsWithLegacyLocalPlugin(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	native := nativeCallbackFixture(t, store, "native-chat", "native", true)
	nativeCallbackFixture(t, store, "legacy-chat", "legacy", false)
	nudgeCount, wakeCount := 0, 0
	d := newSessionCallbackDispatcher(store, slog.New(slog.NewTextHandler(io.Discard, nil)), func(string) bool { return false }, func(_ context.Context, _, prompt string) (sessionCallbackDeliveryResult, error) {
		nudgeCount++
		if !strings.Contains(prompt, "FastSpider_Local") {
			t.Errorf("legacy local callback changed: %s", prompt)
		}
		return testAppServerCallbackDelivery(), nil
	}, func(context.Context, string, int64) error { return nil })
	d.localWake = func() { wakeCount++ }
	d.dispatchOnce()
	if nudgeCount != 1 || wakeCount != 1 {
		t.Fatalf("legacy nudges=%d native wakes=%d", nudgeCount, wakeCount)
	}
	_, legacyEvents, err := store.claim("controller", "legacy-claim", 64, time.Now(), callbackClaimTransportLocal)
	if err != nil {
		t.Fatal(err)
	}
	if len(legacyEvents) != 1 || legacyEvents[0].SourceSessionID != "legacy-chat" {
		t.Fatalf("legacy consumer stole a native block: %+v", legacyEvents)
	}
	transport := newNativeRunnerTransport(&AgentManager{callbackStore: store}, t.TempDir())
	result, err := transport.Observe(context.Background(), native)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Terminal || result.RecoveryOnly || !strings.HasPrefix(result.SHA256, "sha256:") {
		t.Fatalf("native result contract: %+v", result)
	}
}

func TestNativeRunnerTransportExactClaimAndExpiredAck(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	a := nativeCallbackFixture(t, store, "chat-a", "a", true)
	b := nativeCallbackFixture(t, store, "chat-b", "b", true)
	transport := newNativeRunnerTransport(&AgentManager{callbackStore: store}, t.TempDir())
	result, err := transport.Observe(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Path != b.Receipt.ResultPath {
		t.Fatalf("wrong block: %+v", result)
	}
	b.Result = result
	store.mu.Lock()
	event := store.pending[b.Receipt.SessionID]
	event.ClaimedAt = time.Now().Add(-10 * time.Minute)
	store.pending[b.Receipt.SessionID] = event
	store.mu.Unlock()
	if err = transport.Acknowledge(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := store.registrationFor(a.Receipt.SessionID); err != nil || !exists {
		t.Fatalf("ACK retired sibling: exists=%v err=%v", exists, err)
	}
	if _, exists, err := store.registrationFor(b.Receipt.SessionID); err != nil || exists {
		t.Fatalf("ACK failed to retire exact round: exists=%v err=%v", exists, err)
	}
	if err = transport.Acknowledge(context.Background(), b); err != nil {
		t.Fatalf("ACK replay: %v", err)
	}
}

func TestNativeRunnerDispatchWaitsForMachineIdentityBeforeProvider(t *testing.T) {
	root := t.TempDir()
	manager := &AgentManager{chatgptCloud: &ChatGPTCloudAdapter{}}
	transport := newNativeRunnerTransport(manager, root)
	_, err := transport.Dispatch(context.Background(), nativeRunnerDispatch{ProjectID: "p", TaskID: "t", Round: 1, ControllerSessionID: "controller", WorkingDirectory: root, Prompt: "task block", IdempotencyKey: "stable-native-key", ResultPath: filepath.Join(root, "native-runner", "result.md")})
	if err == nil || !strings.Contains(err.Error(), "identity is not ready") {
		t.Fatalf("missing identity must not start Cloud: %v", err)
	}
}
