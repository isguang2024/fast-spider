package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatGPTCloudAdapterSteerPostsToSteerTurn(t *testing.T) {
	var steerBody map[string]any
	var prepareBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/conversation/conv-1":
			writeChatGPTCloudTestJSON(t, w, map[string]any{
				"conversation_id":    "conv-1",
				"default_model_slug": "gpt-5-6",
				"current_node":       "assistant-1",
				"mapping": map[string]any{
					"root": map[string]any{"id": "root", "message": nil, "parent": nil},
					"user-1": map[string]any{
						"id": "user-1", "parent": "root",
						"message": map[string]any{
							"id": "user-1", "author": map[string]any{"role": "user"},
							"create_time": 1.0,
							"content":     map[string]any{"parts": []string{"initial"}},
						},
					},
					"assistant-1": map[string]any{
						"id": "assistant-1", "parent": "user-1",
						"message": map[string]any{
							"id": "assistant-1", "author": map[string]any{"role": "assistant"},
							"create_time": 2.0,
							"content":     map[string]any{"parts": []string{"done"}},
							"metadata":    map[string]any{"turn_exchange_id": "exchange-1", "chime_version": 7},
						},
					},
				},
			})
		case chatgptSentinelPreparePath:
			writeChatGPTCloudTestJSON(t, w, map[string]any{
				"prepare_token": "sentinel-prepare",
				"proofofwork":   map[string]any{"required": true, "seed": "seed", "difficulty": "ffffffff"},
			})
		case chatgptConversationPrepare:
			if err := json.NewDecoder(r.Body).Decode(&prepareBody); err != nil {
				t.Errorf("decode conversation prepare body: %v", err)
			}
			writeChatGPTCloudTestJSON(t, w, map[string]any{"status": "success", "conduit_token": "conduit-1"})
		case chatgptSteerTurnPath:
			if r.Header.Get("OpenAI-Sentinel-Proof-Token") == "" || r.Header.Get("OpenAI-Sentinel-Chat-Requirements-Prepare-Token") == "" || r.Header.Get("OpenAI-Sentinel-Turnstile-Token") == "" {
				t.Errorf("steer request missing Sentinel headers")
			}
			if got := r.Header.Get("x-conduit-token"); got != "conduit-1" {
				t.Errorf("x-conduit-token=%q want conduit-1", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&steerBody); err != nil {
				t.Errorf("decode steer body: %v", err)
			}
			fmt.Fprint(w, "data: {\"conversation_id\":\"conv-1\",\"type\":\"message\",\"message\":{\"id\":\"steer-assistant\",\"author\":{\"role\":\"assistant\"},\"content\":{\"parts\":[\"steered\"]}}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"message_stream_complete\"}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter := NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token-1", nil })
	adapter.baseURL = server.URL
	adapter.http = server.Client()
	result, err := adapter.Steer(context.Background(), "conv-1", "task-1", "please steer")
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if result.ConversationID != "conv-1" || result.AsyncTaskID != "task-1" {
		t.Fatalf("result=%+v", result)
	}
	if got := mapString(steerBody, "async_task_id"); got != "task-1" {
		t.Fatalf("async_task_id=%q want task-1", got)
	}
	if got := mapString(steerBody, "conversation_id"); got != "conv-1" {
		t.Fatalf("conversation_id=%q want conv-1", got)
	}
	if got := mapString(steerBody, "parent_message_id"); got != "assistant-1" {
		t.Fatalf("parent_message_id=%q want assistant-1", got)
	}
	messages, _ := steerBody["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages=%#v", messages)
	}
	message, _ := messages[0].(map[string]any)
	content, _ := message["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	if len(parts) != 1 || parts[0] != "please steer" {
		t.Fatalf("steer prompt=%#v", parts)
	}
	metadata, _ := message["metadata"].(map[string]any)
	if metadata["turn_exchange_id"] != "exchange-1" || metadata["chime_version"] != float64(7) {
		t.Fatalf("steer metadata=%#v", metadata)
	}
	if mapString(prepareBody, "async_task_id") != "task-1" {
		t.Fatalf("prepare body did not carry async_task_id: %#v", prepareBody)
	}
}

