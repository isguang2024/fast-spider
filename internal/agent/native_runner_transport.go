package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// nativeRunnerTransport is the Node-owned side of the native project runner.
// It deliberately talks to the existing ChatGPT adapter and callback store;
// there is no Hub task or Python transport in this path.
type nativeRunnerTransport struct {
	manager *AgentManager
	dataDir string

	mu   sync.Mutex
	jobs nativeRunnerJobStore
}

// nativeRunnerRejectedError marks a deterministic preflight rejection where
// no provider request has been attempted. The runner core may terminally fail
// this block and let its planner repair the input; transport/timeout/unknown
// errors remain ordinary retryable failures under the same idempotency key.
type nativeRunnerRejectedError struct{ err error }

func (e *nativeRunnerRejectedError) Error() string { return e.err.Error() }
func (e *nativeRunnerRejectedError) Unwrap() error { return e.err }

type nativeRunnerJobStore struct {
	jobs     *nativeRunnerJobs
	executor nativeRunnerJobExecutor
}

func newNativeRunnerTransport(manager *AgentManager, dataDir string) *nativeRunnerTransport {
	return &nativeRunnerTransport{manager: manager, dataDir: dataDir}
}

func (t *nativeRunnerTransport) setJobExecutor(value any) error {
	if t == nil {
		return errors.New("native runner transport is unavailable")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.jobs.jobs != nil {
		_ = t.jobs.jobs.Close()
	}
	executor, _ := value.(nativeRunnerJobExecutor)
	t.jobs = nativeRunnerJobStore{executor: executor}
	return nil
}

func (t *nativeRunnerTransport) ensureJobs() (*nativeRunnerJobs, error) {
	if t == nil {
		return nil, errors.New("native runner transport is unavailable")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.jobs.jobs != nil {
		return t.jobs.jobs, nil
	}
	if t.jobs.executor == nil {
		return nil, errors.New("native runner job executor is unavailable")
	}
	jobs, err := newNativeRunnerJobs(t.dataDir, t.jobs.executor)
	if err != nil {
		return nil, err
	}
	t.jobs.jobs = jobs
	return jobs, nil
}

func (t *nativeRunnerTransport) closeJobs() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.jobs.jobs == nil {
		return nil
	}
	err := t.jobs.jobs.Close()
	t.jobs.jobs = nil
	return err
}

// Only goal completion or a real user decision wakes the communication thread.
// Dispatch, callback receipts, checks and ordinary repairs never do so.
func (t *nativeRunnerTransport) Notify(ctx context.Context, notice nativeRunnerNotice) (string, error) {
	if t == nil || t.manager == nil {
		return "", nodeRunnerUnavailableError()
	}
	result, err := t.manager.DeliverLocalCodexTurn(ctx, notice.ControllerSessionID, "FS 项目通知 "+notice.Key+"\n"+notice.Summary)
	if err != nil {
		return "", err
	}
	if err = validateSessionCallbackLocalCodexTurnDelivery(sessionCallbackDeliveryResultFromSessionSend(result)); err != nil {
		return "", err
	}
	return mapString(result, "turnId"), nil
}

func (t *nativeRunnerTransport) Dispatch(ctx context.Context, request nativeRunnerDispatch) (nativeRunnerReceipt, error) {
	if t == nil || t.manager == nil || t.manager.chatgptCloud == nil {
		return nativeRunnerReceipt{}, nodeRunnerUnavailableError()
	}
	if err := validateNativeRunnerDispatch(request, t.dataDir); err != nil {
		return nativeRunnerReceipt{}, &nativeRunnerRejectedError{err: err}
	}
	generation := int64(request.Round)
	if generation < 1 {
		return nativeRunnerReceipt{}, errors.New("runner round must be positive")
	}
	taskRef := nativeRunnerTaskRef(request)
	responseContent, _ := json.Marshal(map[string]any{"taskRef": taskRef, "outcome": "completed", "path": request.ResultPath})
	machineID := ""
	t.manager.nativeRunnerMu.Lock()
	identityProvider := t.manager.nativeRunnerMachineID
	t.manager.nativeRunnerMu.Unlock()
	if identityProvider != nil {
		var err error
		machineID, err = identityProvider()
		if err != nil {
			return nativeRunnerReceipt{}, fmt.Errorf("resolve native runner machine ID: %w", err)
		}
		machineID = strings.TrimSpace(machineID)
	}
	if machineID == "" {
		return nativeRunnerReceipt{}, errors.New("native runner machine identity is not ready; preserve the request until Node is connected")
	}
	controlEnvelope, _ := json.Marshal(map[string]any{"machineId": machineID, "action": "runner.submit", "responseContent": json.RawMessage(responseContent)})
	prompt := request.Prompt + "\n\nNative runner binding: taskRef=" + taskRef + ". Use the installed FastSpider_FS MCP tools file_read to read the packet, file_edit to create the assigned result file, and ai_control to submit. Low-level capability names such as file.read are not MCP tool names. Tools can be deferred: if file_read, file_edit or ai_control is absent from the current tool context, use api_tool.list_resources with paths=[\"FastSpider_FS\"] and query=\"file_read file_edit ai_control\" to load those tools; discovering only fsprobe/capability_list/machine_list does not load all tools. If needed load the exact tool guide with capability_list(view=\"tool\",name=\"file_read\") and the corresponding names. After each meaningful milestone, persist a bounded non-terminal checkpoint through FastSpider_Local agent.control action=runner.checkpoint with responseContent containing taskRef, summary, nextStep, stage, evidence and waitingJobs. After writing the result, call FastSpider_FS ai_control with this exact object: " + string(controlEnvelope) + ". The Node resolves project, task, round and Cloud session from this immutable taskRef; do not create another CHAT or invent a session ID."

	sessionID := strings.TrimSpace(request.TargetSessionID)
	if sessionID != "" {
		// A reused CHAT has a known source ID, so make the callback route durable
		// before sending the turn. The route is armed only after the provider
		// accepts the turn; a failed send remains recoverable without nudging the
		// controller session.
		if err := t.registerCallback(ctx, request, sessionID, "reuse"); err != nil {
			return nativeRunnerReceipt{}, err
		}
		defaults := t.manager.chatGPTCloudCreateDefaults()
		if _, err := t.manager.Control(ctx, "session.send", map[string]any{
			"providerId":     "codex",
			"backend":        sessionBackendChatGPTCloud,
			"sessionId":      sessionID,
			"mode":           "quick_chat",
			"model":          defaults.Model,
			"thinking":       defaults.Thinking,
			"prompt":         prompt,
			"idempotencyKey": request.IdempotencyKey,
		}); err != nil {
			return nativeRunnerReceipt{}, err
		}
	} else {
		// Route new CHAT creation through the existing session.create path. It
		// persists the request idempotency key and ambiguous create state in the
		// durable session-create store; direct CreateQuick would create a second
		// conversation whenever its response is lost.
		created, err := t.manager.Control(ctx, "session.create", map[string]any{
			"providerId":       "codex",
			"backend":          sessionBackendChatGPTCloud,
			"mode":             "quick_chat",
			"prompt":           prompt,
			"workingDirectory": request.WorkingDirectory,
			"idempotencyKey":   request.IdempotencyKey,
		})
		if err != nil {
			return nativeRunnerReceipt{}, err
		}
		sessionID = strings.TrimSpace(mapString(created, "sessionId"))
	}
	if sessionID == "" {
		return nativeRunnerReceipt{}, errors.New("ChatGPT Cloud did not return a conversation ID")
	}
	if strings.TrimSpace(request.TargetSessionID) == "" {
		if err := t.registerCallback(ctx, request, sessionID, "target"); err != nil {
			return nativeRunnerReceipt{}, err
		}
	}
	if err := t.armCallback(ctx, request, sessionID); err != nil {
		return nativeRunnerReceipt{}, err
	}
	return nativeRunnerReceipt{SessionID: sessionID, TaskRef: taskRef, Generation: generation, ResultPath: request.ResultPath, InDoubt: false}, nil
}

func (t *nativeRunnerTransport) registerCallback(ctx context.Context, request nativeRunnerDispatch, sessionID, mode string) error {
	_, err := t.manager.sessionCallbackRegister(ctx, agentControlParams{
		SessionID: sessionID, CallbackTargetSessionID: request.ControllerSessionID,
		Mode:              mode,
		CallbackMissionID: request.ProjectID, CallbackTaskID: request.TaskID,
		CallbackGeneration: int64(request.Round), CallbackType: "local_file",
		CallbackClaimTransport: callbackClaimTransportLocal, CallbackDeliverablePath: request.ResultPath,
		CallbackArmRequired:  true,
		CallbackNativeRunner: true,
	})
	return err
}

func (t *nativeRunnerTransport) armCallback(ctx context.Context, request nativeRunnerDispatch, sessionID string) error {
	_, err := t.manager.sessionCallbackArm(ctx, agentControlParams{
		SessionID: sessionID, CallbackTargetSessionID: request.ControllerSessionID,
		CallbackMissionID: request.ProjectID, CallbackTaskID: request.TaskID,
		CallbackGeneration: int64(request.Round),
	})
	return err
}

func (t *nativeRunnerTransport) Observe(ctx context.Context, task nativeRunnerTask) (*nativeRunnerResult, error) {
	if t == nil || t.manager == nil || t.manager.callbackStore == nil {
		return nil, nodeRunnerUnavailableError()
	}
	if task.Receipt == nil || task.Request == nil {
		return nil, errors.New("runner task has no immutable dispatch binding")
	}
	target := strings.TrimSpace(task.Request.ControllerSessionID)
	if target == "" {
		return nil, errors.New("runner task has no controller session")
	}
	claimID := nativeRunnerClaimID(task)
	_, events, err := t.manager.callbackStore.claimExact(target, task.Receipt.SessionID, task.ProjectID, task.ID, task.Receipt.Generation, claimID, 1, time.Now().UTC(), callbackClaimTransportLocal)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	for _, event := range events {
		if event.SourceSessionID != task.Receipt.SessionID || event.MissionID != task.ProjectID || event.TaskID != task.ID || event.Generation != task.Receipt.Generation {
			_ = t.manager.callbackStore.releaseClaim(target, claimID, callbackClaimTransportLocal)
			return nil, fmt.Errorf("callback owner does not match runner task %s", task.ID)
		}
		if !callbackCompletionSourceIsFormal(event.CompletionSource) {
			_ = t.manager.callbackStore.releaseClaim(target, claimID, callbackClaimTransportLocal)
			return &nativeRunnerResult{EventID: event.EventKey, Outcome: event.CallbackOutcome, Summary: event.ResultText, Path: event.DeliverablePath, SHA256: event.ResultSHA256, RecoveryOnly: true, Terminal: false, ErrorCode: event.CallbackErrorCode}, nil
		}
		return &nativeRunnerResult{EventID: event.EventKey, Outcome: event.CallbackOutcome, Summary: event.ResultText, Path: event.DeliverablePath, SHA256: event.ResultSHA256, Terminal: true, ErrorCode: event.CallbackErrorCode}, nil
	}
	return nil, nil
}

// Acknowledge must be called by nativeRunner after storeResult commits. The
// callback claim is derived from the immutable project/task/round binding.
func (t *nativeRunnerTransport) Acknowledge(_ context.Context, task nativeRunnerTask) error {
	if t == nil || t.manager == nil || t.manager.callbackStore == nil {
		return nodeRunnerUnavailableError()
	}
	if task.Request == nil || task.Receipt == nil {
		return errors.New("runner task has no immutable callback binding")
	}
	evidencePath := ""
	if task.Result != nil && task.Result.Path != "" {
		status, _, digest := inspectCallbackDeliverable(task.Result.Path)
		if status != "ready" && task.Result.ErrorCode == "" {
			// Preserve the original transport error for an unprocessed missing
			// report; nativeRunner.storeResult materializes explicit recovery
			// evidence before retrying this ACK.
			evidencePath = ""
		} else if status != "ready" {
			return fmt.Errorf("runner evidence snapshot is %s", status)
		} else if task.Result.SHA256 != "" && task.Result.ErrorCode != "RUNNER_REPORT_CHANGED" && strings.TrimPrefix(task.Result.SHA256, "sha256:") != strings.TrimPrefix(digest, "sha256:") {
			return errors.New("runner evidence snapshot digest changed; preserve the callback binding")
		} else {
			evidencePath = task.Result.Path
		}
	}
	now := time.Now().UTC()
	_, events, err := t.manager.callbackStore.claimExact(task.Request.ControllerSessionID, task.Receipt.SessionID, task.ProjectID, task.ID, task.Receipt.Generation, nativeRunnerClaimID(task), 1, now, callbackClaimTransportLocal)
	if err != nil {
		return err
	}
	for _, event := range events {
		if task.Result != nil && event.EventKey != task.Result.EventID {
			return errors.New("callback changed after saved result; do not acknowledge another event")
		}
	}
	_, retired, err := t.manager.callbackStore.acknowledgeClaimAndRetireWithEvidence(task.Request.ControllerSessionID, nativeRunnerClaimID(task), now, evidencePath, callbackClaimTransportLocal)
	if err == nil && t.manager.chatgptCloud != nil {
		for _, registration := range retired {
			t.manager.chatgptCloud.ReleaseCallbackRealtimeForGeneration(registration.SourceSessionID, registration.Generation)
		}
	}
	return err
}

func (t *nativeRunnerTransport) Submit(ctx context.Context, params map[string]any) (map[string]any, error) {
	if t == nil || t.manager == nil || t.manager.callbackStore == nil {
		return nil, nodeRunnerUnavailableError()
	}
	var input struct {
		ProjectID string `json:"projectId,omitempty"`
		TaskID    string `json:"taskId,omitempty"`
		Round     int    `json:"round,omitempty"`
		SessionID string `json:"sessionId,omitempty"`
		TaskRef   string `json:"taskRef"`
		Outcome   string `json:"outcome,omitempty"`
		Path      string `json:"path,omitempty"`
	}
	if err := decodeParams(params, &input); err != nil {
		return nil, err
	}
	if input.TaskRef == "" {
		return nil, errors.New("runner.submit requires taskRef")
	}
	if input.Outcome == "" {
		input.Outcome = "completed"
	}
	if input.Outcome != "completed" && input.Outcome != "blocked" && input.Outcome != "failed" {
		return nil, errors.New("runner.submit outcome must be completed, blocked, or failed")
	}
	registrations, _, err := t.manager.callbackStore.registrationsSnapshot("", "")
	if err != nil {
		return nil, err
	}
	var registration sessionCallbackRegistration
	found := false
	for _, candidate := range registrations {
		if candidate.CallbackClaimTransport != callbackClaimTransportLocal || nativeRunnerTaskRef(nativeRunnerDispatch{ProjectID: candidate.MissionID, TaskID: candidate.TaskID, Round: int(candidate.Generation)}) != input.TaskRef {
			continue
		}
		if found {
			return nil, errors.New("runner.submit taskRef matches multiple callback bindings")
		}
		registration, found = candidate, true
	}
	if !found {
		return nil, errors.New("runner.submit taskRef does not match an assigned task")
	}
	if input.ProjectID != "" && input.ProjectID != registration.MissionID || input.TaskID != "" && input.TaskID != registration.TaskID || input.Round > 0 && int64(input.Round) != registration.Generation || input.SessionID != "" && input.SessionID != registration.SourceSessionID {
		return nil, errors.New("runner.submit callback binding does not match the assigned task")
	}
	input.ProjectID, input.TaskID, input.Round, input.SessionID = registration.MissionID, registration.TaskID, int(registration.Generation), registration.SourceSessionID
	path := registration.DeliverablePath
	if input.Path != "" && filepath.Clean(input.Path) != filepath.Clean(path) {
		return nil, errors.New("runner.submit path does not match the registered result path")
	}
	status, size, digest := inspectCallbackDeliverable(path)
	if input.Outcome == "completed" && status != "ready" {
		return nil, fmt.Errorf("runner.submit result file is %s", status)
	}
	sequence := t.manager.callbackStore.maxEventSequence() + 1
	event := chatgptCloudEvent{Sequence: sequence, EventKey: "runner_submit_" + nativeRunnerClaimIDFromParts(input.ProjectID, input.TaskID, input.Round), Type: "conversation.turn.complete", ConversationID: input.SessionID, EventType: "native-runner-submit", Timestamp: time.Now().UTC(), CallbackType: "local_file", CallbackOutcome: input.Outcome, DeliverablePath: path, DeliverableStatus: status, ResultStatus: status, ResultBytes: size, ResultSHA256: digest}
	queued, err := t.manager.callbackStore.enqueue(event)
	if err != nil {
		return nil, err
	}
	if t.manager.callbackDispatcher != nil {
		t.manager.callbackDispatcher.signal()
	}
	return map[string]any{"accepted": true, "queued": queued, "projectId": input.ProjectID, "taskId": input.TaskID, "round": input.Round, "path": path, "status": status, "bytes": size, "sha256": digest}, nil
}

func (t *nativeRunnerTransport) StartCheck(ctx context.Context, request nativeRunnerCheckRequest) (string, error) {
	jobs, err := t.ensureJobs()
	if err != nil {
		return "", err
	}
	return jobs.StartCheck(ctx, request)
}

func (t *nativeRunnerTransport) WatchCheck(ctx context.Context, jobID string) (nativeRunnerCheckResult, error) {
	jobs, err := t.ensureJobs()
	if err != nil {
		return nativeRunnerCheckResult{}, err
	}
	return jobs.WatchCheck(ctx, jobID)
}

func validateNativeRunnerDispatch(request nativeRunnerDispatch, dataDir string) error {
	if request.ProjectID == "" || request.TaskID == "" || request.ControllerSessionID == "" || request.WorkingDirectory == "" || request.Prompt == "" || request.IdempotencyKey == "" || request.ResultPath == "" {
		return errors.New("native runner dispatch is missing a required binding")
	}
	if request.Round < 1 {
		return errors.New("native runner round must be positive")
	}
	root, err := filepath.Abs(request.WorkingDirectory)
	if err != nil {
		return err
	}
	result, err := filepath.Abs(request.ResultPath)
	if err != nil {
		return err
	}
	runnerDir, err := filepath.Abs(filepath.Join(dataDir, "native-runner"))
	if err != nil {
		return err
	}
	if !nativeRunnerWithin(root, request.WriteScope) || !nativeRunnerWithin(runnerDir, result) {
		return errors.New("native runner dispatch path or writeScope is outside its bound directory")
	}
	return nil
}

func nativeRunnerWithin(root, path string) bool {
	if path == "" {
		return true
	}
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func nativeRunnerTaskRef(request nativeRunnerDispatch) string {
	return "native_task_" + nativeRunnerClaimIDFromParts(request.ProjectID, request.TaskID, request.Round)
}

func nativeRunnerClaimID(task nativeRunnerTask) string {
	if task.Request == nil {
		return ""
	}
	return nativeRunnerClaimIDFromParts(task.ProjectID, task.ID, task.Round)
}

func nativeRunnerClaimIDFromParts(projectID, taskID string, round int) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + taskID + "\x00" + fmt.Sprintf("%d", round)))
	return "native_claim_" + hex.EncodeToString(sum[:])[:40]
}

func nodeRunnerUnavailableError() error { return errors.New("native runner transport is unavailable") }
