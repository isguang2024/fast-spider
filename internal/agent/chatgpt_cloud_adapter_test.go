package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type chatgptDeadlineTailReader struct{}

func (chatgptDeadlineTailReader) Read([]byte) (int, error) {
	return 0, context.DeadlineExceeded
}

type chatgptRoundTripFunc func(*http.Request) (*http.Response, error)

func (f chatgptRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type chatgptQuickStreamBody struct {
	mu                sync.Mutex
	step              int
	ctx               context.Context
	release           <-chan struct{}
	secondReadStarted chan struct{}
	closed            chan struct{}
	secondReadOnce    sync.Once
	closeOnce         sync.Once
}

func (b *chatgptQuickStreamBody) Read(dst []byte) (int, error) {
	b.mu.Lock()
	switch b.step {
	case 0:
		b.step++
		b.mu.Unlock()
		return copy(dst, "data: {\"conversation_id\":\"quick-conversation-1\",\"type\":\"message\"}\n\n"), nil
	case 1:
		b.step++
		ctx := b.ctx
		b.secondReadOnce.Do(func() { close(b.secondReadStarted) })
		b.mu.Unlock()
		select {
		case <-b.release:
			return copy(dst, "data: {\"type\":\"message_stream_complete\"}\n\n"), nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	default:
		b.mu.Unlock()
		return 0, io.EOF
	}
}

func (b *chatgptQuickStreamBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func TestChatGPTCloudHTTPClientDoesNotCutOffBoundedStream(t *testing.T) {
	client := newChatGPTCloudHTTPClient()
	if client.Timeout != 0 {
		t.Fatalf("ChatGPT cloud HTTP client timeout=%v, want 0 so operation contexts own deadlines", client.Timeout)
	}
}

func TestChatGPTCloudListBoundsAuthAndProviderStages(t *testing.T) {
	t.Run("auth", func(t *testing.T) {
		adapter := NewChatGPTCloudAdapter(nil, func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		})
		adapter.listAuthTimeout = 20 * time.Millisecond
		started := time.Now()
		if _, err := adapter.List(context.Background(), 20); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("auth timeout error=%v", err)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("auth stage was not bounded: %s", elapsed)
		}
	})

	t.Run("provider", func(t *testing.T) {
		adapter := NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
		adapter.listRequestTimeout = 20 * time.Millisecond
		adapter.http = &http.Client{Transport: chatgptRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		})}
		started := time.Now()
		if _, err := adapter.List(context.Background(), 20); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("provider timeout error=%v", err)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("provider stage was not bounded: %s", elapsed)
		}
	})
}

