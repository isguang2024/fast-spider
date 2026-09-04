package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestChatGPTCloudSessionCreateReplaysThePersistedResult(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	var creates int
	manager.chatgptCloud.createOverride = func(context.Context, string, string) (chatgptCloudTurnResult, error) {
		creates++
		return chatgptCloudTurnResult{ConversationID: "cloud-conversation-1"}, nil
	}
	params := map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"prompt": "hello cloud", "model": "gpt-test", "idempotencyKey": "cloud-idempotency-01", "workingDirectory": t.TempDir(),
	}
	first, err := manager.Control(context.Background(), "session.create", params)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := manager.Control(context.Background(), "session.create", params)
	if err != nil {
		t.Fatalf("replayed create: %v", err)
	}
	if creates != 1 {
		t.Fatalf("cloud creates=%d want 1", creates)
	}
	if first["sessionId"] != "cloud-conversation-1" || second["sessionId"] != first["sessionId"] {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if second["idempotencyStatus"] != "replayed" || second["externalConversationId"] != "cloud-conversation-1" {
		t.Fatalf("replay metadata=%#v", second)
	}
}

func TestChatGPTCloudSessionCreateRejectsPluginBindingBeforeReservation(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	providerCalls := 0
	manager.chatgptCloud.createOverride = func(context.Context, string, string) (chatgptCloudTurnResult, error) {
		providerCalls++
		return chatgptCloudTurnResult{ConversationID: "must-not-create"}, nil
	}
	params := map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"prompt": "plugin binding must fail closed", "pluginName": "fast-spider FS",
		"idempotencyKey": "cloud-plugin-binding-01", "workingDirectory": t.TempDir(),
	}
	_, err := manager.Control(context.Background(), "session.create", params)
	if err == nil {
		t.Fatal("Cloud session.create accepted unsupported per-session plugin binding")
	}
	var capabilityErr interface{ CapabilityError() (string, string, bool) }
	if !errors.As(err, &capabilityErr) {
		t.Fatalf("error=%T does not expose capability classification: %v", err, err)
	}
	code, message, retryable := capabilityErr.CapabilityError()
	if code != "UNSUPPORTED_SESSION_PLUGIN_BINDING" || message == "" || retryable {
		t.Fatalf("classification=%q %q %v", code, message, retryable)
	}
	if providerCalls != 0 {
		t.Fatalf("provider calls=%d, want 0", providerCalls)
	}
	if len(manager.createStore.records) != 0 {
		t.Fatalf("unsupported plugin request changed idempotency records: %#v", manager.createStore.records)
	}
	if strings.Contains(message, "fast-spider FS") || strings.Contains(err.Error(), "fast-spider FS") {
		t.Fatalf("plugin input leaked into error: %v", err)
	}
}

func TestChatGPTCloudLatestAssistantFollowsCurrentNodeAndIsBounded(t *testing.T) {
	detail := map[string]any{
		"currentNode": "user-2",
		"mapping": map[string]any{
			"assistant-old": map[string]any{"parent": "user-1", "message": map[string]any{"id": "assistant-old", "author": map[string]any{"role": "assistant"}, "create_time": 10.0, "content": map[string]any{"parts": []any{"old"}}}},
			"user-2":        map[string]any{"parent": "assistant-new", "message": map[string]any{"id": "user-2", "author": map[string]any{"role": "user"}}},
			"assistant-new": map[string]any{"parent": "user-1", "message": map[string]any{"id": "assistant-new", "author": map[string]any{"role": "assistant"}, "create_time": 20.0, "content": map[string]any{"parts": []any{"new"}}}},
		},
	}
	if got := chatgptCloudLatestAssistantText(detail); got != "new" {
		t.Fatalf("latest assistant=%q", got)
	}
	if status := chatgptCloudConversationStatus(map[string]any{"mapping": map[string]any{}}); status != "unknown" {
		t.Fatalf("status without terminal proof=%q", status)
	}
	if status := chatgptCloudConversationStatus(map[string]any{"currentNode": "assistant", "mapping": map[string]any{"assistant": map[string]any{"message": map[string]any{"status": "finished_successfully"}}}}); status != "completed" {
		t.Fatalf("finished_successfully status=%q", status)
	}
}

func TestChatGPTCloudResultManifestPublishesCompletedResultForPollingRecovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/conversation/cloud-complete" {
			http.NotFound(w, r)
			return
		}
		writeChatGPTCloudTestJSON(t, w, map[string]any{
			"conversation_id": "cloud-complete", "async_status": "completed", "current_node": "assistant-1",
			"mapping": map[string]any{"assistant-1": map[string]any{"message": map[string]any{"author": map[string]any{"role": "assistant"}, "content": map[string]any{"parts": []any{"CLOUD_COLLAB_OK"}}}}},
		})
	}))
	defer server.Close()

	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	if err := manager.chatgptCloud.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.chatgptCloud = NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
	manager.chatgptCloud.baseURL = server.URL
	manager.chatgptCloud.http = server.Client()
	publisher := &testCloudResultPublisher{}
	manager.SetCloudResultPublisher(publisher)

	result, err := manager.Control(context.Background(), "session.result", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "sessionId": "cloud-complete", "resultMode": "manifest", "idempotencyKey": "poll-recovery-result-001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !publisher.called || publisher.text != "CLOUD_COLLAB_OK" || result["resultId"] != "res_callback_1" || result["resultStatus"] != "ready" || result["finalAgentMessage"] != nil {
		t.Fatalf("publisher=%+v result=%#v", publisher, result)
	}
}

