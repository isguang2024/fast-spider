package agent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
	_ "modernc.org/sqlite"
)

type agentControlParams struct {
	ProviderID            string              `json:"providerId,omitempty"`
	AppType               string              `json:"appType,omitempty"`
	SessionID             string              `json:"sessionId,omitempty"`
	TurnID                string              `json:"turnId,omitempty"`
	RequestID             string              `json:"requestId,omitempty"`
	Prompt                string              `json:"prompt,omitempty"`
	WorkingDirectory      string              `json:"workingDirectory,omitempty"`
	Model                 string              `json:"model,omitempty"`
	Thinking              string              `json:"thinking,omitempty"`
	Cursor                int64               `json:"cursor,omitempty"`
	WaitSeconds           int64               `json:"waitSeconds,omitempty"`
	Limit                 int                 `json:"limit,omitempty"`
	Name                  string              `json:"name,omitempty"`
	ForceReload           bool                `json:"forceReload,omitempty"`
	MarketplaceKinds      []string            `json:"marketplaceKinds,omitempty"`
	PluginName            string              `json:"pluginName,omitempty"`
	MarketplacePath       string              `json:"marketplacePath,omitempty"`
	RemoteMarketplaceName string              `json:"remoteMarketplaceName,omitempty"`
	RemotePluginID        string              `json:"remotePluginId,omitempty"`
	SkillName             string              `json:"skillName,omitempty"`
	NumTurns              int                 `json:"numTurns,omitempty"`
	Objective             string              `json:"objective,omitempty"`
	GoalStatus            string              `json:"goalStatus,omitempty"`
	TokenBudget           int64               `json:"tokenBudget,omitempty"`
	Skills                []agentSkillInput   `json:"skills,omitempty"`
	Images                []string            `json:"images,omitempty"`
	LocalImages           []string            `json:"localImages,omitempty"`
	Mentions              []agentMentionInput `json:"mentions,omitempty"`
	ImageDetail           string              `json:"imageDetail,omitempty"`
	OutputSchema          map[string]any      `json:"outputSchema,omitempty"`
	Decision              string              `json:"decision,omitempty"`
	Answers               map[string][]string `json:"answers,omitempty"`
	ResponseContent       map[string]any      `json:"responseContent,omitempty"`
	PageCursor            string              `json:"pageCursor,omitempty"`
	MCPDetail             string              `json:"mcpDetail,omitempty"`
	Effort                string              `json:"effort,omitempty"`
	Permissions           string              `json:"permissions,omitempty"`
	Personality           string              `json:"personality,omitempty"`
	ServiceTier           string              `json:"serviceTier,omitempty"`
	Summary               string              `json:"summary,omitempty"`
	ReviewType            string              `json:"reviewType,omitempty"`
	ReviewDelivery        string              `json:"reviewDelivery,omitempty"`
	ReviewBranch          string              `json:"reviewBranch,omitempty"`
	ReviewSHA             string              `json:"reviewSha,omitempty"`
	ReviewTitle           string              `json:"reviewTitle,omitempty"`
	ReviewInstructions    string              `json:"reviewInstructions,omitempty"`
}

type agentSkillInput struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type agentMentionInput struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type AgentManager struct {
	codex          *CodexAdapter
	claude         *ClaudeCodeAdapter
	ccswitch       *CCSwitchInspector
	logger         *slog.Logger
	codexStatePath string
}

func New(dataDir string, logger *slog.Logger) *AgentManager {
	if logger == nil {
		logger = slog.Default()
	}
	ccswitch := NewCCSwitchInspector(logger)
	return &AgentManager{
		codex:          NewCodexAdapter(logger),
		claude:         NewClaudeCodeAdapter(dataDir, ccswitch, logger),
		ccswitch:       ccswitch,
		logger:         logger,
		codexStatePath: defaultCodexDesktopStatePath(),
	}
}

