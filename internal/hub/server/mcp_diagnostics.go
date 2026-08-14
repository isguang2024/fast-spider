package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxMCPDiagnosticEvents     = 64
	mcpClientAttributionWindow = 5 * time.Minute
)

type mcpDiagnosticEvent struct {
	At         string `json:"at"`
	Method     string `json:"method"`
	ToolName   string `json:"toolName,omitempty"`
	ClientType string `json:"clientType"`
	Result     string `json:"result"`
	ErrorCode  string `json:"errorCode,omitempty"`
}

type mcpDiagnosticSnapshot struct {
	ServerVersion    string               `json:"serverVersion"`
	GuideVersion     string               `json:"guideVersion"`
	ServerStartedAt  string               `json:"serverStartedAt"`
	LastMCPRequestAt string               `json:"lastMcpRequestAt,omitempty"`
	LastInitializeAt string               `json:"lastInitializeAt,omitempty"`
	LastToolsListAt  string               `json:"lastToolsListAt,omitempty"`
	LastToolCallAt   string               `json:"lastToolCallAt,omitempty"`
	LastToolName     string               `json:"lastToolName,omitempty"`
	ClientType       string               `json:"clientType,omitempty"`
	Result           string               `json:"result,omitempty"`
	ErrorCode        string               `json:"errorCode,omitempty"`
	Diagnosis        string               `json:"diagnosis"`
	RecentEvents     []mcpDiagnosticEvent `json:"recentEvents"`
}

type ownerMCPDiagnostics struct {
	lastMCPRequestAt string
	lastInitializeAt string
	lastToolsListAt  string
	lastToolCallAt   string
	lastToolName     string
	clientType       string
	result           string
	errorCode        string
	recognizedClient string
	recognizedAt     time.Time
	events           []mcpDiagnosticEvent
}

type mcpDiagnosticsStore struct {
	mu              sync.RWMutex
	serverVersion   string
	serverStartedAt string
	owners          map[string]*ownerMCPDiagnostics
}

func newMCPDiagnosticsStore(serverVersion string, startedAt time.Time) *mcpDiagnosticsStore {
	return &mcpDiagnosticsStore{
		serverVersion: serverVersion, serverStartedAt: startedAt.UTC().Format(time.RFC3339),
		owners: make(map[string]*ownerMCPDiagnostics),
	}
}

func (d *mcpDiagnosticsStore) recordAuthenticatedRequest(ownerID, clientType string, at time.Time) {
	if strings.TrimSpace(ownerID) == "" {
		return
	}
	if clientType == "" {
		clientType = "other"
	}
	stamp := at.UTC().Format(time.RFC3339)
	d.mu.Lock()
	defer d.mu.Unlock()
	owner := d.owners[ownerID]
	if owner == nil {
		owner = &ownerMCPDiagnostics{}
		d.owners[ownerID] = owner
	}
	owner.lastMCPRequestAt = stamp
	if clientType != "other" {
		owner.recognizedClient = clientType
		owner.recognizedAt = at
		owner.clientType = clientType
	}
}

func (d *mcpDiagnosticsStore) middleware(ownerID string) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "initialize" && method != "tools/list" && method != "tools/call" {
				return next(ctx, method, req)
			}
			clientType := normalizedMCPClientType(req)
			toolName := ""
			if call, ok := req.(*mcp.CallToolRequest); ok && call.Params != nil {
				toolName = strings.TrimSpace(call.Params.Name)
			}
			result, err := next(ctx, method, req)
			outcome, errorCode := mcpDiagnosticOutcome(result, err)
			d.record(ownerID, method, toolName, clientType, outcome, errorCode, time.Now().UTC())
			return result, err
		}
	}
}

func (d *mcpDiagnosticsStore) record(ownerID, method, toolName, clientType, result, errorCode string, at time.Time) {
	if strings.TrimSpace(ownerID) == "" {
		return
	}
	if clientType == "" {
		clientType = "other"
	}
	stamp := at.UTC().Format(time.RFC3339)
	d.mu.Lock()
	defer d.mu.Unlock()
	owner := d.owners[ownerID]
	if owner == nil {
		owner = &ownerMCPDiagnostics{}
		d.owners[ownerID] = owner
	}
	if clientType != "other" {
		owner.recognizedClient = clientType
		owner.recognizedAt = at
	} else if owner.recognizedClient != "" && !at.Before(owner.recognizedAt) && at.Sub(owner.recognizedAt) <= mcpClientAttributionWindow {
		clientType = owner.recognizedClient
	}
	switch method {
	case "initialize":
		owner.lastInitializeAt = stamp
		owner.clientType = clientType
	case "tools/list":
		owner.lastToolsListAt = stamp
		owner.clientType = clientType
	case "tools/call":
		owner.lastToolCallAt = stamp
		owner.lastToolName = toolName
		owner.clientType = clientType
		owner.result = result
		owner.errorCode = errorCode
	}
	owner.events = append(owner.events, mcpDiagnosticEvent{
		At: stamp, Method: method, ToolName: toolName, ClientType: clientType, Result: result, ErrorCode: errorCode,
	})
	if overflow := len(owner.events) - maxMCPDiagnosticEvents; overflow > 0 {
		copy(owner.events, owner.events[overflow:])
		owner.events = owner.events[:maxMCPDiagnosticEvents]
	}
}

