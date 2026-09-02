package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
)

// controlChatGPTCloud dispatches ai_control actions for the codex + chatgpt_cloud
// backend: ChatGPT cloud conversations created through the Codex app-server
// ChatGPT token and the official /backend-api/f/conversation flow.
func (m *AgentManager) controlChatGPTCloud(ctx context.Context, action string, input agentControlParams) (map[string]any, error) {
	if m.chatgptCloud == nil {
		return nil, node.ErrAgentProviderUnavailable
	}
	switch action {
	case "session.create":
		return m.chatgptCloudCreate(ctx, input)
	case "models.list":
		catalog, err := m.chatgptCloud.Models(ctx)
		if err != nil {
			return nil, err
		}
		cfg, err := LoadChatGPTAdvancedConfig(m.dataDir)
		if err != nil {
			return nil, err
		}
		thinkingOptions, _ := catalog["thinkingOptions"].([]ChatGPTThinkingOption)
		catalog["configurationModes"] = []map[string]any{
			{"id": "preset", "title": "Preset", "modelSource": "modelPresets"},
			{"id": "advanced", "title": "Advanced", "modelSource": "advancedModels", "thinkingSource": "thinkingOptions", "combinesWithCreationModes": true},
		}
		catalog["advancedModels"] = filterChatGPTAdvancedModels(cfg.Models, thinkingOptions)
		catalog["advancedConfigFile"] = ChatGPTAdvancedConfigFileName
		catalog["modelSource"] = "chatgpt_cloud"
		defaults := m.chatGPTCloudCreateDefaults()
		catalog["localCreateDefaults"] = map[string]any{
			"configurationMode": defaults.ConfigurationMode,
			"mode":              defaults.Mode,
			"model":             defaults.Model,
			"thinking":          defaults.Thinking,
		}
		return catalog, nil
	case "session.send":
		return m.chatgptCloudSend(ctx, input)
	case "session.get":
		return m.chatgptCloudGet(ctx, input)
	case "session.list":
		return m.chatgptCloudList(ctx, input)
	case "session.result":
		return m.chatgptCloudResult(ctx, input)
	case "session.delete":
		return m.chatgptCloudDelete(ctx, input)
	case "session.rename":
		return m.chatgptCloudRename(ctx, input)
	case "session.archive":
		return m.chatgptCloudSetArchived(ctx, input, true)
	case "session.unarchive":
		return m.chatgptCloudSetArchived(ctx, input, false)
	case "session.cancel":
		return m.chatgptCloudCancel(ctx, input)
	case "session.watch":
		return m.chatgptCloudWatch(ctx, input)
	case "session.callback.register":
		return m.sessionCallbackRegister(ctx, input)
	case "session.callback.unregister":
		return m.sessionCallbackUnregister(input)
	case "session.callback.list":
		return m.sessionCallbackList(input)
	case "session.steer":
		return m.chatgptCloudSteer(ctx, input)
	default:
		return nil, fmt.Errorf("action %q is not supported for backend=chatgpt_cloud", action)
	}
}

func (m *AgentManager) chatgptCloudSetArchived(ctx context.Context, input agentControlParams, archived bool) (map[string]any, error) {
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return nil, errors.New("sessionId is required")
	}
	if err := m.chatgptCloud.Archive(ctx, sessionID, archived); err != nil {
		return nil, err
	}
	return map[string]any{"sessionId": sessionID, "archived": archived}, nil
}

