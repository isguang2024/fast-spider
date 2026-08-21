package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
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
		"prompt": "hello cloud", "model": "gpt-test", "idempotencyKey": "cloud-idempotency-01",
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
		"prompt": "may have been created", "idempotencyKey": "cloud-in-doubt-01",
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
