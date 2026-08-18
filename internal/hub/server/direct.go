package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/isguang2024/fast-spider/internal/hub/store"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

const directAPIVersion = "v1"

var (
	directToolsOnce  sync.Once
	directToolsCache []directToolDescriptor
	directToolsErr   error
)

type directCallRequest struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

type directCallResponse struct {
	OK     bool   `json:"ok"`
	Tool   string `json:"tool"`
	Result any    `json:"result,omitempty"`
}

type directToolDescriptor struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	InputSchema   any    `json:"inputSchema"`
	Authorization string `json:"authorization"`
}

type directToolsResponse struct {
	APIVersion      string                 `json:"apiVersion"`
	ServerVersion   string                 `json:"serverVersion"`
	MachineID       string                 `json:"machineId,omitempty"`
	Scopes          []string               `json:"scopes"`
	ExpiresAt       time.Time              `json:"expiresAt"`
	RateLimitMinute int                    `json:"rateLimitPerMinute"`
	Tools           []directToolDescriptor `json:"tools"`
}

type directProtocolError struct {
	status  int
	code    string
	message string
}

func (e *directProtocolError) Error() string { return e.message }

type directRateWindow struct {
	start time.Time
	count int
}

type directRateLimiter struct {
	mu      sync.Mutex
	entries map[string]directRateWindow
}

func newDirectRateLimiter() *directRateLimiter {
	return &directRateLimiter{entries: make(map[string]directRateWindow)}
}

func (l *directRateLimiter) allow(keyID string, limit int, now time.Time) bool {
	if keyID == "" || limit <= 0 {
		return false
	}
	windowStart := now.Truncate(time.Minute)
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[keyID]
	if !ok || !entry.start.Equal(windowStart) {
		entry = directRateWindow{start: windowStart}
	}
	if entry.count >= limit {
		l.entries[keyID] = entry
		return false
	}
	entry.count++
	l.entries[keyID] = entry
	if len(l.entries) > 4096 {
		for id, current := range l.entries {
			if current.start.Before(windowStart.Add(-2 * time.Minute)) {
				delete(l.entries, id)
			}
		}
	}
	return true
}

