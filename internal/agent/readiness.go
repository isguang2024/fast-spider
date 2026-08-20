package agent

import (
	"context"
	"strings"
	"time"
)

type readinessLayer struct {
	State      string `json:"state"`
	ReasonCode string `json:"reasonCode"`
	ElapsedMs  int64  `json:"elapsedMs"`
}

func measuredReadiness(check func() (string, string)) readinessLayer {
	started := time.Now()
	state, reason := check()
	return readinessLayer{State: state, ReasonCode: reason, ElapsedMs: time.Since(started).Milliseconds()}
}

func (m *AgentManager) providerReadiness(ctx context.Context, input agentControlParams) (map[string]any, error) {
	started := time.Now()
	providerID := strings.TrimSpace(input.ProviderID)
	if providerID == "" {
		providerID = "codex"
	}
	mode := strings.ToLower(strings.TrimSpace(input.Mode))
	if mode == "" {
		mode = "safe"
	}
	if mode != "passive" && mode != "safe" {
		return nil, &createIdempotencyError{code: "INVALID_REQUEST", message: "mode must be passive or safe"}
	}
	layers := map[string]readinessLayer{}
	layers["routing"] = measuredReadiness(func() (string, string) {
		if _, ok := m.registry.get(providerID); !ok {
			return "blocked", "ROUTE_UNAVAILABLE"
		}
		if m.ccswitch == nil {
			return "ready", "OK"
		}
		appType := "codex"
		if providerID == "claude_code" {
			appType = "claude"
		}
		route, err := m.ccswitch.InspectApp(ctx, appType)
		if err != nil {
			return "blocked", "ROUTE_INSPECTION_FAILED"
		}
		return classifyRouteReadiness(route)
	})
	if providerID != "codex" || m.codex == nil {
		layers["provider"] = readinessLayer{State: "blocked", ReasonCode: "PROVIDER_UNAVAILABLE"}
		layers["harness"] = readinessLayer{State: "blocked", ReasonCode: "NOT_CHECKED"}
		layers["sessionBackend"] = readinessLayer{State: "blocked", ReasonCode: "NOT_CHECKED"}
		layers["readyCreate"] = readinessLayer{State: "blocked", ReasonCode: "NOT_CHECKED"}
		return readinessResult(providerID, mode, layers, started), nil
	}
	layers["provider"] = measuredReadiness(func() (string, string) {
		if _, err := m.codex.Availability(ctx); err != nil {
			return "blocked", "PROVIDER_UNAVAILABLE"
		}
		return "ready", "OK"
	})
	if mode == "passive" && !m.codex.IsStarted() {
		layers["harness"] = readinessLayer{State: "unknown", ReasonCode: "HARNESS_NOT_RUNNING"}
		layers["sessionBackend"] = readinessLayer{State: "unknown", ReasonCode: "NOT_CHECKED"}
		layers["readyCreate"] = readinessLayer{State: "unknown", ReasonCode: "NOT_CHECKED"}
		return readinessResult(providerID, mode, layers, started), nil
	}
	layers["harness"] = measuredReadiness(func() (string, string) {
		if err := m.codex.ensureStarted(ctx); err != nil {
			return "blocked", "HARNESS_UNAVAILABLE"
		}
		return "ready", "OK"
	})
	layers["sessionBackend"] = measuredReadiness(func() (string, string) {
		if layers["harness"].State != "ready" {
			return "blocked", "NOT_CHECKED"
		}
		if _, err := m.codex.ListThreads(ctx, "", 1); err != nil {
			return "blocked", "SESSION_BACKEND_UNAVAILABLE"
		}
		return "ready", "OK"
	})
	if strings.EqualFold(strings.TrimSpace(input.Backend), sessionBackendChatGPTCloud) {
		layers["chatgptCloud"] = measuredReadiness(func() (string, string) {
			if m.chatgptCloud == nil {
				return "blocked", "CHATGPT_CLOUD_UNAVAILABLE"
			}
			checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if _, err := m.chatgptCloud.token(checkCtx); err != nil {
				return "blocked", "CHATGPT_CLOUD_NOT_AUTHENTICATED"
			}
			return "ready", "OK"
		})
	}
	layers["readyCreate"] = measuredReadiness(func() (string, string) {
		for _, name := range []string{"routing", "provider", "harness", "sessionBackend"} {
			if layers[name].State != "ready" {
				return "blocked", "NOT_CHECKED"
			}
		}
		if cloud, present := layers["chatgptCloud"]; present && cloud.State != "ready" {
			return "blocked", cloud.ReasonCode
		}
		return "ready", "READY"
	})
	return readinessResult(providerID, mode, layers, started), nil
}

func classifyRouteReadiness(route map[string]any) (string, string) {
	available, _ := route["available"].(bool)
	if !available {
		reason, _ := route["reason"].(string)
		if reason == "database_unavailable" || reason == "" {
			// No CC Switch database means the harness uses its direct route.
			return "ready", "OK"
		}
		if reason == "unsupported_schema" {
			return "blocked", "ROUTE_SCHEMA_UNSUPPORTED"
		}
		return "blocked", "ROUTE_UNAVAILABLE"
	}
	mode, _ := route["routingMode"].(string)
	if mode == "direct" {
		return "ready", "OK"
	}
	if mode != "cc_switch" {
		return "blocked", "ROUTE_MODE_UNKNOWN"
	}
	if consistent, present := route["selectionConsistent"].(bool); present && !consistent {
		return "blocked", "ROUTE_SELECTION_INCONSISTENT"
	}
	current, _ := route["currentProvider"].(map[string]any)
	if current == nil {
		return "blocked", "ROUTE_PROVIDER_UNSELECTED"
	}
	if health, ok := current["health"].(map[string]any); ok {
		if healthy, present := health["healthy"].(bool); present && !healthy {
			return "blocked", "ROUTE_PROVIDER_UNHEALTHY"
		}
	}
	return "ready", "OK"
}

func readinessResult(providerID, mode string, layers map[string]readinessLayer, started time.Time) map[string]any {
	state := layers["readyCreate"].State
	reason := layers["readyCreate"].ReasonCode
	result := map[string]any{
		"providerId": providerID, "mode": mode, "state": state, "ready": state == "ready", "reasonCode": reason,
		"routeAvailable": layers["routing"].State == "ready", "providerAvailable": layers["provider"].State == "ready",
		"harnessAvailable": layers["harness"].State == "ready", "sessionBackendAvailable": layers["sessionBackend"].State == "ready",
		"readyForSessionCreate": state == "ready",
		"sessionVisibility":     sessionVisibilityCapabilityMatrix(),
		"executionHealth":       "unknown_until_turn", "checkedAt": time.Now().UTC().Format(time.RFC3339Nano),
		"elapsedMs": time.Since(started).Milliseconds(), "layers": layers,
	}
	if cloud, present := layers["chatgptCloud"]; present {
		result["chatgptCloudAvailable"] = cloud.State == "ready"
	}
	return result
}
