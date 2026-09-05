package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type providerDiscovery struct {
	codexVersion  func(context.Context) (string, error)
	claudeVersion func(context.Context) (string, error)
	claudeAuth    func(context.Context) map[string]any
	route         func(context.Context, string) (map[string]any, error)
}

type providerDiscoveryResult struct {
	codexVersion, claudeVersion   string
	codexErr, claudeErr           error
	codexRoute, claudeRoute       map[string]any
	codexRouteErr, claudeRouteErr error
	claudeAuth                    map[string]any
}

func (m *AgentManager) providers(ctx context.Context) map[string]any {
	discovery := providerDiscovery{
		codexVersion:  m.codex.Availability,
		claudeVersion: m.claude.Availability,
		claudeAuth:    m.claude.AuthConfiguration,
	}
	if m.ccswitch != nil {
		discovery.route = m.ccswitch.InspectApp
	}
	result := discoverProviders(ctx, m.registry, discovery)
	for _, raw := range result["providers"].([]any) {
		provider := raw.(map[string]any)
		if provider["providerId"] != "codex" {
			continue
		}
		m.codex.mu.Lock()
		path := m.codex.executable
		m.codex.mu.Unlock()
		source := "unavailable"
		if path != "" {
			source = "cli"
			if strings.TrimSpace(os.Getenv("FAST_SPIDER_CODEX_EXECUTABLE")) != "" {
				source = "configured"
			} else if base := os.Getenv("LOCALAPPDATA"); filepath.IsAbs(base) {
				root := filepath.Join(base, "OpenAI", "Codex", "bin") + string(filepath.Separator)
				if strings.HasPrefix(strings.ToLower(filepath.Clean(path)), strings.ToLower(root)) {
					source = "desktop_bundled"
				}
			}
		}
		provider["runtimeSource"] = source
		provider["configurationSource"] = "user_codex"
		if strings.TrimSpace(os.Getenv("CODEX_HOME")) != "" {
			provider["configurationSource"] = "codex_home"
		}
	}
	return result
}

func discoverProviders(ctx context.Context, registry providerRegistry, discovery providerDiscovery) map[string]any {
	var result providerDiscoveryResult
	var wait sync.WaitGroup
	run := func(task func()) {
		wait.Add(1)
		go func() {
			defer wait.Done()
			task()
		}()
	}
	if discovery.codexVersion != nil {
		run(func() { result.codexVersion, result.codexErr = discovery.codexVersion(ctx) })
	}
	if discovery.claudeVersion != nil {
		run(func() { result.claudeVersion, result.claudeErr = discovery.claudeVersion(ctx) })
	}
	if discovery.claudeAuth != nil {
		run(func() { result.claudeAuth = discovery.claudeAuth(ctx) })
	}
	if discovery.route != nil {
		run(func() { result.codexRoute, result.codexRouteErr = discovery.route(ctx, "codex") })
		run(func() { result.claudeRoute, result.claudeRouteErr = discovery.route(ctx, "claude") })
	}
	wait.Wait()

	providers := make([]any, 0, len(registry.ordered))
	for _, definition := range registry.ordered {
		provider := map[string]any{
			"providerId":         definition.ID,
			"name":               definition.Name,
			"executionModes":     append([]string(nil), definition.ExecutionModes...),
			"credentialLocation": definition.CredentialSource,
			"supportedActions":   append([]string(nil), definition.SupportedActions...),
		}
		provider["sessionVisibility"] = sessionVisibilityCapabilityMatrix()
		switch definition.ID {
		case "codex":
			provider["available"] = result.codexErr == nil
			if result.codexErr == nil {
				provider["version"] = result.codexVersion
			} else {
				provider["reason"] = "runtime_unavailable"
				provider["errorClass"] = classifyExecutionError(result.codexErr)
			}
			if route := publicRouteDiscovery("codex", result.codexRoute, result.codexRouteErr); route != nil {
				provider["route"] = route
			}
		case "claude_code":
			provider["available"] = result.claudeErr == nil
			provider["runtimeAvailable"] = result.claudeErr == nil
			provider["executionHealth"] = "unknown_until_turn"
			provider["authConfiguration"] = result.claudeAuth
			provider["sessionPersistence"] = "native_claude_session_id+fast_spider_index"
			if result.claudeErr == nil {
				provider["version"] = result.claudeVersion
			} else {
				provider["reason"] = "runtime_unavailable"
				provider["errorClass"] = classifyExecutionError(result.claudeErr)
			}
			if route := publicRouteDiscovery("claude", result.claudeRoute, result.claudeRouteErr); route != nil {
				provider["route"] = route
			}
		}
		providers = append(providers, provider)
	}
	return map[string]any{"providers": providers, "routingSource": "cc_switch_db"}
}

func publicRouteDiscovery(appType string, route map[string]any, err error) map[string]any {
	if err == nil {
		return route
	}
	return map[string]any{
		"appType": appType, "available": false, "reason": "route_inspection_failed",
		"errorClass": classifyExecutionError(err),
	}
}