func (s *Server) directAccessOnly(next func(http.ResponseWriter, *http.Request, store.DirectAccessKeyRecord)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		record, err := s.service.AuthenticateDirectAccessKey(r.Context(), bearerToken(r.Header.Get("Authorization")))
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="FastSpider Direct API"`)
			writeDirectError(w, &directProtocolError{status: http.StatusUnauthorized, code: "DIRECT_KEY_INVALID", message: "direct access key is invalid, expired, or revoked"})
			return
		}
		if !s.directLimiter.allow(record.ID, record.RateLimitPerMinute, time.Now().UTC()) {
			w.Header().Set("Retry-After", "60")
			writeDirectError(w, &directProtocolError{status: http.StatusTooManyRequests, code: "RATE_LIMITED", message: "direct access key rate limit exceeded"})
			return
		}
		next(w, r, record)
	}
}

func (s *Server) handleDirectTools(w http.ResponseWriter, _ *http.Request, key store.DirectAccessKeyRecord) {
	tools, err := directToolsCatalog()
	if err != nil {
		writeDirectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, directToolsResponse{
		APIVersion:      directAPIVersion,
		ServerVersion:   s.service.Version(),
		MachineID:       key.MachineID,
		Scopes:          append([]string(nil), key.Scopes...),
		ExpiresAt:       key.ExpiresAt,
		RateLimitMinute: key.RateLimitPerMinute,
		Tools:           tools,
	})
}

func (s *Server) handleDirectCall(w http.ResponseWriter, r *http.Request, key store.DirectAccessKeyRecord) {
	var req directCallRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Tool = strings.TrimSpace(req.Tool)
	if req.Tool == "" || len(req.Tool) > 64 {
		writeDirectError(w, &directProtocolError{status: http.StatusBadRequest, code: "INVALID_REQUEST", message: "tool is required"})
		return
	}
	if len(req.Arguments) == 0 || string(req.Arguments) == "null" {
		req.Arguments = json.RawMessage(`{}`)
	}
	result, err := s.executeDirectTool(r.Context(), key, req.Tool, req.Arguments)
	if err != nil {
		writeDirectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, directCallResponse{OK: true, Tool: req.Tool, Result: result})
}

func directToolsCatalog() ([]directToolDescriptor, error) {
	directToolsOnce.Do(func() {
		directToolsCache, directToolsErr = buildDirectToolsCatalog()
	})
	return append([]directToolDescriptor(nil), directToolsCache...), directToolsErr
}

func buildDirectToolsCatalog() ([]directToolDescriptor, error) {
	tools := make([]directToolDescriptor, 0, 17)
	if err := appendDirectTool[machineListInput](&tools, "machine_list", "read-only"); err != nil {
		return nil, err
	}
	if err := appendDirectTool[machineGetInput](&tools, "machine_get", "read-only"); err != nil {
		return nil, err
	}
	if err := appendDirectTool[capabilityListInput](&tools, "capability_list", "read-only"); err != nil {
		return nil, err
	}
	if err := appendDirectTool[fileReadInput](&tools, "file_read", "read-only"); err != nil {
		return nil, err
	}
	if err := appendDirectTool[fileEditInput](&tools, "file_edit", "scope: files.write"); err != nil {
		return nil, err
	}
	if err := appendDirectTool[codeSearchInput](&tools, "code_search", "read-only"); err != nil {
		return nil, err
	}
	if err := appendDirectTool[shellRunInput](&tools, "shell_run", "scope: shell"); err != nil {
		return nil, err
	}
	if err := appendDirectTool[jobWatchInput](&tools, "job_watch", "read-only"); err != nil {
		return nil, err
	}
	if err := appendDirectTool[jobCancelInput](&tools, "job_cancel", "scope: jobs"); err != nil {
		return nil, err
	}
	if err := appendDirectTool[gitControlInput](&tools, "git_control", "read actions are read-only; mutations require scope: git"); err != nil {
		return nil, err
	}
	if err := appendDirectTool[buildControlInput](&tools, "build_control", "scope: shell"); err != nil {
		return nil, err
	}
	if err := appendDirectTool[artifactGetInput](&tools, "artifact_get", "get is read-only; upload/publish require scope: artifacts.write"); err != nil {
		return nil, err
	}
	if err := appendDirectTool[browserControlInput](&tools, "browser_control", "scope: browser"); err != nil {
		return nil, err
	}
	if err := appendDirectTool[screenshotTakeInput](&tools, "screenshot_take", "scope: browser"); err != nil {
		return nil, err
	}
	if err := appendDirectTool[thinkingTeamInput](&tools, "thinking_team", "read-only"); err != nil {
		return nil, err
	}
	if err := appendDirectTool[aiControlInput](&tools, "ai_control", "discovery/read actions are read-only; mutations require scope: ai"); err != nil {
		return nil, err
	}
	if err := appendDirectTool[workingContextInput](&tools, "working_context", "read actions are read-only; mutations require scope: context.write"); err != nil {
		return nil, err
	}
	return tools, nil
}

func appendDirectTool[T any](tools *[]directToolDescriptor, name, authorization string) error {
	schema, err := jsonschema.ForType(reflect.TypeFor[T](), &jsonschema.ForOptions{})
	if err != nil {
		return fmt.Errorf("infer direct schema for %s: %w", name, err)
	}
	entry, ok := mcpToolGuides[name]
	if !ok {
		return fmt.Errorf("missing tool guide for %s", name)
	}
	*tools = append(*tools, directToolDescriptor{Name: name, Description: entry.Description, InputSchema: schema, Authorization: authorization})
	return nil
}

func decodeDirectArguments[T any](raw json.RawMessage) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, &directProtocolError{status: http.StatusBadRequest, code: "INVALID_REQUEST", message: "invalid tool arguments"}
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return value, &directProtocolError{status: http.StatusBadRequest, code: "INVALID_REQUEST", message: "tool arguments must contain one JSON object"}
	}
	return value, nil
}

func directRequireScope(key store.DirectAccessKeyRecord, scope string) error {
	if core.DirectAccessKeyHasScope(key, scope) {
		return nil
	}
	return &directProtocolError{status: http.StatusForbidden, code: "DIRECT_SCOPE_REQUIRED", message: "direct access key does not grant required scope: " + scope}
}

func directRequireMachine(key store.DirectAccessKeyRecord, machineID string) error {
	if key.MachineID == "" {
		return nil
	}
	if strings.TrimSpace(machineID) == key.MachineID {
		return nil
	}
	return &directProtocolError{status: http.StatusForbidden, code: "DIRECT_MACHINE_BOUND", message: "direct access key is bound to a different machine"}
}

func isDirectGitRead(action string) bool {
	switch strings.TrimSpace(action) {
	case "status", "diff", "stagedDiff", "log", "show", "branches", "currentBranch", "worktrees":
		return true
	default:
		return false
	}
}

func isDirectAIRead(action string) bool {
	switch strings.TrimSpace(action) {
	case "routing.status", "providers.list", "provider.readiness", "models.list", "provider.capabilities", "projects.list",
		"skills.list", "hooks.list", "permissions.list", "plugins.list", "plugins.installed", "plugins.get", "plugin.skill.read",
		"mcp.status.list", "session.list", "session.get", "session.watch", "session.result", "session.goal.get":
		return true
	default:
		return false
	}
}

func isDirectContextRead(action string) bool {
	switch strings.TrimSpace(action) {
	case "get", "plan.get", "plan.list", "markdown.list", "markdown.read", "progress.watch":
		return true
	default:
		return false
	}
}

func (s *Server) executeDirectTool(ctx context.Context, key store.DirectAccessKeyRecord, tool string, raw json.RawMessage) (any, error) {
	ownerID := key.OwnerID
	switch tool {
	case "machine_list":
		input, err := decodeDirectArguments[machineListInput](raw)
		if err != nil {
			return nil, err
		}
		out, err := executeTypedTool[machineListOutput](s.toolExecutor, ctx, ownerID, tool, input)
		if err != nil {
			return nil, err
		}
		if key.MachineID != "" {
			filtered := out.Machines[:0]
			for _, machine := range out.Machines {
				if machine.MachineID == key.MachineID {
					filtered = append(filtered, machine)
				}
			}
			out.Machines = filtered
		}
		return out, nil

	case "machine_get":
		input, err := decodeDirectArguments[machineGetInput](raw)
		if err != nil {
			return nil, err
		}
		if err := directRequireMachine(key, input.MachineID); err != nil {
			return nil, err
		}
		return executeTypedTool[machineGetOutput](s.toolExecutor, ctx, ownerID, tool, input)

	case "capability_list":
		input, err := decodeDirectArguments[capabilityListInput](raw)
		if err != nil {
			return nil, err
		}
		if input.MachineID != "" {
			if err := directRequireMachine(key, input.MachineID); err != nil {
				return nil, err
			}
		}
		return executeTypedTool[capabilityListOutput](s.toolExecutor, ctx, ownerID, tool, input)

	case "file_read":
		input, err := decodeDirectArguments[fileReadInput](raw)
		if err != nil {
			return nil, err
		}
		if err := directRequireMachine(key, input.MachineID); err != nil {
			return nil, err
		}
		return executeTypedTool[fileReadOutput](s.toolExecutor, ctx, ownerID, tool, input)

	case "code_search":
		input, err := decodeDirectArguments[codeSearchInput](raw)
		if err != nil {
			return nil, err
		}
		if err := directRequireMachine(key, input.MachineID); err != nil {
			return nil, err
		}
		return executeTypedTool[codeSearchOutput](s.toolExecutor, ctx, ownerID, tool, input)

	case "file_edit":
		if err := directRequireScope(key, core.DirectScopeFilesWrite); err != nil {
			return nil, err
		}
		input, err := decodeDirectArguments[fileEditInput](raw)
		if err != nil {
			return nil, err
		}
		if err := directRequireMachine(key, input.MachineID); err != nil {
			return nil, err
		}
		return executeTypedTool[fileEditOutput](s.toolExecutor, ctx, ownerID, tool, input)

	case "shell_run":
		if err := directRequireScope(key, core.DirectScopeShell); err != nil {
			return nil, err
		}
		input, err := decodeDirectArguments[shellRunInput](raw)
		if err != nil {
			return nil, err
		}
		if err := directRequireMachine(key, input.MachineID); err != nil {
			return nil, err
		}
		return executeTypedTool[jobOutput](s.toolExecutor, ctx, ownerID, tool, input)

	case "job_watch":
		input, err := decodeDirectArguments[jobWatchInput](raw)
		if err != nil {
			return nil, err
		}
		if err := directRequireMachine(key, input.MachineID); err != nil {
			return nil, err
		}
		return executeTypedTool[jobOutput](s.toolExecutor, ctx, ownerID, tool, input)

	case "job_cancel":
		if err := directRequireScope(key, core.DirectScopeJobs); err != nil {
			return nil, err
		}
		input, err := decodeDirectArguments[jobCancelInput](raw)
		if err != nil {
			return nil, err
		}
		if err := directRequireMachine(key, input.MachineID); err != nil {
			return nil, err
		}
		return executeTypedTool[jobOutput](s.toolExecutor, ctx, ownerID, tool, input)

	case "git_control":
		input, err := decodeDirectArguments[gitControlInput](raw)
		if err != nil {
			return nil, err
		}
		if !isDirectGitRead(input.Action) {
			if err := directRequireScope(key, core.DirectScopeGit); err != nil {
				return nil, err
			}
		}
		if err := directRequireMachine(key, input.MachineID); err != nil {
			return nil, err
		}
		return executeTypedTool[genericCapabilityOutput](s.toolExecutor, ctx, ownerID, tool, input)

	case "build_control":
		if err := directRequireScope(key, core.DirectScopeShell); err != nil {
			return nil, err
		}
		input, err := decodeDirectArguments[buildControlInput](raw)
		if err != nil {
			return nil, err
		}
		if err := directRequireMachine(key, input.MachineID); err != nil {
			return nil, err
		}
		return executeTypedTool[genericCapabilityOutput](s.toolExecutor, ctx, ownerID, tool, input)

	case "browser_control":
		if err := directRequireScope(key, core.DirectScopeBrowser); err != nil {
			return nil, err
		}
		input, err := decodeDirectArguments[browserControlInput](raw)
		if err != nil {
			return nil, err
		}
		if err := directRequireMachine(key, input.MachineID); err != nil {
			return nil, err
		}
		out, err := executeTypedTool[genericCapabilityOutput](s.toolExecutor, ctx, ownerID, tool, input)
		if err != nil {
			return nil, err
		}
		if err := s.decoratePublishedAttachmentResult(ownerID, out.Result); err != nil {
			return nil, err
		}
		return out, nil

	case "screenshot_take":
		if err := directRequireScope(key, core.DirectScopeBrowser); err != nil {
			return nil, err
		}
		input, err := decodeDirectArguments[screenshotTakeInput](raw)
		if err != nil {
			return nil, err
		}
		if err := directRequireMachine(key, input.MachineID); err != nil {
			return nil, err
		}
		out, err := executeTypedTool[genericCapabilityOutput](s.toolExecutor, ctx, ownerID, tool, input)
		if err != nil {
			return nil, err
		}
		if err := s.decoratePublishedAttachmentResult(ownerID, out.Result); err != nil {
			return nil, err
		}
		return out, nil

	case "thinking_team":
		input, err := decodeDirectArguments[thinkingTeamInput](raw)
		if err != nil {
			return nil, err
		}
		return executeTypedTool[genericCapabilityOutput](s.toolExecutor, ctx, ownerID, tool, input)

	case "ai_control":
		input, err := decodeDirectArguments[aiControlInput](raw)
		if err != nil {
			return nil, err
		}
		if !isDirectAIRead(input.Action) {
			if err := directRequireScope(key, core.DirectScopeAI); err != nil {
				return nil, err
			}
		}
		if err := directRequireMachine(key, input.MachineID); err != nil {
			return nil, err
		}
		return executeTypedTool[genericCapabilityOutput](s.toolExecutor, ctx, ownerID, tool, input)

	case "working_context":
		input, err := decodeDirectArguments[workingContextInput](raw)
		if err != nil {
			return nil, err
		}
		if !isDirectContextRead(input.Action) {
			if err := directRequireScope(key, core.DirectScopeContextWrite); err != nil {
				return nil, err
			}
		}
		if err := directRequireMachine(key, input.MachineID); err != nil {
			return nil, err
		}
		return executeTypedTool[genericCapabilityOutput](s.toolExecutor, ctx, ownerID, tool, input)

	case "artifact_get":
		input, err := decodeDirectArguments[artifactGetInput](raw)
		if err != nil {
			return nil, err
		}
		if input.Action != "get" {
			if err := directRequireScope(key, core.DirectScopeArtifactWrite); err != nil {
				return nil, err
			}
		}
		switch input.Action {
		case "get":
			if key.MachineID != "" {
				artifact, getErr := s.service.GetArtifact(ctx, ownerID, input.ArtifactID)
				if getErr != nil {
					return nil, getErr
				}
				if err := directRequireMachine(key, artifact.MachineID); err != nil {
					return nil, err
				}
			}
		case "uploadFile", "uploadJobLog", "publishFile":
			if err := directRequireMachine(key, input.MachineID); err != nil {
				return nil, err
			}
		default:
			return nil, &directProtocolError{status: http.StatusBadRequest, code: "INVALID_REQUEST", message: "unsupported artifact action"}
		}
		out, err := executeTypedTool[genericCapabilityOutput](s.toolExecutor, ctx, ownerID, tool, input)
		if err != nil {
			return nil, err
		}
		if input.Action == "publishFile" {
			if err := s.decoratePublishedAttachmentResult(ownerID, out.Result); err != nil {
				return nil, err
			}
		}
		return out, nil

	default:
		return nil, &directProtocolError{status: http.StatusNotFound, code: "TOOL_NOT_FOUND", message: "unknown direct tool"}
	}
}

func writeDirectError(w http.ResponseWriter, err error) {
	var directErr *directProtocolError
	if errors.As(err, &directErr) {
		writeJSON(w, directErr.status, apiError{Error: protocolv1.ProtocolError{Code: directErr.code, Message: directErr.message, Retryable: directErr.status >= 500}})
		return
	}
	var requestErr *toolRequestError
	if errors.As(err, &requestErr) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: protocolv1.ProtocolError{Code: "INVALID_REQUEST", Message: requestErr.message, Retryable: false}})
		return
	}
	status := core.ErrorStatus(err)
	if status < 400 || status > 599 {
		status = http.StatusInternalServerError
	}
	message := "request failed"
	if core.IsClientError(err) {
		message = core.ErrorCode(err)
	}
	writeJSON(w, status, apiError{Error: protocolv1.ProtocolError{Code: core.ErrorCode(err), Message: message, Retryable: status >= 500}})
}
