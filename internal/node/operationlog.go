package node

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/operationlog"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

const (
	defaultOperationLogLimit = 50
	maxOperationLogLimit     = 200
	maxOperationLogCursor    = 256
)

type operationLogQueryParams struct {
	Level    string `json:"level,omitempty"`
	Category string `json:"category,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Before   string `json:"before,omitempty"`
}

type operationLogQueryEntry struct {
	ID         string             `json:"id"`
	Timestamp  int64              `json:"timestamp"`
	Level      operationlog.Level `json:"level"`
	Category   string             `json:"category"`
	Action     string             `json:"action"`
	Status     int                `json:"status,omitempty"`
	DurationMs int64              `json:"durationMs,omitempty"`
}

type operationLogQueryResult struct {
	Entries       []operationLogQueryEntry `json:"entries"`
	HasMore       bool                     `json:"hasMore"`
	NextCursor    string                   `json:"nextCursor,omitempty"`
	RetentionDays int                      `json:"retentionDays"`
}

type operationLogCapabilityError struct {
	code      string
	message   string
	retryable bool
}

func (e *operationLogCapabilityError) Error() string { return e.message }

func (e *operationLogCapabilityError) CapabilityError() (string, string, bool) {
	return e.code, e.message, e.retryable
}

type operationLogCursor struct {
	Timestamp int64  `json:"timestamp"`
	ID        string `json:"id"`
}

func (c *Client) operationLogQuery(ctx context.Context, params map[string]any) (operationLogQueryResult, error) {
	if err := ctx.Err(); err != nil {
		return operationLogQueryResult{}, err
	}
	if c.operationLog == nil {
		return operationLogQueryResult{}, &operationLogCapabilityError{code: "OPERATION_LOG_UNAVAILABLE", message: "operation log is unavailable"}
	}
	var input operationLogQueryParams
	if err := decodeParams(params, &input); err != nil {
		return operationLogQueryResult{}, &operationLogCapabilityError{code: "INVALID_REQUEST", message: "invalid operation log query"}
	}
	input.Level = strings.ToLower(strings.TrimSpace(input.Level))
	if input.Level != "" && input.Level != string(operationlog.LevelInfo) && input.Level != string(operationlog.LevelWarning) && input.Level != string(operationlog.LevelError) {
		return operationLogQueryResult{}, &operationLogCapabilityError{code: "INVALID_REQUEST", message: "invalid operation log level"}
	}
	input.Category = strings.TrimSpace(input.Category)
	if len(input.Category) > 64 {
		return operationLogQueryResult{}, &operationLogCapabilityError{code: "INVALID_REQUEST", message: "operation log category is too long"}
	}
	if input.Limit == 0 {
		input.Limit = defaultOperationLogLimit
	}
	if input.Limit < 1 || input.Limit > maxOperationLogLimit {
		return operationLogQueryResult{}, &operationLogCapabilityError{code: "INVALID_REQUEST", message: "operation log limit must be between 1 and 200"}
	}
	beforeTimestamp, beforeID, err := decodeOperationLogCursor(input.Before)
	if err != nil {
		return operationLogQueryResult{}, &operationLogCapabilityError{code: "INVALID_REQUEST", message: "invalid operation log cursor"}
	}
	entries, hasMore := c.operationLog.QueryRecent(operationlog.Level(input.Level), input.Category, input.Limit, beforeTimestamp, beforeID)
	out := operationLogQueryResult{Entries: make([]operationLogQueryEntry, 0, len(entries)), HasMore: hasMore, RetentionDays: operationlog.RetentionDays}
	for _, entry := range entries {
		out.Entries = append(out.Entries, operationLogQueryEntry{
			ID: entry.ID, Timestamp: entry.Timestamp, Level: entry.Level, Category: entry.Category,
			Action: entry.Action, Status: entry.Status, DurationMs: entry.DurationMs,
		})
	}
	if hasMore && len(entries) > 0 {
		out.NextCursor = encodeOperationLogCursor(entries[len(entries)-1])
	}
	return out, nil
}

func encodeOperationLogCursor(entry operationlog.Entry) string {
	raw, _ := json.Marshal(operationLogCursor{Timestamp: entry.Timestamp, ID: entry.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeOperationLogCursor(value string) (int64, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, "", nil
	}
	if len(value) > maxOperationLogCursor {
		return 0, "", fmt.Errorf("cursor too long")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, "", err
	}
	var cursor operationLogCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.Timestamp <= 0 || strings.TrimSpace(cursor.ID) == "" {
		return 0, "", fmt.Errorf("invalid cursor")
	}
	return cursor.Timestamp, cursor.ID, nil
}

func (c *Client) appendOperationLog(reqCapability, reqAction string, responseError *protocolv1.ProtocolError, started time.Time) {
	if c.operationLog == nil {
		return
	}
	level := operationlog.LevelInfo
	status := 200
	if responseError != nil {
		level = operationlog.LevelWarning
		status = 400
	}
	entry := operationlog.NewEntry(level, "capability", reqCapability+"."+reqAction, reqCapability+"."+reqAction)
	entry.Status = status
	entry.DurationMs = time.Since(started).Milliseconds()
	c.operationLog.Append(entry)
}