func TestChatGPTCloudCompleteCreateReportsConversationBeforeStreamEnds(t *testing.T) {
	observed := errors.New("observed conversation")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case chatgptSentinelPreparePath:
			writeChatGPTCloudTestJSON(t, w, map[string]any{
				"prepare_token": "sentinel-observed",
				"proofofwork":   map[string]any{"required": true, "seed": "seed", "difficulty": "ffffffff"},
			})
		case chatgptConversationPrepare:
			writeChatGPTCloudTestJSON(t, w, map[string]any{"status": "success", "conduit_token": "conduit-observed"})
		case chatgptConversationPath:
			fmt.Fprint(w, "data: {\"conversation_id\":\"cloud-observed-early\",\"type\":\"message\"}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	adapter := NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
	adapter.baseURL = server.URL
	adapter.http = server.Client()
	result, err := adapter.CreateWithThinkingObserved(context.Background(), "hello", "gpt-test", "", func(got chatgptCloudTurnResult) error {
		if got.ConversationID != "cloud-observed-early" {
			t.Fatalf("observed=%+v", got)
		}
		return observed
	})
	if !errors.Is(err, observed) || result.ConversationID != "cloud-observed-early" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestChatGPTCloudStreamKeepsConversationIDWhenTailTimesOut(t *testing.T) {
	stream := io.MultiReader(
		strings.NewReader("data: {\"conversation_id\":\"cloud-created-before-timeout\",\"type\":\"message\"}\n\n"),
		chatgptDeadlineTailReader{},
	)
	result, err := chatgptParseStream(stream, 10)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stream error=%v want deadline exceeded", err)
	}
	if result.ConversationID != "cloud-created-before-timeout" {
		t.Fatalf("conversation ID was lost after timeout: %#v", result)
	}
}

func TestChatGPTCloudAdapterCreateQuickSkipsPrepareAndReturnsBeforeCompletion(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	streamBody := &chatgptQuickStreamBody{
		release: release, secondReadStarted: make(chan struct{}), closed: make(chan struct{}),
	}
	var requestBody map[string]any
	prepareCalls := 0
	client := &http.Client{Transport: chatgptRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		response := func(status int, body io.ReadCloser) *http.Response {
			return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d", status), Header: make(http.Header), Body: body, Request: req}
		}
		switch req.URL.Path {
		case chatgptSentinelPreparePath:
			return response(http.StatusOK, io.NopCloser(strings.NewReader(`{"prepare_token":"sentinel-quick","proofofwork":{"required":true,"seed":"seed","difficulty":"ffffffff"}}`))), nil
		case chatgptConversationPrepare:
			prepareCalls++
			return response(http.StatusInternalServerError, io.NopCloser(strings.NewReader("unexpected prepare"))), nil
		case chatgptConversationPath:
			if req.Header.Get("x-conduit-token") != "" {
				t.Errorf("Quick chat request unexpectedly carried a conduit token")
			}
			if req.Header.Get("OpenAI-Sentinel-Proof-Token") == "" {
				t.Errorf("Quick chat request missing Sentinel headers")
			}
			if err := json.NewDecoder(req.Body).Decode(&requestBody); err != nil {
				return nil, err
			}
			streamBody.ctx = req.Context()
			return response(http.StatusOK, streamBody), nil
		default:
			return response(http.StatusNotFound, io.NopCloser(strings.NewReader("not found"))), nil
		}
	})}

	adapter := NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token-quick", nil })
	adapter.baseURL = "https://chatgpt.test"
	adapter.http = client
	type createResult struct {
		result chatgptCloudTurnResult
		err    error
	}
	created := make(chan createResult, 1)
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	defer cancelCaller()
	go func() {
		result, err := adapter.CreateQuick(callerCtx, "quick question", "")
		created <- createResult{result: result, err: err}
	}()

	var outcome createResult
	select {
	case outcome = <-created:
	case <-time.After(2 * time.Second):
		t.Fatal("CreateQuick waited for the complete SSE stream")
	}
	if outcome.err != nil || outcome.result.ConversationID != "quick-conversation-1" {
		t.Fatalf("CreateQuick result=%+v err=%v", outcome.result, outcome.err)
	}
	cancelCaller()
	if prepareCalls != 0 {
		t.Fatalf("conversation prepare calls=%d want 0", prepareCalls)
	}
	if mapString(requestBody, "model") != "auto" || mapString(requestBody, "client_prepare_state") != "none" {
		t.Fatalf("Quick chat request body=%#v", requestBody)
	}
	if parentID := mapString(requestBody, "parent_message_id"); parentID == "" || parentID == "client-created-root" {
		t.Fatalf("Quick chat parent_message_id=%q", parentID)
	}
	if _, exists := requestBody["conversation_mode"]; exists {
		t.Fatalf("Quick chat request unexpectedly set conversation_mode: %#v", requestBody)
	}
	select {
	case <-streamBody.secondReadStarted:
	case <-time.After(time.Second):
		t.Fatal("Quick chat stream was not drained in the background")
	}
	select {
	case <-streamBody.closed:
		t.Fatal("Quick chat stream closed before the provider completed it")
	default:
	}
	unblock()
	select {
	case <-streamBody.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Quick chat stream was not closed after background drain")
	}
}

func TestChatGPTCloudCreateBodiesCarryThinkingEffort(t *testing.T) {
	quick := chatgptQuickChatBodyWithThinking("quick", "gpt-5-6-thinking", "extended")
	if got := mapString(quick, "thinking_effort"); got != "extended" {
		t.Fatalf("quick thinking_effort=%q", got)
	}
	complete := chatgptNewChatBodyWithThinking("complete", "gpt-5-6-thinking", "max")
	if got := mapString(complete, "thinking_effort"); got != "max" {
		t.Fatalf("complete thinking_effort=%q", got)
	}
	if _, ok := chatgptQuickChatBody("quick", "auto")["thinking_effort"]; ok {
		t.Fatal("quick body sent an unselected thinking effort")
	}
	followUp := chatgptFollowUpBodyWithThinking("conversation-1", "assistant-1", "continue", "gpt-5-6-thinking", "max")
	if got := mapString(followUp, "model"); got != "gpt-5-6-thinking" {
		t.Fatalf("follow-up model=%q", got)
	}
	if got := mapString(followUp, "thinking_effort"); got != "max" {
		t.Fatalf("follow-up thinking_effort=%q", got)
	}
}

