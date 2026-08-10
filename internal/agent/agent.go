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
	codex          *CodexAdapter
	logger         *slog.Logger
	codexStatePath string
}

func New(_ string, logger *slog.Logger) *AgentManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &AgentManager{
		codex:          NewCodexAdapter(logger),
		logger:         logger,
		codexStatePath: defaultCodexDesktopStatePath(),
	}
}

func (m *AgentManager) Close(ctx context.Context) error {
	if m == nil || m.codex == nil {
		return nil
	}
	return m.codex.Close(ctx)
}

func (m *AgentManager) Control(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
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
		root, err := optionalAgentDirectory(input.WorkingDirectory)
		if err != nil {
			return nil, err
		}
		return m.sessionList(ctx, root, input.Limit)
	case "session.get":
		thread, err := m.authorizedThread(ctx, input.SessionID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"session": m.normalizeThread(ctx, thread, nil)}, nil
	case "session.create":
		return m.sessionCreate(ctx, input)
	case "session.send":
		return m.sessionSend(ctx, input)
	case "session.watch":
		if _, err := m.authorizedThread(ctx, input.SessionID); err != nil {
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
		if _, err := m.authorizedThread(ctx, input.SessionID); err != nil {
			return nil, err
		}
		if err := m.codex.InterruptTurn(ctx, input.SessionID, input.TurnID); err != nil {
			return nil, err
		}
		return map[string]any{"sessionId": input.SessionID, "turnId": input.TurnID, "cancelRequested": true}, nil
	case "session.result":
		thread, err := m.authorizedThread(ctx, input.SessionID)
		if err != nil {
			return nil, err
		}
		return normalizeCodexResult(thread), nil
	case "session.rename":
		if _, err := m.authorizedThread(ctx, input.SessionID); err != nil {
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
		if _, err := m.authorizedThread(ctx, input.SessionID); err != nil {
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
	snapshot, err := readCodexDesktopSnapshot(m.codexStatePath)
	if err != nil {
		return nil, err
	}
	projects := make([]map[string]any, 0, len(snapshot.Projects))
	for _, project := range snapshot.Projects {
		projects = append(projects, map[string]any{
			"projectId":        project.ProjectID,
			"name":             project.Name,
			"projectDirectory": project.ProjectDirectory,
			"isGitRepository":  true,
		})
	}
	return map[string]any{"projects": projects}, nil
}

func (m *AgentManager) sessionList(ctx context.Context, root string, limit int) (map[string]any, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	listRoot := root
	listLimit := limit
	var requestedProject agentProjectContext
	if root != "" {
		requestedProject = resolveAgentProjectContext(ctx, root)
		listRoot = ""
		listLimit = 100
	}
	result, err := m.codex.ListThreads(ctx, listRoot, listLimit)
	if err != nil {
		return nil, err
	}
	snapshot, snapshotErr := readCodexDesktopSnapshot(m.codexStatePath)
	if snapshotErr != nil {
		m.logger.Warn("read Codex Desktop project metadata", "error", snapshotErr)
	}
	data, _ := result["data"].([]any)
	sessions := make([]map[string]any, 0, len(data))
	projectCache := map[string]agentProjectContext{}
	for _, raw := range data {
		thread, _ := raw.(map[string]any)
		if len(thread) == 0 {
			continue
		}
		session := m.normalizeThread(ctx, thread, projectCacheWithSnapshot(projectCache, snapshot))
		if root != "" && !sameAgentPath(mapAnyString(session, "projectDirectory"), requestedProject.ProjectDirectory) {
			continue
		}
		sessions = append(sessions, session)
		if len(sessions) >= limit {
			break
		}
	}
	return map[string]any{"sessions": sessions}, nil
}

func (m *AgentManager) sessionCreate(ctx context.Context, input agentControlParams) (map[string]any, error) {
	workingDirectory, err := requiredAgentDirectory(input.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	selectedModel, err := m.resolveModel(ctx, input.Model)
	if err != nil {
		return nil, err
	}
	project := resolveAgentProjectContext(ctx, workingDirectory)
	threadResult, err := m.codex.StartThread(ctx, workingDirectory, project.ProjectDirectory, selectedModel, input.Thinking)
	if err != nil {
		return nil, err
	}
	sessionID := mapNestedString(threadResult, "thread", "id")
	if sessionID == "" {
		return nil, fmt.Errorf("Codex did not return a session ID")
	}
	projectID, projectSynced, syncErr := syncCodexDesktopProject(m.codexStatePath, sessionID, project, time.Now())
	if syncErr != nil {
		m.logger.Warn("sync Codex Desktop project metadata", "sessionId", sessionID, "error", syncErr)
	}
	out := map[string]any{
		"workingDirectory": workingDirectory,
		"projectDirectory": project.ProjectDirectory,
		"sessionId":        sessionID,
		"executionMode":    "bridge_owned",
		"model":            selectedModel,
		"owner":            "node_agent_bridge",
		"phase":            "ready",
		"realtimeChannel":  "session.watch",
	}
	if project.IsGitRepository {
		if projectID == "" {
			projectID = project.ProjectID
		}
		out["projectId"] = projectID
		out["desktopProjectSynced"] = projectSynced
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return out, nil
	}
	turnResult, err := m.codex.StartTurn(ctx, sessionID, input.Prompt, workingDirectory, selectedModel, input.Thinking)
	if err != nil {
		_ = m.codex.ArchiveThread(context.Background(), sessionID)
		if cleanupErr := removeCodexDesktopThreadAssignment(m.codexStatePath, sessionID); cleanupErr != nil {
			m.logger.Warn("remove failed Codex Desktop thread assignment", "sessionId", sessionID, "error", cleanupErr)
		}
		return nil, err
	}
	turnID := mapNestedString(turnResult, "turn", "id")
	out["turnId"] = turnID
	out["phase"] = "running"
	return out, nil
}

func (m *AgentManager) sessionSend(ctx context.Context, input agentControlParams) (map[string]any, error) {
	thread, err := m.authorizedThread(ctx, input.SessionID)
	if err != nil {
		return nil, err
	}
	if threadHasActiveTurn(thread) || m.codex.ActiveTurn(input.SessionID) != "" {
		return nil, node.ErrAgentSessionBusy
	}
	snapshot, snapshotErr := readCodexDesktopSnapshot(m.codexStatePath)
	if snapshotErr != nil {
		m.logger.Warn("read Codex Desktop project metadata", "error", snapshotErr)
	}
	assignment := snapshot.Assignments[input.SessionID]
	workingDirectory := assignment.WorkingDirectory
	if workingDirectory == "" {
		workingDirectory = mapString(thread, "cwd")
	}
	if strings.TrimSpace(input.WorkingDirectory) != "" {
		workingDirectory, err = requiredAgentDirectory(input.WorkingDirectory)
		if err != nil {
			return nil, err
		}
	}
	project := resolveAgentProjectContext(ctx, workingDirectory)
	if assignment.ProjectDirectory != "" && (!project.IsGitRepository || !sameAgentPath(assignment.ProjectDirectory, project.ProjectDirectory)) {
		return nil, fmt.Errorf("workingDirectory belongs to a different project; create a new session instead")
	}
	if project.IsGitRepository {
		if _, _, syncErr := syncCodexDesktopProject(m.codexStatePath, input.SessionID, project, time.Now()); syncErr != nil {
			m.logger.Warn("sync Codex Desktop project metadata", "sessionId", input.SessionID, "error", syncErr)
		}
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

func (m *AgentManager) authorizedThread(ctx context.Context, sessionID string) (map[string]any, error) {
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
	return thread, nil
}

func requiredAgentDirectory(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("absolute workingDirectory is required")
	}
	return optionalAgentDirectory(raw)
}

func optionalAgentDirectory(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	path, err := node.ResolveMachinePath(raw)
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
	for _, key := range []string{"id", "name", "preview", "createdAt", "updatedAt", "lastActivityAt", "archived", "sourceKind", "cwd"} {
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

type threadProjectCache struct {
	contexts map[string]agentProjectContext
	snapshot codexDesktopSnapshot
}

func projectCacheWithSnapshot(contexts map[string]agentProjectContext, snapshot codexDesktopSnapshot) *threadProjectCache {
	return &threadProjectCache{contexts: contexts, snapshot: snapshot}
}

func (m *AgentManager) normalizeThread(ctx context.Context, thread map[string]any, cache *threadProjectCache) map[string]any {
	out := normalizeCodexThread(thread)
	sessionID := mapString(thread, "id")
	workingDirectory := mapString(thread, "cwd")
	if cache == nil {
		snapshot, err := readCodexDesktopSnapshot(m.codexStatePath)
		if err != nil {
			m.logger.Warn("read Codex Desktop project metadata", "error", err)
		}
		cache = projectCacheWithSnapshot(map[string]agentProjectContext{}, snapshot)
	}
	if assignment, ok := cache.snapshot.Assignments[sessionID]; ok {
		if assignment.WorkingDirectory != "" {
			workingDirectory = assignment.WorkingDirectory
		}
		out["workingDirectory"] = workingDirectory
		out["projectDirectory"] = assignment.ProjectDirectory
		out["projectId"] = assignment.ProjectID
		return out
	}
	project, ok := cache.contexts[workingDirectory]
	if !ok {
		project = resolveAgentProjectContext(ctx, workingDirectory)
		cache.contexts[workingDirectory] = project
	}
	projectID := project.ProjectID
	if registeredID := cache.snapshot.ProjectIDByKey[agentPathKey(project.ProjectDirectory)]; registeredID != "" {
		projectID = registeredID
	}
	out["workingDirectory"] = workingDirectory
	out["projectDirectory"] = project.ProjectDirectory
	if project.IsGitRepository {
		out["projectId"] = projectID
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