func (m *AgentManager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	var firstErr error
	if m.codex != nil {
		if err := m.codex.Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if m.claude != nil {
		if err := m.claude.Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *AgentManager) Control(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	if m == nil {
		return nil, node.ErrAgentProviderUnavailable
	}
	var input agentControlParams
	if err := decodeParams(params, &input); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if action == "routing.status" {
		return m.routingStatus(ctx, input)
	}
	if action == "providers.list" {
		return m.providers(ctx), nil
	}

	providerID := strings.TrimSpace(input.ProviderID)
	if providerID == "" {
		providerID = "codex"
	}
	if providerID == "claude_code" {
		if m.claude == nil {
			return nil, node.ErrAgentProviderUnavailable
		}
		return m.controlClaude(ctx, action, input)
	}
	if providerID != "codex" {
		return nil, fmt.Errorf("unsupported providerId %q", providerID)
	}
	if m.codex == nil {
		return nil, node.ErrAgentProviderUnavailable
	}

	switch action {
	case "providers.list":
		return m.providers(ctx), nil
	case "models.list":
		return m.models(ctx)
	case "provider.capabilities":
		return m.codexCapabilities(ctx)
	case "hooks.list":
		root, err := optionalAgentDirectory(input.WorkingDirectory)
		if err != nil {
			return nil, err
		}
		return m.codex.ListHooks(ctx, root)
	case "permissions.list":
		root, err := optionalAgentDirectory(input.WorkingDirectory)
		if err != nil {
			return nil, err
		}
		if input.Limit < 0 || input.Limit > 100 {
			return nil, fmt.Errorf("limit must be between 0 and 100")
		}
		if len(input.PageCursor) > 4096 {
			return nil, fmt.Errorf("pageCursor must be at most 4096 characters")
		}
		return m.codex.ListPermissionProfiles(ctx, root, input.Limit, input.PageCursor)
	case "mcp.status.list":
		if input.Limit < 0 || input.Limit > 100 {
			return nil, fmt.Errorf("limit must be between 0 and 100")
		}
		if len(input.PageCursor) > 4096 {
			return nil, fmt.Errorf("pageCursor must be at most 4096 characters")
		}
		detail := strings.TrimSpace(input.MCPDetail)
		if detail == "" {
			detail = "toolsAndAuthOnly"
		}
		if !stringInSet(detail, "full", "toolsAndAuthOnly") {
			return nil, fmt.Errorf("mcpDetail must be full or toolsAndAuthOnly")
		}
		result, err := m.codex.ListMCPServerStatus(ctx, input.SessionID, detail, input.Limit, input.PageCursor)
		if err != nil {
			return nil, err
		}
		return normalizeMCPStatus(result), nil
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
		return map[string]any{"session": m.normalizeThread(ctx, thread, nil), "pendingRequests": m.codex.PendingRequests(input.SessionID)}, nil
	case "session.create":
		return m.sessionCreate(ctx, input)
	case "session.send":
		return m.sessionSend(ctx, input)
	case "session.steer":
		if _, err := m.authorizedThread(ctx, input.SessionID); err != nil {
			return nil, err
		}
		if strings.TrimSpace(input.TurnID) == "" {
			return nil, fmt.Errorf("turnId is required for session.steer")
		}
		if err := validateSteerInput(input); err != nil {
			return nil, err
		}
		result, err := m.codex.SteerTurn(ctx, input.SessionID, input.TurnID, buildAgentTurnInputsWithDetail(input.Prompt, input.Skills, input.Images, input.LocalImages, input.Mentions, input.ImageDetail))
		if err != nil {
			return nil, err
		}
		return map[string]any{"sessionId": input.SessionID, "turnId": input.TurnID, "steered": true, "result": result}, nil
	case "session.respond":
		if _, err := m.authorizedThread(ctx, input.SessionID); err != nil {
			return nil, err
		}
		if strings.TrimSpace(input.RequestID) == "" {
			return nil, fmt.Errorf("requestId is required for session.respond")
		}
		return m.codex.RespondPendingRequest(ctx, input.SessionID, input.RequestID, input)
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
		return map[string]any{"sessionId": input.SessionID, "events": events, "nextCursor": next, "truncatedBefore": truncatedBefore, "pendingRequests": m.codex.PendingRequests(input.SessionID)}, nil
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
	case "session.unarchive":
		if _, err := m.authorizedThread(ctx, input.SessionID); err != nil {
			return nil, err
		}
		if err := m.codex.UnarchiveThread(ctx, input.SessionID); err != nil {
			return nil, err
		}
		return map[string]any{"sessionId": input.SessionID, "archived": false}, nil
	case "session.delete":
		if _, err := m.authorizedThread(ctx, input.SessionID); err != nil {
			return nil, err
		}
		if err := m.codex.DeleteThread(ctx, input.SessionID); err != nil {
			return nil, err
		}
		return map[string]any{"sessionId": input.SessionID, "deleted": true}, nil
	case "session.fork":
		if _, err := m.authorizedThread(ctx, input.SessionID); err != nil {
			return nil, err
		}
		workingDirectory := ""
		if strings.TrimSpace(input.WorkingDirectory) != "" {
			var err error
			workingDirectory, err = requiredAgentDirectory(input.WorkingDirectory)
			if err != nil {
				return nil, err
			}
		}
		result, err := m.codex.ForkThread(ctx, input.SessionID, workingDirectory)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sessionId": input.SessionID, "thread": result["thread"], "forked": true}, nil
	case "session.compact":
		if _, err := m.authorizedThread(ctx, input.SessionID); err != nil {
			return nil, err
		}
		if err := m.codex.CompactThread(ctx, input.SessionID); err != nil {
			return nil, err
		}
		return map[string]any{"sessionId": input.SessionID, "compactionStarted": true}, nil
	case "session.rollback":
		if _, err := m.authorizedThread(ctx, input.SessionID); err != nil {
			return nil, err
		}
		if input.NumTurns < 1 || input.NumTurns > 1000 {
			return nil, fmt.Errorf("numTurns must be between 1 and 1000")
		}
		if err := m.codex.RollbackThread(ctx, input.SessionID, input.NumTurns); err != nil {
			return nil, err
		}
		return map[string]any{"sessionId": input.SessionID, "numTurns": input.NumTurns, "rolledBack": true, "workingTreeChanged": false}, nil
	case "session.goal.get":
		if _, err := m.authorizedThread(ctx, input.SessionID); err != nil {
			return nil, err
		}
		return m.codex.GetGoal(ctx, input.SessionID)
	case "session.goal.set":
		if _, err := m.authorizedThread(ctx, input.SessionID); err != nil {
			return nil, err
		}
		if err := validateGoalInput(input); err != nil {
			return nil, err
		}
		return m.codex.SetGoal(ctx, input.SessionID, input.Objective, input.GoalStatus, input.TokenBudget)
	case "session.goal.clear":
		if _, err := m.authorizedThread(ctx, input.SessionID); err != nil {
			return nil, err
		}
		return m.codex.ClearGoal(ctx, input.SessionID)
	case "session.settings.update":
		thread, err := m.authorizedThread(ctx, input.SessionID)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(input.WorkingDirectory) != "" {
			candidate, err := requiredAgentDirectory(input.WorkingDirectory)
			if err != nil {
				return nil, err
			}
			snapshot, snapshotErr := readCodexDesktopSnapshot(m.codexStatePath)
			if snapshotErr != nil {
				m.logger.Warn("read Codex Desktop project metadata", "error", snapshotErr)
			}
			assignment := snapshot.Assignments[input.SessionID]
			currentProjectDirectory := assignment.ProjectDirectory
			if currentProjectDirectory == "" {
				currentProjectDirectory = resolveAgentProjectContext(ctx, mapString(thread, "cwd")).ProjectDirectory
			}
			candidateProject := resolveAgentProjectContext(ctx, candidate)
			if currentProjectDirectory != "" && (!candidateProject.IsGitRepository || !sameAgentPath(currentProjectDirectory, candidateProject.ProjectDirectory)) {
				return nil, fmt.Errorf("workingDirectory belongs to a different project; create a new session instead")
			}
			input.WorkingDirectory = candidate
		}
		if err := m.prepareSettingsInput(ctx, &input); err != nil {
			return nil, err
		}
		return m.codex.UpdateSettings(ctx, input.SessionID, input)
	case "session.review":
		if _, err := m.authorizedThread(ctx, input.SessionID); err != nil {
			return nil, err
		}
		if err := validateReviewInput(input); err != nil {
			return nil, err
		}
		return m.codex.StartReview(ctx, input.SessionID, input)
	case "skills.list":
		root, err := optionalAgentDirectory(input.WorkingDirectory)
		if err != nil {
			return nil, err
		}
		return m.codex.ListSkills(ctx, root, input.ForceReload)
	case "plugins.list":
		root, err := optionalAgentDirectory(input.WorkingDirectory)
		if err != nil {
			return nil, err
		}
		if err := validateMarketplaceKinds(input.MarketplaceKinds); err != nil {
			return nil, err
		}
		return m.codex.ListPlugins(ctx, root, input.MarketplaceKinds)
	case "plugins.installed":
		root, err := optionalAgentDirectory(input.WorkingDirectory)
		if err != nil {
			return nil, err
		}
		return m.codex.ListInstalledPlugins(ctx, root)
	case "plugins.get":
		if err := validatePluginReadInput(input); err != nil {
			return nil, err
		}
		return m.codex.ReadPlugin(ctx, input.PluginName, input.MarketplacePath, input.RemoteMarketplaceName)
	case "plugin.skill.read":
		if err := validatePluginSkillReadInput(input); err != nil {
			return nil, err
		}
		return m.codex.ReadPluginSkill(ctx, input.RemoteMarketplaceName, input.RemotePluginID, input.SkillName)
	default:
		return nil, fmt.Errorf("unsupported agent action %q", action)
	}
}

func (m *AgentManager) controlClaude(ctx context.Context, action string, input agentControlParams) (map[string]any, error) {
	switch action {
	case "models.list":
		return m.claude.Models(ctx)
	case "provider.capabilities":
		return m.claude.Capabilities(ctx)
	case "projects.list":
		return map[string]any{
			"providerId":   "claude_code",
			"projectModel": "working_directory",
			"projects":     []any{},
		}, nil
	case "session.list":
		root, err := optionalAgentDirectory(input.WorkingDirectory)
		if err != nil {
			return nil, err
		}
		return m.claude.List(root, input.Limit), nil
	case "session.get":
		if strings.TrimSpace(input.SessionID) == "" {
			return nil, fmt.Errorf("sessionId is required")
		}
		return m.claude.Get(input.SessionID)
	case "session.create":
		root, err := requiredAgentDirectory(input.WorkingDirectory)
		if err != nil {
			return nil, err
		}
		if err := validateClaudeTurnInput(input); err != nil {
			return nil, err
		}
		return m.claude.Create(ctx, root, input.Prompt, input.Model, firstNonEmptyString(input.Thinking, input.Effort), input.Name, input.OutputSchema)
	case "session.send":
		if strings.TrimSpace(input.SessionID) == "" {
			return nil, fmt.Errorf("sessionId is required")
		}
		root, err := optionalAgentDirectory(input.WorkingDirectory)
		if err != nil {
			return nil, err
		}
		if err := validateClaudeTurnInput(input); err != nil {
			return nil, err
		}
		return m.claude.Send(ctx, input.SessionID, input.Prompt, root, input.Model, firstNonEmptyString(input.Thinking, input.Effort), input.OutputSchema)
	case "session.watch":
		if strings.TrimSpace(input.SessionID) == "" {
			return nil, fmt.Errorf("sessionId is required")
		}
		if input.WaitSeconds < 0 || input.WaitSeconds > 15 {
			return nil, fmt.Errorf("waitSeconds must be between 0 and 15")
		}
		if _, err := m.claude.Get(input.SessionID); err != nil {
			return nil, err
		}
		events, next, truncatedBefore, err := m.claude.Watch(ctx, input.SessionID, input.Cursor, time.Duration(input.WaitSeconds)*time.Second)
		if err != nil {
			return nil, err
		}
		return map[string]any{"providerId": "claude_code", "sessionId": input.SessionID, "events": events, "nextCursor": next, "truncatedBefore": truncatedBefore}, nil
	case "session.cancel":
		if strings.TrimSpace(input.SessionID) == "" {
			return nil, fmt.Errorf("sessionId is required")
		}
		if err := m.claude.Cancel(input.SessionID, input.TurnID); err != nil {
			return nil, err
		}
		return map[string]any{"providerId": "claude_code", "sessionId": input.SessionID, "turnId": input.TurnID, "cancelRequested": true}, nil
	case "session.result":
		if strings.TrimSpace(input.SessionID) == "" {
			return nil, fmt.Errorf("sessionId is required")
		}
		return m.claude.Result(input.SessionID)
	case "session.rename":
		return m.claude.Rename(input.SessionID, input.Name)
	case "session.archive":
		return m.claude.SetArchived(input.SessionID, true)
	case "session.unarchive":
		return m.claude.SetArchived(input.SessionID, false)
	default:
		return nil, fmt.Errorf("agent action %q is not supported by provider claude_code", action)
	}
}

func validateClaudeTurnInput(input agentControlParams) error {
	if strings.TrimSpace(input.Prompt) == "" {
		return fmt.Errorf("Claude Code currently requires prompt text for session.create/session.send")
	}
	if len(input.Prompt) > 200000 {
		return fmt.Errorf("prompt must be at most 200000 characters")
	}
	if len(input.Skills) > 0 || len(input.Images) > 0 || len(input.LocalImages) > 0 || len(input.Mentions) > 0 || strings.TrimSpace(input.ImageDetail) != "" {
		return fmt.Errorf("Claude Code provider currently accepts text input only; Skills/Image/Mention inputs are not mapped implicitly")
	}
	if len(input.Model) > 256 {
		return fmt.Errorf("model must be at most 256 characters")
	}
	effort := firstNonEmptyString(input.Thinking, input.Effort)
	if effort != "" && !stringInSet(effort, "low", "medium", "high", "xhigh", "max") {
		return fmt.Errorf("Claude Code effort must be low, medium, high, xhigh, or max")
	}
	if input.OutputSchema != nil {
		if err := validateOutputSchema(input.OutputSchema); err != nil {
			return err
		}
	}
	return nil
}

func (m *AgentManager) providers(ctx context.Context) map[string]any {
	providers := make([]any, 0, 2)

	codexVersion, codexErr := m.codex.Availability(ctx)
	codexProvider := map[string]any{
		"providerId":         "codex",
		"name":               "Codex",
		"available":          codexErr == nil,
		"executionModes":     []string{"bridge_owned"},
		"credentialLocation": "local_only",
		"supportedActions": []string{
			"models.list", "provider.capabilities", "projects.list", "skills.list", "hooks.list", "permissions.list",
			"plugins.list", "plugins.installed", "plugins.get", "plugin.skill.read", "mcp.status.list",
			"session.list", "session.get", "session.create", "session.send", "session.steer", "session.respond", "session.watch", "session.cancel", "session.result", "session.rename", "session.archive", "session.unarchive", "session.delete", "session.fork", "session.compact", "session.rollback", "session.goal.get", "session.goal.set", "session.goal.clear", "session.settings.update", "session.review",
		},
	}
	if codexErr == nil {
		codexProvider["version"] = codexVersion
	} else {
		codexProvider["reason"] = "Codex executable is unavailable on this Node"
	}
	if m.ccswitch != nil {
		if route, routeErr := m.ccswitch.InspectApp(ctx, "codex"); routeErr == nil {
			codexProvider["route"] = route
		}
	}
	providers = append(providers, codexProvider)

	claudeVersion, claudeErr := m.claude.Availability(ctx)
	claudeProvider := map[string]any{
		"providerId":         "claude_code",
		"name":               "Claude Code",
		"available":          claudeErr == nil,
		"runtimeAvailable":   claudeErr == nil,
		"executionHealth":    "unknown_until_turn",
		"authConfiguration":  m.claude.AuthConfiguration(ctx),
		"executionModes":     []string{"cli_stream_json"},
		"credentialLocation": "local_or_cc_switch",
		"sessionPersistence": "native_claude_session_id+fast_spider_index",
		"supportedActions": []string{
			"models.list", "provider.capabilities", "projects.list",
			"session.list", "session.get", "session.create", "session.send", "session.watch", "session.cancel", "session.result", "session.rename", "session.archive", "session.unarchive",
		},
	}
	if claudeErr == nil {
		claudeProvider["version"] = claudeVersion
	} else {
		claudeProvider["reason"] = "Claude Code executable is unavailable on this Node"
	}
	if m.ccswitch != nil {
		if route, routeErr := m.ccswitch.InspectApp(ctx, "claude"); routeErr == nil {
			claudeProvider["route"] = route
		}
	}
	providers = append(providers, claudeProvider)

	return map[string]any{"providers": providers, "routingSource": "cc_switch_db"}
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
		model := map[string]any{"providerId": "codex", "source": "codex_app_server_model_list", "authoritative": true}
		for _, key := range []string{
			"id", "model", "name", "displayName", "description", "hidden", "isDefault",
			"defaultReasoningEffort", "supportedReasoningEfforts", "inputModalities", "supportsPersonality",
			"serviceTiers", "defaultServiceTier", "upgrade", "upgradeInfo", "availabilityNux",
		} {
			if value, ok := item[key]; ok {
				model[key] = value
			}
		}
		models = append(models, model)
	}
	out := map[string]any{"models": models, "source": "codex_app_server_model_list", "authoritative": true}
	if m.ccswitch != nil {
		if route, routeErr := m.ccswitch.InspectApp(ctx, "codex"); routeErr == nil {
			out["route"] = route
			if current, ok := route["currentProvider"].(map[string]any); ok {
				if upstreamModels, ok := current["models"].([]map[string]any); ok && len(upstreamModels) > 0 {
					out["upstreamModels"] = upstreamModels
				}
			}
		}
	}
	return out, nil
}

func (m *AgentManager) codexCapabilities(ctx context.Context) (map[string]any, error) {
	native, err := m.codex.ProviderCapabilities(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"providerId":          "codex",
		"harnessCapabilities": native,
		"source":              "codex_app_server+cc_switch_db",
		"authoritativeInputs": true,
		"derived":             true,
	}
	var route map[string]any
	if m.ccswitch != nil {
		if current, routeErr := m.ccswitch.InspectApp(ctx, "codex"); routeErr == nil {
			route = current
			out["route"] = current
		}
	}
	mode := "direct"
	var routed map[string]any
	if route != nil {
		if value, ok := route["routingMode"].(string); ok && value != "" {
			mode = value
		}
		routed, _ = route["effectiveCapabilities"].(map[string]any)
	}
	effective := map[string]any{}
	for _, key := range []string{"webSearch", "imageGeneration", "namespaceTools"} {
		supported, _ := native[key].(bool)
		if !supported {
			effective[key] = capabilityState("unsupported", "Codex app-server reports this harness capability as unavailable")
			continue
		}
		if mode != "cc_switch" {
			effective[key] = capabilityState("supported", "Codex app-server reports this capability for the direct route")
			continue
		}
		routeState, _ := routed[key].(map[string]any)
		state, _ := routeState["state"].(string)
		reason, _ := routeState["reason"].(string)
		switch state {
		case "supported":
			effective[key] = capabilityState("supported", firstNonEmptyString(reason, "supported by both Codex harness and current routed provider"))
		case "unsupported":
			effective[key] = capabilityState("unsupported", firstNonEmptyString(reason, "current CC Switch route does not preserve this capability"))
		default:
			effective[key] = capabilityState("unknown", firstNonEmptyString(reason, "Codex harness supports this capability, but the current routed provider/conversion has not proven it"))
		}
	}
	for _, key := range []string{"toolCalls", "mcp", "vision", "thinking", "resume"} {
		if value, ok := routed[key]; ok {
			effective[key] = value
		}
	}
	out["effectiveCapabilities"] = effective
	return out, nil
}

func capabilityState(state, reason string) map[string]any {
	out := map[string]any{"state": state}
	if strings.TrimSpace(reason) != "" {
		out["reason"] = reason
	}
	return out
}

func normalizeMCPStatus(result map[string]any) map[string]any {
	data, _ := result["data"].([]any)
	servers := make([]map[string]any, 0, len(data))
	for _, raw := range data {
		item, _ := raw.(map[string]any)
		if len(item) == 0 {
			continue
		}
		server := map[string]any{}
		for _, key := range []string{"name", "authStatus"} {
			if value, ok := item[key]; ok {
				server[key] = value
			}
		}
		if info, ok := item["serverInfo"].(map[string]any); ok {
			summary := map[string]any{}
			for _, key := range []string{"name", "title", "version", "description", "websiteUrl"} {
				if value, exists := info[key]; exists {
					summary[key] = value
				}
			}
			if len(summary) > 0 {
				server["serverInfo"] = summary
			}
		}
		if tools, ok := item["tools"].(map[string]any); ok {
			names := make([]string, 0, len(tools))
			for name := range tools {
				names = append(names, name)
			}
			sort.Strings(names)
			if len(names) > 256 {
				server["toolsTruncated"] = true
				names = names[:256]
			}
			server["tools"] = names
			server["toolCount"] = len(tools)
		}
		server["resources"] = summarizeMCPResources(item["resources"], "uri")
		server["resourceTemplates"] = summarizeMCPResources(item["resourceTemplates"], "uriTemplate")
		servers = append(servers, server)
	}
	out := map[string]any{"servers": servers}
	if next, ok := result["nextCursor"]; ok {
		out["nextCursor"] = next
	}
	return out
}

func summarizeMCPResources(raw any, locatorKey string) []map[string]any {
	items, _ := raw.([]any)
	if len(items) == 0 {
		return nil
	}
	limit := len(items)
	if limit > 128 {
		limit = 128
	}
	out := make([]map[string]any, 0, limit)
	for _, rawItem := range items[:limit] {
		item, _ := rawItem.(map[string]any)
		if len(item) == 0 {
			continue
		}
		summary := map[string]any{}
		for _, key := range []string{"name", "title", "description", "mimeType", locatorKey} {
			if value, ok := item[key]; ok {
				summary[key] = value
			}
		}
		out = append(out, summary)
	}
	return out
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
	if !hasTurnInput(input) {
		if input.OutputSchema != nil {
			_ = m.codex.ArchiveThread(context.Background(), sessionID)
			return nil, fmt.Errorf("outputSchema requires at least one turn input")
		}
		return out, nil
	}
	if err := validateTurnInputs(input); err != nil {
		_ = m.codex.ArchiveThread(context.Background(), sessionID)
		return nil, err
	}
	turnResult, err := m.codex.StartTurnWithOptions(ctx, sessionID, buildAgentTurnInputsWithDetail(input.Prompt, input.Skills, input.Images, input.LocalImages, input.Mentions, input.ImageDetail), codexTurnOptions{
		WorkingDirectory: workingDirectory,
		Model:            selectedModel,
		Effort:           input.Thinking,
		Summary:          input.Summary,
		Personality:      input.Personality,
		ServiceTier:      input.ServiceTier,
		OutputSchema:     input.OutputSchema,
	})
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
	if err := validateTurnInputs(input); err != nil {
		return nil, err
	}
	turnResult, err := m.codex.StartTurnWithOptions(ctx, input.SessionID, buildAgentTurnInputsWithDetail(input.Prompt, input.Skills, input.Images, input.LocalImages, input.Mentions, input.ImageDetail), codexTurnOptions{
		WorkingDirectory: workingDirectory,
		Model:            selectedModel,
		Effort:           input.Thinking,
		Summary:          input.Summary,
		Personality:      input.Personality,
		ServiceTier:      input.ServiceTier,
		OutputSchema:     input.OutputSchema,
	})
	if err != nil {
		return nil, err
	}
	turnID := mapNestedString(turnResult, "turn", "id")
	return map[string]any{
		"sessionId":     input.SessionID,
		"turnId":        turnID,
		"model":         selectedModel,
		"executionMode": "bridge_owned",
		"owner":         "node_agent_bridge",
		"phase":         "running",
	}, nil
}

func hasTurnInput(input agentControlParams) bool {
	return strings.TrimSpace(input.Prompt) != "" || len(input.Skills) > 0 || len(input.Images) > 0 || len(input.LocalImages) > 0 || len(input.Mentions) > 0
}

func validateTurnInputs(input agentControlParams) error {
	if strings.TrimSpace(input.Prompt) == "" && len(input.Skills) == 0 && len(input.Images) == 0 && len(input.LocalImages) == 0 && len(input.Mentions) == 0 {
		return fmt.Errorf("at least one turn input is required")
	}
	if detail := strings.TrimSpace(input.ImageDetail); detail != "" && !stringInSet(detail, "auto", "low", "high", "original") {
		return fmt.Errorf("imageDetail must be auto, low, high, or original")
	}
	if effort := strings.TrimSpace(input.Thinking); effort != "" && !stringInSet(effort, "low", "medium", "high", "xhigh") {
		return fmt.Errorf("thinking must be low, medium, high, or xhigh")
	}
	if personality := strings.TrimSpace(input.Personality); personality != "" && !stringInSet(personality, "none", "friendly", "pragmatic") {
		return fmt.Errorf("personality must be none, friendly, or pragmatic")
	}
	if len(input.ServiceTier) > 128 {
		return fmt.Errorf("serviceTier must be at most 128 characters")
	}
	if summary := strings.TrimSpace(input.Summary); summary != "" && !stringInSet(summary, "auto", "concise", "detailed", "none") {
		return fmt.Errorf("summary must be auto, concise, detailed, or none")
	}
	if len(input.Skills) > 32 || len(input.Images) > 32 || len(input.LocalImages) > 32 || len(input.Mentions) > 32 {
		return fmt.Errorf("turn input collection exceeds 32 items")
	}
	for _, skill := range input.Skills {
		if strings.TrimSpace(skill.Name) == "" || len(skill.Name) > 256 || strings.TrimSpace(skill.Path) == "" {
			return fmt.Errorf("skill name and path are required; name must be at most 256 characters")
		}
		if err := validateAgentInputPath(skill.Path, true); err != nil {
			return fmt.Errorf("skill path: %w", err)
		}
	}
	for _, mention := range input.Mentions {
		if strings.TrimSpace(mention.Name) == "" || len(mention.Name) > 256 || strings.TrimSpace(mention.Path) == "" {
			return fmt.Errorf("mention name and path are required; name must be at most 256 characters")
		}
		if err := validateAgentInputPath(mention.Path, false); err != nil {
			return fmt.Errorf("mention path: %w", err)
		}
	}
	for _, path := range input.LocalImages {
		if err := validateAgentInputPath(path, true); err != nil {
			return fmt.Errorf("localImage path: %w", err)
		}
	}
	for _, raw := range input.Images {
		if len(raw) > 8192 {
			return fmt.Errorf("image URL must be at most 8192 characters")
		}
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("images must be absolute http(s) URLs")
		}
	}
	return nil
}