func TestChatGPTCloudSendInheritsInitialModelAndThinking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/conversation/cloud-follow-up" {
			http.NotFound(w, r)
			return
		}
		writeChatGPTCloudTestJSON(t, w, map[string]any{
			"conversation_id":    "cloud-follow-up",
			"current_node":       "assistant-later",
			"default_model_slug": "gpt-5-mini",
			"mapping": map[string]any{
				"root": map[string]any{"id": "root", "parent": nil, "message": nil},
				"assistant-first": map[string]any{
					"id": "assistant-first", "parent": "root",
					"message": map[string]any{
						"id": "assistant-first", "author": map[string]any{"role": "assistant"}, "create_time": 1.0,
						"metadata": map[string]any{"default_model_slug": "gpt-5-6-thinking", "model_slug": "gpt-5-6-thinking", "thinking_effort": "max"},
					},
				},
				"user-later": map[string]any{
					"id": "user-later", "parent": "assistant-first",
					"message": map[string]any{"id": "user-later", "author": map[string]any{"role": "user"}, "create_time": 2.0},
				},
				"assistant-later": map[string]any{
					"id": "assistant-later", "parent": "user-later",
					"message": map[string]any{
						"id": "assistant-later", "author": map[string]any{"role": "assistant"}, "create_time": 3.0,
						"metadata": map[string]any{"requested_model_slug": "gpt-5-6", "default_model_slug": "gpt-5-mini", "model_slug": "gpt-5-mini"},
					},
				},
			},
		})
	}))
	defer server.Close()

	adapter := NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
	adapter.baseURL = server.URL
	adapter.http = server.Client()
	var parent, model, thinking string
	adapter.sendOverride = func(_ context.Context, _, parentMessageID, _, selectedModel, selectedThinking string) (chatgptCloudTurnResult, error) {
		parent, model, thinking = parentMessageID, selectedModel, selectedThinking
		return chatgptCloudTurnResult{ConversationID: "cloud-follow-up"}, nil
	}
	result, err := adapter.SendWithThinking(context.Background(), "cloud-follow-up", "", "continue", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if parent != "assistant-later" || model != "gpt-5-6-thinking" || thinking != "max" {
		t.Fatalf("parent=%q model=%q thinking=%q", parent, model, thinking)
	}
	if result.Model != model || result.Thinking != thinking {
		t.Fatalf("result=%+v", result)
	}
}

func TestChatGPTCloudModelsReturnsCreationModesAndModelPresets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/models" {
			http.NotFound(w, r)
			return
		}
		writeChatGPTCloudTestJSON(t, w, map[string]any{
			"default_model_slug": "gpt-5-6",
			"models": []any{
				map[string]any{"slug": "gpt-5-6-instant", "title": "GPT-5.6 Instant", "max_tokens": 137000},
				map[string]any{"slug": "gpt-5-6-thinking", "title": "GPT-5.6 Sol", "max_tokens": 262144},
				map[string]any{"slug": "gpt-5-6-thinking", "title": "duplicate provider row", "max_tokens": 262144},
			},
			"versions": []any{map[string]any{
				"id": "5.6",
				"intelligence_presets": []any{
					map[string]any{"lane": "instant", "model_slug": "gpt-5-6-instant", "selected_display_title": "Instant"},
					map[string]any{"lane": "thinking", "model_slug": "gpt-5-6-thinking", "selected_display_title": "High", "thinking_effort": "extended"},
				},
			}},
		})
	}))
	defer server.Close()
	adapter := NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
	adapter.baseURL = server.URL
	adapter.http = server.Client()
	catalog, err := adapter.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if catalog["defaultModel"] != "gpt-5-6" {
		t.Fatalf("catalog=%#v", catalog)
	}
	models, _ := catalog["models"].([]map[string]any)
	if len(models) != 2 || models[1]["id"] != "gpt-5-6-thinking" || models[1]["title"] != "GPT-5.6 Thinking" {
		t.Fatalf("models=%#v", models)
	}
	modes, _ := catalog["creationModes"].([]map[string]any)
	if len(modes) != 2 || modes[0]["id"] != "quick_chat" || modes[1]["id"] != "complete" {
		t.Fatalf("creationModes=%#v", modes)
	}
	thinkingOptions, _ := catalog["thinkingOptions"].([]ChatGPTThinkingOption)
	if len(thinkingOptions) != 2 || thinkingOptions[0].ID != "auto" || thinkingOptions[1].Value != "extended" || thinkingOptions[1].Source != "chatgpt_cloud" {
		t.Fatalf("thinkingOptions=%#v", thinkingOptions)
	}
	presets, _ := catalog["modelPresets"].([]map[string]any)
	if len(presets) != 2 || presets[1]["model"] != "gpt-5-6-thinking" || presets[1]["thinking"] != "extended" {
		t.Fatalf("modelPresets=%#v", presets)
	}
}

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
