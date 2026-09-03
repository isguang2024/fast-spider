package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const providerReadinessSuccessTTL = 30 * time.Second

type providerReadinessCacheEntry struct {
	checkedAt       time.Time
	codexGeneration uint64
	result          map[string]any
}

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
	backend := strings.ToLower(strings.TrimSpace(input.Backend))
	if cached, ok := m.cachedProviderReadiness(providerID, mode, backend, started); ok {
		return cached, nil
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
		return m.readinessResultWithDesktopBridge(providerID, mode, layers, started), nil
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
		return m.readinessResultWithDesktopBridge(providerID, mode, layers, started), nil
	}
	layers["harness"] = measuredReadiness(func() (string, string) {
		if err := m.codex.ensureStarted(ctx); err != nil {
			return "blocked", "HARNESS_UNAVAILABLE"
		}
		return "ready", "OK"
	})
	if requiresSessionBackendProbe(backend) {
		layers["sessionBackend"] = measuredReadiness(func() (string, string) {
			if layers["harness"].State != "ready" {
				return "blocked", "NOT_CHECKED"
			}
			if _, err := m.codex.ListThreads(ctx, "", 1); err != nil {
				return "blocked", "SESSION_BACKEND_UNAVAILABLE"
			}
			return "ready", "OK"
		})
	} else {
		layers["sessionBackend"] = readinessLayer{State: "ready", ReasonCode: "NOT_REQUIRED_FOR_CHATGPT_CLOUD"}
	}
	if strings.EqualFold(strings.TrimSpace(input.Backend), sessionBackendChatGPTCloud) {
		layers["chatgptCloud"] = measuredReadiness(func() (string, string) {
			if m.chatgptCloud == nil {
				return "blocked", "CHATGPT_CLOUD_UNAVAILABLE"
			}
			checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if _, err := m.chatgptCloud.token(checkCtx); err != nil {
				return "blocked", classifyChatGPTCloudAuthError(err)
			}
			return "ready", "OK"
		})
	}
	layers["readyCreate"] = measuredReadiness(func() (string, string) {
		if reason := readinessBlockingReason(layers); reason != "" {
			return "blocked", reason
		}
		if cloud, present := layers["chatgptCloud"]; present && cloud.State != "ready" {
			return "blocked", cloud.ReasonCode
		}
		return "ready", "READY"
	})
	result := m.readinessResultWithDesktopBridge(providerID, mode, layers, started)
	if ready, _ := result["ready"].(bool); ready {
		m.rememberProviderReadiness(providerID, mode, backend, result)
	}
	return result, nil
}

func classifyChatGPTCloudAuthError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "CHATGPT_CLOUD_AUTH_RPC_TIMEOUT"
	case errors.Is(err, errCodexChatGPTNotAuthenticated):
		return "CHATGPT_CLOUD_NOT_AUTHENTICATED"
	default:
		return "CHATGPT_CLOUD_AUTH_RPC_FAILED"
	}
}

func requiresSessionBackendProbe(backend string) bool {
	return !strings.EqualFold(strings.TrimSpace(backend), sessionBackendChatGPTCloud)
}

func readinessBlockingReason(layers map[string]readinessLayer) string {
	for _, name := range []string{"routing", "provider", "harness", "sessionBackend"} {
		if layers[name].State == "ready" {
			continue
		}
		reason := strings.TrimSpace(layers[name].ReasonCode)
		if reason == "" || reason == "OK" {
			return "NOT_CHECKED"
		}
		return reason
	}
	return ""
}

func (m *AgentManager) readinessCacheKey(providerID, mode, backend string) string {
	return fmt.Sprintf("%s\x00%s\x00%s", providerID, mode, backend)
}

func (m *AgentManager) codexReadinessGeneration() (uint64, bool) {
	if m == nil || m.codex == nil {
		return 0, false
	}
	m.codex.mu.Lock()
	defer m.codex.mu.Unlock()
	ready := !m.codex.closed && m.codex.cmd != nil && m.codex.cmd.Process != nil && m.codex.cmd.ProcessState == nil && m.codex.stdin != nil && !m.codex.quarantined && m.codex.configErr == nil
	return m.codex.generation, ready
}

func (m *AgentManager) rememberProviderReadiness(providerID, mode, backend string, result map[string]any) {
	if providerID != "codex" || mode != "safe" || backend != sessionBackendChatGPTCloud {
		return
	}
	generation, ready := m.codexReadinessGeneration()
	if !ready {
		return
	}
	m.readinessMu.Lock()
	if m.readinessCache == nil {
		m.readinessCache = map[string]providerReadinessCacheEntry{}
	}
	m.readinessCache[m.readinessCacheKey(providerID, mode, backend)] = providerReadinessCacheEntry{
		checkedAt: time.Now(), codexGeneration: generation, result: cloneAgentMap(result),
	}
	m.readinessMu.Unlock()
}

func (m *AgentManager) cachedProviderReadiness(providerID, mode, backend string, started time.Time) (map[string]any, bool) {
	if m == nil || providerID != "codex" || mode != "safe" || backend != sessionBackendChatGPTCloud {
		return nil, false
	}
	generation, ready := m.codexReadinessGeneration()
	if !ready {
		return nil, false
	}
	key := m.readinessCacheKey(providerID, mode, backend)
	m.readinessMu.Lock()
	entry, ok := m.readinessCache[key]
	if ok && (entry.codexGeneration != generation || time.Since(entry.checkedAt) > providerReadinessSuccessTTL) {
		delete(m.readinessCache, key)
		ok = false
	}
	m.readinessMu.Unlock()
	if !ok {
		return nil, false
	}
	result := cloneAgentMap(entry.result)
	result["cached"] = true
	result["cacheAgeMs"] = time.Since(entry.checkedAt).Milliseconds()
	result["elapsedMs"] = time.Since(started).Milliseconds()
	return result, true
}

func (m *AgentManager) readinessResultWithDesktopBridge(providerID, mode string, layers map[string]readinessLayer, started time.Time) map[string]any {
	result := readinessResult(providerID, mode, layers, started)
	if providerID == "codex" && m.codex != nil {
		result["desktopBridge"] = m.codex.desktopBridgeMetadata()
	}
	return result
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
