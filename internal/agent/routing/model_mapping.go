package routing

import (
	"sort"
	"strings"
)

func ExtractModels(appType string, settings, meta map[string]any) []map[string]any {
	models := make([]map[string]any, 0)
	seen := map[string]bool{}
	add := func(role, model, display string, contextWindow int64, supports1M bool) {
		model = strings.TrimSpace(model)
		if model == "" {
			return
		}
		key := strings.ToLower(strings.TrimSpace(role) + "\x00" + model)
		if seen[key] {
			return
		}
		seen[key] = true
		entry := map[string]any{"model": model}
		if role != "" {
			entry["clientRole"] = role
		}
		if display != "" {
			entry["displayName"] = display
		}
		if contextWindow > 0 {
			entry["contextWindow"] = contextWindow
		}
		if supports1M {
			entry["supports1m"] = true
		}
		models = append(models, entry)
	}
	if catalog, ok := meta["modelCatalog"].(map[string]any); ok {
		if entries, ok := catalog["models"].([]any); ok {
			for _, raw := range entries {
				entry, _ := raw.(map[string]any)
				add("", String(entry, "model", "id", "slug"), String(entry, "displayName", "display_name", "name"), Int64(entry, "contextWindow", "context_window"), false)
			}
		}
	}
	for _, key := range []string{"modelMapping", "modelMappings", "model_mapping", "modelRoute", "modelRoutes", "claudeDesktopModelRoutes"} {
		collectRoleModels(meta[key], "", add)
	}
	if appType == "codex" {
		if config, ok := settings["config"].(string); ok {
			fields := ParseTopLevelConfig(config)
			add("main", fields["model"], "", 0, false)
			add("review", fields["review_model"], "", 0, false)
		}
	}
	if env, ok := settings["env"].(map[string]any); ok {
		for _, spec := range []struct{ role, key string }{{"main", "ANTHROPIC_MODEL"}, {"sonnet", "ANTHROPIC_DEFAULT_SONNET_MODEL"}, {"opus", "ANTHROPIC_DEFAULT_OPUS_MODEL"}, {"haiku", "ANTHROPIC_DEFAULT_HAIKU_MODEL"}} {
			if value, ok := env[spec.key].(string); ok {
				add(spec.role, value, "", 0, false)
			}
		}
	}
	sort.SliceStable(models, func(a, b int) bool {
		ar, _ := models[a]["clientRole"].(string)
		br, _ := models[b]["clientRole"].(string)
		if ar != br {
			return ar < br
		}
		am, _ := models[a]["model"].(string)
		bm, _ := models[b]["model"].(string)
		return am < bm
	})
	if len(models) > 128 {
		models = models[:128]
	}
	return models
}

func collectRoleModels(raw any, roleHint string, add func(string, string, string, int64, bool)) {
	switch value := raw.(type) {
	case map[string]any:
		model := String(value, "model", "requestedModel", "requested_model", "modelId", "model_id")
		role := String(value, "role", "modelRole", "model_role")
		if role == "" {
			role = roleHint
		}
		if model != "" {
			supports1M, _ := value["supports1m"].(bool)
			if !supports1M {
				supports1M, _ = value["supports1M"].(bool)
			}
			add(role, model, String(value, "displayName", "display_name", "labelOverride", "label_override", "name"), Int64(value, "contextWindow", "context_window"), supports1M)
			return
		}
		for key, child := range value {
			lower := strings.ToLower(key)
			if lower == "main" || strings.Contains(lower, "sonnet") || strings.Contains(lower, "opus") || strings.Contains(lower, "haiku") || strings.Contains(lower, "fable") || strings.HasPrefix(lower, "claude-") {
				collectRoleModels(child, key, add)
			}
		}
	case []any:
		for _, child := range value {
			collectRoleModels(child, roleHint, add)
		}
	case string:
		if roleHint != "" {
			add(roleHint, value, "", 0, false)
		}
	}
}

func ParseTopLevelConfig(raw string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			break
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		if key != "model" && key != "review_model" && key != "model_provider" && key != "wire_api" && key != "service_tier" {
			continue
		}
		value := strings.TrimSpace(parts[1])
		if index := strings.Index(value, " #"); index >= 0 {
			value = strings.TrimSpace(value[:index])
		}
		value = strings.Trim(value, "\"'")
		if value != "" {
			out[key] = value
		}
	}
	return out
}