func validateSteerInput(input agentControlParams) error {
	if err := validateTurnInputs(input); err != nil {
		return err
	}
	if input.OutputSchema != nil || strings.TrimSpace(input.Model) != "" || strings.TrimSpace(input.Thinking) != "" || strings.TrimSpace(input.WorkingDirectory) != "" || strings.TrimSpace(input.Personality) != "" || strings.TrimSpace(input.ServiceTier) != "" || strings.TrimSpace(input.Summary) != "" {
		return fmt.Errorf("session.steer only accepts turn inputs and imageDetail; it does not change model, cwd, outputSchema, personality, serviceTier, or summary")
	}
	return nil
}

func validateAgentInputPath(raw string, regularFile bool) error {
	path, err := node.ResolveMachinePath(raw)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if regularFile && !info.Mode().IsRegular() {
		return fmt.Errorf("path must identify a regular file")
	}
	return nil
}

func validateGoalInput(input agentControlParams) error {
	objective := strings.TrimSpace(input.Objective)
	status := strings.TrimSpace(input.GoalStatus)
	if objective == "" && status == "" && input.TokenBudget == 0 {
		return fmt.Errorf("objective, goalStatus, or tokenBudget is required")
	}
	if len(objective) > 4096 {
		return fmt.Errorf("objective must be at most 4096 characters")
	}
	if status != "" && !stringInSet(status, "active", "paused", "blocked", "usageLimited", "budgetLimited", "complete") {
		return fmt.Errorf("goalStatus must be active, paused, blocked, usageLimited, budgetLimited, or complete")
	}
	if input.TokenBudget < 0 || input.TokenBudget > 1_000_000_000 {
		return fmt.Errorf("tokenBudget must be between 0 and 1000000000")
	}
	return nil
}

