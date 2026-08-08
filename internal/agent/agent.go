package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
)

type agentControlParams struct {
	ProviderID       string `json:"providerId,omitempty"`
	SessionID        string `json:"sessionId,omitempty"`
	TurnID           string `json:"turnId,omitempty"`
	Prompt           string `json:"prompt,omitempty"`
	WorkingDirectory string `json:"workingDirectory,omitempty"`
	Model            string `json:"model,omitempty"`
	Thinking         string `json:"thinking,omitempty"`
	Cursor           int64  `json:"cursor,omitempty"`
	WaitSeconds      int64  `json:"waitSeconds,omitempty"`
	Limit            int    `json:"limit,omitempty"`
	Name             string `json:"name,omitempty"`
}

type AgentManager struct {
	dataDir string
	codex   *CodexAdapter
}

func New(dataDir string, logger *slog.Logger) *AgentManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &AgentManager{dataDir: dataDir, codex: NewCodexAdapter(logger)}
}

func (m *AgentManager) Close(ctx context.Context) error {
	if m == nil || m.codex == nil {
		return nil
	}
	return m.codex.Close(ctx)
}

func (m *AgentManager) Control(ctx context.Context, workspaceID, action string, params map[string]any) (map[string]any, error) {
	if m == nil || m.codex == nil {
		return nil, node.ErrAgentProviderUnavailable
	}
	var input agentControlParams
	if err := decodeParams(params, &input); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	providerID := strings.TrimSpace(input.ProviderID)
	if providerID == "" {
		providerID = "codex"
	}
	if providerID != "codex" {
		return nil, fmt.Errorf("unsupported providerId %q", providerID)
	}

	switch action {
	case "providers.list":
		return m.providers(ctx), nil
	case "models.list":
		return m.models(ctx)
	case "projects.list":
		return m.projects()
	case "session.list":
		workspace, err := m.resolveWorkspace(workspaceID)
		if err != nil {
			return nil, err
		}
		return m.sessionList(ctx, workspace, input.Limit)
	case "session.get":
		workspace, err := m.resolveWorkspace(workspaceID)
		if err != nil {
			return nil, err
		}
		thread, err := m.authorizedThread(ctx, workspace, input.SessionID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"session": normalizeCodexThread(thread)}, nil
	case "session.create":
		workspace, err := m.resolveWorkspace(workspaceID)
		if err != nil {
			return nil, err
		}
		return m.sessionCreate(ctx, workspace, input)
	case "session.send":
		workspace, err := m.resolveWorkspace(workspaceID)
		if err != nil {
			return nil, err
		}
		return m.sessionSend(ctx, workspace, input)
	case "session.watch":
		workspace, err := m.resolveWorkspace(workspaceID)
		if err != nil {
			return nil, err
		}
		if _, err := m.authorizedThread(ctx, workspace, input.SessionID); err != nil {
			return nil, err
		}
		if input.WaitSeconds < 0 || input.WaitSeconds > 15 {
			return nil, fmt.Errorf("waitSeconds must be between 0 and 15")
		}
		events, next, truncatedBefore, err := m.codex.Watch(ctx, input.SessionID, input.Cursor, time.Duration(input.WaitSeconds)*time.Second)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sessionId": input.SessionID, "events": events, "nextCursor": next, "truncatedBefore": truncatedBefore}, nil
	case "session.cancel":
		workspace, err := m.resolveWorkspace(workspaceID)
		if err != nil {
			return nil, err
		}
		if _, err := m.authorizedThread(ctx, workspace, input.SessionID); err != nil {
			return nil, err
		}
		if err := m.codex.InterruptTurn(ctx, input.SessionID, input.TurnID); err != nil {
			return nil, err
		}
		return map[string]any{"sessionId": input.SessionID, "turnId": input.TurnID, "cancelRequested": true}, nil
	case "session.result":
		workspace, err := m.resolveWorkspace(workspaceID)
		if err != nil {
			return nil, err
		}
		thread, err := m.authorizedThread(ctx, workspace, input.SessionID)
		if err != nil {
			return nil, err
		}
		return normalizeCodexResult(thread), nil
	case "session.rename":
		workspace, err := m.resolveWorkspace(workspaceID)
		if err != nil {
			return nil, err
		}
		if _, err := m.authorizedThread(ctx, workspace, input.SessionID); err != nil {
			return nil, err
		}
		if strings.TrimSpace(input.Name) == "" || len(input.Name) > 128 {
			return nil, fmt.Errorf("name is required and must be at most 128 characters")
		}
		if err := m.codex.RenameThread(ctx, input.SessionID, input.Name); err != nil {
			return nil, err
		}
		return map[string]any{"sessionId": input.SessionID, "name": input.Name}, nil
	case "session.archive":
		workspace, err := m.resolveWorkspace(workspaceID)
		if err != nil {
			return nil, err
		}
		if _, err := m.authorizedThread(ctx, workspace, input.SessionID); err != nil {
			return nil, err
		}
		if err := m.codex.ArchiveThread(ctx, input.SessionID); err != nil {
			return nil, err
		}
		return map[string]any{"sessionId": input.SessionID, "archived": true}, nil
	default:
		return nil, fmt.Errorf("unsupported agent action %q", action)
	}
}

