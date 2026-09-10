package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const nativeRunnerProbeTimeout = 45 * time.Second

// Probe reads only the exact Cloud conversation bound to task. It never
// fabricates a callback result and never changes callback-store state.
func (t *nativeRunnerTransport) Probe(ctx context.Context, task nativeRunnerTask) (nativeRunnerProbe, error) {
	if t == nil || t.manager == nil || t.manager.chatgptCloud == nil {
		return nativeRunnerProbe{}, nodeRunnerUnavailableError()
	}
	if task.Request == nil || task.Receipt == nil {
		return nativeRunnerProbe{}, errors.New("runner probe has no immutable dispatch binding")
	}
	if task.Request.ProjectID != task.ProjectID || task.Request.TaskID != task.ID || task.Request.Round != task.Round {
		return nativeRunnerProbe{}, errors.New("runner probe request binding does not match the active task")
	}
	sessionID := strings.TrimSpace(task.Receipt.SessionID)
	if sessionID == "" || task.Receipt.Generation != int64(task.Round) {
		return nativeRunnerProbe{}, errors.New("runner probe binding has no exact session generation")
	}
	if t.manager.callbackStore != nil {
		registration, exists, err := t.manager.callbackStore.registrationFor(sessionID)
		if err != nil {
			return nativeRunnerProbe{}, err
		}
		if !exists || registration.Generation != task.Receipt.Generation || registration.MissionID != task.ProjectID || registration.TaskID != task.ID {
			return nativeRunnerProbe{}, errors.New("runner probe callback binding does not match the active task")
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, nativeRunnerProbeTimeout)
	defer cancel()
	if progress := t.manager.chatgptCloud.progress; progress != nil {
		if key, at := progress.recent(sessionID, task.Receipt.Generation); key != "" {
			return nativeRunnerProbe{ProgressKey: "sse:" + key, Running: true, Authoritative: false, ObservedAt: at.Unix(), Summary: "Recent Cloud SSE delta observed locally; not a completion receipt"}, nil
		}
	}
	probeCtx = withChatGPTCloudReadSource(probeCtx, "native_runner_probe")
	detail, err := t.manager.chatgptCloud.ReadFresh(probeCtx, sessionID)
	if err != nil {
		return nativeRunnerProbe{}, err
	}
	status := chatgptCloudConversationStatus(detail)
	authoritative := status != "unknown"
	running := authoritative && status == "running"
	terminal := authoritative && (status == "completed" || status == "failed" || status == "canceled")
	activity := chatgptCloudActivity(detail)
	progressKey := mapString(activity, "fingerprint")
	contextBytes, contextBytesKnown := chatgptCloudActiveContentBytes(detail)
	summary := fmt.Sprintf("Cloud status=%s authoritative=%t running=%t terminal=%t progressKey=%s", status, authoritative, running, terminal, progressKey)
	return nativeRunnerProbe{
		ProgressKey:       progressKey,
		Summary:           summary,
		Running:           running,
		Terminal:          terminal,
		ContextExhausted:  chatgptCloudContextExhausted(detail),
		ContextBytes:      contextBytes,
		Authoritative:     authoritative,
		ObservedAt:        time.Now().UTC().Unix(),
		ContextBytesKnown: contextBytesKnown,
	}, nil
}

// chatgptCloudActiveContentBytes measures only serialized message.content
// values on the provider's active branch. It is an observation byte count,
// not a token or model-capacity estimate; unrelated mapping branches and
// conversation metadata are deliberately excluded.
func chatgptCloudActiveContentBytes(detail map[string]any) (int64, bool) {
	mapping, ok := detail["mapping"].(map[string]any)
	if !ok || len(mapping) == 0 {
		return 0, false
	}
	path := chatgptCloudActiveBranch(detail, mapping)
	if len(path) == 0 {
		return 0, false
	}
	var total int64
	for _, nodeID := range path {
		node, ok := mapping[nodeID].(map[string]any)
		if !ok {
			return 0, false
		}
		message, _ := node["message"].(map[string]any)
		if message == nil {
			continue
		}
		content, exists := message["content"]
		if !exists {
			continue
		}
		raw, err := json.Marshal(content)
		if err != nil {
			return 0, false
		}
		total += int64(len(raw))
	}
	return total, true
}

// Continue sends one idempotent quick-chat continuation for the exact task
// binding. The scheduler owns the bounded stale-progress decision; this adapter
// preserves the same Cloud writer and lets the request message ID deduplicate
// an already accepted continuation.
func (t *nativeRunnerTransport) Continue(ctx context.Context, task nativeRunnerTask, prompt, idempotencyKey string) error {
	if t == nil || t.manager == nil || t.manager.chatgptCloud == nil {
		return nodeRunnerUnavailableError()
	}
	if task.Request == nil || task.Receipt == nil {
		return errors.New("runner continuation has no immutable dispatch binding")
	}
	if task.Request.ProjectID != task.ProjectID || task.Request.TaskID != task.ID || task.Request.Round != task.Round {
		return errors.New("runner continuation request binding does not match the active task")
	}
	sessionID := strings.TrimSpace(task.Receipt.SessionID)
	if sessionID == "" || task.Receipt.Generation != int64(task.Round) {
		return errors.New("runner continuation binding has no exact session generation")
	}
	prompt = strings.Join(strings.Fields(prompt), " ")
	if strings.TrimSpace(prompt) == "" {
		return errors.New("runner continuation prompt must not be empty")
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if len(idempotencyKey) < 12 || len(idempotencyKey) > 128 || strings.ContainsAny(idempotencyKey, "\x00\r\n") {
		return errors.New("runner continuation requires a stable idempotency key of 12..128 safe characters")
	}
	if t.manager.callbackStore != nil {
		registration, exists, err := t.manager.callbackStore.registrationFor(sessionID)
		if err != nil {
			return err
		}
		if !exists || registration.Generation != task.Receipt.Generation || registration.MissionID != task.ProjectID || registration.TaskID != task.ID {
			return errors.New("runner continuation callback binding does not match the active task")
		}
		allowed, err := t.manager.callbackStore.providerRecoveryAllowed(sessionID, task.Receipt.Generation)
		if err != nil {
			return err
		}
		if !allowed {
			return errors.New("runner continuation callback generation is no longer recoverable")
		}
	}
	requestMessageID := chatgptCloudSendRequestMessageID(sessionID, idempotencyKey)
	machineID, err := t.nativeRunnerMachineID()
	if err != nil {
		return err
	}
	taskRef := nativeRunnerTaskRef(*task.Request)
	prompt += fmt.Sprintf(" Continue protocol: call FastSpider_FS ai_control action=runner.checkpoint with responseContent.taskRef=%s using the same native provider machineId=%s; after waitingJobs or stage=context_handover, end this turn so Node can resume it.", taskRef, machineID)
	if len(prompt) > 32<<10 {
		return errors.New("runner continuation prompt exceeds 32768 UTF-8 bytes")
	}
	readCtx, cancel := context.WithTimeout(ctx, nativeRunnerProbeTimeout)
	readCtx = withChatGPTCloudReadSource(readCtx, "native_runner_continue_probe")
	_, err = t.manager.chatgptCloud.ReadFresh(readCtx, sessionID)
	cancel()
	if err != nil {
		return err
	}
	// The scheduler calls Continue only after its bounded stale-progress policy
	// has selected this exact writer. running/unknown is therefore not a second
	// transport-level veto: the stable request message ID lets the adapter replay
	// an already accepted continuation without creating a duplicate turn.
	defaults := t.manager.chatGPTCloudCreateDefaults()
	advancedConfig, err := LoadChatGPTAdvancedConfig(t.manager.dataDir)
	if err != nil {
		return err
	}
	serviceTier := ""
	if advancedConfig.RequestDefaults.EnableServiceTier {
		serviceTier = advancedConfig.RequestDefaults.ServiceTier
	}
	continuationCtx := ctx
	if t.manager.callbackStore != nil {
		registration, exists, registrationErr := t.manager.callbackStore.registrationFor(sessionID)
		if registrationErr != nil {
			return registrationErr
		}
		if exists {
			continuationCtx = context.WithValue(continuationCtx, chatgptCloudStreamEndKey{}, func() {
				t.manager.startCloudCallbackConfirmation(registration.SourceSessionID, registration.Generation)
			})
		}
	}
	_, err = t.manager.chatgptCloud.SendQuickIdempotentWithThinkingAndServiceTier(
		continuationCtx,
		sessionID,
		"",
		prompt,
		defaults.Model,
		defaults.Thinking,
		serviceTier,
		requestMessageID,
		advancedConfig.RequestDefaults,
	)
	if err != nil && chatgptCloudContextExhaustedError(err) {
		return &nativeRunnerContextExhaustedError{err: err}
	}
	return err
}

func (t *nativeRunnerTransport) nativeRunnerMachineID() (string, error) {
	if t == nil || t.manager == nil {
		return "", nodeRunnerUnavailableError()
	}
	t.manager.nativeRunnerMu.Lock()
	identityProvider := t.manager.nativeRunnerMachineID
	t.manager.nativeRunnerMu.Unlock()
	if identityProvider == nil {
		return "", errors.New("native runner machine identity is not ready; preserve the request until Node is connected")
	}
	machineID, err := identityProvider()
	if err != nil {
		return "", fmt.Errorf("resolve native runner machine ID: %w", err)
	}
	machineID = strings.TrimSpace(machineID)
	if machineID == "" {
		return "", errors.New("native runner machine identity is not ready; preserve the request until Node is connected")
	}
	return machineID, nil
}

// Interrupt cancels the exact Cloud conversation bound to task and only
// succeeds after a fresh terminal observation proves that the writer ended.
// An accepted cancel request without terminal proof remains an error so the
// recovery scheduler keeps the writer fence and does not rotate early.
func (t *nativeRunnerTransport) Interrupt(ctx context.Context, task nativeRunnerTask) (nativeRunnerProbe, error) {
	if t == nil || t.manager == nil || t.manager.chatgptCloud == nil {
		return nativeRunnerProbe{}, nodeRunnerUnavailableError()
	}
	if task.Request == nil || task.Receipt == nil {
		return nativeRunnerProbe{}, errors.New("runner interruption has no immutable dispatch binding")
	}
	if task.Request.ProjectID != task.ProjectID || task.Request.TaskID != task.ID || task.Request.Round != task.Round {
		return nativeRunnerProbe{}, errors.New("runner interruption request binding does not match the active task")
	}
	sessionID := strings.TrimSpace(task.Receipt.SessionID)
	if sessionID == "" || task.Receipt.Generation != int64(task.Round) {
		return nativeRunnerProbe{}, errors.New("runner interruption binding has no exact session generation")
	}
	if t.manager.callbackStore != nil {
		registration, exists, err := t.manager.callbackStore.registrationFor(sessionID)
		if err != nil {
			return nativeRunnerProbe{}, err
		}
		if !exists || registration.Generation != task.Receipt.Generation || registration.MissionID != task.ProjectID || registration.TaskID != task.ID {
			return nativeRunnerProbe{}, errors.New("runner interruption callback binding does not match the active task")
		}
	}
	interruptCtx, cancel := context.WithTimeout(ctx, nativeRunnerProbeTimeout)
	defer cancel()
	if err := t.manager.chatgptCloud.Cancel(interruptCtx, sessionID); err != nil {
		return nativeRunnerProbe{}, err
	}
	probe, err := t.Probe(interruptCtx, task)
	if err != nil {
		return nativeRunnerProbe{}, err
	}
	if !probe.Terminal {
		return nativeRunnerProbe{}, fmt.Errorf("Cloud cancellation accepted but terminal proof is unavailable: %s", probe.Summary)
	}
	return probe, nil
}

// Checkpoint resolves the immutable native task binding before handing the
// bounded, non-terminal progress payload to the runner ledger.
func (t *nativeRunnerTransport) Checkpoint(ctx context.Context, params map[string]any) (map[string]any, error) {
	if t == nil || t.manager == nil || t.manager.callbackStore == nil || t.manager.nativeRunner == nil {
		return nil, nodeRunnerUnavailableError()
	}
	var input struct {
		ProjectID   string   `json:"projectId,omitempty"`
		TaskID      string   `json:"taskId,omitempty"`
		Round       int      `json:"round,omitempty"`
		SessionID   string   `json:"sessionId,omitempty"`
		TaskRef     string   `json:"taskRef"`
		Summary     string   `json:"summary"`
		NextStep    string   `json:"nextStep"`
		Stage       string   `json:"stage"`
		Evidence    []string `json:"evidence,omitempty"`
		WaitingJobs []string `json:"waitingJobs,omitempty"`
	}
	if err := decodeParams(params, &input); err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.TaskRef) == "" {
		return nil, errors.New("runner.checkpoint requires taskRef")
	}
	registrations, _, err := t.manager.callbackStore.registrationsSnapshot("", "")
	if err != nil {
		return nil, err
	}
	var registration sessionCallbackRegistration
	found := false
	for _, candidate := range registrations {
		if candidate.CallbackClaimTransport != callbackClaimTransportLocal ||
			nativeRunnerTaskRef(nativeRunnerDispatch{ProjectID: candidate.MissionID, TaskID: candidate.TaskID, Round: int(candidate.Generation)}) != input.TaskRef {
			continue
		}
		if found {
			return nil, errors.New("runner.checkpoint taskRef matches multiple callback bindings")
		}
		registration, found = candidate, true
	}
	if !found {
		return nil, errors.New("runner.checkpoint taskRef does not match an assigned task")
	}
	if !callbackRegistrationProviderActive(registration) {
		return nil, errors.New("runner.checkpoint callback binding is no longer active")
	}
	if input.ProjectID != "" && input.ProjectID != registration.MissionID ||
		input.TaskID != "" && input.TaskID != registration.TaskID ||
		input.Round > 0 && int64(input.Round) != registration.Generation ||
		input.SessionID != "" && input.SessionID != registration.SourceSessionID {
		return nil, errors.New("runner.checkpoint callback binding does not match the assigned task")
	}
	input.ProjectID, input.TaskID, input.Round, input.SessionID = registration.MissionID, registration.TaskID, int(registration.Generation), registration.SourceSessionID
	checkpoint := nativeRunnerCheckpoint{Summary: input.Summary, NextStep: input.NextStep, Stage: input.Stage, Evidence: input.Evidence, WaitingJobs: input.WaitingJobs}
	if err := t.manager.nativeRunner.checkpoint(ctx, input.ProjectID, input.TaskID, input.Round, input.SessionID, checkpoint); err != nil {
		return nil, err
	}
	return map[string]any{"accepted": true, "checkpointSaved": true, "taskRef": input.TaskRef, "projectId": input.ProjectID, "taskId": input.TaskID, "round": input.Round, "sessionId": input.SessionID}, nil
}

func chatgptCloudContextExhausted(detail map[string]any) bool {
	values := []any{detail["context_error_code"], detail["contextErrorCode"], detail["error_code"], detail["errorCode"], detail["status"]}
	if nested, ok := detail["error"].(map[string]any); ok {
		values = append(values, nested["code"], nested["type"], nested["reason"])
	}
	for _, value := range values {
		code, ok := value.(string)
		if !ok {
			continue
		}
		code = strings.ToLower(strings.TrimSpace(code))
		code = strings.ReplaceAll(code, "-", "_")
		if strings.Contains(code, "context_length_exceeded") || strings.Contains(code, "context_window_exceeded") || code == "context_exhausted" || code == "max_context_length" {
			return true
		}
	}
	return false
}

func chatgptCloudContextExhaustedError(err error) bool {
	var providerErr *chatGPTCloudHTTPError
	if !errors.As(err, &providerErr) {
		return false
	}
	return chatgptCloudContextCode(providerErr.providerCode)
}

func chatgptCloudContextCode(code string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, "-", "_")
	return strings.Contains(code, "context_length_exceeded") ||
		strings.Contains(code, "context_window_exceeded") ||
		code == "context_exhausted" ||
		code == "max_context_length"
}
