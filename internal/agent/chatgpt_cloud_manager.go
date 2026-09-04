package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
)

const chatgptCreateReconcileTimeout = 30 * time.Second
const chatgptCloudReadCacheTTL = 30 * time.Second

const (
	chatgptSessionGetDefaultLimit = 8
	chatgptSessionGetMaxLimit     = 32
	chatgptSessionMessageTextMax  = 8 << 10
)

type chatgptCloudSessionPluginBindingError struct{}

type chatGPTCloudReadCacheEntry struct {
	detail map[string]any
	readAt time.Time
}

type chatGPTCloudReadCall struct {
	done   chan struct{}
	detail map[string]any
	err    error
	epoch  uint64
}

func (chatgptCloudSessionPluginBindingError) Error() string {
	return "chatgpt_cloud session.create does not support per-session plugin binding; installed or catalog plugins are not session bindings"
}

func (chatgptCloudSessionPluginBindingError) CapabilityError() (string, string, bool) {
	return "UNSUPPORTED_SESSION_PLUGIN_BINDING", "chatgpt_cloud session.create does not support per-session plugin binding; installed or catalog plugins are not session bindings", false
}

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
	case "session.callback.arm":
		return m.sessionCallbackArm(ctx, input)
	case "session.callback.enqueue":
		return m.sessionCallbackEnqueue(input)
	case "session.callback.unregister":
		return m.sessionCallbackUnregister(input)
	case "session.callback.list":
		return m.sessionCallbackList(input)
	case "session.callback.claim":
		return m.sessionCallbackClaim(input)
	case "session.callback.ack":
		return m.sessionCallbackAck(input)
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
	// ChatGPT Cloud sessions have no per-session plugin/tool transport. Reject
	// this before touching the idempotency store or provider so a catalog lookup
	// cannot be mistaken for a binding and cannot reserve a misleading create.
	if strings.TrimSpace(input.PluginName) != "" {
		return nil, chatgptCloudSessionPluginBindingError{}
	}
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
			var idempotencyErr *createIdempotencyError
			if errors.As(err, &idempotencyErr) && idempotencyErr.code == "AGENT_CREATE_IN_DOUBT" {
				if attempt, exists := m.createStore.attempt(storeKey); exists && attempt.Backend == sessionBackendChatGPTCloud {
					reconcileCtx, cancel := context.WithTimeout(ctx, chatgptCreateReconcileTimeout)
					sessionID, found, reconcileErr := m.chatgptCloud.FindConversationByMessageID(reconcileCtx, attempt.RequestMessageID)
					cancel()
					if found {
						stored := map[string]any{
							"sessionId": sessionID, "executionMode": "chatgpt_cloud", "createMode": createMode,
							"model": selectedModel, "thinking": selectedThinking, "owner": "node_agent_bridge",
							"phase": "created_execution_unknown", "completionPending": true, "creationConfirmed": true,
							"creationReconciled": true, "realtimeChannel": "session.watch", "idempotencyProtected": true,
							"idempotencyStatus": "created", "workingDirectory": workingDirectory,
						}
						spec.applyToResult(stored, sessionID)
						if visibilityErr := m.persistSessionVisibility(spec.recordForDirectory("codex", sessionID, workingDirectory, time.Now().UTC())); visibilityErr != nil {
							_ = m.createStore.update(storeKey, "in_doubt", stored)
							return nil, visibilityErr
						}
						if persistenceErr := m.createStore.update(storeKey, "succeeded", stored); persistenceErr != nil {
							_ = m.createStore.update(storeKey, "in_doubt", stored)
							return nil, persistenceErr
						}
						replayed := cloneAgentMap(stored)
						replayed["idempotencyStatus"] = "replayed"
						return replayed, nil
					}
					if reconcileErr != nil && m.logger != nil {
						m.logger.Warn("reconcile ambiguous ChatGPT cloud creation", "error", reconcileErr)
					}
				}
			}
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
	var createBody map[string]any
	if m.chatgptCloud.createOverride == nil {
		if createMode == "quick_chat" {
			createBody = chatgptQuickChatBodyWithThinking(input.Prompt, selectedModel, selectedThinking)
		} else {
			createBody = chatgptNewChatBodyWithThinking(input.Prompt, selectedModel, selectedThinking)
		}
		attempt := sessionCreateAttempt{
			Backend: sessionBackendChatGPTCloud, RequestMessageID: chatgptCloudRequestMessageID(createBody), StartedAt: time.Now().UTC(),
		}
		if err := m.createStore.setAttempt(storeKey, attempt); err != nil {
			if abortErr := m.createStore.abort(storeKey); abortErr != nil {
				return nil, errors.Join(err, abortErr)
			}
			return nil, err
		}
	}
	var result chatgptCloudTurnResult
	var createErr error
	persistObservedConversation := func(observed chatgptCloudTurnResult) error {
		sessionID := strings.TrimSpace(observed.ConversationID)
		if sessionID == "" {
			return nil
		}
		partial := map[string]any{
			"sessionId":            sessionID,
			"executionMode":        "chatgpt_cloud",
			"createMode":           createMode,
			"model":                selectedModel,
			"thinking":             selectedThinking,
			"owner":                "node_agent_bridge",
			"phase":                "created_execution_unknown",
			"completionPending":    true,
			"creationConfirmed":    true,
			"realtimeChannel":      "session.watch",
			"idempotencyProtected": true,
			"idempotencyStatus":    "created",
			"workingDirectory":     workingDirectory,
		}
		spec.applyToResult(partial, sessionID)
		if err := m.createStore.update(storeKey, "thread_created", partial); err != nil {
			return fmt.Errorf("persist observed ChatGPT cloud conversation: %w", err)
		}
		if err := m.persistSessionVisibility(spec.recordForDirectory("codex", sessionID, workingDirectory, time.Now().UTC())); err != nil {
			return fmt.Errorf("persist observed ChatGPT cloud visibility metadata: %w", err)
		}
		return nil
	}
	if createMode == "quick_chat" {
		if createBody != nil {
			result, createErr = m.chatgptCloud.createQuickBody(ctx, createBody)
		} else {
			result, createErr = m.chatgptCloud.CreateQuickWithThinking(ctx, input.Prompt, selectedModel, selectedThinking)
		}
	} else {
		if createBody != nil {
			result, createErr = m.chatgptCloud.createBodyObserved(ctx, createBody, persistObservedConversation)
		} else {
			result, createErr = m.chatgptCloud.CreateWithThinkingObserved(ctx, input.Prompt, selectedModel, selectedThinking, persistObservedConversation)
		}
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
		"creationConfirmed":    true,
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
	sendMode := strings.ToLower(strings.TrimSpace(input.Mode))
	if sendMode == "" {
		sendMode = "complete"
	}
	if sendMode != "complete" && sendMode != "quick_chat" {
		return nil, fmt.Errorf("backend=chatgpt_cloud session.send mode must be complete or quick_chat")
	}
	idempotencyKey := strings.TrimSpace(input.IdempotencyKey)
	if idempotencyKey != "" && (len(idempotencyKey) < 12 || len(idempotencyKey) > 128 || strings.ContainsAny(idempotencyKey, "\x00\r\n")) {
		return nil, fmt.Errorf("idempotencyKey must be 12 to 128 safe characters for backend=chatgpt_cloud session.send")
	}
	if idempotencyKey != "" && sendMode != "quick_chat" {
		return nil, fmt.Errorf("idempotencyKey requires mode=quick_chat for backend=chatgpt_cloud session.send")
	}
	var result chatgptCloudTurnResult
	if sendMode == "quick_chat" && idempotencyKey != "" {
		result, err = m.chatgptCloud.SendQuickIdempotentWithThinking(ctx, input.SessionID, "", input.Prompt, input.Model, selectedThinking, chatgptCloudSendRequestMessageID(input.SessionID, idempotencyKey))
	} else if sendMode == "quick_chat" {
		result, err = m.chatgptCloud.SendQuickWithThinking(ctx, input.SessionID, "", input.Prompt, input.Model, selectedThinking)
	} else {
		result, err = m.chatgptCloud.SendWithThinking(ctx, input.SessionID, "", input.Prompt, input.Model, selectedThinking)
	}
	if err != nil {
		return nil, err
	}
	m.invalidateChatGPTCloudRead(input.SessionID)
	out := map[string]any{
		"sessionId": result.ConversationID,
		"phase":     "running",
		"model":     result.Model,
		"thinking":  result.Thinking,
		"sendMode":  sendMode,
	}
	if sendMode == "quick_chat" {
		out["completionPending"] = true
	}
	if idempotencyKey != "" {
		out["idempotencyProtected"] = true
		out["idempotencyStatus"] = "accepted"
		if result.Replayed {
			out["idempotencyStatus"] = "replayed"
		}
	}
	if result.AsyncTaskID != "" {
		out["asyncTaskId"] = result.AsyncTaskID
	}
	return out, nil
}

