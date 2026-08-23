package agent

import (
	"context"
	"sort"
	"strings"
	"time"
)

// sessionListWithManagedCloud keeps the ordinary Codex session.list useful while
// also surfacing ChatGPT cloud conversations that Fast Spider itself created.
// Explicit backend=chatgpt_cloud continues to expose the provider's full cloud
// conversation list; the implicit aggregate deliberately does not import an
// account's unrelated ChatGPT history.
func (m *AgentManager) sessionListWithManagedCloud(ctx context.Context, root string, limit int) (map[string]any, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	localResult, err := m.sessionList(ctx, root, limit)
	if err != nil {
		return nil, err
	}
	cloudSessions, cloudErr := m.managedChatGPTCloudSessions(root, limit)
	if cloudErr != nil {
		if m.logger != nil {
			m.logger.Warn("list managed ChatGPT cloud sessions", "error", cloudErr)
		}
		return localResult, nil
	}
	localSessions, _ := localResult["sessions"].([]map[string]any)
	combined := make([]map[string]any, 0, len(localSessions)+len(cloudSessions))
	combined = append(combined, localSessions...)
	combined = append(combined, cloudSessions...)
	sort.SliceStable(combined, func(i, j int) bool {
		return sessionListTimestamp(combined[i]).After(sessionListTimestamp(combined[j]))
	})
	if len(combined) > limit {
		combined = combined[:limit]
	}
	localResult["sessions"] = combined
	localResult["includedBackends"] = []string{sessionBackendCodexLocal, sessionBackendChatGPTCloud}
	return localResult, nil
}

func (m *AgentManager) managedChatGPTCloudSessions(root string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	snapshot, err := m.visibilitySnapshot()
	if err != nil {
		return nil, err
	}
	records := make([]sessionVisibilityRecord, 0)
	for _, record := range snapshot {
		if record.ProviderID != "codex" || record.Backend != sessionBackendChatGPTCloud || record.Visibility != sessionVisibilityVisible {
			continue
		}
		if root != "" {
			if record.WorkingDirectory == "" || !sameAgentPath(record.WorkingDirectory, root) {
				continue
			}
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return []map[string]any{}, nil
	}
	sort.Slice(records, func(i, j int) bool { return records[i].UpdatedAt.After(records[j].UpdatedAt) })

	// The implicit aggregate is intentionally sidecar-only. It must not turn a
	// local session.list into a ChatGPT network request; explicit
	// backend=chatgpt_cloud is the live provider listing surface.
	out := make([]map[string]any, 0, minInt(limit, len(records)))
	for _, record := range records {
		item := map[string]any{}
		item["sessionId"] = record.SessionID
		item["providerId"] = "codex"
		if strings.TrimSpace(mapString(item, "title")) == "" {
			item["title"] = "ChatGPT cloud conversation"
		}
		if _, ok := item["createdAt"]; !ok && !record.CreatedAt.IsZero() {
			item["createdAt"] = record.CreatedAt.Unix()
		}
		if _, ok := item["updatedAt"]; !ok && !record.UpdatedAt.IsZero() {
			item["updatedAt"] = record.UpdatedAt.Unix()
		}
		record.applyToResult(item)
		out = append(out, item)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func chatgptCloudListSessionID(item map[string]any) string {
	for _, key := range []string{"sessionId", "conversation_id", "id"} {
		if value := strings.TrimSpace(mapString(item, key)); value != "" {
			return value
		}
	}
	return ""
}

func sessionListTimestamp(item map[string]any) time.Time {
	for _, key := range []string{"updatedAt", "updated_at", "update_time", "createdAt", "created_at", "create_time"} {
		value, ok := item[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case time.Time:
			return typed
		case int64:
			return time.Unix(typed, 0)
		case int:
			return time.Unix(int64(typed), 0)
		case float64:
			seconds := int64(typed)
			nanos := int64((typed - float64(seconds)) * float64(time.Second))
			return time.Unix(seconds, nanos)
		case string:
			if parsed, err := time.Parse(time.RFC3339Nano, typed); err == nil {
				return parsed
			}
		}
	}
	return time.Time{}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
