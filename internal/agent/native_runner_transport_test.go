package agent

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeRunnerReuseUsesConfiguredModelInsteadOfFailedInitialAuto(t *testing.T) {
	const session = "native-reuse-auto"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/conversation/"+session {
			http.NotFound(w, r)
			return
		}
		writeChatGPTCloudTestJSON(t, w, map[string]any{
			"conversation_id": session, "current_node": "assistant-1", "default_model_slug": "auto",
			"mapping": map[string]any{"assistant-1": map[string]any{
				"id": "assistant-1", "parent": nil,
				"message": map[string]any{"id": "assistant-1", "author": map[string]any{"role": "assistant"}, "metadata": map[string]any{"model_slug": "auto"}},
			}},
		})
	}))
	defer server.Close()
	root := t.TempDir()
	manager := New(root, nil)
	defer manager.Close(context.Background())
	manager.SetChatGPTCloudCreateDefaults("advanced", "quick_chat", "gpt-5-6-thinking", "max")
	manager.nativeRunnerMachineID = func() (string, error) { return "machine", nil }
	manager.chatgptCloud.baseURL = server.URL
	manager.chatgptCloud.http = server.Client()
	manager.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "token", nil }
	var model, thinking, sentPrompt string
	manager.chatgptCloud.sendOverride = func(_ context.Context, _, _, prompt, selectedModel, selectedThinking string) (chatgptCloudTurnResult, error) {
		model, thinking, sentPrompt = selectedModel, selectedThinking, prompt
		return chatgptCloudTurnResult{ConversationID: session}, nil
	}
	transport := newNativeRunnerTransport(manager, root)
	_, err := transport.Dispatch(context.Background(), nativeRunnerDispatch{
		ProjectID: "project", TaskID: "planner", Round: 3, ControllerSessionID: "controller",
		WorkingDirectory: root, TargetSessionID: session, Prompt: "Read assigned packet", IdempotencyKey: "native-model-recovery-round-3",
		ResultPath: filepath.Join(root, "native-runner", "planner-r3.result"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if model != "gpt-5-6-thinking" || thinking != "max" {
		t.Fatalf("native retry inherited failed auto selection: model=%q thinking=%q", model, thinking)
	}
	for _, required := range []string{"file_read", "file_edit", "ai_control", "api_tool.list_resources", "Low-level capability names"} {
		if !strings.Contains(sentPrompt, required) {
			t.Fatalf("missing tool discovery contract %q", required)
		}
	}
}

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

func TestNativeRunnerTransportKeepsMissingReportPendingUntilFileRecovers(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	task := nativeCallbackFixture(t, store, "missing-report-chat", "missing-report-task", true)
	if err := os.Remove(task.Receipt.ResultPath); err != nil {
		t.Fatal(err)
	}
	transport := newNativeRunnerTransport(&AgentManager{callbackStore: store}, t.TempDir())
	result, err := transport.Observe(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Path != task.Receipt.ResultPath || result.Outcome != "completed" {
		t.Fatalf("missing report observation lost binding: %+v", result)
	}
	task.Result = result
	if err = transport.Acknowledge(context.Background(), task); err == nil || !strings.Contains(err.Error(), "readable regular file") {
		t.Fatalf("missing report was acknowledged: %v", err)
	}
	if _, exists, lookupErr := store.registrationFor(task.Receipt.SessionID); lookupErr != nil || !exists {
		t.Fatalf("missing report route was retired: exists=%v err=%v", exists, lookupErr)
	}
	if err = os.WriteFile(task.Receipt.ResultPath, []byte("recovered report"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = transport.Acknowledge(context.Background(), task); err != nil {
		t.Fatalf("recovered report ACK: %v", err)
	}
	if _, exists, lookupErr := store.registrationFor(task.Receipt.SessionID); lookupErr != nil || exists {
		t.Fatalf("recovered report route was not retired: exists=%v err=%v", exists, lookupErr)
	}
}

func TestNativeRunnerMissingReportMaterializesBlockedEvidenceAndAcksOriginalRoute(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	fixture := nativeCallbackFixture(t, store, "missing-report-recovery-chat", "missing-report-recovery-task", true)
	if err := os.Remove(fixture.Receipt.ResultPath); err != nil {
		t.Fatal(err)
	}
	transport := newNativeRunnerTransport(&AgentManager{callbackStore: store}, t.TempDir())
	result, err := transport.Observe(context.Background(), fixture)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Path != fixture.Receipt.ResultPath || !result.Terminal {
		t.Fatalf("missing report observation lost terminal binding: %+v", result)
	}

	backend := newFakeNativeRunnerBackend()
	runner, err := newNativeRunner(t.TempDir(), backend, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close(context.Background())
	root := t.TempDir()
	if _, err = runner.Handle(context.Background(), "runner.init", map[string]any{
		"projectId": "project", "root": root, "goal": "recover the bound report", "controllerSessionId": "controller",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Handle(context.Background(), "runner.add", map[string]any{
		"projectId": "project",
		"task":      nativeRunnerTask{ID: fixture.ID, Key: fixture.ID, Title: fixture.ID, Objective: "recover report", Acceptance: "blocked evidence is durable"},
	}); err != nil {
		t.Fatal(err)
	}
	task := loadNativeTask(t, runner, "project", fixture.ID)
	task.Request, task.Receipt = fixture.Request, fixture.Receipt
	task.State = "active"
	if err = runner.storeResult(context.Background(), &task, *result); err != nil {
		t.Fatal(err)
	}
	if task.Result == nil || task.Result.ErrorCode != "RUNNER_REPORT_UNAVAILABLE" || task.Result.Outcome != "blocked" || task.Result.SHA256 == "" {
		t.Fatalf("recovery result is not explicit blocked evidence: %+v", task.Result)
	}
	recovery, err := os.ReadFile(fixture.Receipt.ResultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(recovery), "native_runner_report_recovery") || !strings.Contains(string(recovery), "RUNNER_REPORT_UNAVAILABLE") || !strings.Contains(string(recovery), `"status": "blocked"`) {
		t.Fatalf("recovery report does not preserve terminal failure evidence: %s", recovery)
	}
	if err = transport.Acknowledge(context.Background(), task); err != nil {
		t.Fatalf("original missing-report callback was not acknowledged after Node recovery: %v", err)
	}
	if _, exists, lookupErr := store.registrationFor(fixture.Receipt.SessionID); lookupErr != nil || exists {
		t.Fatalf("original callback route remains after recovery ACK: exists=%v err=%v", exists, lookupErr)
	}
}

func TestNativeRunnerAcksFromFrozenSnapshotWhenSourceReportChanges(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	fixture := nativeCallbackFixture(t, store, "frozen-snapshot-chat", "frozen-snapshot-task", true)
	transport := newNativeRunnerTransport(&AgentManager{callbackStore: store}, t.TempDir())
	result, err := transport.Observe(context.Background(), fixture)
	if err != nil {
		t.Fatal(err)
	}

	backend := newFakeNativeRunnerBackend()
	runner, err := newNativeRunner(t.TempDir(), backend, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close(context.Background())
	root := t.TempDir()
	if _, err = runner.Handle(context.Background(), "runner.init", map[string]any{
		"projectId": "project", "root": root, "goal": "ack frozen evidence", "controllerSessionId": "controller",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Handle(context.Background(), "runner.add", map[string]any{
		"projectId": "project",
		"task":      nativeRunnerTask{ID: fixture.ID, Key: fixture.ID, Title: fixture.ID, Objective: "freeze report", Acceptance: "snapshot remains verifiable"},
	}); err != nil {
		t.Fatal(err)
	}
	task := loadNativeTask(t, runner, "project", fixture.ID)
	task.Request, task.Receipt = fixture.Request, fixture.Receipt
	task.State = "active"
	if err = runner.storeResult(context.Background(), &task, *result); err != nil {
		t.Fatal(err)
	}
	if task.Result == nil || task.Result.Path == fixture.Receipt.ResultPath || task.Result.SHA256 == "" {
		t.Fatalf("successful result was not frozen: %+v", task.Result)
	}
	if err = os.Remove(fixture.Receipt.ResultPath); err != nil {
		t.Fatal(err)
	}
	if err = transport.Acknowledge(context.Background(), task); err != nil {
		t.Fatalf("frozen snapshot did not acknowledge original route: %v", err)
	}
	if _, exists, lookupErr := store.registrationFor(fixture.Receipt.SessionID); lookupErr != nil || exists {
		t.Fatalf("callback route remains after frozen snapshot ACK: exists=%v err=%v", exists, lookupErr)
	}
}

func TestNativeRunnerRecoversLegacyMissingHistoryBindingAfterRestart(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	fixture := nativeCallbackFixture(t, store, "legacy-missing-report-chat", "legacy-missing-report-task", true)
	if err := os.Remove(fixture.Receipt.ResultPath); err != nil {
		t.Fatal(err)
	}
	pending, err := store.pendingSnapshot(fixture.Receipt.SessionID, fixture.Request.ControllerSessionID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("legacy callback fixture pending=%+v err=%v", pending, err)
	}
	transport := newNativeRunnerTransport(&AgentManager{callbackStore: store}, t.TempDir())
	backend := newFakeNativeRunnerBackend()
	runner, err := newNativeRunner(t.TempDir(), backend, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close(context.Background())
	runner.backend = transport
	root := t.TempDir()
	if _, err = runner.Handle(context.Background(), "runner.init", map[string]any{
		"projectId": "project", "root": root, "goal": "recover legacy history", "controllerSessionId": "controller",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Handle(context.Background(), "runner.add", map[string]any{
		"projectId": "project",
		"task":      nativeRunnerTask{ID: fixture.ID, Key: fixture.ID, Title: fixture.ID, Objective: "recover old ledger", Acceptance: "legacy ACK closes"},
	}); err != nil {
		t.Fatal(err)
	}
	task := loadNativeTask(t, runner, "project", fixture.ID)
	task.Round = 2
	task.State = "accepted"
	task.History = []nativeRunnerAttempt{{
		Round: 1, GoalVersion: task.GoalVersion, Request: fixture.Request, Receipt: fixture.Receipt,
		Result: &nativeRunnerResult{EventID: pending[0].EventKey, Outcome: "blocked", ExecutionOutcome: "completed", ErrorCode: "RUNNER_REPORT_UNAVAILABLE"},
	}}
	if err = runner.saveTask(context.Background(), task, "legacy_fixture"); err != nil {
		t.Fatal(err)
	}
	if err = runner.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered := loadNativeTask(t, runner, "project", fixture.ID)
	if len(recovered.History) != 1 || !recovered.History[0].Acked || recovered.History[0].Result == nil || recovered.History[0].Result.Path != fixture.Receipt.ResultPath {
		t.Fatalf("legacy history was not repaired and acknowledged: history=%+v result=%+v state=%s retry=%d error=%s", recovered.History, recovered.History[0].Result, recovered.State, recovered.AckRetryAt, recovered.LastError)
	}
	report, err := os.ReadFile(fixture.Receipt.ResultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "native_runner_report_recovery") || !strings.Contains(string(report), "RUNNER_REPORT_UNAVAILABLE") {
		t.Fatalf("legacy recovery evidence missing: %s", report)
	}
	if _, exists, lookupErr := store.registrationFor(fixture.Receipt.SessionID); lookupErr != nil || exists {
		t.Fatalf("legacy callback route remains after restart recovery: exists=%v err=%v", exists, lookupErr)
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
