package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/store"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/isguang2024/fast-spider/internal/security"
)

const (
	cloudCollaborationStateVersion      = 2
	cloudCollaborationIssueMarkdownPath = "docs/progress/04-open-issues.md"
	// cloudCollaborationSelfActor lets an enrolled Cloud CHAT identify itself
	// without having to guess or copy the provider conversation ID. The Hub
	// resolves it against the task supplied in the same request.
	cloudCollaborationSelfActor = "$self"
)

type CloudCollaborationRequest struct {
	Action              string
	CollaborationID     string
	MachineID           string
	IdempotencyKey      string
	RequestHash         string
	ExpectedRevision    int64
	ActorSessionID      string
	ActorRole           string
	ControllerSessionID string
	DispatcherSessionID string
	Title               string
	Goal                string
	Scope               string
	DoneWhen            string
	WorkingDirectory    string
	AllowedActions      []string
	MaxDepth            int
	MaxActiveChats      int
	MaxCreates          int
	HeartbeatMinutes    int
	StallMinutes        int
	Deadline            time.Time
	GoalID              string
	GoalStatus          string
	TaskID              string
	TaskStatus          string
	ParentSessionID     string
	TargetSessionID     string
	Prompt              string
	AccessMode          string
	WriteScope          string
	DeliverablePath     string
	CallbackType        string
	EventID             string
	EventSequence       int64
	EventType           string
	EventGeneration     int64
	ResultID            string
	ResultStatus        string
	ResultBytes         int64
	ResultSHA256        string
	DeliverableStatus   string
	DecisionID          string
	DecisionStatus      string
	Question            string
	Options             []string
	Recommendation      string
	Checkpoint          string
	InactiveVerified    bool
	Limit               int
}

type cloudCollaborationState struct {
	Version              int                          `json:"version"`
	Title                string                       `json:"title"`
	Goal                 string                       `json:"goal"`
	Scope                string                       `json:"scope"`
	DoneWhen             string                       `json:"doneWhen"`
	Status               string                       `json:"status"`
	WorkingDirectory     string                       `json:"workingDirectory"`
	ControllerSessionID  string                       `json:"controllerSessionId"`
	DispatcherSessionID  string                       `json:"dispatcherSessionId"`
	Generation           int64                        `json:"generation"`
	AllowedActions       []string                     `json:"allowedActions"`
	MaxDepth             int                          `json:"maxDepth"`
	MaxActiveChats       int                          `json:"maxActiveChats"`
	MaxCreates           int                          `json:"maxCreates"`
	CreateCount          int                          `json:"createCount"`
	HeartbeatMinutes     int                          `json:"heartbeatMinutes"`
	StallMinutes         int                          `json:"stallMinutes"`
	Deadline             string                       `json:"deadline,omitempty"`
	Lease                cloudCollaborationLease      `json:"lease"`
	Goals                []cloudCollaborationGoal     `json:"goals"`
	Tasks                []cloudCollaborationTask     `json:"tasks"`
	Chats                []cloudCollaborationChat     `json:"chats"`
	Events               []cloudCollaborationEvent    `json:"events"`
	Decisions            []cloudCollaborationDecision `json:"decisions"`
	Checkpoint           string                       `json:"checkpoint,omitempty"`
	CheckpointGeneration int64                        `json:"checkpointGeneration,omitempty"`
}

type cloudCollaborationLease struct {
	SessionID  string `json:"sessionId,omitempty"`
	Generation int64  `json:"generation,omitempty"`
	ExpiresAt  string `json:"expiresAt,omitempty"`
}

type cloudCollaborationGoal struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Evidence string `json:"evidence,omitempty"`
}

type cloudCollaborationTask struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Prompt          string   `json:"prompt"`
	Status          string   `json:"status"`
	ParentSession   string   `json:"parentSessionId,omitempty"`
	Depth           int      `json:"depth"`
	Generation      int64    `json:"generation"`
	AccessMode      string   `json:"accessMode"`
	WriteScope      string   `json:"writeScope,omitempty"`
	DeliverablePath string   `json:"deliverablePath,omitempty"`
	ResultPath      string   `json:"resultPath,omitempty"`
	CallbackType    string   `json:"callbackType"`
	AllowedActions  []string `json:"allowedActions"`
	IdempotencyKey  string   `json:"idempotencyKey"`
	TargetSessionID string   `json:"targetSessionId,omitempty"`
	ChatSessionID   string   `json:"chatSessionId,omitempty"`
	SessionMode     string   `json:"sessionMode,omitempty"`
	DeliveryStatus  string   `json:"deliveryStatus,omitempty"`
	ResultID        string   `json:"resultId,omitempty"`
	ResultStatus    string   `json:"resultStatus,omitempty"`
	ResultSHA256    string   `json:"resultSha256,omitempty"`
	ResultText      string   `json:"resultText,omitempty"`
	CreatedAt       string   `json:"createdAt"`
	UpdatedAt       string   `json:"updatedAt"`
}

type cloudCollaborationChat struct {
	SessionID                string   `json:"sessionId"`
	TaskID                   string   `json:"taskId"`
	ParentSession            string   `json:"parentSessionId,omitempty"`
	Depth                    int      `json:"depth"`
	Generation               int64    `json:"generation"`
	Status                   string   `json:"status"`
	AccessMode               string   `json:"accessMode"`
	WriteScope               string   `json:"writeScope,omitempty"`
	AllowedActions           []string `json:"allowedActions"`
	CallbackRegistered       bool     `json:"callbackRegistered"`
	CallbackArmed            bool     `json:"callbackArmed"`
	CallbackBaselineIdentity string   `json:"callbackBaselineIdentity,omitempty"`
	WatchCursor              int64    `json:"watchCursor"`
	LastObservedAt           string   `json:"lastObservedAt"`
	LastProgressAt           string   `json:"lastProgressAt"`
	QuietChecks              int      `json:"quietChecks"`
	StalledNotified          bool     `json:"stalledNotified"`
	ContinueAttempts         int      `json:"continueAttempts,omitempty"`
	LastContinueAt           string   `json:"lastContinueAt,omitempty"`
	Reused                   bool     `json:"reused,omitempty"`
}

type cloudCollaborationEvent struct {
	ID                string `json:"id"`
	TaskID            string `json:"taskId"`
	SessionID         string `json:"sessionId"`
	Generation        int64  `json:"generation"`
	Sequence          int64  `json:"sequence"`
	Type              string `json:"type"`
	ResultID          string `json:"resultId,omitempty"`
	ResultStatus      string `json:"resultStatus,omitempty"`
	ResultBytes       int64  `json:"resultBytes,omitempty"`
	ResultSHA256      string `json:"resultSHA256,omitempty"`
	CallbackType      string `json:"callbackType,omitempty"`
	Status            string `json:"status"`
	DeliverablePath   string `json:"deliverablePath,omitempty"`
	DeliverableStatus string `json:"deliverableStatus,omitempty"`
	Signature         string `json:"signature"`
	CreatedAt         string `json:"createdAt"`
}

type cloudCollaborationDecision struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	TargetID       string   `json:"targetId,omitempty"`
	Question       string   `json:"question"`
	Options        []string `json:"options,omitempty"`
	Recommendation string   `json:"recommendation,omitempty"`
	Status         string   `json:"status"`
	CreatedAt      string   `json:"createdAt"`
}

func (s *Service) CloudCollaboration(ctx context.Context, ownerID string, req CloudCollaborationRequest) (map[string]any, error) {
	action := strings.TrimSpace(req.Action)
	switch action {
	case "create":
		return s.createCloudCollaboration(ctx, ownerID, req)
	case "list":
		return s.listCloudCollaborations(ctx, ownerID, req.Limit)
	}
	rec, state, err := s.loadCloudCollaboration(ctx, ownerID, req.CollaborationID)
	if err != nil {
		return nil, err
	}
	if err := resolveCloudCollaborationSelfActor(&state, &req); err != nil {
		return nil, err
	}
	role, err := authorizeCloudCollaborationActor(state, req.ActorSessionID, req.ActorRole)
	if err != nil {
		return nil, err
	}
	if action == "get" {
		return cloudCollaborationView(rec, state, role), nil
	}
	if action == "tick" {
		return cloudCollaborationTick(rec, state), nil
	}
	if req.ExpectedRevision != rec.Revision {
		return nil, store.ErrConflict
	}
	if state.Status == "completed" || state.Status == "canceled" {
		return nil, store.ErrConflict
	}
	if cloudCollaborationActionNeedsLease(action, role) {
		if err := requireCloudCollaborationLease(state, req.ActorSessionID, role, s.now().UTC()); err != nil {
			return nil, err
		}
	}
	switch action {
	case "lease.acquire":
		if role != "dispatcher" {
			return nil, store.ErrUnauthorized
		}
		return s.cloudCollaborationAcquireLease(ctx, ownerID, rec, state, req)
	case "lease.release":
		if role != "dispatcher" || state.Lease.SessionID != req.ActorSessionID {
			return nil, store.ErrUnauthorized
		}
		state.Lease = cloudCollaborationLease{}
	case "goal.add":
		if role != "controller" && role != "dispatcher" {
			return nil, store.ErrUnauthorized
		}
		if err := cloudCollaborationAddGoal(&state, req); err != nil {
			return nil, err
		}
	case "goal.update":
		if role != "controller" && role != "dispatcher" {
			return nil, store.ErrUnauthorized
		}
		if err := cloudCollaborationUpdateGoal(&state, req); err != nil {
			return nil, err
		}
	case "task.add":
		if role != "dispatcher" && role != "chat" {
			return nil, store.ErrUnauthorized
		}
		if err := cloudCollaborationAddTask(&state, req, role); err != nil {
			return nil, err
		}
	case "task.update":
		if role != "dispatcher" {
			return nil, store.ErrUnauthorized
		}
		if err := cloudCollaborationUpdateTask(&state, req); err != nil {
			return nil, err
		}
	case "task.dispatch":
		if role != "dispatcher" && role != "chat" {
			return nil, store.ErrUnauthorized
		}
		out, err := s.cloudCollaborationDispatchTask(ctx, ownerID, rec, state, req, role)
		if err != nil {
			return nil, err
		}
		return cloudCollaborationDispatchReceipt(out, role), nil
	case "status.poll":
		if role != "dispatcher" && role != "chat" {
			return nil, store.ErrUnauthorized
		}
		return s.cloudCollaborationPollStatus(ctx, ownerID, rec, state, req)
	case "chat.continue":
		if role != "dispatcher" {
			return nil, store.ErrUnauthorized
		}
		return s.cloudCollaborationContinueChat(ctx, ownerID, rec, state, req)
	case "event.ingest":
		if role != "dispatcher" && role != "chat" {
			return nil, store.ErrUnauthorized
		}
		if err := cloudCollaborationIngestEvent(&state, req); err != nil {
			return nil, err
		}
	case "event.ack":
		if role != "dispatcher" && role != "chat" {
			return nil, store.ErrUnauthorized
		}
		if err := cloudCollaborationAckEvent(&state, req); err != nil {
			return nil, err
		}
		cloudCollaborationAdvanceAcknowledgedEvent(&state, req.EventID, s.now().UTC())
		if cloudCollaborationReadyToClose(state) {
			return s.cloudCollaborationClose(ctx, ownerID, rec, state)
		}
	case "decision.request":
		if role != "dispatcher" && role != "chat" {
			return nil, store.ErrUnauthorized
		}
		if err := cloudCollaborationRequestDecision(&state, req); err != nil {
			return nil, err
		}
	case "decision.resolve":
		if role != "controller" {
			return nil, store.ErrUnauthorized
		}
		if err := cloudCollaborationResolveDecision(&state, req); err != nil {
			return nil, err
		}
	case "pause":
		if role != "controller" {
			return nil, store.ErrUnauthorized
		}
		state.Status = "paused"
	case "resume":
		if role != "controller" {
			return nil, store.ErrUnauthorized
		}
		state.Status = "active"
	case "compact":
		if role != "controller" && role != "dispatcher" {
			return nil, store.ErrUnauthorized
		}
		if len(req.Checkpoint) == 0 || len(req.Checkpoint) > 4000 {
			return nil, store.ErrConflict
		}
		state.Checkpoint = req.Checkpoint
		state.CheckpointGeneration = state.Generation
	case "chat.rotate":
		if role != "dispatcher" || !req.InactiveVerified {
			return nil, store.ErrUnauthorized
		}
		if err := s.unregisterCloudCollaborationTaskCallback(ctx, ownerID, rec, &state, req.TaskID); err != nil {
			return nil, err
		}
		if err := cloudCollaborationRotateChat(&state, req); err != nil {
			return nil, err
		}
	case "chat.delete":
		if role != "dispatcher" {
			return nil, store.ErrUnauthorized
		}
		return s.cloudCollaborationDeleteChat(ctx, ownerID, rec, state, req)
	case "close":
		if role != "controller" {
			return nil, store.ErrUnauthorized
		}
		return s.cloudCollaborationClose(ctx, ownerID, rec, state)
	default:
		return nil, store.ErrConflict
	}
	return s.saveCloudCollaboration(ctx, ownerID, rec, state)
}

