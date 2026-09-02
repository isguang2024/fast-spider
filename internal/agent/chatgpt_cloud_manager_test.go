package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	manager.SetChatGPTCloudCreateDefaults("quick_chat", "gpt-5-6-thinking", "max")
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
	manager.SetChatGPTCloudCreateDefaults("complete", "gpt-other", "extended")
	if _, err := manager.Control(context.Background(), "session.create", params); err == nil {
		t.Fatal("same idempotency key accepted different effective defaults")
	}
}

func TestChatGPTCloudSessionCreateExplicitValuesOverrideLocalDefaults(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	manager.SetChatGPTCloudCreateDefaults("quick_chat", "gpt-default", "max")
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
	manager.SetChatGPTCloudCreateDefaults("quick_chat", "gpt-default-must-not-affect-send", "extended")
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
