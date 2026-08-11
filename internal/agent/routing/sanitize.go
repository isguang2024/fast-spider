package routing

import (
	"encoding/json"
	"net/url"
	"strings"
)

func DecodeJSON(raw string) map[string]any {
	if len(raw) == 0 || len(raw) > 2<<20 {
		return map[string]any{}
	}
	var out map[string]any
	if json.Unmarshal([]byte(raw), &out) != nil {
		return map[string]any{}
	}
	return out
}

func CredentialPresent(settings map[string]any) bool {
	var walk func(map[string]any, int) bool
	walk = func(record map[string]any, depth int) bool {
		if depth > 4 {
			return false
		}
		for key, value := range record {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "token") || strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey") || strings.Contains(lower, "secret") || strings.Contains(lower, "authorization") {
				if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
					return true
				}
			}
			if child, ok := value.(map[string]any); ok && walk(child, depth+1) {
				return true
			}
		}
		return false
	}
	return walk(settings, 0)
}

func EndpointHost(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	host := parsed.Hostname()
	if port := parsed.Port(); port != "" {
		host += ":" + port
	}
	return host
}

func APIFormat(meta, settings map[string]any) string {
	for _, record := range []map[string]any{meta, settings} {
		for _, key := range []string{"api_format", "apiFormat", "wire_api", "wireApi"} {
			if value, ok := record[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func String(record map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := record[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func Int64(record map[string]any, keys ...string) int64 {
	for _, key := range keys {
		switch value := record[key].(type) {
		case float64:
			if value > 0 {
				return int64(value)
			}
		case int64:
			if value > 0 {
				return value
			}
		case json.Number:
			if parsed, err := value.Int64(); err == nil && parsed > 0 {
				return parsed
			}
		}
	}
	return 0
}