func cloudCollaborationDispatchReceipt(out map[string]any, role string) map[string]any {
	if out == nil {
		out = map[string]any{}
	}
	out["awaitMode"] = "callback"
	out["activePollingAllowed"] = false
	out["completionDelivery"] = "cloud-chat-self-callback"
	out["recoveryDelivery"] = "node-fallback-and-30-minute-scheduler-check"
	out["nextAction"] = "end_turn"
	out["nextActionReason"] = "the task was dispatched asynchronously; wait for the durable callback wake instead of calling session.get, session.watch, session.result, tick, or status.poll"
	if role == "dispatcher" {
		out["callerShouldYield"] = true
	} else {
		out["callerShouldYield"] = false
		out["callerGuidance"] = "continue only independent parent work; do not wait or poll for the dispatched CHAT"
	}
	return out
}

func (s *Service) createCloudCollaboration(ctx context.Context, ownerID string, req CloudCollaborationRequest) (map[string]any, error) {
	if len(req.IdempotencyKey) < 12 || len(req.IdempotencyKey) > 128 || strings.TrimSpace(req.MachineID) == "" || strings.TrimSpace(req.ControllerSessionID) == "" || strings.TrimSpace(req.DispatcherSessionID) == "" || strings.TrimSpace(req.ControllerSessionID) == strings.TrimSpace(req.DispatcherSessionID) {
		return nil, store.ErrConflict
	}
	if err := validateCloudCollaborationLimits(req); err != nil {
		return nil, err
	}
	requestHash := strings.TrimSpace(req.RequestHash)
	if requestHash == "" {
		raw, _ := json.Marshal([]any{req.MachineID, req.ControllerSessionID, req.DispatcherSessionID, req.Title, req.Goal, req.Scope, req.DoneWhen, req.WorkingDirectory, req.AllowedActions, req.MaxDepth, req.MaxActiveChats, req.MaxCreates, req.HeartbeatMinutes, req.StallMinutes, req.Deadline})
		sum := sha256.Sum256(raw)
		requestHash = hex.EncodeToString(sum[:])
	}
	if existing, err := s.store.LookupCloudCollaboration(ctx, ownerID, req.IdempotencyKey); err == nil {
		if existing.RequestHash != requestHash {
			return nil, store.ErrConflict
		}
		_, state, loadErr := s.loadCloudCollaboration(ctx, ownerID, existing.CollaborationID)
		if loadErr != nil {
			return nil, loadErr
		}
		return cloudCollaborationView(existing, state, "dispatcher"), nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if err := s.ensureCodexCloudCollaborationReady(ctx, ownerID, req.MachineID); err != nil {
		return nil, err
	}
	if err := s.validateCodexLocalCollaborationSession(ctx, ownerID, req.MachineID, req.ControllerSessionID); err != nil {
		return nil, err
	}
	if err := s.validateCodexLocalCollaborationSession(ctx, ownerID, req.MachineID, req.DispatcherSessionID); err != nil {
		return nil, err
	}
	id, err := security.RandomOpaque("collab_")
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	state := cloudCollaborationState{
		Version: cloudCollaborationStateVersion, Title: boundedCollaborationText(req.Title, 240), Goal: boundedCollaborationText(req.Goal, 2000), Scope: boundedCollaborationText(req.Scope, 1000), DoneWhen: boundedCollaborationText(req.DoneWhen, 2000), Status: "active",
		WorkingDirectory: strings.TrimSpace(req.WorkingDirectory), ControllerSessionID: strings.TrimSpace(req.ControllerSessionID), DispatcherSessionID: strings.TrimSpace(req.DispatcherSessionID), Generation: 1,
		AllowedActions: normalizedCloudActions(req.AllowedActions), MaxDepth: boundedInt(req.MaxDepth, 1, 8, 3), MaxActiveChats: boundedInt(req.MaxActiveChats, 1, 8, 3), MaxCreates: boundedInt(req.MaxCreates, 1, 100, 20), HeartbeatMinutes: boundedInt(req.HeartbeatMinutes, 5, 1440, 30), StallMinutes: boundedInt(req.StallMinutes, 15, 1440, 60),
		Goals: []cloudCollaborationGoal{}, Tasks: []cloudCollaborationTask{}, Chats: []cloudCollaborationChat{}, Events: []cloudCollaborationEvent{}, Decisions: []cloudCollaborationDecision{},
	}
	if state.Title == "" || state.Goal == "" || state.DoneWhen == "" || state.WorkingDirectory == "" || len(state.AllowedActions) == 0 {
		return nil, store.ErrConflict
	}
	if !req.Deadline.IsZero() {
		if !req.Deadline.After(now) {
			return nil, store.ErrExpired
		}
		state.Deadline = req.Deadline.UTC().Format(time.RFC3339)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	rec, err := s.store.CreateCloudCollaboration(ctx, store.CloudCollaborationRecord{CollaborationID: id, OwnerID: ownerID, MachineID: req.MachineID, IdempotencyKey: req.IdempotencyKey, RequestHash: requestHash, Status: state.Status, StateJSON: string(raw), Revision: 1, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return nil, err
	}
	return cloudCollaborationView(rec, state, "dispatcher"), nil
}

func (s *Service) listCloudCollaborations(ctx context.Context, ownerID string, limit int) (map[string]any, error) {
	limit = boundedInt(limit, 1, 100, 20)
	records, err := s.store.ListCloudCollaborations(ctx, ownerID, limit)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		var state cloudCollaborationState
		if json.Unmarshal([]byte(rec.StateJSON), &state) != nil {
			continue
		}
		items = append(items, map[string]any{"collaborationId": rec.CollaborationID, "title": state.Title, "status": rec.Status, "revision": rec.Revision, "updatedAt": rec.UpdatedAt})
	}
	return map[string]any{"collaborations": items, "bounded": true}, nil
}

func (s *Service) loadCloudCollaboration(ctx context.Context, ownerID, id string) (store.CloudCollaborationRecord, cloudCollaborationState, error) {
	rec, err := s.store.GetCloudCollaboration(ctx, ownerID, strings.TrimSpace(id))
	if err != nil {
		return store.CloudCollaborationRecord{}, cloudCollaborationState{}, err
	}
	var state cloudCollaborationState
	if err := json.Unmarshal([]byte(rec.StateJSON), &state); err != nil || (state.Version != 1 && state.Version != cloudCollaborationStateVersion) {
		return store.CloudCollaborationRecord{}, cloudCollaborationState{}, store.ErrConflict
	}
	if state.Version == 1 {
		for i := range state.Chats {
			state.Chats[i].CallbackArmed = state.Chats[i].CallbackRegistered
		}
		state.Version = cloudCollaborationStateVersion
	}
	// Collaborations created before typed callbacks used local result files.
	// Preserve that behavior and derive the same stable path on every load so
	// an in-flight CHAT remains recoverable across a Hub upgrade or restart.
	for i := range state.Tasks {
		if strings.TrimSpace(state.Tasks[i].CallbackType) == "" {
			state.Tasks[i].CallbackType = protocolv1.CloudCallbackTypeLocalFile
		}
		if state.Tasks[i].CallbackType == protocolv1.CloudCallbackTypeLocalFile && strings.TrimSpace(state.Tasks[i].ResultPath) == "" {
			state.Tasks[i].ResultPath = state.Tasks[i].DeliverablePath
			if state.Tasks[i].ResultPath == "" {
				state.Tasks[i].ResultPath = cloudCollaborationManagedResultPath(state.WorkingDirectory, rec.CollaborationID, state.Tasks[i].ID, state.Tasks[i].Generation)
			}
		}
	}
	return rec, state, nil
}

func (s *Service) saveCloudCollaboration(ctx context.Context, ownerID string, rec store.CloudCollaborationRecord, state cloudCollaborationState) (map[string]any, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	updated, err := s.store.UpdateCloudCollaboration(ctx, ownerID, rec.CollaborationID, state.Status, string(raw), rec.Revision, s.now().UTC())
	if err != nil {
		return nil, err
	}
	return cloudCollaborationView(updated, state, "dispatcher"), nil
}

func authorizeCloudCollaborationActor(state cloudCollaborationState, sessionID, claimedRole string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	role := ""
	switch sessionID {
	case state.ControllerSessionID:
		role = "controller"
	case state.DispatcherSessionID:
		role = "dispatcher"
	default:
		for _, chat := range state.Chats {
			if chat.SessionID == sessionID && chat.Status != "archived" && chat.Status != "deleted" {
				role = "chat"
				break
			}
		}
	}
	if role == "" || (strings.TrimSpace(claimedRole) != "" && claimedRole != role) {
		return "", store.ErrUnauthorized
	}
	return role, nil
}

// resolveCloudCollaborationSelfActor binds the explicit "$self" marker to the
// Cloud CHAT already recorded for the requested task. It is intentionally
// limited to chat actors and task-scoped actions so a controller cannot use the
// marker to impersonate an arbitrary session.
func resolveCloudCollaborationSelfActor(state *cloudCollaborationState, req *CloudCollaborationRequest) error {
	if state == nil || req == nil || strings.TrimSpace(req.ActorSessionID) != cloudCollaborationSelfActor {
		return nil
	}
	if strings.TrimSpace(req.ActorRole) != "chat" {
		return store.ErrUnauthorized
	}
	taskID := strings.TrimSpace(req.TaskID)
	if taskID == "" && strings.TrimSpace(req.Action) == "event.ack" {
		for _, event := range state.Events {
			if event.ID == strings.TrimSpace(req.EventID) {
				taskID = event.TaskID
				break
			}
		}
	}
	if taskID == "" {
		return store.ErrUnauthorized
	}
	idx := taskIndex(*state, taskID)
	if idx < 0 || strings.TrimSpace(state.Tasks[idx].ChatSessionID) == "" {
		return store.ErrUnauthorized
	}
	req.ActorSessionID = state.Tasks[idx].ChatSessionID
	return nil
}

func cloudCollaborationView(rec store.CloudCollaborationRecord, state cloudCollaborationState, role string) map[string]any {
	goalCounts, taskCounts := map[string]int{}, map[string]int{}
	for _, goal := range state.Goals {
		goalCounts[goal.Status]++
	}
	for _, task := range state.Tasks {
		taskCounts[task.Status]++
	}
	decisions := make([]map[string]any, 0)
	for _, d := range state.Decisions {
		if d.Status == "requested" {
			decisions = append(decisions, map[string]any{"decisionId": d.ID, "kind": d.Kind, "question": d.Question, "options": d.Options, "recommendation": d.Recommendation})
		}
	}
	out := map[string]any{"collaborationId": rec.CollaborationID, "title": state.Title, "status": state.Status, "revision": rec.Revision, "generation": state.Generation, "goalCounts": goalCounts, "taskCounts": taskCounts, "activeChats": activeCloudChats(state), "createCount": state.CreateCount, "pendingDecisions": decisions, "nextActions": cloudCollaborationTickActions(state), "role": role}
	if role != "controller" {
		out["machineId"] = rec.MachineID
		out["workingDirectory"] = state.WorkingDirectory
		out["allowedActions"] = state.AllowedActions
		out["limits"] = map[string]any{"maxDepth": state.MaxDepth, "maxActiveChats": state.MaxActiveChats, "maxCreates": state.MaxCreates, "heartbeatMinutes": state.HeartbeatMinutes, "stallMinutes": state.StallMinutes, "deadline": state.Deadline}
		out["tasks"] = state.Tasks
		out["chats"] = state.Chats
		out["events"] = state.Events
		out["checkpoint"] = state.Checkpoint
	}
	return out
}

func cloudCollaborationTick(rec store.CloudCollaborationRecord, state cloudCollaborationState) map[string]any {
	now := time.Now().UTC()
	actions := cloudCollaborationTickActionsAt(state, now)
	out := map[string]any{"collaborationId": rec.CollaborationID, "status": state.Status, "revision": rec.Revision, "actions": actions, "bounded": true, "externalCalls": 0, "idle": len(actions) == 0}
	if len(actions) == 0 {
		if next := cloudCollaborationNextStatusCheckAt(state, now); !next.IsZero() {
			out["nextCheckAt"] = next.Format(time.RFC3339)
		}
	}
	return out
}

func cloudCollaborationTickActions(state cloudCollaborationState) []map[string]any {
	return cloudCollaborationTickActionsAt(state, time.Now().UTC())
}

func cloudCollaborationTickActionsAt(state cloudCollaborationState, now time.Time) []map[string]any {
	actions := make([]map[string]any, 0, 8)
	if state.Status == "closing" {
		return []map[string]any{{"type": "retry_close"}}
	}
	if state.Status != "active" {
		return actions
	}
	for _, event := range state.Events {
		if event.Status == "pending" {
			actions = append(actions, map[string]any{"type": "event_pending", "eventId": event.ID, "taskId": event.TaskID})
		}
		if len(actions) == 8 {
			return actions
		}
	}
	for _, task := range state.Tasks {
		if task.Status == "queued" || task.Status == "create_in_doubt" {
			actions = append(actions, map[string]any{"type": "dispatch_task", "taskId": task.ID})
		}
		if task.Status == "active" && task.SessionMode == "reuse" && task.DeliveryStatus != "delivered" {
			actions = append(actions, map[string]any{"type": "dispatch_task", "taskId": task.ID, "deliveryRecovery": true})
		}
		if len(actions) == 8 {
			return actions
		}
	}
	for _, chat := range state.Chats {
		if chat.Status != "active" {
			continue
		}
		if !chat.CallbackRegistered || !chat.CallbackArmed {
			actions = append(actions, map[string]any{"type": "ensure_callback", "taskId": chat.TaskID})
			if len(actions) == 8 {
				return actions
			}
		}
		if !cloudCollaborationNextStatusPollAt(state, chat, now).After(now) {
			action := map[string]any{"type": "poll_chat_status", "taskId": chat.TaskID}
			if chat.StalledNotified {
				action["suspectedStalled"] = true
			}
			actions = append(actions, action)
		}
		if chat.StalledNotified {
			if chat.ContinueAttempts == 0 {
				actions = append(actions, map[string]any{
					"type":                 "continue_chat",
					"taskId":               chat.TaskID,
					"chatSessionId":        chat.SessionID,
					"recommendedPrompt":    "请继续",
					"requiresStatusCheck":  true,
					"automaticReplacement": false,
					"recordIssuesTo":       cloudCollaborationIssueMarkdownPath,
				})
			} else {
				actions = append(actions, map[string]any{
					"type":                 "chat_recovery_decision",
					"taskId":               chat.TaskID,
					"chatSessionId":        chat.SessionID,
					"continueAttempts":     chat.ContinueAttempts,
					"replacementAllowed":   true,
					"automaticReplacement": false,
					"recordIssuesTo":       cloudCollaborationIssueMarkdownPath,
				})
			}
		}
		if len(actions) == 8 {
			return actions
		}
	}
	return actions
}

func cloudCollaborationHeartbeatInterval(state cloudCollaborationState) time.Duration {
	minutes := state.HeartbeatMinutes
	if minutes <= 0 {
		minutes = 30
	}
	return time.Duration(minutes) * time.Minute
}

func cloudCollaborationNextStatusPollAt(state cloudCollaborationState, chat cloudCollaborationChat, now time.Time) time.Time {
	last, _ := time.Parse(time.RFC3339, chat.LastObservedAt)
	if last.IsZero() {
		return now
	}
	return last.Add(cloudCollaborationHeartbeatInterval(state))
}

func cloudCollaborationNextStatusCheckAt(state cloudCollaborationState, now time.Time) time.Time {
	var next time.Time
	for _, chat := range state.Chats {
		if chat.Status != "active" {
			continue
		}
		candidate := cloudCollaborationNextStatusPollAt(state, chat, now)
		if next.IsZero() || candidate.Before(next) {
			next = candidate
		}
	}
	return next
}

func (s *Service) cloudCollaborationAcquireLease(ctx context.Context, ownerID string, rec store.CloudCollaborationRecord, state cloudCollaborationState, req CloudCollaborationRequest) (map[string]any, error) {
	now := s.now().UTC()
	if state.Lease.ExpiresAt != "" {
		expires, _ := time.Parse(time.RFC3339, state.Lease.ExpiresAt)
		if expires.After(now) && (state.Lease.SessionID != req.ActorSessionID || state.Lease.Generation != state.Generation) {
			return nil, store.ErrConflict
		}
	}
	leaseTTL := time.Duration(state.HeartbeatMinutes*2) * time.Minute
	if leaseTTL < 5*time.Minute {
		leaseTTL = 5 * time.Minute
	}
	if leaseTTL > 30*time.Minute {
		leaseTTL = 30 * time.Minute
	}
	state.Lease = cloudCollaborationLease{SessionID: req.ActorSessionID, Generation: state.Generation, ExpiresAt: now.Add(leaseTTL).Format(time.RFC3339)}
	return s.saveCloudCollaboration(ctx, ownerID, rec, state)
}

func cloudCollaborationAddGoal(state *cloudCollaborationState, req CloudCollaborationRequest) error {
	if len(state.Goals) >= 100 {
		return store.ErrResourceLimit
	}
	id := strings.TrimSpace(req.GoalID)
	if id == "" {
		id = fmt.Sprintf("goal-%d", len(state.Goals)+1)
	}
	for _, goal := range state.Goals {
		if goal.ID == id {
			return store.ErrConflict
		}
	}
	title := boundedCollaborationText(req.Title, 500)
	if title == "" {
		return store.ErrConflict
	}
	state.Goals = append(state.Goals, cloudCollaborationGoal{ID: id, Title: title, Status: "queued"})
	return nil
}

func cloudCollaborationUpdateGoal(state *cloudCollaborationState, req CloudCollaborationRequest) error {
	for i := range state.Goals {
		if state.Goals[i].ID != req.GoalID {
			continue
		}
		if req.GoalStatus != "queued" && req.GoalStatus != "active" && req.GoalStatus != "blocked" && req.GoalStatus != "done" && req.GoalStatus != "dropped" {
			return store.ErrConflict
		}
		state.Goals[i].Status = req.GoalStatus
		if req.ResultSHA256 != "" {
			state.Goals[i].Evidence = req.ResultSHA256
		}
		return nil
	}
	return store.ErrNotFound
}

func cloudCollaborationAddTask(state *cloudCollaborationState, req CloudCollaborationRequest, role string) error {
	if len(state.Tasks) >= 500 || state.CreateCount >= state.MaxCreates {
		return store.ErrResourceLimit
	}
	if deadlinePassed(*state, time.Now().UTC()) {
		return store.ErrExpired
	}
	id := strings.TrimSpace(req.TaskID)
	if id == "" {
		id = fmt.Sprintf("task-%d", len(state.Tasks)+1)
	}
	for _, task := range state.Tasks {
		if task.ID == id {
			return store.ErrConflict
		}
	}
	parent := strings.TrimSpace(req.ParentSessionID)
	depth := 0
	parentActions := state.AllowedActions
	parentScope := state.Scope
	if role == "chat" {
		if parent == "" {
			parent = req.ActorSessionID
		}
		if parent != req.ActorSessionID {
			return store.ErrUnauthorized
		}
		found := false
		for _, chat := range state.Chats {
			if chat.SessionID == parent && chat.Status == "active" {
				depth, parentActions, parentScope, found = chat.Depth+1, chat.AllowedActions, chat.WriteScope, true
				break
			}
		}
		if !found {
			return store.ErrUnauthorized
		}
	}
	if depth > state.MaxDepth {
		return store.ErrResourceLimit
	}
	actions := normalizedCloudActions(req.AllowedActions)
	if len(actions) == 0 {
		actions = append([]string(nil), parentActions...)
	}
	if !stringSubset(actions, parentActions) {
		return store.ErrUnauthorized
	}
	access := strings.TrimSpace(req.AccessMode)
	if access == "" {
		access = "read_only"
	}
	if access != "read_only" && access != "write" {
		return store.ErrConflict
	}
	writeScope := strings.TrimSpace(req.WriteScope)
	deliverablePath := strings.TrimSpace(req.DeliverablePath)
	callbackType := strings.TrimSpace(req.CallbackType)
	if callbackType == "" {
		callbackType = protocolv1.CloudCallbackTypeLocalFile
	}
	if callbackType != protocolv1.CloudCallbackTypeLocalFile && callbackType != protocolv1.CloudCallbackTypeText && callbackType != protocolv1.CloudCallbackTypeStatus {
		return store.ErrConflict
	}
	if callbackType != protocolv1.CloudCallbackTypeLocalFile && deliverablePath != "" {
		return store.ErrConflict
	}
	if access == "write" {
		if writeScope == "" || (parentScope != "" && role == "chat" && !scopeContains(parentScope, writeScope)) {
			return store.ErrUnauthorized
		}
		for _, chat := range state.Chats {
			if chat.Status == "active" && chat.AccessMode == "write" && scopesOverlap(chat.WriteScope, writeScope) {
				return store.ErrConflict
			}
		}
		if deliverablePath != "" {
			if !containsString(actions, "file.write") || !validCollaborationAbsolutePath(deliverablePath) || !scopeContains(state.WorkingDirectory, deliverablePath) || !scopeContains(resolveCollaborationScope(state.WorkingDirectory, writeScope), deliverablePath) {
				return store.ErrUnauthorized
			}
		}
	} else if deliverablePath != "" {
		return store.ErrUnauthorized
	}
	if !containsString(actions, "chat.create") && !containsString(actions, "chat.send") {
		return store.ErrUnauthorized
	}
	targetSessionID := strings.TrimSpace(req.TargetSessionID)
	if targetSessionID != "" && !validCollaborationSessionID(targetSessionID) {
		return store.ErrConflict
	}
	if targetSessionID != "" && !containsString(actions, "chat.send") {
		return store.ErrUnauthorized
	}
	now := time.Now().UTC().Format(time.RFC3339)
	idemSum := sha256.Sum256([]byte(id + "\x00" + fmt.Sprintf("%d", state.Generation)))
	resultPath := ""
	if callbackType == protocolv1.CloudCallbackTypeLocalFile {
		resultPath = deliverablePath
	}
	if callbackType == protocolv1.CloudCallbackTypeLocalFile && resultPath == "" {
		resultPath = cloudCollaborationManagedResultPath(state.WorkingDirectory, req.CollaborationID, id, state.Generation)
	}
	state.Tasks = append(state.Tasks, cloudCollaborationTask{ID: id, Title: boundedCollaborationText(req.Title, 500), Prompt: boundedCollaborationText(req.Prompt, 12000), Status: "queued", ParentSession: parent, Depth: depth, Generation: state.Generation, AccessMode: access, WriteScope: writeScope, DeliverablePath: deliverablePath, ResultPath: resultPath, CallbackType: callbackType, AllowedActions: actions, IdempotencyKey: "collab_chat_" + hex.EncodeToString(idemSum[:])[:40], TargetSessionID: targetSessionID, DeliveryStatus: "pending", CreatedAt: now, UpdatedAt: now})
	if state.Tasks[len(state.Tasks)-1].Title == "" || state.Tasks[len(state.Tasks)-1].Prompt == "" {
		return store.ErrConflict
	}
	return nil
}

func cloudCollaborationUpdateTask(state *cloudCollaborationState, req CloudCollaborationRequest) error {
	idx := taskIndex(*state, strings.TrimSpace(req.TaskID))
	if idx < 0 {
		return store.ErrNotFound
	}
	task := &state.Tasks[idx]
	next := strings.TrimSpace(req.TaskStatus)
	allowed := false
	switch task.Status {
	case "queued":
		allowed = next == "blocked" || next == "dropped"
	case "creating", "create_in_doubt", "active":
		allowed = next == "blocked"
	case "result_available":
		allowed = next == "done" || next == "blocked"
	case "blocked":
		allowed = next == "dropped" || (next == "queued" && task.ChatSessionID == "")
	case "done", "dropped":
		return store.ErrConflict
	}
	if !allowed {
		return store.ErrConflict
	}
	if next == "done" {
		validated := false
		for _, event := range state.Events {
			if event.TaskID == task.ID && event.Status == "acked" && (event.ResultStatus == "ready" || event.ResultStatus == "completed") {
				validated = true
				break
			}
		}
		if !validated {
			return store.ErrConflict
		}
	}
	task.Status = next
	task.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return nil
}

func (s *Service) cloudCollaborationDispatchTask(ctx context.Context, ownerID string, rec store.CloudCollaborationRecord, state cloudCollaborationState, req CloudCollaborationRequest, role string) (map[string]any, error) {
	idx := taskIndex(state, req.TaskID)
	if idx < 0 {
		return nil, store.ErrNotFound
	}
	task := &state.Tasks[idx]
	if role == "chat" && task.ParentSession != req.ActorSessionID {
		return nil, store.ErrUnauthorized
	}
	targetSessionID := strings.TrimSpace(req.TargetSessionID)
	if targetSessionID != "" && !validCollaborationSessionID(targetSessionID) {
		return nil, store.ErrConflict
	}
	if task.TargetSessionID != "" {
		if targetSessionID != "" && targetSessionID != task.TargetSessionID {
			return nil, store.ErrConflict
		}
		targetSessionID = task.TargetSessionID
	}
	if task.Status == "active" && task.ChatSessionID != "" {
		if targetSessionID != "" && targetSessionID != task.ChatSessionID {
			return nil, store.ErrConflict
		}
		chatIdx := chatTaskIndex(state, *task)
		if chatIdx < 0 {
			return nil, store.ErrConflict
		}
		if task.SessionMode == "reuse" && task.DeliveryStatus != "delivered" {
			return s.deliverCloudCollaborationExistingTask(ctx, ownerID, rec, state, task.ID)
		}
		if state.Chats[chatIdx].CallbackRegistered && state.Chats[chatIdx].CallbackArmed {
			return cloudCollaborationView(rec, state, "dispatcher"), nil
		}
		registered, armed, err := s.activateCloudCollaborationCallback(ctx, ownerID, rec, state, *task, "")
		state.Chats[chatIdx].CallbackRegistered = registered
		state.Chats[chatIdx].CallbackArmed = armed
		if err != nil {
			out := cloudCollaborationView(rec, state, "dispatcher")
			if registered {
				var saveErr error
				out, saveErr = s.saveCloudCollaboration(ctx, ownerID, rec, state)
				if saveErr != nil {
					return nil, saveErr
				}
			}
			out["callbackPending"] = true
			return out, nil
		}
		return s.saveCloudCollaboration(ctx, ownerID, rec, state)
	}
	if task.Status != "queued" && task.Status != "create_in_doubt" {
		return nil, store.ErrConflict
	}
	if deadlinePassed(state, s.now().UTC()) || activeCloudChats(state) >= state.MaxActiveChats {
		return nil, store.ErrResourceLimit
	}
	if targetSessionID != "" {
		if task.Status != "queued" {
			return nil, store.ErrConflict
		}
		return s.cloudCollaborationDispatchExistingTask(ctx, ownerID, rec, state, req, targetSessionID)
	}
	if !containsString(task.AllowedActions, "chat.create") {
		return nil, store.ErrUnauthorized
	}
	if err := s.ensureCodexCloudCollaborationReady(ctx, ownerID, rec.MachineID); err != nil {
		return nil, err
	}
	if task.Status == "queued" {
		state.CreateCount++
		task.Status = "creating"
		task.SessionMode = "new"
		task.DeliveryStatus = "delivering"
		task.UpdatedAt = s.now().UTC().Format(time.RFC3339)
	}
	reserved, err := s.saveCloudCollaboration(ctx, ownerID, rec, state)
	if err != nil {
		return nil, err
	}
	rec.Revision = numberField(reserved, "revision")
	prompt := cloudCollaborationBootstrap(rec.CollaborationID, state, *task)
	result, callErr := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.create", map[string]any{"providerId": "codex", "backend": "chatgpt_cloud", "visibility": "visible", "mode": "quick_chat", "workingDirectory": state.WorkingDirectory, "prompt": prompt, "idempotencyKey": task.IdempotencyKey})
	if callErr != nil {
		task.Status = "create_in_doubt"
		task.DeliveryStatus = "in_doubt"
		task.UpdatedAt = s.now().UTC().Format(time.RFC3339)
		out, saveErr := s.saveCloudCollaboration(ctx, ownerID, rec, state)
		if saveErr != nil {
			return nil, saveErr
		}
		out["createInDoubt"] = true
		return out, nil
	}
	sessionID, _ := result["sessionId"].(string)
	backend, _ := result["backend"].(string)
	visibility, _ := result["visibility"].(string)
	externalType, _ := result["externalIdType"].(string)
	if sessionID == "" || (backend != "" && backend != "chatgpt_cloud") || (visibility != "" && visibility != "visible") || (externalType != "" && externalType != "chatgpt_conversation") {
		task.Status = "create_in_doubt"
		task.DeliveryStatus = "in_doubt"
		task.UpdatedAt = s.now().UTC().Format(time.RFC3339)
		out, saveErr := s.saveCloudCollaboration(ctx, ownerID, rec, state)
		if saveErr != nil {
			return nil, saveErr
		}
		out["createInDoubt"] = true
		return out, nil
	}
	updatedRec, updatedState, _, persistErr := s.persistCloudCollaborationCreatedChat(ctx, ownerID, rec, state, *task, sessionID)
	if persistErr != nil {
		return nil, persistErr
	}
	rec, state = updatedRec, updatedState
	idx = taskIndex(state, req.TaskID)
	task = &state.Tasks[idx]
	chatIdx := chatTaskIndex(state, *task)
	if chatIdx < 0 {
		return nil, store.ErrConflict
	}
	registered, armed, callbackErr := s.activateCloudCollaborationCallback(ctx, ownerID, rec, state, *task, "")
	state.Chats[chatIdx].CallbackRegistered = registered
	state.Chats[chatIdx].CallbackArmed = armed
	out, saveErr := s.saveCloudCollaboration(ctx, ownerID, rec, state)
	if saveErr != nil {
		return nil, saveErr
	}
	if callbackErr != nil {
		out["callbackPending"] = true
	}
	return out, nil
}

func (s *Service) cloudCollaborationDispatchExistingTask(ctx context.Context, ownerID string, rec store.CloudCollaborationRecord, state cloudCollaborationState, req CloudCollaborationRequest, targetSessionID string) (map[string]any, error) {
	idx := taskIndex(state, req.TaskID)
	if idx < 0 {
		return nil, store.ErrNotFound
	}
	task := &state.Tasks[idx]
	if !containsString(task.AllowedActions, "chat.send") {
		return nil, store.ErrUnauthorized
	}
	for _, chat := range state.Chats {
		if chat.SessionID == targetSessionID && chat.TaskID != task.ID && (chat.Status == "active" || chat.Status == "creating") {
			return nil, store.ErrConflict
		}
	}
	if err := s.ensureCodexCloudCollaborationReady(ctx, ownerID, rec.MachineID); err != nil {
		return nil, err
	}
	sessionResult, err := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.get", map[string]any{
		"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": targetSessionID, "limit": 8,
	})
	if err != nil {
		return nil, err
	}
	if err := validateReusableCloudCollaborationSession(sessionResult, targetSessionID); err != nil {
		return nil, err
	}
	baselineIdentity := cloudCollaborationCallbackBaseline(sessionResult)
	if baselineIdentity == "" {
		return nil, &CapabilityCallError{Code: "CALLBACK_BASELINE_UNAVAILABLE", Message: "the selected Cloud CHAT did not expose a stable current message identity; use a new CHAT or retry session.get", Retryable: true}
	}
	if err := s.releaseCompletedCloudCallbackForReuse(ctx, ownerID, rec, state, *task, targetSessionID); err != nil {
		return nil, err
	}

	now := s.now().UTC().Format(time.RFC3339)
	task.TargetSessionID = targetSessionID
	task.ChatSessionID = targetSessionID
	task.SessionMode = "reuse"
	task.DeliveryStatus = "delivering"
	task.Status = "active"
	task.UpdatedAt = now
	state.Chats = append(state.Chats, cloudCollaborationChat{
		SessionID: targetSessionID, TaskID: task.ID, ParentSession: task.ParentSession, Depth: task.Depth, Generation: task.Generation,
		Status: "active", AccessMode: task.AccessMode, WriteScope: task.WriteScope, AllowedActions: task.AllowedActions,
		CallbackBaselineIdentity: baselineIdentity, LastObservedAt: now, LastProgressAt: now, Reused: true,
	})
	return s.deliverCloudCollaborationExistingTask(ctx, ownerID, rec, state, task.ID)
}

