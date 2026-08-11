package nodeui

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const aiSensitiveMarker = "SECRET_TOKEN_041"

type fakeAIController struct {
	mu      sync.Mutex
	actions []string
}

func (f *fakeAIController) Control(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	f.mu.Lock()
	f.actions = append(f.actions, action)
	f.mu.Unlock()
	providerID, _ := params["providerId"].(string)
	switch action {
	case "providers.list":
		return map[string]any{"providers": []any{
			map[string]any{
				"providerId": "codex", "available": true, "version": "codex-cli 1.2.3", "executionHealth": "unknown_until_turn",
				"supportedActions": []string{"models.list", "provider.capabilities", "session.create"},
				"token":            aiSensitiveMarker, "email": "private@example.test", "raw": map[string]any{"prompt": aiSensitiveMarker},
			},
			map[string]any{
				"providerId": "claude_code", "available": true, "runtimeAvailable": true, "version": "2.1.207", "executionHealth": "unknown_until_turn",
				"supportedActions":  []string{"models.list", "provider.capabilities", "session.send"},
				"authConfiguration": map[string]any{"configured": true, "email": "private@example.test", "orgId": aiSensitiveMarker, "apiKey": aiSensitiveMarker},
			},
		}}, nil
	case "models.list":
		if providerID == "claude_code" {
			return map[string]any{"models": []map[string]any{{"id": "sonnet", "displayName": "Claude Sonnet", "rawSettings": aiSensitiveMarker}}}, nil
		}
		return map[string]any{"models": []map[string]any{{"id": "gpt-test", "displayName": "GPT Test", "endpoint": "https://user:pass@private.example/v1?token=" + aiSensitiveMarker}}}, nil
	case "provider.capabilities":
		return map[string]any{"effectiveCapabilities": map[string]any{
			"toolCalls": map[string]any{"state": "supported", "reason": "supported by the local harness"},
			"webSearch": map[string]any{"state": "unknown", "reason": "depends on the selected route"},
			"rawMeta":   map[string]any{"state": "supported", "reason": aiSensitiveMarker},
		}}, nil
	case "routing.status":
		fingerprint := "sha256:" + strings.Repeat("a", 64)
		return map[string]any{"routes": []map[string]any{
			{
				"appType": "codex", "available": true, "schemaFingerprint": fingerprint, "routingMode": "cc_switch", "selectionConsistent": true,
				"proxy": map[string]any{"proxyEnabled": true, "takeoverEnabled": true, "liveTakeoverActive": false, "listenAddress": `C:\Users\Secret`, "token": aiSensitiveMarker},
				"currentProvider": map[string]any{
					"providerId": "provider-safe", "name": "Safe Provider", "settings_config": aiSensitiveMarker, "meta": aiSensitiveMarker,
					"endpoint": "https://user:pass@private.example/v1?token=" + aiSensitiveMarker,
					"models":   []map[string]any{{"model": "mapped-gpt", "clientRole": "main", "raw": aiSensitiveMarker}},
					"health":   map[string]any{"healthy": true, "consecutiveFailures": 0, "lastError": aiSensitiveMarker},
				},
				"effectiveCapabilities": map[string]any{"thinking": map[string]any{"state": "supported", "reason": "route supports reasoning"}},
			},
			{"appType": "claude", "available": false, "reason": "unsupported_schema", "schemaFingerprint": fingerprint, "error": aiSensitiveMarker},
		}}, nil
	default:
		return map[string]any{}, nil
	}
}

func (f *fakeAIController) Close(context.Context) error { return nil }

func (f *fakeAIController) called(action string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, item := range f.actions {
		if item == action {
			return true
		}
	}
	return false
}