func (m *AgentManager) chatgptCloudCreate(ctx context.Context, input agentControlParams) (map[string]any, error) {
	defaults := m.chatGPTCloudCreateDefaults()
	if strings.TrimSpace(input.Mode) == "" {
		input.Mode = defaults.Mode
	}
	if !input.modelProvided {
		input.Model = defaults.Model
	}
	if !input.thinkingProvided {
		input.Thinking = defaults.Thinking
	}
	spec, err := resolveSessionVisibility("codex", input)
	if err != nil {
		return nil, err
	}
	workingDirectory, err := requiredAgentDirectory(input.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return nil, fmt.Errorf("backend=chatgpt_cloud session.create requires a prompt (the first message); ChatGPT has no empty cloud conversation create API")
	}
	idempotencyKey := strings.TrimSpace(input.IdempotencyKey)
	if idempotencyKey == "" {
		return nil, fmt.Errorf("idempotencyKey is required for backend=chatgpt_cloud session.create")
	}
	if len(idempotencyKey) < 12 || len(idempotencyKey) > 128 || strings.ContainsAny(idempotencyKey, "\x00\r\n") {
		return nil, fmt.Errorf("idempotencyKey must be 12 to 128 safe characters")
	}
	createMode := strings.ToLower(strings.TrimSpace(input.Mode))
	if createMode == "" {
		createMode = "complete"
	}
	if createMode != "complete" && createMode != "quick_chat" {
		return nil, fmt.Errorf("backend=chatgpt_cloud session.create mode must be complete or quick_chat")
	}
	selectedModel := strings.TrimSpace(input.Model)
	if createMode == "quick_chat" && selectedModel == "" {
		selectedModel = "auto"
	}
	selectedThinking, err := normalizeChatGPTCloudThinking(input.Thinking)
	if err != nil {
		return nil, err
	}
	legacySpecValue := map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"prompt": strings.TrimSpace(input.Prompt), "model": strings.TrimSpace(input.Model),
	}
	for key, value := range spec.hashFields() {
		legacySpecValue[key] = value
	}
	legacySpecWithoutDirectoryHash := sessionCreateSpecHash(legacySpecValue)
	legacySpecWithDirectory := cloneAgentMap(legacySpecValue)
	legacySpecWithDirectory["workingDirectory"] = workingDirectory
	legacySpecWithDirectoryHash := sessionCreateSpecHash(legacySpecWithDirectory)
	specValue := cloneAgentMap(legacySpecWithDirectory)
	specValue["mode"] = createMode
	specValue["model"] = selectedModel
	specValue["workingDirectory"] = workingDirectory
	modeSpecHash := sessionCreateSpecHash(specValue)
	specValue["thinking"] = selectedThinking
	specHash := sessionCreateSpecHash(specValue)
	storeKey := "codex:" + idempotencyKey
	if idempotencyKey != "" {
		if m.createStore == nil {
			return nil, &createIdempotencyError{code: "AGENT_IDEMPOTENCY_STORE_UNAVAILABLE", message: "session.create idempotency state is unavailable"}
		}
		legacyHashes := []string(nil)
		if selectedThinking == "" {
			legacyHashes = append(legacyHashes, modeSpecHash)
		}
		if createMode == "complete" {
			legacyHashes = append(legacyHashes, legacySpecWithDirectoryHash, legacySpecWithoutDirectoryHash)
		}
		replayed, ok, err := m.createStore.begin(storeKey, specHash, legacyHashes...)
		if err != nil {
			return nil, err
		}
		if ok {
			sessionID := mapString(replayed, "sessionId")
			if sessionID == "" {
				return nil, &createIdempotencyError{code: "AGENT_CREATE_IN_DOUBT", message: "replayed session.create result has no session ID; inspect session.list before retrying"}
			}
			spec.applyToResult(replayed, sessionID)
			if err := m.persistSessionVisibility(spec.recordForDirectory("codex", sessionID, workingDirectory, time.Now().UTC())); err != nil {
				return nil, err
			}
			replayed["idempotencyProtected"] = true
			replayed["idempotencyStatus"] = "replayed"
			replayed["createMode"] = createMode
			replayed["thinking"] = selectedThinking
			return replayed, nil
		}
	}
	var result chatgptCloudTurnResult
	var createErr error
	if createMode == "quick_chat" {
		result, createErr = m.chatgptCloud.CreateQuickWithThinking(ctx, input.Prompt, selectedModel, selectedThinking)
	} else {
		result, createErr = m.chatgptCloud.CreateWithThinking(ctx, input.Prompt, selectedModel, selectedThinking)
	}
	if createErr != nil && strings.TrimSpace(result.ConversationID) == "" {
		if idempotencyKey != "" {
			if persistenceErr := m.createStore.update(storeKey, "in_doubt", nil); persistenceErr != nil {
				return nil, errors.Join(createErr, fmt.Errorf("persist ambiguous ChatGPT cloud conversation creation: %w", persistenceErr))
			}
		}
		return nil, createErr
	}
	sessionID := strings.TrimSpace(result.ConversationID)
	if sessionID == "" {
		missingIDErr := fmt.Errorf("ChatGPT cloud did not return a conversation id")
		if idempotencyKey != "" {
			if persistenceErr := m.createStore.update(storeKey, "in_doubt", nil); persistenceErr != nil {
				missingIDErr = errors.Join(missingIDErr, fmt.Errorf("persist ambiguous ChatGPT cloud conversation creation: %w", persistenceErr))
			}
		}
		return nil, missingIDErr
	}
	if err := m.persistSessionVisibility(spec.recordForDirectory("codex", sessionID, workingDirectory, time.Now().UTC())); err != nil {
		if idempotencyKey != "" {
			if persistenceErr := m.createStore.update(storeKey, "in_doubt", map[string]any{"sessionId": sessionID}); persistenceErr != nil {
				err = errors.Join(err, fmt.Errorf("persist ambiguous ChatGPT cloud visibility metadata: %w", persistenceErr))
			}
		}
		return nil, err
	}
	out := map[string]any{
		"sessionId":            sessionID,
		"executionMode":        "chatgpt_cloud",
		"createMode":           createMode,
		"model":                selectedModel,
		"thinking":             selectedThinking,
		"owner":                "node_agent_bridge",
		"phase":                "ready",
		"realtimeChannel":      "session.watch",
		"idempotencyProtected": idempotencyKey != "",
		"workingDirectory":     workingDirectory,
	}
	if createMode == "quick_chat" {
		out["phase"] = "running"
		out["completionPending"] = true
	} else if createErr != nil {
		out["phase"] = "created_execution_unknown"
		out["creationRecoveredFromStreamError"] = true
	}
	spec.applyToResult(out, sessionID)
	if idempotencyKey != "" {
		out["idempotencyStatus"] = "created"
		if err := m.createStore.update(storeKey, "succeeded", out); err != nil {
			if persistenceErr := m.createStore.update(storeKey, "in_doubt", map[string]any{"sessionId": sessionID}); persistenceErr != nil {
				return nil, errors.Join(err, fmt.Errorf("persist ambiguous ChatGPT cloud conversation result: %w", persistenceErr))
			}
			return nil, err
		}
	}
	return out, nil
}