func (s *Service) deliverCloudCollaborationExistingTask(ctx context.Context, ownerID string, rec store.CloudCollaborationRecord, state cloudCollaborationState, taskID string) (map[string]any, error) {
	idx := taskIndex(state, taskID)
	if idx < 0 {
		return nil, store.ErrNotFound
	}
	task := &state.Tasks[idx]
	chatIdx := chatTaskIndex(state, *task)
	if chatIdx < 0 || task.SessionMode != "reuse" || task.ChatSessionID == "" {
		return nil, store.ErrConflict
	}
	chat := &state.Chats[chatIdx]
	baselineIdentity := strings.TrimSpace(chat.CallbackBaselineIdentity)
	if baselineIdentity == "" {
		listed, err := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.callback.list", map[string]any{
			"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": task.ChatSessionID,
		})
		if err != nil {
			return nil, err
		}
		for _, callback := range collaborationMapItems(listed["callbacks"]) {
			generation, _ := numericInt64(callback["generation"])
			if mapString(callback, "missionId") == rec.CollaborationID && mapString(callback, "taskId") == task.ID && mapString(callback, "targetSessionId") == state.DispatcherSessionID && generation == task.Generation {
				baselineIdentity = mapString(callback, "baselineIdentity")
				break
			}
		}
		if baselineIdentity == "" {
			return nil, &CapabilityCallError{Code: "CALLBACK_BASELINE_UNAVAILABLE", Message: "the reused Cloud CHAT callback baseline is unavailable; preserve the route and record the issue before manual recovery", Retryable: false}
		}
		chat.CallbackBaselineIdentity = baselineIdentity
	}
	registeredResult, callbackErr := s.registerCloudCollaborationCallback(ctx, ownerID, rec, state, *task, baselineIdentity)
	if callbackErr != nil {
		return nil, callbackErr
	}
	chat.CallbackRegistered = true
	if callback, _ := registeredResult["callback"].(map[string]any); callback != nil {
		chat.CallbackArmed, _ = callback["armed"].(bool)
	}
	reserved, err := s.saveCloudCollaboration(ctx, ownerID, rec, state)
	if err != nil {
		return nil, err
	}
	rec.Revision = numberField(reserved, "revision")
	prompt := cloudCollaborationBootstrap(rec.CollaborationID, state, *task)
	result, sendErr := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.send", map[string]any{
		"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": task.ChatSessionID, "mode": "quick_chat", "prompt": prompt, "idempotencyKey": task.IdempotencyKey,
	})
	_, armErr := s.armCloudCollaborationCallback(ctx, ownerID, rec, state, *task)
	if armErr == nil {
		chat.CallbackArmed = true
	}
	if sendErr != nil {
		task.DeliveryStatus = "in_doubt"
		out, saveErr := s.saveCloudCollaboration(ctx, ownerID, rec, state)
		if saveErr != nil {
			return nil, saveErr
		}
		out["deliveryInDoubt"] = true
		out["reusedSession"] = true
		if armErr != nil {
			out["callbackPending"] = true
		}
		return out, nil
	}
	task.DeliveryStatus = "delivered"
	out, saveErr := s.saveCloudCollaboration(ctx, ownerID, rec, state)
	if saveErr != nil {
		return nil, saveErr
	}
	out["reusedSession"] = true
	out["targetSessionId"] = task.ChatSessionID
	if armErr != nil {
		out["callbackPending"] = true
	}
	if result != nil {
		if asyncTaskID, _ := result["asyncTaskId"].(string); asyncTaskID != "" {
			out["asyncTaskId"] = asyncTaskID
		}
		if status, _ := result["idempotencyStatus"].(string); status != "" {
			out["deliveryIdempotencyStatus"] = status
		}
	}
	return out, nil
}

