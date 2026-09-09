package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCloudStreamHintIsNotTerminalProofAndCannotCrossGeneration(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprint(stale), func(t *testing.T) {
			m := New(t.TempDir(), nil)
			defer m.Close(context.Background())
			var reads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reads.Add(1)
				// Finished intermediate assistant node, but the actual turn is running.
				fmt.Fprint(w, `{"conversation_id":"hint-chat","async_status":"running","current_node":"tool","mapping":{"tool":{"message":{"id":"tool","author":{"role":"assistant"},"status":"finished_successfully","end_turn":false,"recipient":"tool"}}}}`)
			}))
			defer server.Close()
			m.chatgptCloud.baseURL, m.chatgptCloud.http = server.URL, server.Client()
			m.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "token", nil }
			generation := int64(1)
			if stale {
				generation = 2
			}
			r := testCallbackRegistration("hint-chat", "target", "task", generation)
			if _, _, err := m.callbackStore.register(r); err != nil {
				t.Fatal(err)
			}
			m.startCloudCallbackConfirmation(r.SourceSessionID, 1)
			// Waiting on the finite confirmation is test synchronization, not runtime polling.
			m.callbackDispatcher.wg.Wait()
			pending, err := m.callbackStore.pendingSnapshot(r.SourceSessionID, r.TargetSessionID)
			if err != nil || len(pending) != 0 {
				t.Fatalf("hint fabricated completion: %#v %v", pending, err)
			}
			want := int32(2)
			if stale {
				want = 0
			}
			if reads.Load() != want {
				t.Fatalf("confirmation reads=%d want=%d", reads.Load(), want)
			}
		})
	}
}

// No websocket server, timer recovery or explicit callback.recover is involved.
// Exercise actual HTTP/SSE send -> authoritative finality -> durable queue.
func TestRegisteredCloudSendQueuesCallbackWithoutWebsocket(t *testing.T) {
	for _, mode := range []string{"complete", "quick_chat", "continuation"} {
		t.Run(mode, func(t *testing.T) {
			var finished atomic.Bool
			var sends atomic.Int32
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			dataDir := t.TempDir()
			report := filepath.Join(dataDir, "result.md")
			m := New(dataDir, nil)
			defer m.Close(context.Background())
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case chatgptSentinelPreparePath:
					fmt.Fprint(w, `{"prepare_token":"test","proofofwork":{"required":true,"seed":"seed","difficulty":"ffffffff"}}`)
				case chatgptConversationPrepare:
					fmt.Fprint(w, `{"status":"success","conduit_token":"test"}`)
				case "/backend-api/conversation/stream-chat":
					status, id := "unknown", "old"
					if finished.Load() {
						status, id = "completed", "final"
					}
					json.NewEncoder(w).Encode(map[string]any{
						"conversation_id": "stream-chat", "async_status": status, "current_node": id,
						"mapping": map[string]any{id: map[string]any{"message": map[string]any{
							"id": id, "author": map[string]any{"role": "assistant"}, "status": "finished_successfully",
							"end_turn": finished.Load(), "content": map[string]any{"parts": []any{"report saved"}},
						}}},
					})
				case chatgptConversationPath:
					sends.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"conversation_id\":\"stream-chat\"}\n\n")
					w.(http.Flusher).Flush()
					select {
					case <-release:
					case <-req.Context().Done():
						return
					}
					if err := os.WriteFile(report, []byte("verified report"), 0600); err != nil {
						t.Error(err)
						return
					}
					finished.Store(true)
					fmt.Fprint(w, "data: {\"type\":\"message_stream_complete\"}\n\n")
				default:
					http.NotFound(w, req)
				}
			}))
			defer server.Close()
			defer unblock()
			m.chatgptCloud.baseURL, m.chatgptCloud.http = server.URL, server.Client()
			m.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "test-token", nil }
			route := testCallbackRegistration("stream-chat", "target", "task", 1)
			route.CallbackType, route.DeliverablePath = "local_file", report
			if _, _, err := m.callbackStore.register(route); err != nil {
				t.Fatal(err)
			}
			input := agentControlParams{SessionID: route.SourceSessionID, Prompt: "Continue the current task.", Mode: mode,
				CallbackTargetSessionID: route.TargetSessionID, CallbackMissionID: route.MissionID,
				CallbackTaskID: route.TaskID, CallbackGeneration: route.Generation}
			done := make(chan error, 1)
			var output map[string]any
			go func() {
				var err error
				if mode == "continuation" {
					input.IdempotencyKey = "continue-stream-regression"
					output, err = m.sessionCallbackContinue(context.Background(), input)
				} else {
					output, err = m.chatgptCloudSend(context.Background(), input)
				}
				done <- err
			}()
			if mode != "complete" {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("send blocked on completion")
				}
				if pending, err := m.callbackStore.pendingSnapshot(route.SourceSessionID, route.TargetSessionID); err != nil || len(pending) != 0 {
					t.Fatalf("early callback: %#v %v", pending, err)
				}
				if mode == "continuation" && (output["recoveryAction"] != "wait_callback" || output["executionRef"] == nil || output["continueSent"] != true) {
					t.Fatalf("bad continuation contract: %#v", output)
				}
			}
			unblock()
			if mode == "complete" {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				pending, err := m.callbackStore.pendingSnapshot(route.SourceSessionID, route.TargetSessionID)
				if err != nil {
					t.Fatal(err)
				}
				if len(pending) == 1 {
					if pending[0].CallbackOutcome != "completed" || pending[0].DeliverablePath != report {
						t.Fatalf("bad callback: %#v", pending[0])
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("stream finished with readable report but callback remained armed")
				}
				time.Sleep(10 * time.Millisecond)
			}
			// Repeated completion hints must reuse the same durable event.
			if _, err := m.recoverCompletedCloudCallbackState(context.Background(), route.SourceSessionID, route.Generation, true); err != nil {
				t.Fatal(err)
			}
			reloaded := newSessionCallbackStore(dataDir)
			pending, err := reloaded.pendingSnapshot(route.SourceSessionID, route.TargetSessionID)
			if err != nil || len(pending) != 1 || sends.Load() != 1 {
				t.Fatalf("not exactly one durable result: %#v err=%v sends=%d", pending, err, sends.Load())
			}
			_, claimed, err := reloaded.claim(route.TargetSessionID, "stream-claim", 1, time.Now())
			if err != nil || len(claimed) != 1 || claimed[0].DeliverableStatus != "ready" || claimed[0].ResultSHA256 == "" {
				t.Fatalf("durable report not readable at claim: %#v %v", claimed, err)
			}
		})
	}
}