func TestChatGPTCloudResultManifestRetriesFailedCallbackPublication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatGPTCloudTestJSON(t, w, map[string]any{
			"conversation_id": "cloud-retry", "async_status": "completed", "current_node": "assistant-1",
			"mapping": map[string]any{"assistant-1": map[string]any{"message": map[string]any{"author": map[string]any{"role": "assistant"}, "content": map[string]any{"parts": []any{"CLOUD_COLLAB_OK"}}}}},
		})
	}))
	defer server.Close()

	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	if err := manager.chatgptCloud.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.chatgptCloud = NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
	manager.chatgptCloud.baseURL, manager.chatgptCloud.http = server.URL, server.Client()
	if _, _, err := manager.callbackStore.register(testCallbackRegistration("cloud-retry", "target", "task", 1)); err != nil {
		t.Fatal(err)
	}
	failed := testCallbackEvent("cloud-retry", 1)
	failed.ResultStatus = "failed"
	if queued, err := manager.callbackStore.enqueue(failed); err != nil || !queued {
		t.Fatalf("enqueue failed metadata queued=%v err=%v", queued, err)
	}
	publisher := &testCloudResultPublisher{}
	manager.SetCloudResultPublisher(publisher)
	result, err := manager.Control(context.Background(), "session.result", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "sessionId": "cloud-retry", "resultMode": "manifest", "idempotencyKey": "poll-recovery-retry-001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !publisher.called || result["status"] != "completed" || result["resultStatus"] != "ready" || result["resultId"] != "res_callback_1" {
		t.Fatalf("publisher=%+v result=%#v", publisher, result)
	}
}

func TestChatGPTCloudResultManifestInspectsAssignedDeliverable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatGPTCloudTestJSON(t, w, map[string]any{"conversation_id": "cloud-file", "mapping": map[string]any{}})
	}))
	defer server.Close()
	deliverable := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(deliverable, []byte("complete\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	if err := manager.chatgptCloud.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.chatgptCloud = NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
	manager.chatgptCloud.baseURL = server.URL
	manager.chatgptCloud.http = server.Client()
	result, err := manager.Control(context.Background(), "session.result", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "sessionId": "cloud-file", "resultMode": "manifest", "callbackDeliverablePath": deliverable,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "completed" || result["resultStatus"] != "ready" || result["deliverableStatus"] != "ready" || result["deliverablePath"] != deliverable || result["resultSHA256"] == "" {
		t.Fatalf("deliverable manifest=%#v", result)
	}
}

func TestChatGPTCloudSessionCreateFailsClosedAfterAmbiguousError(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	var creates int
	manager.chatgptCloud.createOverride = func(context.Context, string, string) (chatgptCloudTurnResult, error) {
		creates++
		return chatgptCloudTurnResult{}, context.DeadlineExceeded
	}
	params := map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"prompt": "may have been created", "idempotencyKey": "cloud-in-doubt-01", "workingDirectory": t.TempDir(),
	}
	if _, err := manager.Control(context.Background(), "session.create", params); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first create error=%v", err)
	}
	record := manager.createStore.records["codex:cloud-in-doubt-01"]
	if record.State != "in_doubt" {
		t.Fatalf("record=%#v want in_doubt", record)
	}
	if _, err := manager.Control(context.Background(), "session.create", params); err == nil {
		t.Fatal("ambiguous cloud create allowed a duplicate retry")
	} else {
		var capabilityErr testCapabilityError
		if !errors.As(err, &capabilityErr) {
			t.Fatalf("retry error=%T %v", err, err)
		}
		code, _, _ := capabilityErr.CapabilityError()
		if code != "AGENT_CREATE_IN_DOUBT" {
			t.Fatalf("retry code=%s", code)
		}
	}
	if creates != 1 {
		t.Fatalf("cloud creates=%d want 1", creates)
	}
}

func TestChatGPTCloudSessionCreatePersistsProviderVisibleRequestIDBeforeSideEffect(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	var providerMessageID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case chatgptSentinelPreparePath:
			writeChatGPTCloudTestJSON(t, w, map[string]any{
				"prepare_token": "sentinel-attempt",
				"proofofwork":   map[string]any{"required": true, "seed": "seed", "difficulty": "ffffffff"},
			})
		case chatgptConversationPrepare:
			writeChatGPTCloudTestJSON(t, w, map[string]any{"status": "success", "conduit_token": "conduit-attempt"})
		case chatgptConversationPath:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create body: %v", err)
			}
			providerMessageID = chatgptCloudRequestMessageID(body)
			http.Error(w, "provider unavailable", http.StatusBadGateway)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager.chatgptCloud.baseURL = server.URL
	manager.chatgptCloud.http = server.Client()
	manager.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "token", nil }
	params := map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "mode": "complete",
		"prompt": "persist exact provider request", "model": "gpt-test",
		"idempotencyKey": "cloud-provider-request-01", "workingDirectory": t.TempDir(),
	}
	if _, err := manager.Control(context.Background(), "session.create", params); err == nil {
		t.Fatal("provider failure was reported as success")
	}
	record := manager.createStore.records["codex:cloud-provider-request-01"]
	if record.State != "in_doubt" || record.Attempt == nil || record.Attempt.RequestMessageID == "" || record.Attempt.RequestMessageID != providerMessageID {
		t.Fatalf("record=%#v providerMessageID=%q", record, providerMessageID)
	}
}