func (m *AgentManager) prepareSettingsInput(ctx context.Context, input *agentControlParams) error {
	if input == nil {
		return fmt.Errorf("settings are required")
	}
	changed := false
	if strings.TrimSpace(input.WorkingDirectory) != "" {
		cwd, err := requiredAgentDirectory(input.WorkingDirectory)
		if err != nil {
			return err
		}
		input.WorkingDirectory = cwd
		changed = true
	}
	if strings.TrimSpace(input.Model) != "" {
		model, err := m.resolveModel(ctx, input.Model)
		if err != nil {
			return err
		}
		input.Model = model
		changed = true
	}
	if effort := strings.TrimSpace(input.Effort); effort != "" {
		if !stringInSet(effort, "low", "medium", "high", "xhigh") {
			return fmt.Errorf("effort must be low, medium, high, or xhigh")
		}
		changed = true
	}
	if permissions := strings.TrimSpace(input.Permissions); permissions != "" {
		if len(permissions) > 256 {
			return fmt.Errorf("permissions must be at most 256 characters")
		}
		changed = true
	}
	if personality := strings.TrimSpace(input.Personality); personality != "" {
		if !stringInSet(personality, "none", "friendly", "pragmatic") {
			return fmt.Errorf("personality must be none, friendly, or pragmatic")
		}
		changed = true
	}
	if tier := strings.TrimSpace(input.ServiceTier); tier != "" {
		if len(tier) > 128 {
			return fmt.Errorf("serviceTier must be at most 128 characters")
		}
		changed = true
	}
	if summary := strings.TrimSpace(input.Summary); summary != "" {
		if !stringInSet(summary, "auto", "concise", "detailed", "none") {
			return fmt.Errorf("summary must be auto, concise, detailed, or none")
		}
		changed = true
	}
	if !changed {
		return fmt.Errorf("at least one stable thread setting is required")
	}
	return nil
}