func (s *Service) persistCloudCollaborationCreatedChat(ctx context.Context, ownerID string, rec store.CloudCollaborationRecord, state cloudCollaborationState, createdTask cloudCollaborationTask, sessionID string) (store.CloudCollaborationRecord, cloudCollaborationState, map[string]any, error) {
	for range 3 {
		idx := taskIndex(state, createdTask.ID)
		if idx < 0 {
			return store.CloudCollaborationRecord{}, cloudCollaborationState{}, nil, store.ErrNotFound
		}
		task := &state.Tasks[idx]
		if task.IdempotencyKey != createdTask.IdempotencyKey || task.Generation != createdTask.Generation {
			return store.CloudCollaborationRecord{}, cloudCollaborationState{}, nil, store.ErrConflict
		}
		if task.ChatSessionID != "" && task.ChatSessionID != sessionID {
			return store.CloudCollaborationRecord{}, cloudCollaborationState{}, nil, store.ErrConflict
		}
		if task.ChatSessionID == "" && task.Status != "creating" && task.Status != "create_in_doubt" {
			return store.CloudCollaborationRecord{}, cloudCollaborationState{}, nil, store.ErrConflict
		}
		now := s.now().UTC().Format(time.RFC3339)
		task.ChatSessionID, task.Status, task.UpdatedAt = sessionID, "active", now
		task.SessionMode, task.DeliveryStatus = "new", "delivered"
		if chatTaskIndex(state, *task) < 0 {
			state.Chats = append(state.Chats, cloudCollaborationChat{SessionID: sessionID, TaskID: task.ID, ParentSession: task.ParentSession, Depth: task.Depth, Generation: task.Generation, Status: "active", AccessMode: task.AccessMode, WriteScope: task.WriteScope, AllowedActions: task.AllowedActions, LastObservedAt: now, LastProgressAt: now})
		}
		raw, err := json.Marshal(state)
		if err != nil {
			return store.CloudCollaborationRecord{}, cloudCollaborationState{}, nil, err
		}
		updatedRec, err := s.store.UpdateCloudCollaboration(ctx, ownerID, rec.CollaborationID, state.Status, string(raw), rec.Revision, s.now().UTC())
		if err == nil {
			return updatedRec, state, cloudCollaborationView(updatedRec, state, "dispatcher"), nil
		}
		if !errors.Is(err, store.ErrConflict) {
			return store.CloudCollaborationRecord{}, cloudCollaborationState{}, nil, err
		}
		rec, state, err = s.loadCloudCollaboration(ctx, ownerID, rec.CollaborationID)
		if err != nil {
			return store.CloudCollaborationRecord{}, cloudCollaborationState{}, nil, err
		}
	}
	return store.CloudCollaborationRecord{}, cloudCollaborationState{}, nil, store.ErrConflict
}