func TestChatGPTCloudSessionCreateKeepsKnownConversationAfterStreamError(t *testing.T) {
	dataDir := t.TempDir()
	manager := New(dataDir, nil)
	workingDirectory := t.TempDir()
	var creates int
	manager.chatgptCloud.createOverride = func(context.Context, string, string) (chatgptCloudTurnResult, error) {
		creates++
		return chatgptCloudTurnResult{ConversationID: "cloud-known-after-error"}, context.DeadlineExceeded
	}
	params := map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"prompt": "created before stream ended", "idempotencyKey": "cloud-known-error-01", "workingDirectory": workingDirectory,
	}
	first, err := manager.Control(context.Background(), "session.create", params)
	if err != nil {
		t.Fatalf("create with known conversation ID: %v", err)
	}
	if first["sessionId"] != "cloud-known-after-error" || first["phase"] != "created_execution_unknown" {
		t.Fatalf("first=%#v", first)
	}
	second, err := manager.Control(context.Background(), "session.create", params)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if creates != 1 || second["sessionId"] != first["sessionId"] {
		t.Fatalf("creates=%d first=%#v second=%#v", creates, first, second)
	}
	if record := manager.createStore.records["codex:cloud-known-error-01"]; record.State != "succeeded" {
		t.Fatalf("recovered create record=%#v want succeeded", record)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted := New(dataDir, nil)
	defer restarted.Close(context.Background())
	restarted.chatgptCloud.createOverride = manager.chatgptCloud.createOverride
	replayed, err := restarted.Control(context.Background(), "session.create", params)
	if err != nil {
		t.Fatalf("restart replay: %v", err)
	}
	if creates != 1 || replayed["sessionId"] != first["sessionId"] || replayed["idempotencyStatus"] != "replayed" {
		t.Fatalf("restart creates=%d replay=%#v", creates, replayed)
	}
}

func TestChatGPTCloudSessionCreateReplaysConversationPersistedBeforeCompletion(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	workingDirectory := t.TempDir()
	storeKey := "codex:cloud-observed-before-complete-01"
	observed := make(chan struct{})
	release := make(chan struct{})
	creates := 0
	manager.chatgptCloud.createOverride = func(context.Context, string, string) (chatgptCloudTurnResult, error) {
		creates++
		if err := manager.createStore.update(storeKey, "thread_created", map[string]any{
			"sessionId": "cloud-observed-before-complete", "creationConfirmed": true,
			"phase": "created_execution_unknown", "completionPending": true,
		}); err != nil {
			t.Errorf("persist observed conversation: %v", err)
		}
		close(observed)
		<-release
		return chatgptCloudTurnResult{ConversationID: "cloud-observed-before-complete"}, context.DeadlineExceeded
	}
	params := map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"prompt": "persist before completion", "idempotencyKey": "cloud-observed-before-complete-01", "workingDirectory": workingDirectory,
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := manager.Control(context.Background(), "session.create", params)
		firstDone <- err
	}()
	<-observed
	replayed, err := manager.Control(context.Background(), "session.create", params)
	if err != nil {
		t.Fatalf("replay observed conversation: %v", err)
	}
	if replayed["sessionId"] != "cloud-observed-before-complete" || replayed["idempotencyStatus"] != "replayed" || creates != 1 {
		t.Fatalf("replayed=%#v creates=%d", replayed, creates)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("original create completion: %v", err)
	}
}

func TestChatGPTCloudSessionCreateReconcilesExactProviderMessageID(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	workingDirectory := t.TempDir()
	input := agentControlParams{
		Backend: sessionBackendChatGPTCloud, Mode: "complete", Prompt: "reconcile exact request",
		Model: "gpt-test", IdempotencyKey: "cloud-exact-reconcile-01", WorkingDirectory: workingDirectory,
		modelProvided: true,
	}
	spec, err := resolveSessionVisibility("codex", input)
	if err != nil {
		t.Fatal(err)
	}
	normalizedDirectory, err := requiredAgentDirectory(workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	specValue := map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "prompt": input.Prompt,
		"model": input.Model, "workingDirectory": normalizedDirectory, "mode": "complete", "thinking": "",
	}
	for key, value := range spec.hashFields() {
		specValue[key] = value
	}
	storeKey := "codex:" + input.IdempotencyKey
	requestMessageID := "request-message-exact-01"
	manager.createStore.records[storeKey] = sessionCreateRecord{
		Key: storeKey, SpecHash: sessionCreateSpecHash(specValue), State: "in_doubt",
		Attempt:   &sessionCreateAttempt{Backend: sessionBackendChatGPTCloud, RequestMessageID: requestMessageID, StartedAt: time.Now().UTC()},
		UpdatedAt: time.Now().UTC(),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case chatgptConversationsList:
			writeChatGPTCloudTestJSON(t, w, map[string]any{"items": []any{map[string]any{"id": "cloud-reconciled-exact"}}})
		case "/backend-api/conversation/cloud-reconciled-exact":
			writeChatGPTCloudTestJSON(t, w, map[string]any{
				"conversation_id": "cloud-reconciled-exact",
				"mapping":         map[string]any{"request-node": map[string]any{"id": "request-node", "message": map[string]any{"id": requestMessageID}}},
			})
		default:
			t.Fatalf("unexpected provider request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	manager.chatgptCloud.baseURL = server.URL
	manager.chatgptCloud.http = server.Client()
	manager.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "token", nil }
	result, err := manager.Control(context.Background(), "session.create", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "mode": "complete",
		"prompt": input.Prompt, "model": input.Model, "idempotencyKey": input.IdempotencyKey, "workingDirectory": workingDirectory,
	})
	if err != nil {
		t.Fatalf("reconcile create: %v", err)
	}
	if result["sessionId"] != "cloud-reconciled-exact" || result["creationReconciled"] != true || result["idempotencyStatus"] != "replayed" {
		t.Fatalf("result=%#v", result)
	}
	if record := manager.createStore.records[storeKey]; record.State != "succeeded" || record.Result["sessionId"] != "cloud-reconciled-exact" {
		t.Fatalf("record=%#v", record)
	}
}