func validateReviewInput(input agentControlParams) error {
	delivery := strings.TrimSpace(input.ReviewDelivery)
	if delivery != "" && !stringInSet(delivery, "inline", "detached") {
		return fmt.Errorf("reviewDelivery must be inline or detached")
	}
	target := strings.TrimSpace(input.ReviewType)
	if target == "" {
		target = "uncommittedChanges"
	}
	switch target {
	case "uncommittedChanges":
		return nil
	case "baseBranch":
		if strings.TrimSpace(input.ReviewBranch) == "" || len(input.ReviewBranch) > 512 {
			return fmt.Errorf("reviewBranch is required for reviewType=baseBranch and must be at most 512 characters")
		}
	case "commit":
		if strings.TrimSpace(input.ReviewSHA) == "" || len(input.ReviewSHA) > 128 {
			return fmt.Errorf("reviewSha is required for reviewType=commit and must be at most 128 characters")
		}
		if len(input.ReviewTitle) > 512 {
			return fmt.Errorf("reviewTitle must be at most 512 characters")
		}
	case "custom":
		if strings.TrimSpace(input.ReviewInstructions) == "" || len(input.ReviewInstructions) > 16*1024 {
			return fmt.Errorf("reviewInstructions is required for reviewType=custom and must be at most 16384 characters")
		}
	default:
		return fmt.Errorf("reviewType must be uncommittedChanges, baseBranch, commit, or custom")
	}
	return nil
}