func (s *Service) registerCloudCollaborationCallback(ctx context.Context, ownerID string, rec store.CloudCollaborationRecord, state cloudCollaborationState, task cloudCollaborationTask, baselineIdentity string) (map[string]any, error) {
	return s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.callback.register", map[string]any{
		"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": task.ChatSessionID,
		"callbackTargetSessionId": state.DispatcherSessionID, "callbackMissionId": rec.CollaborationID,
		"callbackTaskId": task.ID, "callbackGeneration": task.Generation,
		"callbackType":             task.CallbackType,
		"callbackDeliverablePath":  cloudCollaborationTaskResultPath(task),
		"callbackBaselineIdentity": strings.TrimSpace(baselineIdentity), "callbackArmRequired": true,
	})
}

func (s *Service) armCloudCollaborationCallback(ctx context.Context, ownerID string, rec store.CloudCollaborationRecord, state cloudCollaborationState, task cloudCollaborationTask) (map[string]any, error) {
	return s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.callback.arm", map[string]any{
		"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": task.ChatSessionID,
		"callbackTargetSessionId": state.DispatcherSessionID, "callbackMissionId": rec.CollaborationID,
		"callbackTaskId": task.ID, "callbackGeneration": task.Generation,
	})
}

func (s *Service) activateCloudCollaborationCallback(ctx context.Context, ownerID string, rec store.CloudCollaborationRecord, state cloudCollaborationState, task cloudCollaborationTask, baselineIdentity string) (bool, bool, error) {
	if _, err := s.registerCloudCollaborationCallback(ctx, ownerID, rec, state, task, baselineIdentity); err != nil {
		return false, false, err
	}
	if _, err := s.armCloudCollaborationCallback(ctx, ownerID, rec, state, task); err != nil {
		return true, false, err
	}
	return true, true, nil
}

func (s *Service) unregisterCloudCollaborationTaskCallback(ctx context.Context, ownerID string, rec store.CloudCollaborationRecord, state *cloudCollaborationState, taskID string) error {
	idx := taskIndex(*state, strings.TrimSpace(taskID))
	if idx < 0 {
		return store.ErrNotFound
	}
	task := state.Tasks[idx]
	chatIdx := chatTaskIndex(*state, task)
	if chatIdx < 0 {
		return store.ErrNotFound
	}
	chat := &state.Chats[chatIdx]
	if !chat.CallbackRegistered {
		return nil
	}
	if _, err := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.callback.unregister", map[string]any{
		"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": chat.SessionID, "callbackTargetSessionId": state.DispatcherSessionID,
		"callbackMissionId": rec.CollaborationID, "callbackTaskId": chat.TaskID, "callbackGeneration": chat.Generation,
	}); err != nil {
		return err
	}
	chat.CallbackRegistered = false
	chat.CallbackArmed = false
	return nil
}

func validateReusableCloudCollaborationSession(result map[string]any, expectedSessionID string) error {
	session, _ := result["session"].(map[string]any)
	if session == nil || mapString(session, "sessionId") != expectedSessionID || mapString(session, "providerId") != "codex" || mapString(session, "backend") != "chatgpt_cloud" {
		return &CapabilityCallError{Code: "INVALID_REQUEST", Message: "targetSessionId must identify an accessible ChatGPT Cloud CHAT", Retryable: false}
	}
	if visibility := mapString(session, "visibility"); visibility != "" && visibility != "visible" {
		return &CapabilityCallError{Code: "INVALID_REQUEST", Message: "targetSessionId must identify a normal visible ChatGPT Cloud CHAT", Retryable: false}
	}
	if externalType := mapString(session, "externalIdType"); externalType != "" && externalType != "chatgpt_conversation" {
		return &CapabilityCallError{Code: "INVALID_REQUEST", Message: "targetSessionId is not a ChatGPT conversation", Retryable: false}
	}
	if cloudCollaborationSessionStatus(result) == "running" {
		return &CapabilityCallError{Code: "AGENT_SESSION_BUSY", Message: "the explicitly selected Cloud CHAT is already running; wait for it or choose a new session", Retryable: true}
	}
	return nil
}

func cloudCollaborationCallbackBaseline(result map[string]any) string {
	session, _ := result["session"].(map[string]any)
	if session == nil {
		return ""
	}
	messages := collaborationMapItems(session["messages"])
	for i := len(messages) - 1; i >= 0; i-- {
		if mapString(messages[i], "role") == "assistant" {
			if id := strings.TrimSpace(mapString(messages[i], "id")); id != "" {
				return id
			}
		}
	}
	for _, key := range []string{"currentNode", "current_node"} {
		if id := strings.TrimSpace(mapString(session, key)); id != "" {
			return id
		}
	}
	if value := session["updateTime"]; value != nil {
		identity := strings.TrimSpace(fmt.Sprint(value))
		if len(identity) > 256 {
			identity = identity[:256]
		}
		return identity
	}
	return ""
}

func (s *Service) releaseCompletedCloudCallbackForReuse(ctx context.Context, ownerID string, rec store.CloudCollaborationRecord, state cloudCollaborationState, task cloudCollaborationTask, sessionID string) error {
	listed, err := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.callback.list", map[string]any{
		"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": sessionID,
	})
	if err != nil {
		return err
	}
	for _, callback := range collaborationMapItems(listed["callbacks"]) {
		missionID := mapString(callback, "missionId")
		taskID := mapString(callback, "taskId")
		targetID := mapString(callback, "targetSessionId")
		generation, _ := numericInt64(callback["generation"])
		if missionID == rec.CollaborationID && taskID == task.ID && targetID == state.DispatcherSessionID && generation == task.Generation {
			return nil
		}
		pendingCount, _ := numericInt64(callback["pendingCount"])
		if pendingCount != 0 || missionID == "" || taskID == "" || targetID == "" || generation < 1 {
			return &CapabilityCallError{Code: "CALLBACK_OWNER_CONFLICT", Message: "the selected Cloud CHAT still has an active or pending callback owner", Retryable: true}
		}
		_, oldState, loadErr := s.loadCloudCollaboration(ctx, ownerID, missionID)
		if loadErr != nil {
			if errors.Is(loadErr, store.ErrNotFound) {
				return &CapabilityCallError{Code: "ORPHAN_CALLBACK_ROUTE", Message: "the selected Cloud CHAT has a callback route whose collaboration no longer exists; inspect and explicitly unregister the orphan route", Retryable: false, Details: map[string]any{"collaborationId": missionID, "taskId": taskID, "sourceSessionId": sessionID}}
			}
			return &CapabilityCallError{Code: "CALLBACK_OWNER_CONFLICT", Message: "the selected Cloud CHAT has a callback owner that cannot be safely released", Retryable: true}
		}
		oldTaskIdx := taskIndex(oldState, taskID)
		if oldTaskIdx < 0 || (oldState.Tasks[oldTaskIdx].Status != "done" && oldState.Tasks[oldTaskIdx].Status != "dropped") {
			return &CapabilityCallError{Code: "CALLBACK_OWNER_CONFLICT", Message: "the selected Cloud CHAT is still assigned to unfinished work", Retryable: true}
		}
		if _, unregisterErr := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.callback.unregister", map[string]any{
			"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": sessionID, "callbackTargetSessionId": targetID,
			"callbackMissionId": missionID, "callbackTaskId": taskID, "callbackGeneration": generation,
		}); unregisterErr != nil {
			return unregisterErr
		}
	}
	return nil
}