func TestChatGPTCloudListReturnsMarkedSidecarFallbackOnProviderTimeout(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	workingDirectory := t.TempDir()
	spec, err := resolveSessionVisibility("codex", agentControlParams{Backend: sessionBackendChatGPTCloud, Visibility: sessionVisibilityVisible})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.persistSessionVisibility(spec.recordForDirectory("codex", "cloud-sidecar-fallback", workingDirectory, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	manager.chatgptCloud.listAuthTimeout = 20 * time.Millisecond
	manager.chatgptCloud.tokenSource = func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	result, err := manager.Control(context.Background(), "session.list", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "limit": 20,
	})
	if err != nil {
		t.Fatalf("partial list: %v", err)
	}
	sessions, _ := result["sessions"].([]map[string]any)
	if len(sessions) != 1 || sessions[0]["sessionId"] != "cloud-sidecar-fallback" {
		t.Fatalf("sessions=%#v", sessions)
	}
	if result["incomplete"] != true || result["authoritative"] != false || result["reconciliationRequired"] != true || result["providerStatus"] != "timeout" {
		t.Fatalf("fallback metadata=%#v", result)
	}
}

func TestChatGPTCloudSessionCreateRequiresIdempotencyKey(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.chatgptCloud.createOverride = func(context.Context, string, string) (chatgptCloudTurnResult, error) {
		t.Fatal("provider create was called without an idempotency key")
		return chatgptCloudTurnResult{}, nil
	}
	_, err := manager.Control(context.Background(), "session.create", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"prompt": "must be protected", "workingDirectory": t.TempDir(),
	})
	if err == nil || err.Error() != "idempotencyKey is required for backend=chatgpt_cloud session.create" {
		t.Fatalf("missing key error=%v", err)
	}
}

func TestChatGPTCloudSessionArchiveRoutesToCloudConversation(t *testing.T) {
	var archived any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/backend-api/conversation/cloud-archive-1" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		archived = body["is_archived"]
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.chatgptCloud = NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
	manager.chatgptCloud.baseURL = server.URL
	manager.chatgptCloud.http = server.Client()
	result, err := manager.Control(context.Background(), "session.archive", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "sessionId": "cloud-archive-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if archived != true || result["archived"] != true {
		t.Fatalf("archive body=%v result=%#v", archived, result)
	}
}

func TestChatGPTCloudSessionRenamePatchesConversationTitle(t *testing.T) {
	var title string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/backend-api/conversation/cloud-rename-1" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		title, _ = body["title"].(string)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.chatgptCloud = NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
	manager.chatgptCloud.baseURL = server.URL
	manager.chatgptCloud.http = server.Client()
	result, err := manager.Control(context.Background(), "session.rename", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "sessionId": "cloud-rename-1", "name": "Advanced | Model | High",
	})
	if err != nil {
		t.Fatal(err)
	}
	if title != "Advanced | Model | High" || result["renamed"] != true {
		t.Fatalf("rename title=%q result=%#v", title, result)
	}
}

func TestChatGPTCloudSessionRenameRetriesNewConversation404(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.chatgptCloud = NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
	manager.chatgptCloud.baseURL = server.URL
	manager.chatgptCloud.http = server.Client()
	if _, err := manager.Control(context.Background(), "session.rename", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "sessionId": "cloud-rename-new", "name": "Advanced | Model | High",
	}); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("rename attempts=%d, want 3", attempts)
	}
}

func TestChatGPTCloudQuickChatCreateReturnsRunningAndReplays(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	var creates int
	var selectedModel string
	manager.chatgptCloud.createOverride = func(_ context.Context, _ string, model string) (chatgptCloudTurnResult, error) {
		creates++
		selectedModel = model
		return chatgptCloudTurnResult{ConversationID: "quick-cloud-conversation"}, nil
	}
	params := map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "mode": "quick_chat",
		"prompt": "answer quickly", "idempotencyKey": "cloud-quick-create-01", "workingDirectory": t.TempDir(),
	}
	first, err := manager.Control(context.Background(), "session.create", params)
	if err != nil {
		t.Fatalf("quick create: %v", err)
	}
	if selectedModel != "auto" || first["model"] != "auto" || first["createMode"] != "quick_chat" || first["phase"] != "running" || first["completionPending"] != true {
		t.Fatalf("quick create result=%#v selectedModel=%q", first, selectedModel)
	}
	replayed, err := manager.Control(context.Background(), "session.create", params)
	if err != nil {
		t.Fatalf("quick replay: %v", err)
	}
	if creates != 1 || replayed["sessionId"] != first["sessionId"] || replayed["idempotencyStatus"] != "replayed" || replayed["createMode"] != "quick_chat" {
		t.Fatalf("creates=%d first=%#v replayed=%#v", creates, first, replayed)
	}
	conflicting := cloneAgentMap(params)
	conflicting["mode"] = "complete"
	if _, err := manager.Control(context.Background(), "session.create", conflicting); err == nil {
		t.Fatal("same idempotency key allowed quick_chat to change to complete")
	}
}

func TestChatGPTCloudSessionCreateRejectsUnknownMode(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.chatgptCloud.createOverride = func(context.Context, string, string) (chatgptCloudTurnResult, error) {
		t.Fatal("provider create was called for an invalid mode")
		return chatgptCloudTurnResult{}, nil
	}
	_, err := manager.Control(context.Background(), "session.create", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "mode": "fastest",
		"prompt": "invalid mode", "idempotencyKey": "cloud-invalid-mode-01", "workingDirectory": t.TempDir(),
	})
	if err == nil || err.Error() != "backend=chatgpt_cloud session.create mode must be complete or quick_chat" {
		t.Fatalf("invalid mode error=%v", err)
	}
}

