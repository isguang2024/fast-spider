package agent

import (
	"context"
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
		models, err := m.chatgptCloud.Models(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"models": models, "modelSource": "chatgpt_cloud"}, nil
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
	case "session.cancel":
		return m.chatgptCloudCancel(ctx, input)
	case "session.watch":
		return m.chatgptCloudWatch(ctx, input)
	case "session.steer":
		return nil, fmt.Errorf("action %q is not yet supported for backend=chatgpt_cloud", action)
	default:
		return nil, fmt.Errorf("action %q is not supported for backend=chatgpt_cloud", action)
	}
}

func (m *AgentManager) chatgptCloudCreate(ctx context.Context, input agentControlParams) (map[string]any, error) {
	spec, err := resolveSessionVisibility("codex", input)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return nil, fmt.Errorf("backend=chatgpt_cloud session.create requires a prompt (the first message); ChatGPT has no empty cloud conversation create API")
	}
	result, err := m.chatgptCloud.Create(ctx, input.Prompt, input.Model)
	if err != nil {
		return nil, err
	}
	sessionID := result.ConversationID
	if sessionID == "" {
		return nil, fmt.Errorf("ChatGPT cloud did not return a conversation id")
	}
	if err := m.persistSessionVisibility(spec.record("codex", sessionID, time.Now().UTC())); err != nil {
		return nil, err
	}
	out := map[string]any{
		"sessionId":       sessionID,
		"executionMode":   "chatgpt_cloud",
		"model":           input.Model,
		"owner":           "node_agent_bridge",
		"phase":           "ready",
		"realtimeChannel": "session.watch",
	}
	spec.applyToResult(out, sessionID)
	return out, nil
}

func (m *AgentManager) chatgptCloudSend(ctx context.Context, input agentControlParams) (map[string]any, error) {
	if strings.TrimSpace(input.SessionID) == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	result, err := m.chatgptCloud.Send(ctx, input.SessionID, "", input.Prompt, input.Model)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"sessionId": result.ConversationID,
		"phase":     "running",
		"model":     input.Model,
	}, nil
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
	_, _ = decorateSessionWithVisibility(detail, "codex", snapshot)
	return map[string]any{"session": detail, "pendingRequests": []map[string]any{}}, nil
}

func (m *AgentManager) chatgptCloudList(ctx context.Context, input agentControlParams) (map[string]any, error) {
	items, err := m.chatgptCloud.List(ctx, input.Limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"sessions": items}, nil
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