func validateMarketplaceKinds(kinds []string) error {
	if len(kinds) > 8 {
		return fmt.Errorf("marketplaceKinds exceeds 8 items")
	}
	for _, kind := range kinds {
		if !stringInSet(strings.TrimSpace(kind), "local", "vertical", "workspace-directory", "shared-with-me", "created-by-me-remote") {
			return fmt.Errorf("unsupported marketplace kind %q", kind)
		}
	}
	return nil
}

func validatePluginReadInput(input agentControlParams) error {
	if strings.TrimSpace(input.PluginName) == "" || len(input.PluginName) > 256 {
		return fmt.Errorf("pluginName is required and must be at most 256 characters")
	}
	if len(input.RemoteMarketplaceName) > 256 {
		return fmt.Errorf("remoteMarketplaceName must be at most 256 characters")
	}
	if strings.TrimSpace(input.MarketplacePath) != "" {
		if _, err := node.ResolveMachinePath(input.MarketplacePath); err != nil {
			return fmt.Errorf("marketplacePath: %w", err)
		}
	}
	return nil
}

func validatePluginSkillReadInput(input agentControlParams) error {
	if strings.TrimSpace(input.RemoteMarketplaceName) == "" || len(input.RemoteMarketplaceName) > 256 {
		return fmt.Errorf("remoteMarketplaceName is required and must be at most 256 characters")
	}
	if strings.TrimSpace(input.RemotePluginID) == "" || len(input.RemotePluginID) > 512 {
		return fmt.Errorf("remotePluginId is required and must be at most 512 characters")
	}
	if strings.TrimSpace(input.SkillName) == "" || len(input.SkillName) > 256 {
		return fmt.Errorf("skillName is required and must be at most 256 characters")
	}
	return nil
}

func stringInSet(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
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
	defaultID := ""
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
		if isDefault, _ := item["isDefault"].(bool); isDefault && defaultID == "" {
			defaultID = id
		}
		if requested != "" && id == requested {
			return id, nil
		}
	}
	if requested != "" {
		return "", fmt.Errorf("model %q is not available from the current Codex CLI", requested)
	}
	if defaultID != "" {
		return defaultID, nil
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

type CCSwitchInspector struct {
	logger       *slog.Logger
	dbPath       string
	settingsPath string
}

func NewCCSwitchInspector(logger *slog.Logger) *CCSwitchInspector {
	if logger == nil {
		logger = slog.Default()
	}
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".cc-switch")
	return &CCSwitchInspector{
		logger:       logger,
		dbPath:       filepath.Join(root, "cc-switch.db"),
		settingsPath: filepath.Join(root, "settings.json"),
	}
}

func (m *AgentManager) routingStatus(ctx context.Context, input agentControlParams) (map[string]any, error) {
	if m == nil || m.ccswitch == nil {
		return map[string]any{"available": false, "source": "cc_switch"}, nil
	}
	appType := strings.TrimSpace(input.AppType)
	if appType != "" {
		if !stringInSet(appType, "claude", "codex", "claude-desktop") {
			return nil, fmt.Errorf("appType must be claude, codex, or claude-desktop")
		}
		route, err := m.ccswitch.InspectApp(ctx, appType)
		if err != nil {
			return nil, err
		}
		return map[string]any{"available": route["available"], "route": route, "source": "cc_switch_db", "authoritative": true}, nil
	}
	routes := make([]map[string]any, 0, 3)
	for _, app := range []string{"claude", "codex", "claude-desktop"} {
		route, err := m.ccswitch.InspectApp(ctx, app)
		if err != nil {
			m.logger.Warn("inspect CC Switch route", "appType", app, "error", err)
			route = map[string]any{"appType": app, "available": false, "error": boundedAgentText(err.Error(), 512)}
		}
		routes = append(routes, route)
	}
	return map[string]any{"available": true, "routes": routes, "source": "cc_switch_db", "authoritative": true}, nil
}

func (i *CCSwitchInspector) InspectApp(ctx context.Context, appType string) (map[string]any, error) {
	appType = strings.TrimSpace(appType)
	if appType == "" {
		return nil, fmt.Errorf("appType is required")
	}
	out := map[string]any{
		"appType":       appType,
		"harness":       ccSwitchHarnessName(appType),
		"source":        "cc_switch_db",
		"authoritative": true,
		"capturedAt":    protocolTimestampNow(),
	}
	if info, err := os.Stat(i.dbPath); err != nil || !info.Mode().IsRegular() {
		out["available"] = false
		out["reason"] = "cc-switch.db is unavailable"
		return out, nil
	}
	settings := i.readDeviceSettings()
	if enabled, ok := settings["enableLocalProxy"].(bool); ok {
		out["localProxyEnabled"] = enabled
	}
	if current := ccSwitchCurrentProviderSetting(settings, appType); current != "" {
		out["deviceCurrentProviderId"] = current
	}

	dsn := "file:" + filepath.ToSlash(i.dbPath) + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open CC Switch database: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("read CC Switch database: %w", err)
	}
	out["available"] = true

	proxy := inspectCCSwitchProxy(ctx, db, appType)
	out["proxy"] = proxy
	providers, current, err := inspectCCSwitchProviders(ctx, db, appType)
	if err != nil {
		return nil, err
	}
	out["providers"] = providers
	if current != nil {
		out["currentProvider"] = current
		if dbCurrent, _ := current["providerId"].(string); dbCurrent != "" {
			out["dbCurrentProviderId"] = dbCurrent
			if deviceCurrent, _ := out["deviceCurrentProviderId"].(string); deviceCurrent != "" {
				out["selectionConsistent"] = deviceCurrent == dbCurrent
			}
		}
		out["routingMode"] = ccSwitchRoutingMode(proxy, current)
		out["effectiveCapabilities"] = deriveCCSwitchEffectiveCapabilities(appType, out["routingMode"], current)
	} else {
		out["routingMode"] = "direct"
	}
	if last := inspectCCSwitchLastRequest(ctx, db, appType); last != nil {
		out["lastRequest"] = last
	}
	return out, nil
}