func TestCloudContinuationRejectsInvalidPromptAndKeyBeforeProvider(t *testing.T) {
	m := New(t.TempDir(), nil)
	defer m.Close(context.Background())
	var reads atomic.Int32
	m.chatgptCloud.tokenSource = func(context.Context) (string, error) {
		reads.Add(1)
		return "", fmt.Errorf("unexpected provider access")
	}
	r := testCallbackRegistration("invalid-chat", "target", "task", 1)
	if _, _, err := m.callbackStore.register(r); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ prompt, key, want string }{
		{strings.Repeat("中", 67), "continue-valid-key", "200 UTF-8 bytes"},
		{"continue\nnow", "continue-valid-key", "single line"},
		{"continue", "", "idempotencyKey"},
	} {
		_, err := m.sessionCallbackContinue(context.Background(), agentControlParams{
			SessionID: r.SourceSessionID, CallbackTargetSessionID: r.TargetSessionID, CallbackMissionID: r.MissionID, CallbackTaskID: r.TaskID, CallbackGeneration: r.Generation,
			Prompt: tc.prompt, IdempotencyKey: tc.key,
		})
		typed, ok := err.(*sessionCallbackError)
		if !ok || typed.code != "INVALID_REQUEST" || !strings.Contains(typed.message, tc.want) {
			t.Fatalf("imprecise contract error: %v", err)
		}
	}
	if reads.Load() != 0 {
		t.Fatalf("invalid parameters accessed provider %d times", reads.Load())
	}
}

func TestCloudActivityFingerprintIsProgressNotRunningLabel(t *testing.T) {
	msg := map[string]any{"id": "node", "author": map[string]any{"role": "assistant"}, "status": "in_progress", "content": map[string]any{"parts": []any{"private text"}}}
	detail := map[string]any{"current_node": "node", "mapping": map[string]any{"node": map[string]any{"message": msg}}}
	first := chatgptCloudActivity(detail)
	raw, _ := json.Marshal(first)
	if strings.Contains(string(raw), "private text") || first["toolExecutionKnown"] != false || first["fingerprint"] == nil {
		t.Fatalf("unsafe activity: %s", raw)
	}
	detail["title"], detail["update_time"], msg["update_time"] = "renamed", 999, 999
	if chatgptCloudActivity(detail)["fingerprint"] != first["fingerprint"] {
		t.Fatal("view metadata pretended progress")
	}
	msg["content"] = map[string]any{"parts": []any{"private text advanced"}}
	if chatgptCloudActivity(detail)["fingerprint"] == first["fingerprint"] {
		t.Fatal("same-node token ignored real progress")
	}
	if chatgptCloudActivity(map[string]any{})["available"] != false {
		t.Fatal("missing activity fabricated")
	}
}
