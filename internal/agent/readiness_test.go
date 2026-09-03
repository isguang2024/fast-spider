package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestReadinessResultExposesExplicitCreateLayers(t *testing.T) {
	layers := map[string]readinessLayer{
		"routing":        {State: "ready", ReasonCode: "OK"},
		"provider":       {State: "ready", ReasonCode: "OK"},
		"harness":        {State: "ready", ReasonCode: "OK"},
		"sessionBackend": {State: "ready", ReasonCode: "OK"},
		"readyCreate":    {State: "ready", ReasonCode: "READY"},
	}
	result := readinessResult("codex", "safe", layers, time.Now())
	for _, field := range []string{"routeAvailable", "providerAvailable", "harnessAvailable", "sessionBackendAvailable", "readyForSessionCreate"} {
		if value, ok := result[field].(bool); !ok || !value {
			t.Fatalf("%s=%T(%v), want true", field, result[field], result[field])
		}
	}
	if result["reasonCode"] != "READY" {
		t.Fatalf("reasonCode=%v", result["reasonCode"])
	}
}

func TestReadinessResultPublishesCodexDesktopBridgeMetadata(t *testing.T) {
	t.Setenv(codexDesktopBridgeEnv, "0")
	manager := &AgentManager{codex: NewCodexAdapter(nil)}
	layers := map[string]readinessLayer{
		"routing":        {State: "ready", ReasonCode: "OK"},
		"provider":       {State: "ready", ReasonCode: "OK"},
		"harness":        {State: "ready", ReasonCode: "OK"},
		"sessionBackend": {State: "ready", ReasonCode: "OK"},
		"readyCreate":    {State: "ready", ReasonCode: "READY"},
	}
	result := manager.readinessResultWithDesktopBridge("codex", "safe", layers, time.Now())
	desktopBridge, _ := result["desktopBridge"].(map[string]any)
	if desktopBridge["state"] != "disabled" || desktopBridge["nativeConversationStreaming"] != "unsupported" {
		t.Fatalf("desktopBridge=%#v", desktopBridge)
	}
}

func TestProviderReadinessReusesRecentSuccessForSameCodexGeneration(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	adapter := &CodexAdapter{
		cmd:        &exec.Cmd{Process: &os.Process{Pid: 12345}},
		stdin:      writer,
		generation: 7,
	}
	manager := &AgentManager{codex: adapter}
	result := map[string]any{
		"providerId": "codex", "mode": "safe", "ready": true,
		"readyForSessionCreate": true, "chatgptCloudAvailable": true,
		"reasonCode": "READY", "checkedAt": time.Now().UTC().Format(time.RFC3339Nano),
	}
	manager.rememberProviderReadiness("codex", "safe", sessionBackendChatGPTCloud, result)

	cached, err := manager.providerReadiness(t.Context(), agentControlParams{ProviderID: "codex", Backend: sessionBackendChatGPTCloud, Mode: "safe"})
	if err != nil {
		t.Fatal(err)
	}
	if cached["cached"] != true || cached["ready"] != true || cached["reasonCode"] != "READY" {
		t.Fatalf("cached readiness=%#v", cached)
	}

	adapter.mu.Lock()
	adapter.generation++
	adapter.mu.Unlock()
	if _, ok := manager.cachedProviderReadiness("codex", "safe", sessionBackendChatGPTCloud, time.Now()); ok {
		t.Fatal("readiness from an earlier Codex app-server generation was reused")
	}
}

func TestProviderReadinessReportsFailingLayerReason(t *testing.T) {
	layers := map[string]readinessLayer{
		"routing":        {State: "ready", ReasonCode: "OK"},
		"provider":       {State: "ready", ReasonCode: "OK"},
		"harness":        {State: "ready", ReasonCode: "OK"},
		"sessionBackend": {State: "blocked", ReasonCode: "SESSION_BACKEND_UNAVAILABLE"},
	}
	if reason := readinessBlockingReason(layers); reason != "SESSION_BACKEND_UNAVAILABLE" {
		t.Fatalf("reason=%s", reason)
	}
}

func TestChatGPTCloudReadinessDoesNotProbeLocalThreadList(t *testing.T) {
	if requiresSessionBackendProbe(sessionBackendChatGPTCloud) {
		t.Fatal("chatgpt_cloud readiness must not depend on the local thread/list backend")
	}
	if !requiresSessionBackendProbe("") || !requiresSessionBackendProbe("codex_local") {
		t.Fatal("local Codex readiness must retain the thread/list backend probe")
	}
}

func TestClassifyChatGPTCloudAuthError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "rpc timeout", err: context.DeadlineExceeded, want: "CHATGPT_CLOUD_AUTH_RPC_TIMEOUT"},
		{name: "not authenticated", err: errCodexChatGPTNotAuthenticated, want: "CHATGPT_CLOUD_NOT_AUTHENTICATED"},
		{name: "rpc failure", err: errors.New("connection closed"), want: "CHATGPT_CLOUD_AUTH_RPC_FAILED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyChatGPTCloudAuthError(test.err); got != test.want {
				t.Fatalf("reason=%q want %q", got, test.want)
			}
		})
	}
}

func TestClassifyRouteReadinessUsesInspectedRouteFacts(t *testing.T) {
	tests := []struct {
		name, state, reason string
		route               map[string]any
	}{
		{name: "direct without cc switch", state: "ready", reason: "OK", route: map[string]any{"available": false, "reason": "database_unavailable"}},
		{name: "direct inspected", state: "ready", reason: "OK", route: map[string]any{"available": true, "routingMode": "direct"}},
		{name: "schema mismatch", state: "blocked", reason: "ROUTE_SCHEMA_UNSUPPORTED", route: map[string]any{"available": false, "reason": "unsupported_schema"}},
		{name: "selection mismatch", state: "blocked", reason: "ROUTE_SELECTION_INCONSISTENT", route: map[string]any{"available": true, "routingMode": "cc_switch", "selectionConsistent": false, "currentProvider": map[string]any{"providerId": "one"}}},
		{name: "missing selection", state: "blocked", reason: "ROUTE_PROVIDER_UNSELECTED", route: map[string]any{"available": true, "routingMode": "cc_switch"}},
		{name: "unhealthy selection", state: "blocked", reason: "ROUTE_PROVIDER_UNHEALTHY", route: map[string]any{"available": true, "routingMode": "cc_switch", "currentProvider": map[string]any{"providerId": "one", "health": map[string]any{"healthy": false}}}},
		{name: "healthy cc switch", state: "ready", reason: "OK", route: map[string]any{"available": true, "routingMode": "cc_switch", "selectionConsistent": true, "currentProvider": map[string]any{"providerId": "one", "health": map[string]any{"healthy": true}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, reason := classifyRouteReadiness(test.route)
			if state != test.state || reason != test.reason {
				t.Fatalf("state=%s reason=%s, want %s/%s", state, reason, test.state, test.reason)
			}
		})
	}
}
