package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCloudCallbackPrepareKeepsBaselineAndHistoryOnNode(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		writeChatGPTCloudTestJSON(t, w, map[string]any{
			"conversation_id": "cloud-local-baseline", "current_node": "private-assistant-id",
			"mapping": map[string]any{"private-assistant-id": map[string]any{"message": map[string]any{
				"id": "private-assistant-id", "author": map[string]any{"role": "assistant"},
				"status": "finished_successfully", "end_turn": true,
				"content": map[string]any{"parts": []any{"private previous task transcript"}},
			}}},
		})
	}))
	defer server.Close()
	m := New(t.TempDir(), nil)
	defer m.Close(context.Background())
	m.chatgptCloud.baseURL, m.chatgptCloud.http = server.URL, server.Client()
	m.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "test-token", nil }
	prepared, err := m.Control(context.Background(), "session.callback.prepare", map[string]any{"providerId": "codex", "sessionId": "cloud-local-baseline"})
	if err != nil || prepared["prepared"] != true {
		t.Fatalf("prepare=%#v err=%v", prepared, err)
	}
	raw, _ := json.Marshal(prepared)
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "mapping") {
		t.Fatalf("prepare leaked session data: %s", raw)
	}
	_, err = m.Control(context.Background(), "session.callback.register", map[string]any{
		"providerId": "codex", "sessionId": "cloud-local-baseline", "mode": "reuse",
		"callbackTargetSessionId": "local-target", "callbackMissionId": "mission", "callbackTaskId": "task",
		"callbackGeneration": 1, "callbackType": "status", "callbackArmRequired": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	r, ok, err := m.callbackStore.registrationFor("cloud-local-baseline")
	if err != nil || !ok || r.BaselineIdentity != "private-assistant-id" || r.Armed {
		t.Fatalf("route=%#v err=%v", r, err)
	}
	if reads.Load() != 1 {
		t.Fatalf("prepare/register repeated provider reads: %d", reads.Load())
	}
	if _, _, err := m.callbackStore.arm(r.SourceSessionID, r.Generation, r); err != nil {
		t.Fatal(err)
	}
	settled, err := m.recoverCompletedCloudCallbackState(context.Background(), r.SourceSessionID, r.Generation, true)
	if err != nil || settled {
		t.Fatalf("old baseline incorrectly settled a new terminal hint: settled=%v err=%v", settled, err)
	}
}

func TestCloudCallbackPrepareRejectsBusyAndLocalSessions(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		writeChatGPTCloudTestJSON(t, w, map[string]any{"conversation_id": "busy-chat", "current_node": "user-new", "async_status": "running"})
	}))
	defer server.Close()
	m := New(t.TempDir(), nil)
	defer m.Close(context.Background())
	m.chatgptCloud.baseURL, m.chatgptCloud.http = server.URL, server.Client()
	m.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "test-token", nil }
	_, err := m.prepareCloudCallbackBaseline(context.Background(), "busy-chat")
	var callbackErr *sessionCallbackError
	if !errors.As(err, &callbackErr) || callbackErr.code != "AGENT_SESSION_BUSY" {
		t.Fatalf("busy error=%v", err)
	}
	spec, err := resolveSessionVisibility("codex", agentControlParams{Backend: "codex_local", Visibility: "internal"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.persistSessionVisibility(spec.record("codex", "local-chat", time.Now())); err != nil {
		t.Fatal(err)
	}
	_, err = m.prepareCloudCallbackBaseline(context.Background(), "local-chat")
	if !errors.As(err, &callbackErr) || callbackErr.code != "INVALID_REQUEST" || reads.Load() != 1 {
		t.Fatalf("local error=%v reads=%d", err, reads.Load())
	}
}

func TestCloudCallbackRecoveryAndContinueRequireExactNodeRoute(t *testing.T) {
	m := New(t.TempDir(), nil)
	defer m.Close(context.Background())
	var reads atomic.Int32
	m.chatgptCloud.tokenSource = func(context.Context) (string, error) { reads.Add(1); return "", errors.New("must not access provider") }
	r := testCallbackRegistration("route-chat", "route-target", "route-task", 3)
	if _, _, err := m.callbackStore.register(r); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"session.callback.recover", "session.callback.continue"} {
		for _, mismatch := range []string{"callbackMissionId", "callbackTaskId", "callbackTargetSessionId", "callbackGeneration"} {
			params := map[string]any{"providerId": "codex", "sessionId": r.SourceSessionID, "callbackTargetSessionId": r.TargetSessionID, "callbackMissionId": r.MissionID, "callbackTaskId": r.TaskID, "callbackGeneration": r.Generation}
			if mismatch == "callbackGeneration" {
				params[mismatch] = int64(2)
			} else {
				params[mismatch] = "other-owner"
			}
			if _, err := m.Control(context.Background(), action, params); err == nil {
				t.Fatalf("%s accepted mismatched %s", action, mismatch)
			}
		}
	}
	if reads.Load() != 0 {
		t.Fatalf("wrong route accessed provider %d times", reads.Load())
	}
}

func TestCloudCallbackResultValidationUsesOnlyBoundLocalPath(t *testing.T) {
	m := New(t.TempDir(), nil)
	defer m.Close(context.Background())
	path := filepath.Join(t.TempDir(), "result.md")
	if err := os.WriteFile(path, []byte("private local report"), 0600); err != nil {
		t.Fatal(err)
	}
	r := testCallbackRegistration("result-chat", "local-reader", "file-task", 1)
	r.CallbackType, r.DeliverablePath = "local_file", path
	if _, _, err := m.callbackStore.register(r); err != nil {
		t.Fatal(err)
	}
	params := map[string]any{"providerId": "codex", "mode": "result", "sessionId": r.SourceSessionID, "callbackTargetSessionId": r.TargetSessionID, "callbackMissionId": r.MissionID, "callbackTaskId": r.TaskID, "callbackGeneration": r.Generation, "callbackDeliverablePath": "untrusted-path-must-be-ignored"}
	result, err := m.Control(context.Background(), "session.callback.prepare", params)
	if err != nil || result["prepared"] != true || result["size"] != int64(len("private local report")) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), path) {
		t.Fatalf("result metadata leaked content: %s", raw)
	}
	params["callbackGeneration"] = 2
	if _, err := m.Control(context.Background(), "session.callback.prepare", params); err == nil {
		t.Fatal("stale generation could inspect local result")
	}
}