func TestChatGPTCloudSessionCreateAppliesLocalDefaultsBeforeIdempotency(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.SetChatGPTCloudCreateDefaults("preset", "quick_chat", "gpt-5-6-thinking", "max")
	var selectedModel string
	manager.chatgptCloud.createOverride = func(_ context.Context, _ string, model string) (chatgptCloudTurnResult, error) {
		selectedModel = model
		return chatgptCloudTurnResult{ConversationID: "cloud-defaults"}, nil
	}
	params := map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"prompt": "use defaults", "idempotencyKey": "cloud-defaults-01", "workingDirectory": t.TempDir(),
	}
	created, err := manager.Control(context.Background(), "session.create", params)
	if err != nil {
		t.Fatal(err)
	}
	if selectedModel != "gpt-5-6-thinking" || created["createMode"] != "quick_chat" || created["model"] != selectedModel || created["thinking"] != "max" {
		t.Fatalf("created=%#v selectedModel=%q", created, selectedModel)
	}
	manager.SetChatGPTCloudCreateDefaults("advanced", "complete", "gpt-other", "extended")
	if _, err := manager.Control(context.Background(), "session.create", params); err == nil {
		t.Fatal("same idempotency key accepted different effective defaults")
	}
}

func TestChatGPTCloudSessionCreateExplicitValuesOverrideLocalDefaults(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.SetChatGPTCloudCreateDefaults("advanced", "quick_chat", "gpt-default", "max")
	var selectedModel string
	manager.chatgptCloud.createOverride = func(_ context.Context, _ string, model string) (chatgptCloudTurnResult, error) {
		selectedModel = model
		return chatgptCloudTurnResult{ConversationID: "cloud-explicit-auto"}, nil
	}
	created, err := manager.Control(context.Background(), "session.create", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "mode": "complete", "model": "", "thinking": "",
		"prompt": "explicit auto", "idempotencyKey": "cloud-explicit-auto-01", "workingDirectory": t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if selectedModel != "" || created["createMode"] != "complete" || created["model"] != "" || created["thinking"] != "" {
		t.Fatalf("explicit values did not override defaults: created=%#v selectedModel=%q", created, selectedModel)
	}
}

func TestChatGPTCloudSessionCreateTracksThinkingSelection(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.chatgptCloud.createOverride = func(context.Context, string, string) (chatgptCloudTurnResult, error) {
		return chatgptCloudTurnResult{ConversationID: "cloud-thinking-selection"}, nil
	}
	params := map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "mode": "quick_chat",
		"prompt": "think", "model": "gpt-5-6-thinking", "thinking": "extended",
		"idempotencyKey": "cloud-thinking-create-01", "workingDirectory": t.TempDir(),
	}
	created, err := manager.Control(context.Background(), "session.create", params)
	if err != nil {
		t.Fatal(err)
	}
	if created["model"] != "gpt-5-6-thinking" || created["thinking"] != "extended" {
		t.Fatalf("created=%#v", created)
	}
	conflicting := cloneAgentMap(params)
	conflicting["thinking"] = "max"
	if _, err := manager.Control(context.Background(), "session.create", conflicting); err == nil {
		t.Fatal("same idempotency key allowed the thinking selection to change")
	}
}

func TestChatGPTCloudSessionSendInheritsInitialSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/conversation/cloud-inherit-send" {
			http.NotFound(w, r)
			return
		}
		writeChatGPTCloudTestJSON(t, w, map[string]any{
			"conversation_id":    "cloud-inherit-send",
			"current_node":       "assistant-1",
			"default_model_slug": "gpt-5-6-thinking",
			"mapping": map[string]any{
				"assistant-1": map[string]any{
					"id": "assistant-1", "parent": nil,
					"message": map[string]any{
						"id": "assistant-1", "author": map[string]any{"role": "assistant"}, "create_time": 1.0,
						"metadata": map[string]any{"default_model_slug": "gpt-5-6-thinking", "model_slug": "gpt-5-6-thinking", "thinking_effort": "max"},
					},
				},
			},
		})
	}))
	defer server.Close()

	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.SetChatGPTCloudCreateDefaults("advanced", "quick_chat", "gpt-default-must-not-affect-send", "extended")
	manager.chatgptCloud.baseURL = server.URL
	manager.chatgptCloud.http = server.Client()
	manager.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "token", nil }
	var model, thinking string
	manager.chatgptCloud.sendOverride = func(_ context.Context, _, _, _, selectedModel, selectedThinking string) (chatgptCloudTurnResult, error) {
		model, thinking = selectedModel, selectedThinking
		return chatgptCloudTurnResult{ConversationID: "cloud-inherit-send"}, nil
	}
	result, err := manager.Control(context.Background(), "session.send", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"sessionId": "cloud-inherit-send", "prompt": "continue",
	})
	if err != nil {
		t.Fatal(err)
	}
	if model != "gpt-5-6-thinking" || thinking != "max" || result["model"] != model || result["thinking"] != thinking {
		t.Fatalf("model=%q thinking=%q result=%#v", model, thinking, result)
	}
	if result["sendMode"] != "complete" {
		t.Fatalf("default send mode result=%#v", result)
	}
	quick, err := manager.Control(context.Background(), "session.send", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "mode": "quick_chat",
		"sessionId": "cloud-inherit-send", "prompt": "continue quickly",
	})
	if err != nil {
		t.Fatal(err)
	}
	if quick["sendMode"] != "quick_chat" || quick["phase"] != "running" || quick["completionPending"] != true {
		t.Fatalf("quick send result=%#v", quick)
	}
	if _, err := manager.Control(context.Background(), "session.send", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "mode": "fastest",
		"sessionId": "cloud-inherit-send", "prompt": "invalid",
	}); err == nil || err.Error() != "backend=chatgpt_cloud session.send mode must be complete or quick_chat" {
		t.Fatalf("invalid send mode error=%v", err)
	}
}