func (m *AgentManager) providers(ctx context.Context) map[string]any {
	version, err := m.codex.Availability(ctx)
	provider := map[string]any{
		"providerId":         "codex",
		"name":               "Codex",
		"available":          err == nil,
		"executionModes":     []string{"bridge_owned"},
		"credentialLocation": "local_only",
	}
	if err == nil {
		provider["version"] = version
	} else {
		provider["reason"] = "Codex executable is unavailable on this Node"
	}
	return map[string]any{"providers": []any{provider}}
}

func (m *AgentManager) models(ctx context.Context) (map[string]any, error) {
	result, err := m.codex.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	items, _ := result["data"].([]any)
	models := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if len(item) == 0 {
			continue
		}
		model := map[string]any{"providerId": "codex"}
		for _, key := range []string{"id", "model", "name", "displayName", "description", "defaultReasoningEffort", "supportedReasoningEfforts"} {
			if value, ok := item[key]; ok {
				model[key] = value
			}
		}
		models = append(models, model)
	}
	return map[string]any{"models": models}, nil
}

func (m *AgentManager) projects() (map[string]any, error) {
	items, err := node.NewWorkspaceStore(m.dataDir).List()
	if err != nil {
		return nil, err
	}
	projects := make([]map[string]any, 0, len(items))
	for _, item := range items {
		projects = append(projects, map[string]any{
			"projectId":   item.WorkspaceId,
			"workspaceId": item.WorkspaceId,
			"displayName": item.DisplayName,
			"enabled":     item.Enabled,
		})
	}
	return map[string]any{"projects": projects}, nil
}

func (m *AgentManager) sessionList(ctx context.Context, workspace node.WorkspaceRecord, limit int) (map[string]any, error) {
	result, err := m.codex.ListThreads(ctx, workspace.Root, limit)
	if err != nil {
		return nil, err
	}
	data, _ := result["data"].([]any)
	sessions := make([]map[string]any, 0, len(data))
	for _, raw := range data {
		thread, _ := raw.(map[string]any)
		if len(thread) == 0 {
			continue
		}
		cwd := mapString(thread, "cwd")
		if cwd != "" && !node.PathWithin(workspace.Root, cwd) {
			continue
		}
		sessions = append(sessions, normalizeCodexThread(thread))
	}
	return map[string]any{"sessions": sessions}, nil
}

func (m *AgentManager) sessionCreate(ctx context.Context, workspace node.WorkspaceRecord, input agentControlParams) (map[string]any, error) {
	if !workspace.Allows(node.WorkspacePermissionShell) || !workspace.Allows(node.WorkspacePermissionWrite) {
		return nil, node.ErrPermissionDenied
	}
	workingDirectory, err := agentWorkingDirectory(workspace.Root, input.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	selectedModel, err := m.resolveModel(ctx, input.Model)
	if err != nil {
		return nil, err
	}
	threadResult, err := m.codex.StartThread(ctx, workspace.Root, selectedModel, input.Thinking)
	if err != nil {
		return nil, err
	}
	sessionID := mapNestedString(threadResult, "thread", "id")
	if sessionID == "" {
		return nil, fmt.Errorf("Codex did not return a session ID")
	}
	out := map[string]any{
		"projectId":       workspace.WorkspaceID,
		"workspaceId":     workspace.WorkspaceID,
		"sessionId":       sessionID,
		"executionMode":   "bridge_owned",
		"model":           selectedModel,
		"owner":           "node_agent_bridge",
		"phase":           "ready",
		"realtimeChannel": "session.watch",
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return out, nil
	}
	turnResult, err := m.codex.StartTurn(ctx, sessionID, input.Prompt, workingDirectory, selectedModel, input.Thinking)
	if err != nil {
		_ = m.codex.ArchiveThread(context.Background(), sessionID)
		return nil, err
	}
	turnID := mapNestedString(turnResult, "turn", "id")
	out["turnId"] = turnID
	out["phase"] = "running"
	return out, nil
}

func (m *AgentManager) sessionSend(ctx context.Context, workspace node.WorkspaceRecord, input agentControlParams) (map[string]any, error) {
	if !workspace.Allows(node.WorkspacePermissionShell) || !workspace.Allows(node.WorkspacePermissionWrite) {
		return nil, node.ErrPermissionDenied
	}
	thread, err := m.authorizedThread(ctx, workspace, input.SessionID)
	if err != nil {
		return nil, err
	}
	if threadHasActiveTurn(thread) || m.codex.ActiveTurn(input.SessionID) != "" {
		return nil, node.ErrAgentSessionBusy
	}
	workingDirectory, err := agentWorkingDirectory(workspace.Root, input.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	selectedModel := strings.TrimSpace(input.Model)
	if selectedModel != "" {
		selectedModel, err = m.resolveModel(ctx, selectedModel)
		if err != nil {
			return nil, err
		}
	}
	result, err := m.codex.StartTurn(ctx, input.SessionID, input.Prompt, workingDirectory, selectedModel, input.Thinking)
	if err != nil {
		return nil, err
	}
	turnID := mapNestedString(result, "turn", "id")
	return map[string]any{
		"sessionId":     input.SessionID,
		"turnId":        turnID,
		"model":         selectedModel,
		"executionMode": "bridge_owned",
		"owner":         "node_agent_bridge",
		"phase":         "running",
	}, nil
}

func (m *AgentManager) resolveModel(ctx context.Context, requested string) (string, error) {
	result, err := m.codex.ListModels(ctx)
	if err != nil {
		return "", err
	}
	items, _ := result["data"].([]any)
	if len(items) == 0 {
		return "", fmt.Errorf("Codex returned no available models")
	}
	requested = strings.TrimSpace(requested)
	first := ""
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		id := mapString(item, "id")
		if id == "" {
			id = mapString(item, "model")
		}
		if id == "" {
			continue
		}
		if first == "" {
			first = id
		}
		if requested != "" && id == requested {
			return id, nil
		}
	}
	if requested != "" {
		return "", fmt.Errorf("model %q is not available from the current Codex CLI", requested)
	}
	if first == "" {
		return "", fmt.Errorf("Codex returned no usable models")
	}
	return first, nil
}

func (m *AgentManager) resolveWorkspace(workspaceID string) (node.WorkspaceRecord, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return node.WorkspaceRecord{}, fmt.Errorf("workspaceId is required")
	}
	return node.NewWorkspaceStore(m.dataDir).Resolve(workspaceID)
}