func (s *Service) cloudCollaborationPollStatus(ctx context.Context, ownerID string, rec store.CloudCollaborationRecord, state cloudCollaborationState, req CloudCollaborationRequest) (map[string]any, error) {
	if state.Status == "closing" {
		return s.cloudCollaborationClose(ctx, ownerID, rec, state)
	}
	idx := taskIndex(state, req.TaskID)
	if idx < 0 {
		return nil, store.ErrNotFound
	}
	task := &state.Tasks[idx]
	chatIdx := chatTaskIndex(state, *task)
	if chatIdx < 0 {
		return nil, store.ErrNotFound
	}
	chat := &state.Chats[chatIdx]
	if task.SessionMode == "reuse" && task.DeliveryStatus != "delivered" {
		out := cloudCollaborationView(rec, state, "dispatcher")
		out["polled"] = false
		out["deliveryInDoubt"] = true
		out["recoveryAction"] = "dispatch_task"
		out["recordIssuesTo"] = cloudCollaborationIssueMarkdownPath
		return out, nil
	}
	now := s.now().UTC()
	nextPollAt := cloudCollaborationNextStatusPollAt(state, *chat, now)
	if now.Before(nextPollAt) {
		out := cloudCollaborationView(rec, state, "dispatcher")
		retryAfter := int64((nextPollAt.Sub(now) + time.Second - 1) / time.Second)
		if retryAfter < 1 {
			retryAfter = 1
		}
		out["polled"] = false
		out["pollReason"] = "not_due"
		out["nextPollAt"] = nextPollAt.Format(time.RFC3339)
		out["retryAfterSeconds"] = retryAfter
		out["recordIssuesTo"] = cloudCollaborationIssueMarkdownPath
		return out, nil
	}
	if !chat.CallbackRegistered || !chat.CallbackArmed {
		registered, armed, err := s.activateCloudCollaborationCallback(ctx, ownerID, rec, state, *task, "")
		chat.CallbackRegistered = registered
		chat.CallbackArmed = armed
		if err != nil {
			if registered {
				_, _ = s.saveCloudCollaboration(ctx, ownerID, rec, state)
			}
			return nil, err
		}
	}
	result, err := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.watch", map[string]any{"providerId": "codex", "sessionId": chat.SessionID, "cursor": chat.WatchCursor, "waitSeconds": 0})
	if err != nil {
		return nil, err
	}
	next, _ := numericInt64(result["cursor"])
	if next == 0 {
		next, _ = numericInt64(result["nextCursor"])
	}
	progressed := next > chat.WatchCursor
	if progressed {
		chat.WatchCursor, chat.LastProgressAt, chat.QuietChecks, chat.StalledNotified = next, now.Format(time.RFC3339), 0, false
		chat.ContinueAttempts, chat.LastContinueAt = 0, ""
	} else {
		chat.QuietChecks++
	}
	chat.LastObservedAt = now.Format(time.RFC3339)
	lastProgress, _ := time.Parse(time.RFC3339, chat.LastProgressAt)
	newlyStalled := !progressed && chat.QuietChecks >= 2 && now.Sub(lastProgress) >= time.Duration(state.StallMinutes)*time.Minute && !chat.StalledNotified
	if newlyStalled {
		chat.StalledNotified = true
	}
	providerStatus := ""
	terminalEventID, _ := cloudCollaborationTerminalWatchEvent(result, task.ChatSessionID)
	recoveryOutcome := ""
	recoveryText := ""
	if terminalEventID != "" {
		if task.CallbackType == protocolv1.CloudCallbackTypeText {
			textResult, textErr := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.result", map[string]any{
				"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": chat.SessionID,
			})
			if textErr == nil {
				recoveryOutcome, recoveryText, providerStatus = cloudCollaborationTextResultOutcome(textResult)
			}
		} else {
			recoveryOutcome = "completed"
		}
	} else if chat.StalledNotified {
		switch task.CallbackType {
		case protocolv1.CloudCallbackTypeLocalFile:
			manifest, manifestErr := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.result", map[string]any{
				"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": chat.SessionID, "resultMode": "manifest",
				"callbackDeliverablePath": cloudCollaborationTaskResultPath(*task), "idempotencyKey": cloudCollaborationResultIdempotencyKey(rec.CollaborationID, *task),
			})
			if manifestErr == nil {
				recoveryOutcome = cloudCollaborationManifestOutcome(manifest)
				if status, _ := manifest["status"].(string); status != "" {
					providerStatus = status
				}
			}
		case protocolv1.CloudCallbackTypeText:
			textResult, textErr := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.result", map[string]any{
				"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": chat.SessionID,
			})
			if textErr == nil {
				recoveryOutcome, recoveryText, providerStatus = cloudCollaborationTextResultOutcome(textResult)
			}
		case protocolv1.CloudCallbackTypeStatus:
			statusResult, statusErr := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.get", map[string]any{
				"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": chat.SessionID, "metadataOnly": true,
			})
			if statusErr == nil {
				providerStatus = cloudCollaborationSessionStatus(statusResult)
				if providerStatus == "completed" {
					recoveryOutcome = "completed"
				} else if providerStatus == "failed" || providerStatus == "canceled" {
					recoveryOutcome = "failed"
				}
			}
		}
	}
	recoveryNotify := recoveryOutcome != "" && (task.Status == "active" || task.Status == "result_available" || task.Status == "completion_reported")
	if recoveryNotify {
		if providerStatus == "" {
			providerStatus = recoveryOutcome
		}
	}
	out, saveErr := s.saveCloudCollaboration(ctx, ownerID, rec, state)
	if saveErr != nil {
		return nil, saveErr
	}
	out["progressed"], out["newlyStalled"] = progressed, newlyStalled
	out["recordIssuesTo"] = cloudCollaborationIssueMarkdownPath
	if providerStatus != "" {
		out["providerStatus"] = providerStatus
	}
	if newlyStalled {
		out["recoveryAction"] = "continue_chat"
	} else if chat.StalledNotified && chat.ContinueAttempts > 0 {
		out["recoveryAction"] = "controller_decision"
	}
	if recoveryNotify {
		notification, notifyErr := s.notifyCloudCompletion(ctx, ownerID, CloudCompletionRequest{
			Action: "notify", CollaborationID: rec.CollaborationID, TaskID: task.ID,
			ActorSessionID: state.DispatcherSessionID, SourceSessionID: task.ChatSessionID, Outcome: recoveryOutcome, CallbackType: task.CallbackType, Text: recoveryText,
		})
		if notifyErr != nil {
			out["recoveryPending"] = true
		} else {
			out["completionNotification"] = notification["notification"]
			if latestRec, latestState, loadErr := s.loadCloudCollaboration(ctx, ownerID, rec.CollaborationID); loadErr == nil {
				latest := cloudCollaborationView(latestRec, latestState, "dispatcher")
				latest["progressed"], latest["newlyStalled"] = progressed, newlyStalled
				latest["providerStatus"] = providerStatus
				latest["completionNotification"] = notification["notification"]
				latest["recordIssuesTo"] = cloudCollaborationIssueMarkdownPath
				out = latest
			}
		}
	}
	out["polled"] = true
	out["polledAt"] = now.Format(time.RFC3339)
	out["nextPollAt"] = now.Add(cloudCollaborationHeartbeatInterval(state)).Format(time.RFC3339)
	return out, nil
}

func cloudCollaborationManifestOutcome(manifest map[string]any) string {
	resultStatus, _ := manifest["resultStatus"].(string)
	resultSHA256, _ := manifest["resultSHA256"].(string)
	deliverableStatus, _ := manifest["deliverableStatus"].(string)
	if (resultStatus == "ready" || resultStatus == "completed") && validCloudCompletionSHA256(resultSHA256) {
		return "completed"
	}
	providerStatus, _ := manifest["status"].(string)
	if resultStatus == "failed" || providerStatus == "failed" || providerStatus == "canceled" || providerStatus == "completed" && deliverableStatus != "ready" {
		return "failed"
	}
	return ""
}

func cloudCollaborationTextResultOutcome(result map[string]any) (outcome, text, providerStatus string) {
	providerStatus, _ = result["status"].(string)
	if providerStatus == "failed" || providerStatus == "canceled" {
		return "failed", "", providerStatus
	}
	if providerStatus != "completed" {
		return "", "", providerStatus
	}
	text, _ = result["finalAgentMessage"].(string)
	if validateCloudCallbackText(text, true) != nil {
		return "failed", "", providerStatus
	}
	return "completed", text, providerStatus
}

// cloudCollaborationContinueChat performs the conservative recovery action for
// a stalled Cloud CHAT. It checks the provider state first, sends the normal
// follow-up “请继续” only while the provider is still running, suppresses a
// duplicate until progress is observed, and never rotates a replacement
// session automatically.
func (s *Service) cloudCollaborationContinueChat(ctx context.Context, ownerID string, rec store.CloudCollaborationRecord, state cloudCollaborationState, req CloudCollaborationRequest) (map[string]any, error) {
	idx := taskIndex(state, strings.TrimSpace(req.TaskID))
	if idx < 0 {
		return nil, store.ErrNotFound
	}
	task := &state.Tasks[idx]
	chatIdx := chatTaskIndex(state, *task)
	if chatIdx < 0 || task.Status != "active" || state.Chats[chatIdx].Status != "active" {
		return nil, store.ErrConflict
	}
	chat := &state.Chats[chatIdx]
	base := func() map[string]any {
		out := cloudCollaborationView(rec, state, "dispatcher")
		out["taskId"] = task.ID
		out["chatSessionId"] = chat.SessionID
		out["continueSent"] = false
		out["automaticReplacement"] = false
		out["recordIssuesTo"] = cloudCollaborationIssueMarkdownPath
		return out
	}
	if !chat.StalledNotified {
		out := base()
		out["recoveryAction"] = "status_check_required"
		return out, nil
	}
	if chat.ContinueAttempts > 0 {
		out := base()
		out["continueAttempts"] = chat.ContinueAttempts
		out["recoveryAction"] = "controller_decision"
		out["replacementAllowed"] = true
		return out, nil
	}

	statusResult, err := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.get", map[string]any{
		"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": chat.SessionID,
	})
	if err != nil {
		return nil, err
	}
	providerStatus := cloudCollaborationSessionStatus(statusResult)
	if providerStatus != "running" && providerStatus != "unknown" {
		out := base()
		out["providerStatus"] = providerStatus
		switch providerStatus {
		case "completed":
			out["recoveryAction"] = "status_poll"
		case "failed", "canceled":
			out["recoveryAction"] = "controller_decision"
			out["decisionRequired"] = true
			out["replacementAllowed"] = true
		default:
			out["recoveryAction"] = "status_uncertain"
			out["decisionRequired"] = true
		}
		return out, nil
	}

	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = "请继续"
	}
	if len(prompt) > 200 || strings.ContainsAny(prompt, "\x00\r\n") {
		return nil, store.ErrConflict
	}
	// Reserve the one allowed attempt before the external call. A concurrent
	// dispatcher or a process restart can therefore never send a second “继续”
	// because the first request's outcome was uncertain.
	chat.ContinueAttempts = 1
	chat.LastContinueAt = s.now().UTC().Format(time.RFC3339)
	reserved, err := s.saveCloudCollaboration(ctx, ownerID, rec, state)
	if err != nil {
		return nil, err
	}
	rec.Revision = numberField(reserved, "revision")
	result, err := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.send", map[string]any{
		"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": chat.SessionID, "prompt": prompt,
	})
	if err != nil {
		return nil, fmt.Errorf("continue Cloud CHAT: %w", err)
	}
	out := cloudCollaborationView(rec, state, "dispatcher")
	out["taskId"] = task.ID
	out["chatSessionId"] = chat.SessionID
	out["providerStatus"] = providerStatus
	out["continueSent"] = true
	out["continuePrompt"] = prompt
	out["continueAttempts"] = chat.ContinueAttempts
	out["recoveryAction"] = "poll_status"
	out["automaticReplacement"] = false
	out["recordIssuesTo"] = cloudCollaborationIssueMarkdownPath
	if result != nil {
		if turnID, ok := result["turnId"].(string); ok && strings.TrimSpace(turnID) != "" {
			out["turnId"] = turnID
		}
	}
	return out, nil
}

func cloudCollaborationSessionStatus(result map[string]any) string {
	if result == nil {
		return "unknown"
	}
	status := ""
	if session, ok := result["session"].(map[string]any); ok {
		status, _ = session["status"].(string)
	}
	if status == "" {
		status, _ = result["status"].(string)
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "in_progress", "in-progress", "queued", "pending", "processing", "generating", "active", "started":
		return "running"
	case "completed", "complete", "success", "succeeded", "done", "finished":
		return "completed"
	case "failed", "failure", "error", "errored":
		return "failed"
	case "canceled", "cancelled", "stopped", "aborted", "interrupted":
		return "canceled"
	default:
		return "unknown"
	}
}

func cloudCollaborationResultIdempotencyKey(collaborationID string, task cloudCollaborationTask) string {
	sum := sha256.Sum256([]byte(collaborationID + "\x00" + task.ID + "\x00" + fmt.Sprintf("%d", task.Generation)))
	return "collab_result_" + hex.EncodeToString(sum[:])[:48]
}

func cloudCollaborationManagedResultPath(workingDirectory, collaborationID, taskID string, generation int64) string {
	sum := sha256.Sum256([]byte(collaborationID + "\x00" + taskID + "\x00" + fmt.Sprintf("%d", generation)))
	return strings.TrimRight(workingDirectory, "\\/") + "/.fast-spider-result-" + hex.EncodeToString(sum[:])[:24] + ".md"
}

func cloudCollaborationTaskResultPath(task cloudCollaborationTask) string {
	if task.CallbackType != protocolv1.CloudCallbackTypeLocalFile {
		return ""
	}
	if strings.TrimSpace(task.DeliverablePath) != "" {
		return task.DeliverablePath
	}
	return task.ResultPath
}

