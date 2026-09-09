package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatGPTCloudMetadataOnlyObservesProviderWithoutTranscript(t *testing.T) {
	for _, status := range []string{"running", "completed", "failed", "canceled", "unknown"} {
		t.Run(status, func(t *testing.T) {
			reads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/backend-api/conversation/cloud-metadata" || r.Method != http.MethodGet {
					t.Errorf("unexpected provider call: %s %s", r.Method, r.URL.Path)
				}
				reads++
				writeChatGPTCloudTestJSON(t, w, map[string]any{
					"conversation_id": "cloud-metadata", "async_status": status, "title": "PRIVATE-TITLE", "current_node": "a1",
					"mapping": map[string]any{"a1": map[string]any{"id": "a1", "message": map[string]any{"id": "a1", "status": status, "end_turn": status == "completed", "author": map[string]any{"role": "assistant"}, "content": map[string]any{"parts": []any{"PRIVATE-TRANSCRIPT"}}}}},
				})
			}))
			defer server.Close()
			m := New(t.TempDir(), nil)
			defer m.Close(context.Background())
			m.chatgptCloud.baseURL, m.chatgptCloud.http = server.URL, server.Client()
			m.chatgptCloud.tokenSource = func(context.Context) (string, error) { return "token", nil }
			result, err := m.Control(context.Background(), "session.get", map[string]any{"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": "cloud-metadata", "metadataOnly": true})
			if err != nil {
				t.Fatal(err)
			}
			if reads != 1 || result["providerStatus"] != status || result["authoritative"] != (status != "unknown") || result["observedAt"] == nil {
				t.Fatalf("reads=%d result=%#v", reads, result)
			}
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "mapping") || strings.Contains(string(raw), "messages") {
				t.Fatalf("metadata leaked transcript fields: %s", raw)
			}
		})
	}
}