func TestChatGPTCloudAdapterSteerResolvesTaskFromConversationDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/conversation/conv-2":
			writeChatGPTCloudTestJSON(t, w, map[string]any{
				"conversation_id": "conv-2", "current_node": "assistant-2",
				"async_status": map[string]any{"async_task_id": "task-from-detail"},
				"mapping": map[string]any{
					"assistant-2": map[string]any{
						"id": "assistant-2", "message": map[string]any{
							"id": "assistant-2", "author": map[string]any{"role": "assistant"}, "create_time": 2.0,
						},
					},
				},
			})
		case chatgptSentinelPreparePath:
			writeChatGPTCloudTestJSON(t, w, map[string]any{
				"prepare_token": "sentinel-prepare",
				"proofofwork":   map[string]any{"required": true, "seed": "seed", "difficulty": "ffffffff"},
			})
		case chatgptConversationPrepare:
			writeChatGPTCloudTestJSON(t, w, map[string]any{"status": "success", "conduit_token": "conduit-2"})
		case chatgptSteerTurnPath:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode steer body: %v", err)
			}
			if mapString(body, "async_task_id") != "task-from-detail" {
				t.Errorf("resolved async_task_id=%#v", body["async_task_id"])
			}
			fmt.Fprint(w, "data: {\"conversation_id\":\"conv-2\",\"type\":\"message_stream_complete\"}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	adapter := NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token-2", nil })
	adapter.baseURL = server.URL
	adapter.http = server.Client()
	if _, err := adapter.Steer(context.Background(), "conv-2", "", "steer from detail"); err != nil {
		t.Fatalf("Steer with detail task: %v", err)
	}
}

func TestChatGPTCloudAdapterSteerRejectsConversationWithoutActiveTask(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/conversation/idle" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		writeChatGPTCloudTestJSON(t, w, map[string]any{
			"conversation_id": "idle", "current_node": "assistant-idle", "async_status": nil,
			"mapping": map[string]any{"assistant-idle": map[string]any{
				"id": "assistant-idle", "message": map[string]any{
					"id": "assistant-idle", "author": map[string]any{"role": "assistant"},
				},
			}},
		})
	}))
	defer server.Close()
	adapter := NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token-idle", nil })
	adapter.baseURL = server.URL
	adapter.http = server.Client()
	_, err := adapter.Steer(context.Background(), "idle", "", "cannot steer")
	if err == nil || !strings.Contains(err.Error(), "no active steerable turn") {
		t.Fatalf("Steer error=%v, want no active steerable turn", err)
	}
}

func TestChatGPTCloudManagerSteerDispatchesAsyncTask(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/conversation/conv-manager":
			writeChatGPTCloudTestJSON(t, w, map[string]any{
				"conversation_id": "conv-manager", "current_node": "assistant-manager",
				"mapping": map[string]any{"assistant-manager": map[string]any{
					"id": "assistant-manager", "message": map[string]any{
						"id": "assistant-manager", "author": map[string]any{"role": "assistant"},
					},
				}},
			})
		case chatgptSentinelPreparePath:
			writeChatGPTCloudTestJSON(t, w, map[string]any{
				"prepare_token": "sentinel-manager",
				"proofofwork":   map[string]any{"required": true, "seed": "seed", "difficulty": "ffffffff"},
			})
		case chatgptConversationPrepare:
			writeChatGPTCloudTestJSON(t, w, map[string]any{"status": "success", "conduit_token": "conduit-manager"})
		case chatgptSteerTurnPath:
			fmt.Fprint(w, "data: {\"conversation_id\":\"conv-manager\",\"type\":\"message_stream_complete\"}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	adapter := NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token-manager", nil })
	adapter.baseURL = server.URL
	adapter.http = server.Client()
	manager := &AgentManager{chatgptCloud: adapter, registry: staticProviderRegistry()}
	result, err := manager.Control(context.Background(), "session.steer", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"sessionId": "conv-manager", "asyncTaskId": "task-manager", "prompt": "manager steer",
	})
	if err != nil {
		t.Fatalf("manager session.steer: %v", err)
	}
	if result["steered"] != true || result["asyncTaskId"] != "task-manager" {
		t.Fatalf("manager steer result=%#v", result)
	}
}

func writeChatGPTCloudTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("write JSON: %v", err)
	}
}