func cloudCollaborationRecoveryEventRequest(collaborationID string, state cloudCollaborationState, task cloudCollaborationTask, watch, manifest map[string]any) (CloudCollaborationRequest, bool) {
	providerStatus, _ := manifest["status"].(string)
	resultStatus, _ := manifest["resultStatus"].(string)
	if providerStatus != "completed" && resultStatus != "ready" && resultStatus != "completed" && resultStatus != "failed" {
		return CloudCollaborationRequest{}, false
	}
	resultID, _ := manifest["resultId"].(string)
	resultSHA256, _ := manifest["resultSHA256"].(string)
	deliverablePath, _ := manifest["deliverablePath"].(string)
	deliverableStatus, _ := manifest["deliverableStatus"].(string)
	resultBytes, _ := numericInt64(manifest["resultBytes"])
	if resultStatus == "" {
		resultStatus = providerStatus
	}
	if (resultStatus == "ready" || resultStatus == "completed") && resultSHA256 == "" {
		return CloudCollaborationRequest{}, false
	}
	eventID, sequence := cloudCollaborationTerminalWatchEvent(watch, task.ChatSessionID)
	if eventID == "" {
		sum := sha256.Sum256([]byte(collaborationID + "\x00" + task.ID + "\x00" + fmt.Sprintf("%d", task.Generation) + "\x00" + resultID + "\x00" + resultSHA256 + "\x00" + deliverableStatus))
		eventID = "recovery_evt_" + hex.EncodeToString(sum[:])[:48]
	}
	for _, event := range state.Events {
		if event.ID == eventID {
			sequence = event.Sequence
			break
		}
		if event.SessionID == task.ChatSessionID && event.Sequence >= sequence {
			sequence = event.Sequence + 1
		}
	}
	if sequence < 1 {
		sequence = 1
	}
	return CloudCollaborationRequest{
		TaskID: task.ID, EventID: eventID, EventSequence: sequence, EventGeneration: task.Generation, EventType: "conversation.turn.complete",
		ResultID: resultID, ResultStatus: resultStatus, ResultBytes: resultBytes, ResultSHA256: resultSHA256, DeliverablePath: deliverablePath, DeliverableStatus: deliverableStatus,
	}, true
}

func cloudCollaborationTerminalWatchEvent(watch map[string]any, sessionID string) (string, int64) {
	var eventID string
	var sequence int64
	for _, event := range collaborationMapItems(watch["events"]) {
		typeName, _ := event["type"].(string)
		eventSessionID, _ := event["sessionId"].(string)
		if typeName != "conversation.turn.complete" || eventSessionID != "" && eventSessionID != sessionID {
			continue
		}
		candidate, _ := numericInt64(event["sequence"])
		if candidate < sequence {
			continue
		}
		sequence = candidate
		eventID, _ = event["eventKey"].(string)
	}
	return eventID, sequence
}

func collaborationMapItems(value any) []map[string]any {
	switch items := value.(type) {
	case []map[string]any:
		return items
	case []any:
		out := make([]map[string]any, 0, len(items))
		for _, raw := range items {
			if item, ok := raw.(map[string]any); ok {
				out = append(out, item)
			}
		}
		return out
	default:
		return nil
	}
}

func cloudCollaborationIngestEvent(state *cloudCollaborationState, req CloudCollaborationRequest) error {
	if req.EventID == "" || req.TaskID == "" || req.EventSequence < 1 || req.EventGeneration < 1 {
		return store.ErrConflict
	}
	idx := taskIndex(*state, req.TaskID)
	if idx < 0 {
		return store.ErrNotFound
	}
	task := &state.Tasks[idx]
	if task.Generation != req.EventGeneration || task.ChatSessionID == "" {
		return store.ErrUnauthorized
	}
	if task.DeliverablePath != "" && strings.TrimSpace(req.DeliverablePath) != task.DeliverablePath {
		return store.ErrUnauthorized
	}
	if task.DeliverablePath == "" && strings.TrimSpace(req.DeliverablePath) != "" {
		return store.ErrUnauthorized
	}
	if err := validateCloudCollaborationEventResult(*task, req); err != nil {
		return err
	}
	signatureRaw, _ := json.Marshal([]any{req.TaskID, task.ChatSessionID, req.EventGeneration, req.EventSequence, req.EventType, req.ResultID, req.ResultStatus, req.ResultBytes, req.ResultSHA256, req.DeliverablePath, req.DeliverableStatus})
	sum := sha256.Sum256(signatureRaw)
	signature := hex.EncodeToString(sum[:])
	for _, event := range state.Events {
		if event.ID == req.EventID {
			if event.Signature == signature {
				return nil
			}
			return store.ErrConflict
		}
		if event.SessionID == task.ChatSessionID && event.Generation == req.EventGeneration && event.Sequence >= req.EventSequence {
			return store.ErrConflict
		}
	}
	if len(state.Events) >= 512 {
		state.Events = append([]cloudCollaborationEvent(nil), state.Events[len(state.Events)-384:]...)
	}
	state.Events = append(state.Events, cloudCollaborationEvent{ID: req.EventID, TaskID: task.ID, SessionID: task.ChatSessionID, Generation: req.EventGeneration, Sequence: req.EventSequence, Type: req.EventType, ResultID: req.ResultID, ResultStatus: req.ResultStatus, ResultBytes: req.ResultBytes, ResultSHA256: req.ResultSHA256, Status: "pending", DeliverablePath: req.DeliverablePath, DeliverableStatus: req.DeliverableStatus, Signature: signature, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	task.ResultID, task.ResultStatus, task.ResultSHA256, task.UpdatedAt = req.ResultID, req.ResultStatus, req.ResultSHA256, time.Now().UTC().Format(time.RFC3339)
	if req.ResultStatus == "ready" || req.ResultStatus == "completed" {
		task.Status = "result_available"
	}
	return nil
}

func validateCloudCollaborationEventResult(task cloudCollaborationTask, req CloudCollaborationRequest) error {
	if req.EventType != "conversation.turn.complete" || len(req.EventID) > 128 || strings.ContainsAny(req.EventID, "\x00\r\n\t ") {
		return store.ErrConflict
	}
	if !containsString([]string{"open", "ready", "failed", "aborted", "running", "completed", "canceled", "unknown"}, req.ResultStatus) || req.ResultBytes < 0 || req.ResultBytes > 256<<20 {
		return store.ErrConflict
	}
	if req.ResultID != "" && (len(req.ResultID) > 256 || strings.ContainsAny(req.ResultID, "\x00\r\n")) {
		return store.ErrConflict
	}
	if req.ResultSHA256 != "" {
		if len(req.ResultSHA256) != len("sha256:")+64 || !strings.HasPrefix(req.ResultSHA256, "sha256:") {
			return store.ErrConflict
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(req.ResultSHA256, "sha256:")); err != nil {
			return store.ErrConflict
		}
	}
	if task.DeliverablePath != "" {
		if !containsString([]string{"ready", "missing", "invalid", "unreadable", "too_large"}, req.DeliverableStatus) {
			return store.ErrConflict
		}
		if req.DeliverableStatus == "ready" && (req.ResultStatus != "ready" && req.ResultStatus != "completed" || req.ResultSHA256 == "") {
			return store.ErrConflict
		}
		if req.DeliverableStatus != "ready" && req.ResultStatus != "failed" {
			return store.ErrConflict
		}
		return nil
	}
	if req.DeliverableStatus != "" {
		return store.ErrConflict
	}
	if (req.ResultStatus == "ready" || req.ResultStatus == "completed") && (strings.TrimSpace(req.ResultID) == "" || req.ResultSHA256 == "") {
		return store.ErrConflict
	}
	return nil
}

func cloudCollaborationAckEvent(state *cloudCollaborationState, req CloudCollaborationRequest) error {
	for i := range state.Events {
		if state.Events[i].ID == req.EventID {
			if state.Events[i].Status == "acked" {
				return nil
			}
			if state.Events[i].ResultSHA256 != "" && strings.TrimSpace(req.ResultSHA256) != state.Events[i].ResultSHA256 {
				return store.ErrUnauthorized
			}
			state.Events[i].Status = "acked"
			return nil
		}
	}
	return store.ErrNotFound
}

func cloudCollaborationAdvanceAcknowledgedEvent(state *cloudCollaborationState, eventID string, now time.Time) {
	if state == nil {
		return
	}
	var acknowledged *cloudCollaborationEvent
	for i := range state.Events {
		if state.Events[i].ID == eventID && state.Events[i].Status == "acked" {
			acknowledged = &state.Events[i]
			break
		}
	}
	if acknowledged == nil {
		return
	}
	idx := taskIndex(*state, acknowledged.TaskID)
	if idx < 0 {
		return
	}
	task := &state.Tasks[idx]
	switch acknowledged.ResultStatus {
	case "ready", "completed":
		task.Status = "done"
	case "failed", "aborted", "canceled":
		task.Status = "blocked"
	default:
		return
	}
	task.UpdatedAt = now.Format(time.RFC3339)
	if chatIdx := chatTaskIndex(*state, *task); chatIdx >= 0 {
		if task.Status == "done" {
			state.Chats[chatIdx].Status = "completed"
		} else {
			state.Chats[chatIdx].Status = "failed"
		}
		state.Chats[chatIdx].LastObservedAt = now.Format(time.RFC3339)
	}
	if len(state.Tasks) == 0 {
		return
	}
	for _, current := range state.Tasks {
		if current.Status != "done" && current.Status != "dropped" {
			return
		}
	}
	for i := range state.Goals {
		if state.Goals[i].Status != "dropped" {
			state.Goals[i].Status = "done"
		}
	}
}

func cloudCollaborationReadyToClose(state cloudCollaborationState) bool {
	if len(state.Tasks) == 0 {
		return false
	}
	for _, task := range state.Tasks {
		if task.Status != "done" && task.Status != "dropped" {
			return false
		}
	}
	for _, goal := range state.Goals {
		if goal.Status != "done" && goal.Status != "dropped" {
			return false
		}
	}
	for _, event := range state.Events {
		if event.Status != "acked" {
			return false
		}
	}
	for _, decision := range state.Decisions {
		if decision.Status == "requested" {
			return false
		}
	}
	return true
}

func cloudCollaborationRequestDecision(state *cloudCollaborationState, req CloudCollaborationRequest) error {
	if len(req.Options) > 3 || len(req.Question) == 0 || len(req.Question) > 1000 {
		return store.ErrConflict
	}
	id := req.DecisionID
	if id == "" {
		id = fmt.Sprintf("decision-%d", len(state.Decisions)+1)
	}
	for _, d := range state.Decisions {
		if d.ID == id {
			return store.ErrConflict
		}
	}
	kind := "choice"
	if req.EventType == "delete_chat" {
		kind = "delete_chat"
	}
	state.Decisions = append(state.Decisions, cloudCollaborationDecision{ID: id, Kind: kind, TargetID: req.TaskID, Question: req.Question, Options: req.Options, Recommendation: req.Recommendation, Status: "requested", CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	return nil
}

func cloudCollaborationResolveDecision(state *cloudCollaborationState, req CloudCollaborationRequest) error {
	if req.DecisionStatus != "approved" && req.DecisionStatus != "rejected" {
		return store.ErrConflict
	}
	for i := range state.Decisions {
		if state.Decisions[i].ID == req.DecisionID && state.Decisions[i].Status == "requested" {
			state.Decisions[i].Status = req.DecisionStatus
			return nil
		}
	}
	return store.ErrNotFound
}

func cloudCollaborationRotateChat(state *cloudCollaborationState, req CloudCollaborationRequest) error {
	idx := taskIndex(*state, req.TaskID)
	if idx < 0 {
		return store.ErrNotFound
	}
	task := &state.Tasks[idx]
	chatIdx := chatTaskIndex(*state, *task)
	if chatIdx < 0 {
		return store.ErrNotFound
	}
	if state.Chats[chatIdx].Reused {
		state.Chats[chatIdx].Status = "released"
	} else {
		state.Chats[chatIdx].Status = "retiring"
	}
	task.Status = "queued"
	task.ChatSessionID = ""
	task.TargetSessionID = ""
	task.SessionMode = ""
	task.DeliveryStatus = "pending"
	task.Generation++
	state.Generation++
	if state.Lease.SessionID != "" {
		state.Lease.Generation = state.Generation
	}
	task.IdempotencyKey += fmt.Sprintf("_g%d", task.Generation)
	if req.Checkpoint != "" {
		state.Checkpoint = boundedCollaborationText(req.Checkpoint, 4000)
		state.CheckpointGeneration = state.Generation
	}
	return nil
}

func (s *Service) cloudCollaborationDeleteChat(ctx context.Context, ownerID string, rec store.CloudCollaborationRecord, state cloudCollaborationState, req CloudCollaborationRequest) (map[string]any, error) {
	idx := taskIndex(state, req.TaskID)
	if idx < 0 {
		return nil, store.ErrNotFound
	}
	task := &state.Tasks[idx]
	approved := -1
	for i, d := range state.Decisions {
		if d.Kind == "delete_chat" && d.TargetID == req.TaskID && d.Status == "approved" {
			approved = i
			break
		}
	}
	if approved < 0 || task.ChatSessionID == "" {
		return nil, store.ErrUnauthorized
	}
	chatIdx := chatTaskIndex(state, *task)
	if chatIdx < 0 || state.Chats[chatIdx].Reused {
		return nil, store.ErrUnauthorized
	}
	if err := s.unregisterCloudCollaborationTaskCallback(ctx, ownerID, rec, &state, task.ID); err != nil {
		return nil, err
	}
	if _, err := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.delete", map[string]any{"providerId": "codex", "sessionId": task.ChatSessionID}); err != nil {
		return nil, err
	}
	state.Chats[chatIdx].Status = "deleted"
	state.Decisions[approved].Status = "consumed"
	task.Status = "done"
	return s.saveCloudCollaboration(ctx, ownerID, rec, state)
}

func (s *Service) cloudCollaborationClose(ctx context.Context, ownerID string, rec store.CloudCollaborationRecord, state cloudCollaborationState) (map[string]any, error) {
	for _, goal := range state.Goals {
		if goal.Status != "done" && goal.Status != "dropped" {
			return nil, store.ErrConflict
		}
	}
	for _, event := range state.Events {
		if event.Status != "acked" {
			return nil, store.ErrConflict
		}
	}
	for _, task := range state.Tasks {
		if task.Status != "done" && task.Status != "dropped" {
			return nil, store.ErrConflict
		}
	}
	for _, decision := range state.Decisions {
		if decision.Status == "requested" {
			return nil, store.ErrConflict
		}
	}
	state.Status = "closing"
	archived := map[string]bool{}
	for i := range state.Chats {
		chat := &state.Chats[i]
		if chat.Status == "archived" || chat.Status == "deleted" || chat.Status == "released" {
			continue
		}
		if chat.CallbackRegistered {
			if _, err := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.callback.unregister", map[string]any{
				"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": chat.SessionID, "callbackTargetSessionId": state.DispatcherSessionID,
				"callbackMissionId": rec.CollaborationID, "callbackTaskId": chat.TaskID, "callbackGeneration": chat.Generation,
			}); err != nil {
				return s.saveCloudCollaboration(ctx, ownerID, rec, state)
			}
			chat.CallbackRegistered = false
			chat.CallbackArmed = false
		}
		if chat.Reused {
			chat.Status = "released"
			continue
		}
		if archived[chat.SessionID] {
			chat.Status = "archived"
			continue
		}
		if _, err := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.archive", map[string]any{"providerId": "codex", "sessionId": chat.SessionID}); err != nil {
			return s.saveCloudCollaboration(ctx, ownerID, rec, state)
		}
		archived[chat.SessionID] = true
		chat.Status = "archived"
	}
	state.Status = "completed"
	return s.saveCloudCollaboration(ctx, ownerID, rec, state)
}