func chatgptCloudSendRequestMessageID(sessionID, idempotencyKey string) string {
	sum := sha256.Sum256([]byte("chatgpt_cloud_send\x00" + strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(idempotencyKey)))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
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
	m.invalidateChatGPTCloudRead(input.SessionID)
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
	limit := input.Limit
	if limit == 0 {
		limit = chatgptSessionGetDefaultLimit
	}
	if limit < 1 || limit > chatgptSessionGetMaxLimit {
		return nil, fmt.Errorf("backend=chatgpt_cloud session.get limit must be between 1 and %d", chatgptSessionGetMaxLimit)
	}
	if len(input.PageCursor) > 256 {
		return nil, fmt.Errorf("backend=chatgpt_cloud session.get pageCursor must be at most 256 characters")
	}
	detail, err := m.readChatGPTCloud(ctx, input.SessionID, 0)
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
	session, messages, nextCursor, hasMore, hasEarlier, err := chatgptCloudBoundedSessionView(detail, input.PageCursor, limit)
	if err != nil {
		return nil, err
	}
	session["messages"] = messages
	session["historyMode"] = "bounded_delta"
	session["historyCursor"] = nextCursor
	session["historyHasMore"] = hasMore
	session["historyHasEarlier"] = hasEarlier
	session["historyMessageLimit"] = limit
	session["fullMappingOmitted"] = true
	return map[string]any{"session": session, "nextCursor": nextCursor, "pendingRequests": []map[string]any{}}, nil
}