func (m *AgentManager) chatgptCloudSend(ctx context.Context, input agentControlParams) (map[string]any, error) {
	if strings.TrimSpace(input.SessionID) == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	selectedThinking, err := normalizeChatGPTCloudThinking(input.Thinking)
	if err != nil {
		return nil, err
	}
	result, err := m.chatgptCloud.SendWithThinking(ctx, input.SessionID, "", input.Prompt, input.Model, selectedThinking)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"sessionId": result.ConversationID,
		"phase":     "running",
		"model":     result.Model,
		"thinking":  result.Thinking,
	}
	if result.AsyncTaskID != "" {
		out["asyncTaskId"] = result.AsyncTaskID
	}
	return out, nil
}

func normalizeChatGPTCloudThinking(value string) (string, error) {
	thinking := strings.ToLower(strings.TrimSpace(value))
	if thinking != "" && !stringInSet(thinking, "standard", "extended", "min", "max", "ultra", "xhigh", "zero") {
		return "", fmt.Errorf("backend=chatgpt_cloud thinking must be standard, extended, min, max, ultra, xhigh, or zero")
	}
	return thinking, nil
}

func (m *AgentManager) chatgptCloudSteer(ctx context.Context, input agentControlParams) (map[string]any, error) {
	if strings.TrimSpace(input.SessionID) == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if len(input.Skills) > 0 || len(input.Images) > 0 || len(input.LocalImages) > 0 || len(input.Mentions) > 0 || strings.TrimSpace(input.ImageDetail) != "" {
		return nil, fmt.Errorf("backend=chatgpt_cloud session.steer currently accepts prompt text only")
	}
	result, err := m.chatgptCloud.Steer(ctx, input.SessionID, input.AsyncTaskID, input.Prompt)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"sessionId":   input.SessionID,
		"asyncTaskId": result.AsyncTaskID,
		"steered":     true,
		"phase":       "running",
		"result":      result,
	}
	return out, nil
}

func (m *AgentManager) chatgptCloudGet(ctx context.Context, input agentControlParams) (map[string]any, error) {
	if strings.TrimSpace(input.SessionID) == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	detail, err := m.chatgptCloud.Read(ctx, input.SessionID)
	if err != nil {
		return nil, err
	}
	snapshot, err := m.visibilitySnapshot()
	if err != nil {
		return nil, err
	}
	detail["providerId"] = "codex"
	if record, ok := snapshot[sessionVisibilityKey("codex", input.SessionID)]; ok {
		record.applyToResult(detail)
	} else {
		defaultChatGPTCloudVisibilityRecord(input.SessionID).applyToResult(detail)
	}
	return map[string]any{"session": detail, "pendingRequests": []map[string]any{}}, nil
}