func TestChatGPTCloudSessionSendIdempotencyReconcilesAnUncertainRetry(t *testing.T) {
	const sessionID = "cloud-idempotent-send"
	const idempotencyKey = "cloud-send-idempotency-001"
	requestMessageID := chatgptCloudSendRequestMessageID(sessionID, idempotencyKey)
	var accepted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/conversation/"+sessionID {
			http.NotFound(w, r)
			return
		}
		mapping := map[string]any{
			"assistant-1": map[string]any{"id": "assistant-1", "parent": nil, "message": map[string]any{
				"id": "assistant-1", "author": map[string]any{"role": "assistant"}, "metadata": map[string]any{"model_slug": "gpt-5-6-thinking", "thinking_effort": "max"},
			}},
		}
		currentNode := "assistant-1"
		if accepted.Load() {
			mapping[requestMessageID] = map[string]any{"id": requestMessageID, "parent": "assistant-1", "message": map[string]any{
				"id": requestMessageID, "author": map[string]any{"role": "user"}, "content": map[string]any{"parts": []string{"continue idempotently"}},
			}}
			currentNode = requestMessageID
		}
		writeChatGPTCloudTestJSON(t, w, map[string]any{"conversation_id": sessionID, "current_node": currentNode, "mapping": mapping})
	}))
	defer server.Close()

	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.chatgptCloud.baseURL = server.URL
	manager.chatgptCloud.http = server.Client()
	manager.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "token", nil }
	sends := 0
	manager.chatgptCloud.sendOverride = func(context.Context, string, string, string, string, string) (chatgptCloudTurnResult, error) {
		sends++
		accepted.Store(true)
		return chatgptCloudTurnResult{ConversationID: sessionID}, context.DeadlineExceeded
	}
	params := map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "mode": "quick_chat", "sessionId": sessionID,
		"prompt": "continue idempotently", "idempotencyKey": idempotencyKey,
	}
	first, err := manager.Control(context.Background(), "session.send", params)
	if err != nil || first["idempotencyProtected"] != true || first["idempotencyStatus"] != "replayed" {
		t.Fatalf("uncertain send reconciliation=%#v err=%v", first, err)
	}
	second, err := manager.Control(context.Background(), "session.send", params)
	if err != nil || second["idempotencyStatus"] != "replayed" || sends != 1 {
		t.Fatalf("send replay=%#v sends=%d err=%v", second, sends, err)
	}
	changed := cloneAgentMap(params)
	changed["prompt"] = "different content"
	if _, err := manager.Control(context.Background(), "session.send", changed); err == nil || !strings.Contains(err.Error(), "different session.send content") {
		t.Fatalf("changed idempotent send error=%v", err)
	}
	complete := cloneAgentMap(params)
	complete["mode"] = "complete"
	if _, err := manager.Control(context.Background(), "session.send", complete); err == nil || !strings.Contains(err.Error(), "requires mode=quick_chat") {
		t.Fatalf("complete idempotent send error=%v", err)
	}
}

func TestChatGPTCloudSessionCreateRejectsUnknownThinking(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	_, err := manager.Control(context.Background(), "session.create", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"prompt": "think", "thinking": "medium", "idempotencyKey": "cloud-invalid-thinking-01", "workingDirectory": t.TempDir(),
	})
	if err == nil || err.Error() != "backend=chatgpt_cloud thinking must be standard, extended, min, max, ultra, xhigh, or zero" {
		t.Fatalf("invalid thinking error=%v", err)
	}
}

func TestChatGPTCloudSessionWatchValidatesProviderOnlyOnce(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/backend-api/conversation/cloud-watch-once":
			reads.Add(1)
			writeChatGPTCloudTestJSON(t, w, map[string]any{
				"conversation_id": "cloud-watch-once", "async_status": "running", "mapping": map[string]any{},
			})
		case "/backend-api/celsius/ws/user":
			http.NotFound(w, req)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.chatgptCloud.baseURL, manager.chatgptCloud.http = server.URL, server.Client()
	manager.chatgptCloud.realtime.baseURL, manager.chatgptCloud.realtime.http = server.URL, server.Client()
	manager.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "token", nil }
	manager.chatgptCloud.realtime.tokenSource = manager.chatgptCloud.tokenSource
	for i := 0; i < 2; i++ {
		if _, err := manager.Control(context.Background(), "session.watch", map[string]any{
			"providerId": "codex", "backend": sessionBackendChatGPTCloud, "sessionId": "cloud-watch-once",
		}); err != nil {
			t.Fatalf("watch %d: %v", i+1, err)
		}
	}
	if reads.Load() != 1 {
		t.Fatalf("provider conversation reads=%d want 1", reads.Load())
	}
}

