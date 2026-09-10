package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func nativeRecoveryCloudDetail(session, currentNode string, requestMessageID, requestText string) map[string]any {
	mapping := map[string]any{
		"assistant-1": map[string]any{
			"id": "assistant-1",
			"message": map[string]any{
				"id": "assistant-1", "author": map[string]any{"role": "assistant"},
				"content": map[string]any{"parts": []any{"working"}},
			},
		},
	}
	if requestMessageID != "" {
		mapping[requestMessageID] = map[string]any{
			"id": requestMessageID,
			"message": map[string]any{
				"id": requestMessageID, "author": map[string]any{"role": "user"},
				"content": map[string]any{"parts": []any{requestText}},
			},
		}
	}
	return map[string]any{"conversation_id": session, "current_node": currentNode, "mapping": mapping}
}

func nativeRecoveryTask(projectID, taskID, sessionID string, round int) nativeRunnerTask {
	request := nativeRunnerDispatch{ProjectID: projectID, TaskID: taskID, Round: round, ControllerSessionID: "controller", ResultPath: filepath.Join("native-runner", "result.md")}
	return nativeRunnerTask{
		ID: taskID, ProjectID: projectID, Round: round, State: "active", Request: &request,
		Receipt: &nativeRunnerReceipt{SessionID: sessionID, TaskRef: nativeRunnerTaskRef(request), Generation: int64(round), ResultPath: request.ResultPath},
	}
}

func registerNativeRecoveryRoute(t *testing.T, store *sessionCallbackStore, projectID, taskID, sessionID string, generation int64) {
	t.Helper()
	route := testCallbackRegistration(sessionID, "controller", taskID, generation)
	route.MissionID = projectID
	route.CallbackClaimTransport = callbackClaimTransportLocal
	route.NativeRunner = true
	if _, _, err := store.register(route); err != nil {
		t.Fatal(err)
	}
}

func TestNativeRunnerProbeUnknownPreservesActivityWithoutTerminalInference(t *testing.T) {
	const session, project, task = "probe-unknown-chat", "project", "task"
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	registerNativeRecoveryRoute(t, manager.callbackStore, project, task, session, 1)
	manager.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "token", nil }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/conversation/"+session {
			http.NotFound(w, r)
			return
		}
		writeChatGPTCloudTestJSON(t, w, nativeRecoveryCloudDetail(session, "assistant-1", "", ""))
	}))
	defer server.Close()
	manager.chatgptCloud.baseURL, manager.chatgptCloud.http = server.URL, server.Client()
	taskValue := nativeRecoveryTask(project, task, session, 1)
	probe, err := newNativeRunnerTransport(manager, t.TempDir()).Probe(context.Background(), taskValue)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Authoritative || probe.Terminal || probe.Running || probe.ProgressKey == "" || probe.ContextExhausted {
		t.Fatalf("unknown probe was misclassified: %+v", probe)
	}
}

func TestNativeRunnerProbeContextBytesIgnoreUnrelatedBranch(t *testing.T) {
	const session, project, task = "probe-context-chat", "project", "task-context"
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	registerNativeRecoveryRoute(t, manager.callbackStore, project, task, session, 1)
	manager.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "token", nil }
	detail := nativeRecoveryCloudDetail(session, "assistant-1", "", "")
	mapping := detail["mapping"].(map[string]any)
	mapping["unrelated"] = map[string]any{
		"id": "unrelated", "message": map[string]any{
			"id": "unrelated", "author": map[string]any{"role": "assistant"},
			"content": map[string]any{"parts": []any{strings.Repeat("irrelevant", 10000)}},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatGPTCloudTestJSON(t, w, detail)
	}))
	defer server.Close()
	manager.chatgptCloud.baseURL, manager.chatgptCloud.http = server.URL, server.Client()
	probe, err := newNativeRunnerTransport(manager, t.TempDir()).Probe(context.Background(), nativeRecoveryTask(project, task, session, 1))
	if err != nil {
		t.Fatal(err)
	}
	activeContent := mapping["assistant-1"].(map[string]any)["message"].(map[string]any)["content"]
	raw, err := json.Marshal(activeContent)
	if err != nil {
		t.Fatal(err)
	}
	if !probe.ContextBytesKnown || probe.ContextBytes != int64(len(raw)) {
		t.Fatalf("context bytes=%d known=%v want=%d", probe.ContextBytes, probe.ContextBytesKnown, len(raw))
	}
}

