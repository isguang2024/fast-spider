package routing

import "strings"

func NeedsRouting(appType, apiFormat string, meta map[string]any) (bool, bool) {
	for _, key := range []string{"needsLocalRouting", "needs_local_routing", "requiresRouting", "requires_routing", "routingRequired"} {
		if value, ok := meta[key].(bool); ok {
			return value, true
		}
	}
	format := strings.ToLower(strings.TrimSpace(apiFormat))
	switch appType {
	case "claude", "claude-desktop":
		if format == "anthropic" {
			return false, true
		}
		if format == "openai_chat" || format == "openai_responses" {
			return true, true
		}
	case "codex":
		if format == "openai_responses" {
			return false, true
		}
		if format == "openai_chat" {
			return true, true
		}
	}
	return false, false
}

func RoutingMode(proxy map[string]any) string {
	takeover, _ := proxy["takeoverEnabled"].(bool)
	live, _ := proxy["liveTakeoverActive"].(bool)
	if takeover || live {
		return "cc_switch"
	}
	return "direct"
}

func EffectiveCapabilities(appType, mode string, provider map[string]any) map[string]any {
	apiFormat, _ := provider["apiFormat"].(string)
	capability := func(state, reason string) map[string]any { return map[string]any{"state": state, "reason": reason} }
	out := map[string]any{
		"toolCalls": capability("supported", "supported by the AI harness"), "mcp": capability("supported", "supported by the AI harness"),
		"webSearch": capability("unknown", "depends on upstream provider and routed tool compatibility"), "vision": capability("unknown", "depends on upstream model and request conversion"),
		"thinking": capability("unknown", "depends on upstream reasoning interface"),
	}
	if appType == "claude" {
		out["resume"] = capability("supported", "Claude Code persists and resumes sessions by session ID")
		out["imageGeneration"] = capability("unsupported", "Claude Code is not an image generation harness")
		providerID, _ := provider["providerId"].(string)
		category, _ := provider["category"].(string)
		providerType, _ := provider["providerType"].(string)
		official := providerID == "claude-official" || category == "official" || providerType == "official"
		if official {
			out["webSearch"] = capability("supported", "current route is the official Claude provider; actual availability still depends on account and service state")
		} else if providerID != "" {
			out["webSearch"] = capability("unsupported", "Claude hosted WebSearch is not treated as portable to a non-official upstream provider")
		}
		if mode == "cc_switch" && apiFormat != "" && apiFormat != "anthropic" {
			out["thinking"] = capability("supported", "CC Switch converts common reasoning interfaces; exact effort tiers remain model-dependent")
		}
	}
	if appType == "codex" {
		out["resume"] = capability("supported", "Codex threads are persistent")
		if mode == "cc_switch" {
			out["thinking"] = capability("supported", "CC Switch adapts common third-party reasoning interfaces; exact effort tiers remain model-dependent")
		}
	}
	return out
}