func TestAIRoutingLoopbackAllowlistAndReadOnlyDiscovery(t *testing.T) {
	app, err := New(Options{DataDir: t.TempDir(), Version: "ui-test", MachineName: "Test Node", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAIController{}
	app.agentController = fake

	index := httptest.NewRecorder()
	app.handler().ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/", nil))
	if index.Code != http.StatusOK {
		t.Fatalf("index status=%d", index.Code)
	}
	if len(fake.actions) != 0 {
		t.Fatalf("page load triggered Agent actions: %#v", fake.actions)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/ai-routing", nil)
	req.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
	response := httptest.NewRecorder()
	app.handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("AI routing status=%d body=%s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{aiSensitiveMarker, "private@example.test", "private.example", `C:\Users\Secret`, "settings_config", "rawMeta", "apiKey", "orgId"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("allowlist response leaked %q: %s", forbidden, response.Body.String())
		}
	}
	var view aiRoutingResponse
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Codex.Available || view.Codex.Version != "codex-cli 1.2.3" || len(view.Codex.Models) != 1 || len(view.Codex.SupportedActions) == 0 || len(view.Codex.EffectiveCapabilities) == 0 || view.Codex.ExecutionHealth == "" {
		t.Fatalf("Codex DTO incomplete: %+v", view.Codex)
	}
	if !view.ClaudeCode.Available || view.ClaudeCode.AuthConfigured == nil || !*view.ClaudeCode.AuthConfigured || view.ClaudeCode.AuthStatus != "configured" || len(view.ClaudeCode.Models) != 1 || len(view.ClaudeCode.SupportedActions) == 0 {
		t.Fatalf("Claude DTO incomplete: %+v", view.ClaudeCode)
	}
	if !view.CCSwitch.DBDetected || view.CCSwitch.SchemaSupported || view.CCSwitch.Reason != "unsupported_schema" || view.CCSwitch.SchemaFingerprint == "" || !view.CCSwitch.ProxyEnabled || !view.CCSwitch.Takeover || view.CCSwitch.LiveTakeover || view.CCSwitch.CurrentProvider == "" || len(view.CCSwitch.ModelMapping) != 1 || view.CCSwitch.SelectionConsistent == nil || !*view.CCSwitch.SelectionConsistent || len(view.CCSwitch.ProviderHealth) != 1 || len(view.CCSwitch.EffectiveCapabilities) == 0 {
		t.Fatalf("CC Switch DTO incomplete: %+v", view.CCSwitch)
	}
	if view.HealthTest.Mode != "manual_session_required" || !strings.Contains(view.HealthTest.Message, "手动从会话执行") {
		t.Fatalf("unexpected health-test policy: %+v", view.HealthTest)
	}
	for _, forbiddenAction := range []string{"session.create", "session.send"} {
		if fake.called(forbiddenAction) {
			t.Fatalf("read-only page triggered %s", forbiddenAction)
		}
	}
}

func TestAIRoutingNavigationAndNoAutomaticHealthGeneration(t *testing.T) {
	for _, required := range []string{
		`data-tab="ai"`, `id="tab-ai"`, `id="ai-refresh"`, `id="ai-codex-runtime"`, `id="ai-claude-auth"`,
		`id="ai-cc-schema"`, `id="ai-cc-fingerprint"`, "/api/ai-routing", "需用户手动从会话执行",
	} {
		if !strings.Contains(localUIHTML, required) {
			t.Fatalf("AI routing UI missing %q", required)
		}
	}
	for _, forbidden := range []string{"/api/ai-health", "session.create", "session.send", "setInterval(refreshAI"} {
		if strings.Contains(localUIHTML, forbidden) {
			t.Fatalf("AI routing UI contains automatic health execution marker %q", forbidden)
		}
	}
}

func TestAIRoutingLoopbackGuardsTokenOriginAndMethod(t *testing.T) {
	app, err := New(Options{DataDir: t.TempDir(), Version: "ui-test", MachineName: "Test Node", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	app.agentController = &fakeAIController{}
	handler := app.handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/ai-routing", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}

	badOriginReq := httptest.NewRequest(http.MethodGet, "/api/ai-routing", nil)
	badOriginReq.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
	badOriginReq.Header.Set("Origin", "https://attacker.example")
	badOrigin := httptest.NewRecorder()
	handler.ServeHTTP(badOrigin, badOriginReq)
	if badOrigin.Code != http.StatusForbidden {
		t.Fatalf("bad origin status=%d", badOrigin.Code)
	}

	wrongMethodReq := httptest.NewRequest(http.MethodPost, "/api/ai-routing", nil)
	wrongMethodReq.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
	wrongMethod := httptest.NewRecorder()
	handler.ServeHTTP(wrongMethod, wrongMethodReq)
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status=%d", wrongMethod.Code)
	}
}