func TestNativeRunnerContinueSameKeySendsOnceWhenCloudIsUnknown(t *testing.T) {
	const session, project, taskID = "continue-unknown-chat", "project", "task"
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.nativeRunnerMachineID = func() (string, error) { return "machine", nil }
	registerNativeRecoveryRoute(t, manager.callbackStore, project, taskID, session, 1)
	manager.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "token", nil }
	var mu sync.Mutex
	var detail = nativeRecoveryCloudDetail(session, "assistant-1", "", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/conversation/"+session {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		current := detail
		mu.Unlock()
		writeChatGPTCloudTestJSON(t, w, current)
	}))
	defer server.Close()
	manager.chatgptCloud.baseURL, manager.chatgptCloud.http = server.URL, server.Client()
	sends := 0
	var sentPrompt string
	manager.chatgptCloud.sendOverride = func(_ context.Context, _, _, prompt, _, _ string) (chatgptCloudTurnResult, error) {
		sends++
		sentPrompt = prompt
		requestID := chatgptCloudSendRequestMessageID(session, "continue-unknown-key")
		mu.Lock()
		detail = nativeRecoveryCloudDetail(session, "assistant-1", requestID, prompt)
		mu.Unlock()
		return chatgptCloudTurnResult{ConversationID: session}, nil
	}
	transport := newNativeRunnerTransport(manager, manager.dataDir)
	taskValue := nativeRecoveryTask(project, taskID, session, 1)
	if err := transport.Continue(context.Background(), taskValue, "resume this same block", "continue-unknown-key"); err != nil {
		t.Fatal(err)
	}
	if err := transport.Continue(context.Background(), taskValue, "resume this same block", "continue-unknown-key"); err != nil {
		t.Fatal(err)
	}
	if sends != 1 || !strings.Contains(sentPrompt, taskValue.Receipt.TaskRef) {
		t.Fatalf("same-key continuation sends=%d prompt=%q", sends, sentPrompt)
	}
}

func TestNativeRunnerCheckpointRejectsStaleTaskRefAndStoresNonTerminalProgress(t *testing.T) {
	projectID, taskID, sessionID := "project-test", "task-checkpoint", "checkpoint-chat"
	runner, _, _ := newNativeRunnerForTest(t, "checkpoint", nil)
	task := addNativeTask(t, runner, projectID, taskID, "")
	task.Request = &nativeRunnerDispatch{ProjectID: projectID, TaskID: taskID, Round: task.Round, ControllerSessionID: "controller", ResultPath: filepath.Join(runner.dir, "result.md")}
	task.Receipt = &nativeRunnerReceipt{SessionID: sessionID, TaskRef: nativeRunnerTaskRef(*task.Request), Generation: int64(task.Round), ResultPath: task.Request.ResultPath}
	task.State = "active"
	if err := runner.saveTask(context.Background(), task, "test_active"); err != nil {
		t.Fatal(err)
	}
	store := newSessionCallbackStore(t.TempDir())
	registerNativeRecoveryRoute(t, store, projectID, taskID, sessionID, int64(task.Round))
	manager := &AgentManager{callbackStore: store, nativeRunner: runner}
	transport := newNativeRunnerTransport(manager, t.TempDir())
	manager.nativeRunnerTransport = transport
	stale := task.Request
	stale.Round++
	if _, err := manager.Control(context.Background(), "runner.checkpoint", map[string]any{"responseContent": map[string]any{"taskRef": nativeRunnerTaskRef(*stale), "summary": "stale"}}); err == nil {
		t.Fatal("accepted checkpoint for an old generation")
	}
	result, err := manager.Control(context.Background(), "runner.checkpoint", map[string]any{"responseContent": map[string]any{
		"taskRef": task.Receipt.TaskRef, "summary": "build started", "nextStep": "run checks", "stage": "implementation",
	}})
	if err != nil || result["checkpointSaved"] != true {
		t.Fatalf("checkpoint result=%v err=%v", result, err)
	}
	updated := loadNativeTask(t, runner, projectID, taskID)
	if updated.Result != nil || updated.Recovery == nil || updated.Recovery.Checkpoint.Summary != "build started" {
		t.Fatalf("checkpoint became terminal or was not persisted: %+v", updated)
	}
}