// chatgptCloudBoundedSessionView keeps the provider's full mapping inside the
// Node. The public session.get response contains only a bounded slice of the
// active branch. Passing the previous nextCursor returns only newer nodes, so
// callers do not repeatedly inject the same long conversation into AI context.
func chatgptCloudBoundedSessionView(detail map[string]any, afterCursor string, limit int) (map[string]any, []map[string]any, string, bool, bool, error) {
	session := cloneAgentMap(detail)
	delete(session, "mapping")
	mapping, _ := detail["mapping"].(map[string]any)
	path := chatgptCloudActiveBranch(detail, mapping)
	start := 0
	if afterCursor != "" {
		start = -1
		for i, nodeID := range path {
			if nodeID == afterCursor {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return nil, nil, "", false, false, fmt.Errorf("backend=chatgpt_cloud session.get pageCursor is not on the current conversation branch")
		}
	} else if len(path) > limit {
		start = len(path) - limit
	}
	end := len(path)
	if afterCursor != "" && end-start > limit {
		end = start + limit
	}
	messages := make([]map[string]any, 0, end-start)
	for _, nodeID := range path[start:end] {
		node, _ := mapping[nodeID].(map[string]any)
		if message, ok := chatgptCloudBoundedMessage(nodeID, node); ok {
			messages = append(messages, message)
		}
	}
	nextCursor := afterCursor
	if end > start {
		nextCursor = path[end-1]
	}
	return session, messages, nextCursor, end < len(path), afterCursor == "" && start > 0, nil
}

func chatgptCloudActiveBranch(detail map[string]any, mapping map[string]any) []string {
	current := mapString(detail, "currentNode")
	if current == "" {
		current = chatgptCloudLastMessageID(detail)
	}
	seen := map[string]bool{}
	reversed := make([]string, 0, len(mapping))
	for current != "" && !seen[current] {
		seen[current] = true
		reversed = append(reversed, current)
		node, _ := mapping[current].(map[string]any)
		current = mapString(node, "parent")
	}
	path := make([]string, len(reversed))
	for i := range reversed {
		path[len(reversed)-1-i] = reversed[i]
	}
	return path
}

func chatgptCloudBoundedMessage(nodeID string, node map[string]any) (map[string]any, bool) {
	message, _ := node["message"].(map[string]any)
	if message == nil {
		return nil, false
	}
	out := map[string]any{"id": firstNonEmptyString(mapString(message, "id"), nodeID)}
	if role := chatgptCloudMessageRole(message); role != "" {
		out["role"] = role
	}
	if status := mapString(message, "status"); status != "" {
		out["status"] = status
	}
	if created := message["create_time"]; created != nil {
		out["createTime"] = created
	}
	content, _ := message["content"].(map[string]any)
	var builder strings.Builder
	appendPart := func(part string) {
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(part)
	}
	switch parts := content["parts"].(type) {
	case []any:
		for _, raw := range parts {
			if part, ok := raw.(string); ok {
				appendPart(part)
			}
		}
	case []string:
		for _, part := range parts {
			appendPart(part)
		}
	}
	text := builder.String()
	if text != "" {
		out["text"] = boundedAgentText(text, chatgptSessionMessageTextMax)
		if len(text) > chatgptSessionMessageTextMax {
			out["textTruncated"] = true
		}
	}
	return out, true
}

func (m *AgentManager) chatgptCloudList(ctx context.Context, input agentControlParams) (map[string]any, error) {
	items, err := m.chatgptCloud.List(ctx, input.Limit)
	if err != nil {
		fallback, fallbackErr := m.managedChatGPTCloudSessions("", input.Limit)
		if fallbackErr != nil {
			return nil, errors.Join(err, fallbackErr)
		}
		providerStatus := "unavailable"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			providerStatus = "timeout"
		}
		return map[string]any{
			"sessions":               fallback,
			"source":                 "fast_spider_sidecar",
			"providerStatus":         providerStatus,
			"authoritative":          false,
			"incomplete":             true,
			"reconciliationRequired": true,
		}, nil
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
	return map[string]any{
		"sessions":       out,
		"source":         "chatgpt_cloud",
		"providerStatus": "ok",
		"authoritative":  true,
		"incomplete":     false,
	}, nil
}

func (m *AgentManager) chatgptCloudResult(ctx context.Context, input agentControlParams) (map[string]any, error) {
	if strings.TrimSpace(input.SessionID) == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	detail, err := m.readChatGPTCloud(ctx, input.SessionID, 0)
	if err != nil {
		return nil, err
	}
	providerStatus := chatgptCloudConversationStatus(detail)
	status := providerStatus
	var callbackResult callbackResultMetadata
	hasCallbackResult := false
	if m.callbackStore != nil {
		var callbackErr error
		callbackResult, hasCallbackResult, callbackErr = m.callbackStore.resultFor(input.SessionID)
		if callbackErr != nil {
			return nil, callbackErr
		} else if hasCallbackResult && callbackResult.Status != "" && (callbackResult.Status != "failed" || providerStatus != "completed") {
			status = callbackResult.Status
		}
	}
	if status == "" {
		status = "unknown"
	}
	result := map[string]any{
		"sessionId":         detail["sessionId"],
		"status":            status,
		"finalAgentMessage": chatgptCloudLatestAssistantText(detail),
	}
	resultMode := strings.ToLower(strings.TrimSpace(firstNonEmptyString(input.ResultMode, input.Mode)))
	if resultMode == "manifest" || resultMode == "result-id" || resultMode == "result_id" || strings.TrimSpace(input.ResultID) != "" {
		delete(result, "finalAgentMessage")
		result["resultMode"] = firstNonEmptyString(resultMode, "manifest")
		if strings.TrimSpace(input.ResultID) != "" {
			result["resultId"] = strings.TrimSpace(input.ResultID)
		}
		if hasCallbackResult && callbackResult.Status != "failed" {
			applyCallbackResultMetadata(result, callbackResult)
		} else {
			deliverablePath := strings.TrimSpace(input.CallbackDeliverablePath)
			if deliverablePath != "" {
				deliverableStatus, size, digest := inspectCallbackDeliverable(deliverablePath)
				result["deliverablePath"] = deliverablePath
				result["deliverableStatus"] = deliverableStatus
				if deliverableStatus == "ready" {
					result["status"] = "completed"
					applyCallbackResultMetadata(result, callbackResultMetadata{Status: "ready", Bytes: size, SHA256: digest})
				} else if providerStatus == "completed" {
					applyCallbackResultMetadata(result, callbackResultMetadata{Status: "failed", Bytes: size})
				}
			} else if providerStatus == "completed" && m.resultPublisher != nil {
				text, textErr := chatgptCloudLatestAssistantTextLimit(detail, 8<<20)
				if textErr != nil {
					return nil, textErr
				}
				idempotencyKey := strings.TrimSpace(input.IdempotencyKey)
				if idempotencyKey == "" {
					sum := sha256.Sum256([]byte(input.SessionID + "\x00" + text))
					idempotencyKey = "cloud_result_poll_" + hex.EncodeToString(sum[:])[:40]
				}
				if len(idempotencyKey) < 12 || len(idempotencyKey) > 128 || strings.ContainsAny(idempotencyKey, "\x00\r\n") {
					return nil, fmt.Errorf("idempotencyKey must be 12 to 128 safe characters for a Cloud result manifest")
				}
				metadata, publishErr := m.resultPublisher.PublishCloudResult(ctx, input.SessionID, idempotencyKey, text)
				if publishErr != nil {
					return nil, publishErr
				}
				applyCallbackResultMetadata(result, callbackResultMetadata{
					ResultID:  mapString(metadata, "resultId"),
					Status:    firstNonEmptyString(mapString(metadata, "status"), "ready"),
					Bytes:     mapInt64(metadata, "bytes"),
					SHA256:    mapString(metadata, "sha256"),
					PageCount: int(mapInt64(metadata, "pageCount")),
				})
			}
		}
	}
	if result["finalAgentMessage"] == "" {
		delete(result, "finalAgentMessage")
	}
	return result, nil
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
	if !m.chatgptCloud.IsWatchingRealtime(input.SessionID) {
		if _, err := m.readChatGPTCloud(ctx, input.SessionID, chatgptCloudReadCacheTTL); err != nil {
			return nil, fmt.Errorf("validate ChatGPT cloud conversation: %w", err)
		}
	}
	events, next, err := m.chatgptCloud.WatchRealtime(ctx, input.SessionID, input.Cursor, wait)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		out = append(out, map[string]any{
			"sequence": event.Sequence, "type": event.Type, "eventType": event.EventType,
			"eventKey": event.EventKey, "sessionId": event.ConversationID, "timestamp": event.Timestamp.Format(time.RFC3339Nano),
		})
	}
	return map[string]any{
		"sessionId": input.SessionID, "events": out, "cursor": next,
		"note": "chatgpt_cloud realtime events signal a conversation update; refetch session.get for content",
	}, nil
}