func (m *AgentManager) authorizedThread(ctx context.Context, workspace node.WorkspaceRecord, sessionID string) (map[string]any, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	result, err := m.codex.ReadThread(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	thread, _ := result["thread"].(map[string]any)
	if len(thread) == 0 {
		return nil, node.ErrAgentSessionNotFound
	}
	cwd := mapString(thread, "cwd")
	if cwd == "" || !node.PathWithin(workspace.Root, cwd) {
		return nil, node.ErrAgentSessionNotFound
	}
	return thread, nil
}

func agentWorkingDirectory(root, relative string) (string, error) {
	if strings.TrimSpace(relative) == "" || relative == "." {
		return root, nil
	}
	path, err := node.ResolveWorkspacePath(root, relative)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workingDirectory must be a directory")
	}
	return path, nil
}

func normalizeCodexThread(thread map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"id", "name", "preview", "createdAt", "updatedAt", "lastActivityAt", "archived", "sourceKind"} {
		if value, ok := thread[key]; ok {
			out[key] = value
		}
	}
	if id := mapString(thread, "id"); id != "" {
		out["sessionId"] = id
	}
	if status, ok := thread["status"].(map[string]any); ok {
		normalized := map[string]any{}
		for _, key := range []string{"type", "runtime", "phase", "turnStatus"} {
			if value, exists := status[key]; exists {
				normalized[key] = value
			}
		}
		if len(normalized) > 0 {
			out["status"] = normalized
		}
	}
	turns, _ := thread["turns"].([]any)
	if len(turns) > 0 {
		if turn, ok := turns[len(turns)-1].(map[string]any); ok {
			out["latestTurn"] = normalizeCodexTurn(turn)
		}
	}
	return out
}

func normalizeCodexTurn(turn map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"id", "status", "startedAt", "completedAt"} {
		if value, ok := turn[key]; ok {
			out[key] = value
		}
	}
	if message := finalAgentMessageFromCodexTurn(turn); message != "" {
		out["finalAgentMessage"] = message
	}
	return out
}

func normalizeCodexResult(thread map[string]any) map[string]any {
	turns, _ := thread["turns"].([]any)
	if len(turns) == 0 {
		return map[string]any{"sessionId": mapString(thread, "id"), "status": "no_turns"}
	}
	turn, _ := turns[len(turns)-1].(map[string]any)
	out := normalizeCodexTurn(turn)
	out["sessionId"] = mapString(thread, "id")
	return out
}

func threadHasActiveTurn(thread map[string]any) bool {
	turns, _ := thread["turns"].([]any)
	for i := len(turns) - 1; i >= 0; i-- {
		turn, _ := turns[i].(map[string]any)
		status := strings.ToLower(mapString(turn, "status"))
		if status == "inprogress" || status == "in_progress" || status == "running" {
			return true
		}
		if status != "" {
			return false
		}
	}
	return false
}

func finalAgentMessageFromCodexTurn(turn map[string]any) string {
	items, _ := turn["items"].([]any)
	for i := len(items) - 1; i >= 0; i-- {
		item, _ := items[i].(map[string]any)
		if mapString(item, "type") != "agentMessage" {
			continue
		}
		if text := mapString(item, "text"); text != "" {
			return boundedAgentText(text, 64*1024)
		}
	}
	return ""
}

func decodeParams(input map[string]any, output any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(output)
}