func (i *CCSwitchInspector) readDeviceSettings() map[string]any {
	raw, err := os.ReadFile(i.settingsPath)
	if err != nil || len(raw) > 256<<10 {
		return map[string]any{}
	}
	var settings map[string]any
	if json.Unmarshal(raw, &settings) != nil {
		return map[string]any{}
	}
	return settings
}

func ccSwitchHarnessName(appType string) string {
	switch appType {
	case "claude":
		return "claude_code"
	case "claude-desktop":
		return "claude_desktop"
	case "codex":
		return "codex"
	default:
		return appType
	}
}

func ccSwitchCurrentProviderSetting(settings map[string]any, appType string) string {
	key := ""
	switch appType {
	case "claude":
		key = "currentProviderClaude"
	case "claude-desktop":
		key = "currentProviderClaudeDesktop"
	case "codex":
		key = "currentProviderCodex"
	}
	value, _ := settings[key].(string)
	return strings.TrimSpace(value)
}

func inspectCCSwitchProxy(ctx context.Context, db *sql.DB, appType string) map[string]any {
	out := map[string]any{"takeoverEnabled": false, "liveTakeoverActive": false, "proxyEnabled": false}
	var proxyEnabled, takeoverEnabled, liveTakeoverActive, autoFailover int
	var address string
	var port int
	err := db.QueryRowContext(ctx, `SELECT proxy_enabled, enabled, live_takeover_active, listen_address, listen_port, auto_failover_enabled FROM proxy_config WHERE app_type = ?`, appType).Scan(&proxyEnabled, &takeoverEnabled, &liveTakeoverActive, &address, &port, &autoFailover)
	if err != nil {
		return out
	}
	out["proxyEnabled"] = proxyEnabled != 0
	out["takeoverEnabled"] = takeoverEnabled != 0
	out["liveTakeoverActive"] = liveTakeoverActive != 0
	out["autoFailoverEnabled"] = autoFailover != 0
	out["listenAddress"] = address
	out["listenPort"] = port
	return out
}

func inspectCCSwitchProviders(ctx context.Context, db *sql.DB, appType string) ([]map[string]any, map[string]any, error) {
	endpointByProvider := map[string]string{}
	if rows, err := db.QueryContext(ctx, `SELECT provider_id, url FROM provider_endpoints WHERE app_type = ? ORDER BY id`, appType); err == nil {
		for rows.Next() {
			var id, endpoint string
			if rows.Scan(&id, &endpoint) == nil && endpointByProvider[id] == "" {
				endpointByProvider[id] = endpoint
			}
		}
		rows.Close()
	}
	healthByProvider := map[string]map[string]any{}
	if rows, err := db.QueryContext(ctx, `SELECT provider_id, is_healthy, consecutive_failures, last_success_at, last_failure_at FROM provider_health WHERE app_type = ?`, appType); err == nil {
		for rows.Next() {
			var id string
			var healthy, failures int
			var success, failure sql.NullString
			if rows.Scan(&id, &healthy, &failures, &success, &failure) == nil {
				h := map[string]any{"healthy": healthy != 0, "consecutiveFailures": failures}
				if success.Valid {
					h["lastSuccessAt"] = success.String
				}
				if failure.Valid {
					h["lastFailureAt"] = failure.String
				}
				healthByProvider[id] = h
			}
		}
		rows.Close()
	}

	rows, err := db.QueryContext(ctx, `SELECT id, name, settings_config, category, meta, is_current, in_failover_queue, provider_type FROM providers WHERE app_type = ? ORDER BY is_current DESC, sort_index ASC, name ASC LIMIT 128`, appType)
	if err != nil {
		return nil, nil, fmt.Errorf("read CC Switch providers: %w", err)
	}
	defer rows.Close()
	providers := make([]map[string]any, 0)
	var current map[string]any
	for rows.Next() {
		var id, name, settingsRaw, metaRaw string
		var category, providerType sql.NullString
		var isCurrent, failover int
		if err := rows.Scan(&id, &name, &settingsRaw, &category, &metaRaw, &isCurrent, &failover, &providerType); err != nil {
			return nil, nil, err
		}
		settings := decodeCCSwitchJSON(settingsRaw)
		meta := decodeCCSwitchJSON(metaRaw)
		provider := map[string]any{
			"providerId":        id,
			"name":              name,
			"appType":           appType,
			"isCurrent":         isCurrent != 0,
			"inFailoverQueue":   failover != 0,
			"credentialPresent": ccSwitchCredentialPresent(settings),
		}
		if category.Valid && category.String != "" {
			provider["category"] = category.String
		}
		if providerType.Valid && providerType.String != "" {
			provider["providerType"] = providerType.String
		}
		apiFormat := ccSwitchAPIFormat(meta, settings)
		if apiFormat != "" {
			provider["apiFormat"] = apiFormat
		}
		if appType == "codex" {
			if config, ok := settings["config"].(string); ok {
				fields := parseCCSwitchTopLevelConfig(config)
				if value := fields["model_provider"]; value != "" {
					provider["modelProvider"] = value
				}
				if value := fields["wire_api"]; value != "" {
					provider["wireAPI"] = value
				}
				if value := fields["service_tier"]; value != "" {
					provider["serviceTier"] = value
				}
			}
		}
		if endpoint := endpointByProvider[id]; endpoint != "" {
			if host := ccSwitchEndpointHost(endpoint); host != "" {
				provider["endpointHost"] = host
			}
		}
		if health := healthByProvider[id]; health != nil {
			provider["health"] = health
		}
		models := extractCCSwitchModels(appType, settings, meta)
		if len(models) > 0 {
			provider["models"] = models
		}
		if required, known := ccSwitchNeedsRouting(appType, apiFormat, meta); known {
			provider["needsLocalRouting"] = required
		}
		providers = append(providers, provider)
		if isCurrent != 0 {
			current = provider
		}
	}
	return providers, current, rows.Err()
}

