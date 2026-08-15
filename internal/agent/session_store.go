package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type claudeSessionIndex struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Sessions      []*ClaudeSessionRecord `json:"sessions"`
}

func claudeSessionSummary(record *ClaudeSessionRecord) map[string]any {
	out := map[string]any{
		"providerId":       "claude_code",
		"sessionId":        record.SessionID,
		"name":             record.Name,
		"workingDirectory": record.WorkingDirectory,
		"requestedModel":   record.RequestedModel,
		"nativeModel":      record.NativeModel,
		"status":           record.Status,
		"latestTurnId":     record.LatestTurnID,
		"archived":         record.Archived,
		"createdAt":        record.CreatedAt,
		"updatedAt":        record.UpdatedAt,
		"routeBefore":      record.RouteBefore,
		"routeAfter":       record.RouteAfter,
		"actualUpstream":   record.ActualUpstream,
	}
	if record.LastError != "" {
		out["lastError"] = record.LastError
		out["errorClass"] = record.ErrorClass
	}
	return out
}

func (a *ClaudeCodeAdapter) loadIndex() error {
	raw, err := os.ReadFile(a.indexPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(raw) > claudeCodeIndexMaxBytes {
		return fmt.Errorf("Claude Code session index exceeds limit")
	}
	var index claudeSessionIndex
	if err := json.Unmarshal(raw, &index); err != nil {
		return err
	}
	if index.SchemaVersion != 1 {
		return fmt.Errorf("unsupported Claude Code session index schema version %d", index.SchemaVersion)
	}
	seen := make(map[string]struct{}, len(index.Sessions))
	for _, record := range index.Sessions {
		if record == nil || strings.TrimSpace(record.SessionID) == "" || strings.TrimSpace(record.WorkingDirectory) == "" {
			return fmt.Errorf("invalid Claude Code session index record")
		}
		if _, duplicate := seen[record.SessionID]; duplicate {
			return fmt.Errorf("invalid Claude Code session index: duplicate session ID")
		}
		seen[record.SessionID] = struct{}{}
		if record.Status != "running" && record.LastError != "" {
			if !validErrorClass(record.ErrorClass) {
				record.ErrorClass = classifyExecutionText(record.LastError)
			}
			record.LastError = publicErrorMessage(record.ErrorClass)
		}
		a.sessions[record.SessionID] = record
	}
	markInterruptedClaudeSessions(a.sessions)
	return nil
}

func (a *ClaudeCodeAdapter) saveIndexLocked() (bool, error) {
	if a.indexLoadErr != nil {
		return false, fmt.Errorf("Claude Code session index is unavailable; repair the existing index before making session changes: %w", a.indexLoadErr)
	}
	if a.beforeCommitSaveOverride != nil {
		return false, a.beforeCommitSaveOverride()
	}
	if err := os.MkdirAll(filepath.Dir(a.indexPath), 0o700); err != nil {
		return false, err
	}
	records := make([]*ClaudeSessionRecord, 0, len(a.sessions))
	for _, record := range a.sessions {
		copy := *record
		records = append(records, &copy)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].UpdatedAt > records[j].UpdatedAt })
	index := claudeSessionIndex{SchemaVersion: 1, Sessions: records}
	raw, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return false, err
	}
	if len(raw) > claudeCodeIndexMaxBytes {
		return false, fmt.Errorf("Claude Code session index exceeds its %d-byte capacity; delete inactive Fast Spider session entries to continue (native Claude history is preserved)", claudeCodeIndexMaxBytes)
	}
	temp, err := os.CreateTemp(filepath.Dir(a.indexPath), ".claude-code-sessions-*")
	if err != nil {
		return false, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return false, err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return false, err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return false, err
	}
	if err := temp.Close(); err != nil {
		return false, err
	}
	if err := replaceAgentFile(tempPath, a.indexPath); err != nil {
		return false, err
	}
	syncParent := syncAgentParentDirectory
	if a.syncParentOverride != nil {
		syncParent = a.syncParentOverride
	}
	if err := syncParent(a.indexPath); err != nil {
		return true, err
	}
	return true, nil
}

func markInterruptedClaudeSessions(sessions map[string]*ClaudeSessionRecord) {
	for _, record := range sessions {
		if record != nil && record.Status == "running" {
			record.Status = "interrupted"
			record.LastError = "Fast Spider Node restarted while this Claude Code turn was running"
			record.ErrorClass = ErrorRuntimeUnavailable
		}
	}
}