func (m *AgentManager) readChatGPTCloud(ctx context.Context, conversationID string, maxAge time.Duration) (map[string]any, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	now := time.Now()
	m.chatgptReadMu.Lock()
	if m.chatgptReadCache == nil {
		m.chatgptReadCache = map[string]chatGPTCloudReadCacheEntry{}
	}
	if m.chatgptReadActive == nil {
		m.chatgptReadActive = map[string]*chatGPTCloudReadCall{}
	}
	if m.chatgptReadEpoch == nil {
		m.chatgptReadEpoch = map[string]uint64{}
	}
	epoch := m.chatgptReadEpoch[conversationID]
	if cached, ok := m.chatgptReadCache[conversationID]; ok && maxAge > 0 && now.Sub(cached.readAt) <= maxAge {
		detail := cached.detail
		m.chatgptReadMu.Unlock()
		return detail, nil
	}
	if active := m.chatgptReadActive[conversationID]; active != nil && active.epoch == epoch {
		m.chatgptReadMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-active.done:
			return active.detail, active.err
		}
	}
	call := &chatGPTCloudReadCall{done: make(chan struct{}), epoch: epoch}
	m.chatgptReadActive[conversationID] = call
	m.chatgptReadMu.Unlock()

	detail, err := m.chatgptCloud.Read(ctx, conversationID)
	m.chatgptReadMu.Lock()
	call.detail, call.err = detail, err
	if err == nil && m.chatgptReadEpoch[conversationID] == call.epoch {
		m.chatgptReadCache[conversationID] = chatGPTCloudReadCacheEntry{detail: detail, readAt: time.Now()}
	}
	if m.chatgptReadActive[conversationID] == call {
		delete(m.chatgptReadActive, conversationID)
	}
	close(call.done)
	m.chatgptReadMu.Unlock()
	return detail, err
}

