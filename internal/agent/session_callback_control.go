package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Collaboration RPCs return control-plane metadata only. Provider credentials,
// conversation mappings, baseline selection and recovery decisions stay here.
func (m *AgentManager) sessionCallbackPrepare(ctx context.Context, input agentControlParams) (map[string]any, error) {
	if input.Mode == "result" {
		r, err := m.ownedCloudCallback(input)
		if err != nil {
			return nil, err
		}
		if r.CallbackType != "local_file" || r.DeliverablePath == "" {
			return nil, &sessionCallbackError{code: "INVALID_REQUEST", message: "the registered callback is not a local file result"}
		}
		status, bytes, digest := inspectCallbackDeliverable(r.DeliverablePath)
		if status != "ready" {
			return nil, &sessionCallbackError{code: "TASK_RESULT_FILE_INVALID", message: "the assigned local result must be a readable regular file no larger than 256 MiB"}
		}
		return map[string]any{"prepared": true, "size": bytes, "fileSha256": digest, "executionOwner": "node"}, nil
	}
	if input.Mode == "target" {
		record, exists, err := m.storedSessionVisibilityRecord("codex", input.SessionID)
		if err != nil {
			return nil, err
		}
		if exists && record.Backend != sessionBackendCodexLocal {
			return nil, &sessionCallbackError{code: "INVALID_REQUEST", message: "callback target must be an existing local Codex session"}
		}
		result, err := m.Control(ctx, "session.get", map[string]any{"providerId": "codex", "backend": "codex_local", "sessionId": input.SessionID, "metadataOnly": true})
		if err != nil {
			return nil, err
		}
		session, _ := result["session"].(map[string]any)
		if mapString(session, "providerId") != "codex" || mapString(session, "backend") != "codex_local" {
			return nil, &sessionCallbackError{code: "INVALID_REQUEST", message: "callback target must be an existing local Codex session"}
		}
	} else {
		if _, err := m.prepareCloudCallbackBaseline(ctx, input.SessionID); err != nil {
			return nil, err
		}
	}
	return map[string]any{"prepared": true, "executionOwner": "node"}, nil
}

func (m *AgentManager) prepareCloudCallbackBaseline(ctx context.Context, sessionID string) (string, error) {
	if err := validateCallbackOpaqueID(strings.TrimSpace(sessionID), "source session ID", 256); err != nil {
		return "", err
	}
	record, exists, err := m.storedSessionVisibilityRecord("codex", sessionID)
	if err != nil {
		return "", err
	}
	if exists && (record.Backend != sessionBackendChatGPTCloud || record.Visibility != sessionVisibilityVisible) {
		return "", &sessionCallbackError{code: "INVALID_REQUEST", message: "reuse requires a visible ChatGPT Cloud CHAT"}
	}
	detail, err := m.chatgptCloud.ReadCached(withChatGPTCloudReadSource(ctx, "callback_baseline"), sessionID, 5*time.Second)
	if err != nil {
		return "", err
	}
	if chatgptCloudConversationStatus(detail) == "running" {
		return "", &sessionCallbackError{code: "AGENT_SESSION_BUSY", message: "the selected Cloud CHAT is already running", retryable: true}
	}
	identity := chatgptCloudCompletionIdentity(detail)
	if identity == "" {
		return "", &sessionCallbackError{code: "CALLBACK_BASELINE_UNAVAILABLE", message: "the selected Cloud CHAT has no stable message identity", retryable: true}
	}
	return identity, nil
}

func (m *AgentManager) ownedCloudCallback(input agentControlParams) (sessionCallbackRegistration, error) {
	if m.callbackStore == nil {
		return sessionCallbackRegistration{}, callbackStoreUnavailableError()
	}
	r, exists, err := m.callbackStore.registrationFor(strings.TrimSpace(input.SessionID))
	if err != nil {
		return r, err
	}
	if !exists {
		return r, &sessionCallbackError{code: "CALLBACK_ROUTE_NOT_FOUND", message: "callback route is not registered", retryable: true}
	}
	if r.Generation != input.CallbackGeneration {
		return r, &sessionCallbackError{code: "CALLBACK_GENERATION_STALE", message: "callback generation does not match"}
	}
	if r.MissionID != input.CallbackMissionID || r.TaskID != input.CallbackTaskID || r.TargetSessionID != input.CallbackTargetSessionID {
		return r, &sessionCallbackError{code: "CALLBACK_OWNER_CONFLICT", message: "callback owner does not match"}
	}
	return r, nil
}

// A recovery CALL is routed to the registered Node. The Node synthesizes its
// durable recovery event; the Hub must never fetch/interpret a CHAT transcript.
func (m *AgentManager) sessionCallbackRecover(ctx context.Context, input agentControlParams) (map[string]any, error) {
	r, err := m.ownedCloudCallback(input)
	if err != nil {
		return nil, err
	}
	if !callbackRegistrationProviderActive(r) {
		return map[string]any{"recoveryQueued": false, "settled": true, "executionOwner": "node"}, nil
	}
	settled, err := m.recoverCompletedCloudCallbackState(withChatGPTCloudReadSource(ctx, "routed_recovery"), r.SourceSessionID, r.Generation, false)
	if err != nil {
		return nil, err
	}
	latest, _, err := m.callbackStore.registrationFor(r.SourceSessionID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"cursor": latest.LastEventSequence, "progressed": latest.LastEventSequence > input.Cursor, "settled": settled, "recoveryQueued": latest.LastEventSequence > r.LastEventSequence, "executionOwner": "node"}, nil
}

// The caller reserves the attempt in its task ledger; only the owning Node
// checks provider state and decides whether a continuation may be sent.
func (m *AgentManager) sessionCallbackContinue(ctx context.Context, input agentControlParams) (map[string]any, error) {
	r, err := m.ownedCloudCallback(input)
	if err != nil {
		return nil, err
	}
	if !callbackRegistrationProviderActive(r) {
		return map[string]any{"continueSent": false, "recoveryAction": "controller_decision"}, nil
	}
	allowed, err := m.callbackStore.providerRecoveryAllowed(r.SourceSessionID, r.Generation)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return map[string]any{"continueSent": false, "recoveryAction": "controller_decision"}, nil
	}
	detail, err := m.readChatGPTCloud(withChatGPTCloudReadSource(ctx, "callback_continue"), r.SourceSessionID, chatgptCloudReadCacheTTL)
	if err != nil {
		return nil, err
	}
	status := chatgptCloudConversationStatus(detail)
	out := map[string]any{"continueSent": false, "providerStatus": status, "executionOwner": "node"}
	if status != "running" && status != "unknown" {
		out["recoveryAction"] = "controller_decision"
		if status == "completed" {
			out["recoveryAction"] = "status_poll"
		}
		return out, nil
	}
	if strings.TrimSpace(input.Prompt) == "" || len(input.Prompt) > 200 || strings.ContainsAny(input.Prompt, "\x00\r\n") {
		return nil, fmt.Errorf("continuation prompt must be a single line of at most 200 bytes")
	}
	if _, err := m.ownedCloudCallback(input); err != nil {
		return nil, err
	}
	allowed, err = m.callbackStore.providerRecoveryAllowed(r.SourceSessionID, r.Generation)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return map[string]any{"continueSent": false, "recoveryAction": "controller_decision"}, nil
	}
	result, err := m.chatgptCloudSend(ctx, input)
	if err != nil {
		return nil, err
	}
	out["continueSent"], out["recoveryAction"] = true, "poll_status"
	if id := mapString(result, "asyncTaskId"); id != "" {
		out["asyncTaskId"] = id
	}
	return out, nil
}
