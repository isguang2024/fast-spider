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
	for _, record := range index.Sessions {
		if record == nil || strings.TrimSpace(record.SessionID) == "" || strings.TrimSpace(record.WorkingDirectory) == "" {
			continue
		}
		if record.Status == "running" {
			record.Status = "interrupted"
			record.LastError = "Fast Spider Node restarted while this Claude Code turn was running"
			record.ErrorClass = ErrorRuntimeUnavailable
		} else if record.LastError != "" {
			if !validErrorClass(record.ErrorClass) {
				record.ErrorClass = classifyExecutionText(record.LastError)
			}
			record.LastError = publicErrorMessage(record.ErrorClass)
		}
		a.sessions[record.SessionID] = record
	}
	return nil
}

func (a *ClaudeCodeAdapter) saveIndexLocked() error {
	if err := os.MkdirAll(filepath.Dir(a.indexPath), 0o700); err != nil {
		return err
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
		return err
	}
	if len(raw) > claudeCodeIndexMaxBytes {
		return fmt.Errorf("Claude Code session index exceeds limit")
	}
	temp, err := os.CreateTemp(filepath.Dir(a.indexPath), ".claude-code-sessions-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceAgentFile(tempPath, a.indexPath); err != nil {
		return err
	}
	return syncAgentParentDirectory(a.indexPath)
}