func inspectCCSwitchLastRequest(ctx context.Context, db *sql.DB, appType string) map[string]any {
	var providerID, model string
	var requestModel, sessionID sql.NullString
	var createdAt int64
	err := db.QueryRowContext(ctx, `SELECT provider_id, model, request_model, session_id, created_at FROM proxy_request_logs WHERE app_type = ? ORDER BY created_at DESC LIMIT 1`, appType).Scan(&providerID, &model, &requestModel, &sessionID, &createdAt)
	if err != nil {
		return nil
	}
	out := map[string]any{"providerId": providerID, "upstreamModel": model, "createdAt": createdAt}
	if requestModel.Valid && requestModel.String != "" {
		out["requestModel"] = requestModel.String
	}
	if sessionID.Valid && sessionID.String != "" {
		out["sessionId"] = sessionID.String
	}
	return out
}

func decodeCCSwitchJSON(raw string) map[string]any {
	if len(raw) == 0 || len(raw) > 2<<20 {
		return map[string]any{}
	}
	var out map[string]any
	if json.Unmarshal([]byte(raw), &out) != nil {
		return map[string]any{}
	}
	return out
}

func ccSwitchAPIFormat(meta, settings map[string]any) string {
	for _, record := range []map[string]any{meta, settings} {
		for _, key := range []string{"api_format", "apiFormat", "wire_api", "wireApi"} {
			if value, ok := record[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func ccSwitchNeedsRouting(appType, apiFormat string, meta map[string]any) (bool, bool) {
	for _, key := range []string{"needsLocalRouting", "needs_local_routing", "requiresRouting", "requires_routing", "routingRequired"} {
		if value, ok := meta[key].(bool); ok {
			return value, true
		}
	}
	format := strings.ToLower(strings.TrimSpace(apiFormat))
	switch appType {
	case "claude", "claude-desktop":
		if format == "anthropic" {
			return false, true
		}
		if format == "openai_chat" || format == "openai_responses" {
			return true, true
		}
	case "codex":
		if format == "openai_responses" {
			return false, true
		}
		if format == "openai_chat" {
			return true, true
		}
	}
	return false, false
}

func ccSwitchCredentialPresent(settings map[string]any) bool {
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

func ccSwitchEndpointHost(raw string) string {
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

func extractCCSwitchModels(appType string, settings, meta map[string]any) []map[string]any {
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
				add("", ccSwitchString(entry, "model", "id", "slug"), ccSwitchString(entry, "displayName", "display_name", "name"), ccSwitchInt64(entry, "contextWindow", "context_window"), false)
			}
		}
	}
	for _, key := range []string{"modelMapping", "modelMappings", "model_mapping", "modelRoute", "modelRoutes", "claudeDesktopModelRoutes"} {
		collectCCSwitchRoleModels(meta[key], "", add)
	}
	if appType == "codex" {
		if config, ok := settings["config"].(string); ok {
			fields := parseCCSwitchTopLevelConfig(config)
			add("main", fields["model"], "", 0, false)
			add("review", fields["review_model"], "", 0, false)
		}
	}
	if env, ok := settings["env"].(map[string]any); ok {
		for _, spec := range []struct{ role, key string }{
			{"main", "ANTHROPIC_MODEL"}, {"sonnet", "ANTHROPIC_DEFAULT_SONNET_MODEL"},
			{"opus", "ANTHROPIC_DEFAULT_OPUS_MODEL"}, {"haiku", "ANTHROPIC_DEFAULT_HAIKU_MODEL"},
		} {
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

func collectCCSwitchRoleModels(raw any, roleHint string, add func(string, string, string, int64, bool)) {
	switch value := raw.(type) {
	case map[string]any:
		model := ccSwitchString(value, "model", "requestedModel", "requested_model", "modelId", "model_id")
		role := ccSwitchString(value, "role", "modelRole", "model_role")
		if role == "" {
			role = roleHint
		}
		if model != "" {
			supports1M, _ := value["supports1m"].(bool)
			if !supports1M {
				supports1M, _ = value["supports1M"].(bool)
			}
			add(role, model, ccSwitchString(value, "displayName", "display_name", "labelOverride", "label_override", "name"), ccSwitchInt64(value, "contextWindow", "context_window"), supports1M)
			return
		}
		for key, child := range value {
			lower := strings.ToLower(key)
			if lower == "main" || strings.Contains(lower, "sonnet") || strings.Contains(lower, "opus") || strings.Contains(lower, "haiku") || strings.Contains(lower, "fable") || strings.HasPrefix(lower, "claude-") {
				collectCCSwitchRoleModels(child, key, add)
			}
		}
	case []any:
		for _, child := range value {
			collectCCSwitchRoleModels(child, roleHint, add)
		}
	case string:
		if roleHint != "" {
			add(roleHint, value, "", 0, false)
		}
	}
}

func parseCCSwitchTopLevelConfig(raw string) map[string]string {
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
		if !stringInSet(key, "model", "review_model", "model_provider", "wire_api", "service_tier") {
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

func ccSwitchString(record map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := record[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func ccSwitchInt64(record map[string]any, keys ...string) int64 {
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

func ccSwitchRoutingMode(proxy map[string]any, provider map[string]any) string {
	takeover, _ := proxy["takeoverEnabled"].(bool)
	live, _ := proxy["liveTakeoverActive"].(bool)
	if takeover || live {
		return "cc_switch"
	}
	return "direct"
}

func deriveCCSwitchEffectiveCapabilities(appType string, routingMode any, provider map[string]any) map[string]any {
	mode, _ := routingMode.(string)
	apiFormat, _ := provider["apiFormat"].(string)
	capability := func(state, reason string) map[string]any {
		out := map[string]any{"state": state}
		if reason != "" {
			out["reason"] = reason
		}
		return out
	}
	out := map[string]any{
		"toolCalls": capability("supported", "supported by the AI harness"),
		"mcp":       capability("supported", "supported by the AI harness"),
		"webSearch": capability("unknown", "depends on upstream provider and routed tool compatibility"),
		"vision":    capability("unknown", "depends on upstream model and request conversion"),
		"thinking":  capability("unknown", "depends on upstream reasoning interface"),
	}
	if appType == "claude" {
		out["resume"] = capability("supported", "Claude Code persists and resumes sessions by session ID")
		out["imageGeneration"] = capability("unsupported", "Claude Code is not an image generation harness")
		providerID, _ := provider["providerId"].(string)
		category, _ := provider["category"].(string)
		providerType, _ := provider["providerType"].(string)
		official := providerID == "claude-official" || category == "official" || providerType == "official"
		if official {
			out["webSearch"] = capability("supported", "current route is the official Claude provider; actual availability still depends on account and service state")
		} else if providerID != "" {
			out["webSearch"] = capability("unsupported", "Claude hosted WebSearch is not treated as portable to a non-official upstream provider")
		}
		if mode == "cc_switch" && apiFormat != "" && apiFormat != "anthropic" {
			out["thinking"] = capability("supported", "CC Switch converts common reasoning interfaces; exact effort tiers remain model-dependent")
		}
	}
	if appType == "codex" {
		out["resume"] = capability("supported", "Codex threads are persistent")
		if mode == "cc_switch" {
			out["thinking"] = capability("supported", "CC Switch adapts common third-party reasoning interfaces; exact effort tiers remain model-dependent")
		}
	}
	return out
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
