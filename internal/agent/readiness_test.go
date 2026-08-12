package agent

import (
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