func TestChatGPTCloudReadInvalidationFencesStaleInflightCache(t *testing.T) {
	var reads atomic.Int32
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/backend-api/conversation/cloud-cache-fence" {
			http.NotFound(w, req)
			return
		}
		call := reads.Add(1)
		status := "completed"
		if call == 1 {
			close(firstStarted)
			<-releaseFirst
			status = "running"
		}
		writeChatGPTCloudTestJSON(t, w, map[string]any{
			"conversation_id": "cloud-cache-fence", "async_status": status, "mapping": map[string]any{},
		})
	}))
	defer server.Close()

	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.chatgptCloud.baseURL, manager.chatgptCloud.http = server.URL, server.Client()
	manager.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "token", nil }
	type readOutcome struct {
		detail map[string]any
		err    error
	}
	firstDone := make(chan readOutcome, 1)
	go func() {
		detail, err := manager.readChatGPTCloud(context.Background(), "cloud-cache-fence", chatgptCloudReadCacheTTL)
		firstDone <- readOutcome{detail: detail, err: err}
	}()
	<-firstStarted
	manager.invalidateChatGPTCloudRead("cloud-cache-fence")
	fresh, freshErr := manager.readChatGPTCloud(context.Background(), "cloud-cache-fence", chatgptCloudReadCacheTTL)
	close(releaseFirst)
	stale := <-firstDone
	if freshErr != nil || chatgptCloudConversationStatus(fresh) != "completed" {
		t.Fatalf("fresh read status=%q err=%v", chatgptCloudConversationStatus(fresh), freshErr)
	}
	if stale.err != nil || chatgptCloudConversationStatus(stale.detail) != "running" {
		t.Fatalf("stale in-flight read status=%q err=%v", chatgptCloudConversationStatus(stale.detail), stale.err)
	}
	cached, err := manager.readChatGPTCloud(context.Background(), "cloud-cache-fence", chatgptCloudReadCacheTTL)
	if err != nil || chatgptCloudConversationStatus(cached) != "completed" || reads.Load() != 2 {
		t.Fatalf("cached read status=%q reads=%d err=%v", chatgptCloudConversationStatus(cached), reads.Load(), err)
	}
}

func TestChatGPTCloudCompleteCreateReplaysLegacySpecHash(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	workingDirectory := t.TempDir()
	normalizedWorkingDirectory, err := requiredAgentDirectory(workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	input := agentControlParams{Backend: sessionBackendChatGPTCloud, Prompt: "legacy complete", WorkingDirectory: workingDirectory}
	spec, err := resolveSessionVisibility("codex", input)
	if err != nil {
		t.Fatal(err)
	}
	legacySpec := map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"prompt": input.Prompt, "model": "", "workingDirectory": normalizedWorkingDirectory,
	}
	for key, value := range spec.hashFields() {
		legacySpec[key] = value
	}
	legacyHash := sessionCreateSpecHash(legacySpec)
	storeKey := "codex:cloud-legacy-mode-01"
	manager.createStore.records[storeKey] = sessionCreateRecord{
		Key: storeKey, SpecHash: legacyHash, State: "succeeded",
		Result: map[string]any{"sessionId": "legacy-cloud-conversation", "phase": "ready"}, UpdatedAt: time.Now().UTC(),
	}
	manager.chatgptCloud.createOverride = func(context.Context, string, string) (chatgptCloudTurnResult, error) {
		t.Fatal("legacy complete create was not replayed")
		return chatgptCloudTurnResult{}, nil
	}
	replayed, err := manager.Control(context.Background(), "session.create", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"prompt": input.Prompt, "idempotencyKey": "cloud-legacy-mode-01", "workingDirectory": workingDirectory,
	})
	if err != nil {
		t.Fatalf("legacy replay: %v", err)
	}
	if replayed["sessionId"] != "legacy-cloud-conversation" || replayed["createMode"] != "complete" || replayed["idempotencyStatus"] != "replayed" {
		t.Fatalf("legacy replay=%#v", replayed)
	}
	if manager.createStore.records[storeKey].SpecHash == legacyHash {
		t.Fatal("legacy complete spec hash was not migrated to include mode")
	}
}