func (m *AgentManager) invalidateChatGPTCloudRead(conversationID string) {
	if m == nil {
		return
	}
	m.chatgptReadMu.Lock()
	conversationID = strings.TrimSpace(conversationID)
	delete(m.chatgptReadCache, conversationID)
	if m.chatgptReadEpoch == nil {
		m.chatgptReadEpoch = map[string]uint64{}
	}
	m.chatgptReadEpoch[conversationID]++
	m.chatgptReadMu.Unlock()
}

// chatgptCloudLatestAssistantText extracts the newest assistant message text.
func chatgptCloudLatestAssistantText(detail map[string]any) string {
	text, _ := chatgptCloudLatestAssistantTextLimit(detail, 64*1024)
	return text
}

func chatgptCloudLatestAssistantTextLimit(detail map[string]any, limit int) (string, error) {
	mapping, _ := detail["mapping"].(map[string]any)
	candidateID := chatgptCloudLastAssistantNodeID(detail)
	if candidateID == "" {
		return "", nil
	}
	node, _ := mapping[candidateID].(map[string]any)
	message, _ := node["message"].(map[string]any)
	content, _ := message["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	var builder strings.Builder
	for _, part := range parts {
		if text, ok := part.(string); ok {
			builder.WriteString(text)
			if builder.Len() > limit {
				return "", fmt.Errorf("cloud assistant result exceeds %d bytes", limit)
			}
		}
	}
	return strings.TrimSpace(builder.String()), nil
}

func chatgptCloudLastAssistantNodeID(detail map[string]any) string {
	mapping, _ := detail["mapping"].(map[string]any)
	if current := chatgptCloudCurrentNodeID(detail); current != "" {
		seen := map[string]bool{}
		for current != "" && !seen[current] {
			seen[current] = true
			node, _ := mapping[current].(map[string]any)
			message, _ := node["message"].(map[string]any)
			if chatgptCloudMessageRole(message) == "assistant" {
				return current
			}
			current = mapString(node, "parent")
		}
	}
	type candidate struct {
		key     string
		created float64
	}
	candidates := make([]candidate, 0, len(mapping))
	for key, raw := range mapping {
		node, _ := raw.(map[string]any)
		message, _ := node["message"].(map[string]any)
		if chatgptCloudMessageRole(message) != "assistant" {
			continue
		}
		created, _ := message["create_time"].(float64)
		candidates = append(candidates, candidate{key: key, created: created})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].created == candidates[j].created {
			return candidates[i].key > candidates[j].key
		}
		return candidates[i].created > candidates[j].created
	})
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0].key
}