func (d *mcpDiagnosticsStore) snapshot(ownerID string) mcpDiagnosticSnapshot {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := mcpDiagnosticSnapshot{
		ServerVersion: d.serverVersion, GuideVersion: mcpGuideVersion, ServerStartedAt: d.serverStartedAt,
		RecentEvents: make([]mcpDiagnosticEvent, 0),
	}
	owner := d.owners[ownerID]
	if owner == nil {
		out.Diagnosis = "no_initialize"
		return out
	}
	out.LastMCPRequestAt = owner.lastMCPRequestAt
	out.LastInitializeAt = owner.lastInitializeAt
	out.LastToolsListAt = owner.lastToolsListAt
	out.LastToolCallAt = owner.lastToolCallAt
	out.LastToolName = owner.lastToolName
	out.ClientType = owner.clientType
	out.Result = owner.result
	out.ErrorCode = owner.errorCode
	out.RecentEvents = append(out.RecentEvents, owner.events...)
	switch {
	case owner.lastToolCallAt != "" && owner.result == "failure":
		out.Diagnosis = "tool_call_failed"
	case owner.lastToolCallAt != "":
		out.Diagnosis = "tool_call_succeeded"
	case owner.lastToolsListAt != "":
		out.Diagnosis = "tools_listed_no_tool_call"
	case owner.lastInitializeAt != "":
		out.Diagnosis = "initialized_no_tools_list"
	default:
		out.Diagnosis = "no_initialize"
	}
	return out
}

func normalizedMCPClientType(req mcp.Request) string {
	if params, ok := req.GetParams().(*mcp.InitializeParams); ok && params != nil && params.ClientInfo != nil {
		return normalizeMCPClientName(params.ClientInfo.Name)
	}
	if session, ok := req.GetSession().(*mcp.ServerSession); ok {
		if params := session.InitializeParams(); params != nil && params.ClientInfo != nil {
			return normalizeMCPClientName(params.ClientInfo.Name)
		}
	}
	if extra := req.GetExtra(); extra != nil {
		return normalizeMCPClientName(extra.Header.Get("User-Agent"))
	}
	return "other"
}

func normalizeMCPClientName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.Contains(value, "chatgpt"), strings.Contains(value, "openai"):
		return "chatgpt"
	case strings.Contains(value, "codex"):
		return "codex"
	case strings.Contains(value, "mcpcli"), strings.Contains(value, "mcp-cli"):
		return "mcpcli"
	default:
		return "other"
	}
}

func mcpDiagnosticOutcome(result mcp.Result, err error) (string, string) {
	if err != nil {
		return "failure", stableMCPErrorCode(err)
	}
	if call, ok := result.(*mcp.CallToolResult); ok && call.IsError {
		if toolErr := call.GetError(); toolErr != nil {
			return "failure", stableMCPErrorCode(toolErr)
		}
		return "failure", "TOOL_ERROR"
	}
	return "success", ""
}

func stableMCPErrorCode(err error) string {
	if err == nil {
		return ""
	}
	code := core.ErrorCode(err)
	if code != "INTERNAL" {
		return code
	}
	message := err.Error()
	for _, candidate := range []string{
		"INVALID_REQUEST", "NOT_FOUND", "CONNECTION_LOST", "MACHINE_OFFLINE", "DEADLINE_EXCEEDED",
		"ABSOLUTE_PATH_REQUIRED", "BROWSER_REF_STALE", "NODE_UPDATING", "RUNTIME_UNAVAILABLE", "WSL_CWD_UNMAPPABLE", "JOB_NOT_FOUND",
	} {
		if strings.HasPrefix(message, candidate+":") || errors.Is(err, context.DeadlineExceeded) && candidate == "DEADLINE_EXCEEDED" {
			return candidate
		}
	}
	return "INTERNAL"
}