func TestChatGPTCloudSessionGetAutoRoutesFromStoredBackend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/conversation/cloud-auto-route" {
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}
		writeChatGPTCloudTestJSON(t, w, map[string]any{
			"conversation_id": "cloud-auto-route", "title": "Cloud auto route", "default_model_slug": "gpt-5-6",
			"mapping": map[string]any{},
		})
	}))
	defer server.Close()
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.chatgptCloud.baseURL = server.URL
	manager.chatgptCloud.http = server.Client()
	manager.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "token", nil }
	workingDirectory := t.TempDir()
	spec, err := resolveSessionVisibility("codex", agentControlParams{Backend: sessionBackendChatGPTCloud, Visibility: sessionVisibilityVisible})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.persistSessionVisibility(spec.recordForDirectory("codex", "cloud-auto-route", workingDirectory, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Control(context.Background(), "session.get", map[string]any{
		"providerId": "codex", "sessionId": "cloud-auto-route",
	})
	if err != nil {
		t.Fatalf("session.get without backend: %v", err)
	}
	session, _ := result["session"].(map[string]any)
	if session["backend"] != sessionBackendChatGPTCloud || session["externalIdType"] != "chatgpt_conversation" {
		t.Fatalf("session=%#v", session)
	}
}

func TestChatGPTCloudSessionGetAppTypeRoutesAndOmitsLargeRepeatedHistory(t *testing.T) {
	var reads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/conversation/cloud-bounded" {
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}
		reads++
		mapping := map[string]any{
			"root": map[string]any{"id": "root", "parent": nil, "message": nil},
			"user-1": map[string]any{"id": "user-1", "parent": "root", "message": map[string]any{
				"id": "user-1", "author": map[string]any{"role": "user"}, "content": map[string]any{"parts": []any{strings.Repeat("old-history-", 200000)}},
			}},
			"assistant-1": map[string]any{"id": "assistant-1", "parent": "user-1", "message": map[string]any{
				"id": "assistant-1", "author": map[string]any{"role": "assistant"}, "content": map[string]any{"parts": []any{"first answer"}},
			}},
		}
		current := "assistant-1"
		if reads > 1 {
			mapping["user-2"] = map[string]any{"id": "user-2", "parent": "assistant-1", "message": map[string]any{
				"id": "user-2", "author": map[string]any{"role": "user"}, "content": map[string]any{"parts": []any{"new question"}},
			}}
			mapping["assistant-2"] = map[string]any{"id": "assistant-2", "parent": "user-2", "message": map[string]any{
				"id": "assistant-2", "author": map[string]any{"role": "assistant"}, "content": map[string]any{"parts": []any{"new answer"}},
			}}
			current = "assistant-2"
		}
		writeChatGPTCloudTestJSON(t, w, map[string]any{
			"conversation_id": "cloud-bounded", "title": "Bounded", "current_node": current, "mapping": mapping,
		})
	}))
	defer server.Close()
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.chatgptCloud.baseURL = server.URL
	manager.chatgptCloud.http = server.Client()
	manager.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "token", nil }

	first, err := manager.Control(context.Background(), "session.get", map[string]any{
		"appType": "chatgpt", "sessionId": "cloud-bounded",
	})
	if err != nil {
		t.Fatal(err)
	}
	session, _ := first["session"].(map[string]any)
	if _, leaked := session["mapping"]; leaked {
		t.Fatal("session.get leaked the full provider mapping")
	}
	messages, _ := session["messages"].([]map[string]any)
	if len(messages) != 2 || messages[0]["textTruncated"] != true {
		t.Fatalf("messages=%#v", messages)
	}
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= 128<<10 {
		t.Fatalf("bounded session.get response is too large: %d bytes", len(raw))
	}
	cursor, _ := first["nextCursor"].(string)
	if cursor != "assistant-1" {
		t.Fatalf("nextCursor=%q", cursor)
	}

	second, err := manager.Control(context.Background(), "session.get", map[string]any{
		"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": "cloud-bounded", "pageCursor": cursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondSession, _ := second["session"].(map[string]any)
	delta, _ := secondSession["messages"].([]map[string]any)
	if len(delta) != 2 || delta[0]["id"] != "user-2" || delta[1]["id"] != "assistant-2" {
		t.Fatalf("delta=%#v", delta)
	}
	if second["nextCursor"] != "assistant-2" {
		t.Fatalf("second nextCursor=%#v", second["nextCursor"])
	}

	third, err := manager.Control(context.Background(), "session.get", map[string]any{
		"appType": "chatgpt", "sessionId": "cloud-bounded", "pageCursor": "assistant-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	thirdSession, _ := third["session"].(map[string]any)
	unchanged, _ := thirdSession["messages"].([]map[string]any)
	if len(unchanged) != 0 || third["nextCursor"] != "assistant-2" {
		t.Fatalf("unchanged delta=%#v nextCursor=%#v", unchanged, third["nextCursor"])
	}
}

func TestDefaultCodexSessionListIncludesManagedCloudSessions(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.codex.requestOverride = func(_ context.Context, method string, _ map[string]any) (map[string]any, error) {
		if method != "thread/list" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		return map[string]any{"data": []any{}}, nil
	}
	workingDirectory := t.TempDir()
	spec, err := resolveSessionVisibility("codex", agentControlParams{Backend: sessionBackendChatGPTCloud, Visibility: sessionVisibilityVisible})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.persistSessionVisibility(spec.recordForDirectory("codex", "cloud-managed-list", workingDirectory, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Control(context.Background(), "session.list", map[string]any{
		"providerId": "codex", "workingDirectory": workingDirectory, "limit": 10,
	})
	if err != nil {
		t.Fatalf("default session.list: %v", err)
	}
	sessions, _ := result["sessions"].([]map[string]any)
	if len(sessions) != 1 || sessions[0]["sessionId"] != "cloud-managed-list" || sessions[0]["backend"] != sessionBackendChatGPTCloud {
		t.Fatalf("sessions=%#v", sessions)
	}
}

func TestChatGPTCloudWatchRejectsUnknownConversationBeforeSubscribing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/conversation/unknown" {
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.chatgptCloud.baseURL = server.URL
	manager.chatgptCloud.http = server.Client()
	manager.chatgptCloud.realtime.baseURL = server.URL
	manager.chatgptCloud.realtime.http = server.Client()
	manager.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "token", nil }
	if _, err := manager.Control(context.Background(), "session.watch", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"sessionId": "unknown", "waitSeconds": 0,
	}); err == nil {
		t.Fatal("unknown cloud conversation was accepted")
	}
	manager.chatgptCloud.realtime.mu.Lock()
	count := len(manager.chatgptCloud.realtime.watching)
	manager.chatgptCloud.realtime.mu.Unlock()
	if count != 0 {
		t.Fatalf("unknown conversation occupied %d realtime slots", count)
	}
}

func TestChatGPTCloudDeleteStopsRealtimeSubscription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/backend-api/conversation/id/cloud-delete" {
			t.Fatalf("unexpected delete request %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.chatgptCloud.baseURL = server.URL
	manager.chatgptCloud.http = server.Client()
	manager.chatgptCloud.realtime.baseURL = server.URL
	manager.chatgptCloud.realtime.http = server.Client()
	manager.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "token", nil }
	if _, err := manager.chatgptCloud.realtime.ensureWatching(context.Background(), "cloud-delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Control(context.Background(), "session.delete", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "sessionId": "cloud-delete",
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	manager.chatgptCloud.realtime.mu.Lock()
	_, exists := manager.chatgptCloud.realtime.watching["cloud-delete"]
	manager.chatgptCloud.realtime.mu.Unlock()
	if exists {
		t.Fatal("successful delete left realtime subscription active")
	}
}