func (m *AgentManager) chatgptCloudList(ctx context.Context, input agentControlParams) (map[string]any, error) {
	items, err := m.chatgptCloud.List(ctx, input.Limit)
	if err != nil {
		return nil, err
	}
	snapshot, err := m.visibilitySnapshot()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		id := chatgptCloudListSessionID(item)
		if id == "" {
			continue
		}
		item["sessionId"] = id
		item["providerId"] = "codex"
		if record, ok := snapshot[sessionVisibilityKey("codex", id)]; ok {
			record.applyToResult(item)
		} else {
			defaultChatGPTCloudVisibilityRecord(id).applyToResult(item)
		}
		out = append(out, item)
	}
	return map[string]any{"sessions": out}, nil
}

func (m *AgentManager) chatgptCloudResult(ctx context.Context, input agentControlParams) (map[string]any, error) {
	if strings.TrimSpace(input.SessionID) == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	detail, err := m.chatgptCloud.Read(ctx, input.SessionID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"sessionId":         detail["sessionId"],
		"status":            "completed",
		"finalAgentMessage": chatgptCloudLatestAssistantText(detail),
	}, nil
}

func (m *AgentManager) chatgptCloudDelete(ctx context.Context, input agentControlParams) (map[string]any, error) {
	if strings.TrimSpace(input.SessionID) == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	if err := m.chatgptCloud.Delete(ctx, input.SessionID); err != nil {
		return nil, err
	}
	m.chatgptCloud.StopRealtime(input.SessionID)
	if err := m.forgetSessionVisibility("codex", input.SessionID); err != nil {
		return nil, err
	}
	return map[string]any{"sessionId": input.SessionID, "deleted": true}, nil
}

func (m *AgentManager) chatgptCloudRename(ctx context.Context, input agentControlParams) (map[string]any, error) {
	if strings.TrimSpace(input.SessionID) == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	if strings.TrimSpace(input.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	if err := m.chatgptCloud.Rename(ctx, input.SessionID, input.Name); err != nil {
		return nil, err
	}
	return map[string]any{"sessionId": input.SessionID, "renamed": true}, nil
}

func (m *AgentManager) chatgptCloudCancel(ctx context.Context, input agentControlParams) (map[string]any, error) {
	if strings.TrimSpace(input.SessionID) == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	if err := m.chatgptCloud.Cancel(ctx, input.SessionID); err != nil {
		return nil, err
	}
	return map[string]any{"sessionId": input.SessionID, "cancelRequested": true}, nil
}

func (m *AgentManager) chatgptCloudWatch(ctx context.Context, input agentControlParams) (map[string]any, error) {
	if strings.TrimSpace(input.SessionID) == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	wait := time.Duration(input.WaitSeconds) * time.Second
	if wait < 0 || wait > 15*time.Second {
		return nil, fmt.Errorf("waitSeconds must be between 0 and 15")
	}
	if _, err := m.chatgptCloud.Read(ctx, input.SessionID); err != nil {
		return nil, fmt.Errorf("validate ChatGPT cloud conversation: %w", err)
	}
	events, next, err := m.chatgptCloud.WatchRealtime(ctx, input.SessionID, input.Cursor, wait)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		out = append(out, map[string]any{
			"sequence": event.Sequence, "type": event.Type, "eventType": event.EventType,
			"sessionId": event.ConversationID, "timestamp": event.Timestamp.Format(time.RFC3339Nano),
		})
	}
	return map[string]any{
		"sessionId": input.SessionID, "events": out, "cursor": next,
		"note": "chatgpt_cloud realtime events signal a conversation update; refetch session.get for content",
	}, nil
}

// chatgptCloudLatestAssistantText extracts the newest assistant message text.
func chatgptCloudLatestAssistantText(detail map[string]any) string {
	mapping, _ := detail["mapping"].(map[string]any)
	var latest string
	for _, raw := range mapping {
		node, _ := raw.(map[string]any)
		msg, _ := node["message"].(map[string]any)
		if msg == nil {
			continue
		}
		author, _ := msg["author"].(map[string]any)
		if role, _ := author["role"].(string); role != "assistant" {
			continue
		}
		content, _ := msg["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		var sb strings.Builder
		for _, p := range parts {
			sb.WriteString(strings.TrimSpace(fmt.Sprintf("%v", p)))
		}
		if sb.Len() > 0 {
			latest = sb.String()
		}
	}
	return latest
}