func cloudCollaborationBootstrap(collaborationID string, state cloudCollaborationState, task cloudCollaborationTask) string {
	deliverable := "Return only a final status callback. Do not create or upload a result document and do not include result text."
	switch task.CallbackType {
	case protocolv1.CloudCallbackTypeLocalFile:
		deliverable = fmt.Sprintf("Write the complete final result directly to the fixed Node-local path %s through FastSpider_FS file tools before notifying completion. That exact resultPath is a callback-only output slot and remains writable when accessMode=read_only or allowedActions omits file.write; it grants no permission to write any other path. Do not upload that file to Fast Spider and do not send its body in the callback; the dispatcher receives the registered path and verifies it after claiming.", cloudCollaborationTaskResultPath(task))
	case protocolv1.CloudCallbackTypeText:
		deliverable = fmt.Sprintf("Return the complete short result in the text field of the completion callback. It must be valid UTF-8, at most %d Unicode characters and at most %d bytes. For a longer result, the dispatcher must create a new local_file task instead of uploading a file or truncating the answer.", protocolv1.CloudCallbackTextMaxRunes, protocolv1.CloudCallbackTextMaxBytes)
	}
	deliverable += " If you encounter a blocker, unclear requirement, or tool defect, leave a concise bounded note through FastSpider_FS working_context markdown.append in docs/progress/04-open-issues.md. If markdown.read returns NOT_FOUND, first call plan.init with initializeMarkdown=true, then read the file and use file-revision CAS; omit secrets, raw provider payloads, full transcripts, and long logs."
	return fmt.Sprintf("FAST_SPIDER_CODEX_CLOUD_COLLABORATION_V3\ncollaboration_id=%s task_id=%s generation=%d depth=%d\nYou are the existing normal visible ChatGPT Cloud CHAT assigned to this task. Complete only this bounded task in this CHAT. Do not create a new Cloud Worker, Cloud CHAT, Codex session, fork, or child conversation; do not call session.create, session.send, or fork; do not invoke another AI provider, expand permissions, or delete sessions. The FastSpider_FS MCP plugin in this same conversation is the primary completion callback channel. Node callback delivery is only a recovery fallback; do not wait for Node and do not treat its callback as the normal completion path.\n\nBefore doing work, call FastSpider_FS codex_cloud_collaboration with action=get and params {collaborationId=%q, taskId=%q, actorSessionId=%q, actorRole=%q}. The $self actor marker binds to this task's currently registered Cloud CHAT. Use the returned current task, generation, machineId, workingDirectory, allowedActions, accessMode, writeScope, callbackType, and any resultPath; do not use stale values.\n\n%s\n\nCALLBACK REQUIRED BEFORE YOUR FINAL CHAT MESSAGE:\n1. Finish the task inside the inherited scope and produce the result in the callbackType stated above.\n2. Call FastSpider_FS codex_cloud_collaboration exactly once with action=completion.notify and params containing the same collaborationId/taskId, actorSessionId=$self, outcome=completed|blocked|failed, and callbackType. Include text only when callbackType=text. This existing-tool route works even when the ChatGPT connector has not refreshed the optional codex_cloud_completion shortcut. Never send a local file body or caller-selected path; for callbackType=local_file the Hub returns the task's registered Node-local path.\n3. Retry the identical completion.notify call if transport is uncertain; it is idempotent. Do not call event.ingest, event.ack, artifact upload, or session.result on the normal completion path.\n4. Only after completion.notify succeeds, return a short business receipt. If the FS notification cannot be persisted, report that failure and do not claim the collaboration task is done; the Node fallback may recover it.\n\nTASK:\n%s", collaborationID, task.ID, task.Generation, task.Depth, collaborationID, task.ID, cloudCollaborationSelfActor, "chat", deliverable, task.Prompt)
}

func (s *Service) ensureCodexCloudCollaborationReady(ctx context.Context, ownerID, machineID string) error {
	result, err := s.CallCapability(ctx, ownerID, machineID, "agent.control", "provider.readiness", map[string]any{"providerId": "codex", "backend": "chatgpt_cloud", "mode": "safe"})
	if err != nil {
		return err
	}
	ready, _ := result["ready"].(bool)
	cloudReady, _ := result["chatgptCloudAvailable"].(bool)
	if ready && cloudReady {
		return nil
	}
	reason, _ := result["reasonCode"].(string)
	return &CapabilityCallError{Code: "RUNTIME_UNAVAILABLE", Message: "Codex cloud collaboration requires the local Codex app-server to be available and authenticated with ChatGPT", Retryable: true, Details: map[string]any{"reasonCode": reason, "providerId": "codex", "backend": "chatgpt_cloud"}}
}

func (s *Service) validateCodexLocalCollaborationSession(ctx context.Context, ownerID, machineID, sessionID string) error {
	result, err := s.CallCapability(ctx, ownerID, machineID, "agent.control", "session.get", map[string]any{"providerId": "codex", "sessionId": strings.TrimSpace(sessionID), "metadataOnly": true, "preferDesktopRegistry": true})
	if err != nil {
		return err
	}
	session, _ := result["session"].(map[string]any)
	providerID, _ := session["providerId"].(string)
	backend, _ := session["backend"].(string)
	if session == nil || providerID != "codex" || backend != "codex_local" {
		return &CapabilityCallError{Code: "INVALID_REQUEST", Message: "Codex cloud collaboration controller and dispatcher must be existing local Codex sessions", Retryable: false}
	}
	return nil
}

func cloudCollaborationActionNeedsLease(action, role string) bool {
	if role != "dispatcher" && role != "chat" {
		return false
	}
	switch action {
	case "get", "tick", "lease.acquire", "lease.release":
		return false
	default:
		return true
	}
}

func requireCloudCollaborationLease(state cloudCollaborationState, actorSessionID, role string, now time.Time) error {
	expiresAt, err := time.Parse(time.RFC3339, state.Lease.ExpiresAt)
	if err != nil || !expiresAt.After(now) || state.Lease.Generation != state.Generation || state.Lease.SessionID != state.DispatcherSessionID {
		return store.ErrUnauthorized
	}
	if role == "dispatcher" && state.Lease.SessionID != strings.TrimSpace(actorSessionID) {
		return store.ErrUnauthorized
	}
	return nil
}

func validateCloudCollaborationLimits(req CloudCollaborationRequest) error {
	for _, item := range []struct{ value, min, max int }{
		{req.MaxDepth, 1, 8}, {req.MaxActiveChats, 1, 8}, {req.MaxCreates, 1, 100}, {req.HeartbeatMinutes, 5, 1440}, {req.StallMinutes, 15, 1440},
	} {
		if item.value != 0 && (item.value < item.min || item.value > item.max) {
			return store.ErrConflict
		}
	}
	return nil
}

func deadlinePassed(state cloudCollaborationState, now time.Time) bool {
	if state.Deadline == "" {
		return false
	}
	deadline, err := time.Parse(time.RFC3339, state.Deadline)
	return err != nil || !deadline.After(now)
}
func taskIndex(state cloudCollaborationState, id string) int {
	for i := range state.Tasks {
		if state.Tasks[i].ID == id {
			return i
		}
	}
	return -1
}
func chatTaskIndex(state cloudCollaborationState, task cloudCollaborationTask) int {
	for i := len(state.Chats) - 1; i >= 0; i-- {
		if state.Chats[i].SessionID == task.ChatSessionID && state.Chats[i].TaskID == task.ID && state.Chats[i].Generation == task.Generation {
			return i
		}
	}
	return -1
}
func activeCloudChats(state cloudCollaborationState) int {
	count := 0
	for _, c := range state.Chats {
		if c.Status == "active" || c.Status == "creating" {
			count++
		}
	}
	return count
}
func boundedCollaborationText(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		return ""
	}
	return value
}
func validCollaborationSessionID(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n\t ")
}
func boundedInt(value, min, max, fallback int) int {
	if value == 0 {
		return fallback
	}
	if value < min || value > max {
		return fallback
	}
	return value
}

func validCollaborationAbsolutePath(value string) bool {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	return strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || (len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && value[2] == '/')
}

func resolveCollaborationScope(root, scope string) string {
	scope = strings.TrimSpace(scope)
	if validCollaborationAbsolutePath(scope) {
		return scope
	}
	return strings.TrimRight(root, "\\/") + "/" + strings.TrimLeft(scope, "\\/")
}
func normalizedCloudActions(values []string) []string {
	allowed := map[string]bool{"chat.create": true, "chat.send": true, "file.read": true, "file.write": true, "shell": true, "git.read": true, "git.write": true, "browser": true, "archive": true, "delete": true}
	set := map[string]bool{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if allowed[v] {
			set[v] = true
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
func stringSubset(child, parent []string) bool {
	for _, v := range child {
		if !containsString(parent, v) {
			return false
		}
	}
	return true
}
func containsString(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}
func scopeContains(parent, child string) bool {
	parent = normalizeCollaborationPath(parent)
	child = normalizeCollaborationPath(child)
	if parent == "" || child == "" || parent == "." || child == "." {
		return false
	}
	return child == parent || strings.HasPrefix(child, parent+"/")
}
func scopesOverlap(a, b string) bool { return scopeContains(a, b) || scopeContains(b, a) }

func normalizeCollaborationPath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return ""
	}
	value = path.Clean(value)
	if (len(value) >= 2 && value[1] == ':') || strings.HasPrefix(value, "//") {
		value = strings.ToLower(value)
	}
	return strings.TrimSuffix(value, "/")
}
func numberField(value map[string]any, key string) int64 {
	out, _ := numericInt64(value[key])
	return out
}

func mapString(value map[string]any, key string) string {
	if value == nil {
		return ""
	}
	text, _ := value[key].(string)
	return strings.TrimSpace(text)
}
func numericInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	case json.Number:
		n, e := v.Int64()
		return n, e == nil
	}
	return 0, false
}
