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
	"github.com/isguang2024/fast-spider/internal/security"
)

const cloudCollaborationStateVersion = 1

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
	Prompt              string
	AccessMode          string
	WriteScope          string
	DeliverablePath     string
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
	AllowedActions  []string `json:"allowedActions"`
	IdempotencyKey  string   `json:"idempotencyKey"`
	ChatSessionID   string   `json:"chatSessionId,omitempty"`
	ResultID        string   `json:"resultId,omitempty"`
	ResultStatus    string   `json:"resultStatus,omitempty"`
	ResultSHA256    string   `json:"resultSha256,omitempty"`
	CreatedAt       string   `json:"createdAt"`
	UpdatedAt       string   `json:"updatedAt"`
}

type cloudCollaborationChat struct {
	SessionID          string   `json:"sessionId"`
	TaskID             string   `json:"taskId"`
	ParentSession      string   `json:"parentSessionId,omitempty"`
	Depth              int      `json:"depth"`
	Generation         int64    `json:"generation"`
	Status             string   `json:"status"`
	AccessMode         string   `json:"accessMode"`
	WriteScope         string   `json:"writeScope,omitempty"`
	AllowedActions     []string `json:"allowedActions"`
	CallbackRegistered bool     `json:"callbackRegistered"`
	WatchCursor        int64    `json:"watchCursor"`
	LastObservedAt     string   `json:"lastObservedAt"`
	LastProgressAt     string   `json:"lastProgressAt"`
	QuietChecks        int      `json:"quietChecks"`
	StalledNotified    bool     `json:"stalledNotified"`
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
		return s.cloudCollaborationDispatchTask(ctx, ownerID, rec, state, req, role)
	case "status.poll":
		if role != "dispatcher" && role != "chat" {
			return nil, store.ErrUnauthorized
		}
		return s.cloudCollaborationPollStatus(ctx, ownerID, rec, state, req)
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
		AllowedActions: normalizedCloudActions(req.AllowedActions), MaxDepth: boundedInt(req.MaxDepth, 1, 8, 3), MaxActiveChats: boundedInt(req.MaxActiveChats, 1, 8, 3), MaxCreates: boundedInt(req.MaxCreates, 1, 100, 20), HeartbeatMinutes: boundedInt(req.HeartbeatMinutes, 5, 1440, 15), StallMinutes: boundedInt(req.StallMinutes, 15, 1440, 60),
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
	if err := json.Unmarshal([]byte(rec.StateJSON), &state); err != nil || state.Version != cloudCollaborationStateVersion {
		return store.CloudCollaborationRecord{}, cloudCollaborationState{}, store.ErrConflict
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
	return map[string]any{"collaborationId": rec.CollaborationID, "status": state.Status, "revision": rec.Revision, "actions": cloudCollaborationTickActions(state), "bounded": true, "externalCalls": 0}
}

func cloudCollaborationTickActions(state cloudCollaborationState) []map[string]any {
	now := time.Now().UTC()
	actions := make([]map[string]any, 0, 8)
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
		if len(actions) == 8 {
			return actions
		}
	}
	for _, chat := range state.Chats {
		if chat.Status != "active" {
			continue
		}
		if !chat.CallbackRegistered {
			actions = append(actions, map[string]any{"type": "ensure_callback", "taskId": chat.TaskID})
			if len(actions) == 8 {
				return actions
			}
		}
		last, _ := time.Parse(time.RFC3339, chat.LastObservedAt)
		if last.IsZero() || now.Sub(last) >= time.Duration(state.HeartbeatMinutes)*time.Minute {
			action := map[string]any{"type": "poll_chat_status", "taskId": chat.TaskID}
			if chat.StalledNotified {
				action["suspectedStalled"] = true
			}
			actions = append(actions, action)
		}
		if len(actions) == 8 {
			return actions
		}
	}
	return actions
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
	if !containsString(actions, "chat.create") {
		return store.ErrUnauthorized
	}
	now := time.Now().UTC().Format(time.RFC3339)
	idemSum := sha256.Sum256([]byte(id + "\x00" + fmt.Sprintf("%d", state.Generation)))
	state.Tasks = append(state.Tasks, cloudCollaborationTask{ID: id, Title: boundedCollaborationText(req.Title, 500), Prompt: boundedCollaborationText(req.Prompt, 12000), Status: "queued", ParentSession: parent, Depth: depth, Generation: state.Generation, AccessMode: access, WriteScope: writeScope, DeliverablePath: deliverablePath, AllowedActions: actions, IdempotencyKey: "collab_chat_" + hex.EncodeToString(idemSum[:])[:40], CreatedAt: now, UpdatedAt: now})
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
	if task.Status == "active" && task.ChatSessionID != "" {
		chatIdx := chatIndex(state, task.ChatSessionID)
		if chatIdx < 0 {
			return nil, store.ErrConflict
		}
		if state.Chats[chatIdx].CallbackRegistered {
			return cloudCollaborationView(rec, state, "dispatcher"), nil
		}
		if _, err := s.registerCloudCollaborationCallback(ctx, ownerID, rec, state, *task); err != nil {
			out := cloudCollaborationView(rec, state, "dispatcher")
			out["callbackPending"] = true
			return out, nil
		}
		state.Chats[chatIdx].CallbackRegistered = true
		return s.saveCloudCollaboration(ctx, ownerID, rec, state)
	}
	if task.Status != "queued" && task.Status != "create_in_doubt" {
		return nil, store.ErrConflict
	}
	if deadlinePassed(state, s.now().UTC()) || activeCloudChats(state) >= state.MaxActiveChats {
		return nil, store.ErrResourceLimit
	}
	if err := s.ensureCodexCloudCollaborationReady(ctx, ownerID, rec.MachineID); err != nil {
		return nil, err
	}
	if task.Status == "queued" {
		state.CreateCount++
		task.Status = "creating"
		task.UpdatedAt = s.now().UTC().Format(time.RFC3339)
	}
	reserved, err := s.saveCloudCollaboration(ctx, ownerID, rec, state)
	if err != nil {
		return nil, err
	}
	rec.Revision = numberField(reserved, "revision")
	prompt := cloudCollaborationBootstrap(rec.CollaborationID, state, *task)
	result, callErr := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.create", map[string]any{"providerId": "codex", "backend": "chatgpt_cloud", "visibility": "visible", "mode": "complete", "workingDirectory": state.WorkingDirectory, "prompt": prompt, "idempotencyKey": task.IdempotencyKey})
	if callErr != nil {
		task.Status = "create_in_doubt"
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
		task.UpdatedAt = s.now().UTC().Format(time.RFC3339)
		out, saveErr := s.saveCloudCollaboration(ctx, ownerID, rec, state)
		if saveErr != nil {
			return nil, saveErr
		}
		out["createInDoubt"] = true
		return out, nil
	}
	updatedRec, updatedState, updated, persistErr := s.persistCloudCollaborationCreatedChat(ctx, ownerID, rec, state, *task, sessionID)
	if persistErr != nil {
		return nil, persistErr
	}
	rec, state = updatedRec, updatedState
	idx = taskIndex(state, req.TaskID)
	task = &state.Tasks[idx]
	_, callbackErr := s.registerCloudCollaborationCallback(ctx, ownerID, rec, state, *task)
	if callbackErr == nil {
		state.Chats[chatIndex(state, task.ChatSessionID)].CallbackRegistered = true
		return s.saveCloudCollaboration(ctx, ownerID, rec, state)
	}
	updated["callbackPending"] = true
	return updated, nil
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
		if chatIndex(state, sessionID) < 0 {
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

func (s *Service) registerCloudCollaborationCallback(ctx context.Context, ownerID string, rec store.CloudCollaborationRecord, state cloudCollaborationState, task cloudCollaborationTask) (map[string]any, error) {
	return s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.callback.register", map[string]any{"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": task.ChatSessionID, "callbackTargetSessionId": state.DispatcherSessionID, "callbackMissionId": rec.CollaborationID, "callbackTaskId": task.ID, "callbackGeneration": task.Generation, "callbackDeliverablePath": task.DeliverablePath})
}

func (s *Service) cloudCollaborationPollStatus(ctx context.Context, ownerID string, rec store.CloudCollaborationRecord, state cloudCollaborationState, req CloudCollaborationRequest) (map[string]any, error) {
	idx := taskIndex(state, req.TaskID)
	if idx < 0 {
		return nil, store.ErrNotFound
	}
	task := &state.Tasks[idx]
	chatIdx := chatIndex(state, task.ChatSessionID)
	if chatIdx < 0 {
		return nil, store.ErrNotFound
	}
	chat := &state.Chats[chatIdx]
	result, err := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.watch", map[string]any{"providerId": "codex", "sessionId": chat.SessionID, "cursor": chat.WatchCursor, "waitSeconds": 0})
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	next, _ := numericInt64(result["nextCursor"])
	progressed := next > chat.WatchCursor
	if progressed {
		chat.WatchCursor, chat.LastProgressAt, chat.QuietChecks, chat.StalledNotified = next, now.Format(time.RFC3339), 0, false
	} else {
		chat.QuietChecks++
	}
	chat.LastObservedAt = now.Format(time.RFC3339)
	lastProgress, _ := time.Parse(time.RFC3339, chat.LastProgressAt)
	newlyStalled := !progressed && chat.QuietChecks >= 2 && now.Sub(lastProgress) >= time.Duration(state.StallMinutes)*time.Minute && !chat.StalledNotified
	if newlyStalled {
		chat.StalledNotified = true
	}
	out, saveErr := s.saveCloudCollaboration(ctx, ownerID, rec, state)
	if saveErr != nil {
		return nil, saveErr
	}
	out["progressed"], out["newlyStalled"] = progressed, newlyStalled
	return out, nil
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
	chatIdx := chatIndex(*state, task.ChatSessionID)
	if chatIdx < 0 {
		return store.ErrNotFound
	}
	state.Chats[chatIdx].Status = "retiring"
	task.Status = "queued"
	task.ChatSessionID = ""
	task.Generation++
	state.Generation++
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
	if _, err := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.delete", map[string]any{"providerId": "codex", "sessionId": task.ChatSessionID}); err != nil {
		return nil, err
	}
	if ci := chatIndex(state, task.ChatSessionID); ci >= 0 {
		state.Chats[ci].Status = "deleted"
	}
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
	state.Status = "closing"
	for i := range state.Chats {
		chat := &state.Chats[i]
		if chat.Status == "archived" || chat.Status == "deleted" {
			continue
		}
		if _, err := s.CallCapability(ctx, ownerID, rec.MachineID, "agent.control", "session.archive", map[string]any{"providerId": "codex", "sessionId": chat.SessionID}); err != nil {
			return s.saveCloudCollaboration(ctx, ownerID, rec, state)
		}
		chat.Status = "archived"
	}
	state.Status = "completed"
	return s.saveCloudCollaboration(ctx, ownerID, rec, state)
}

func cloudCollaborationBootstrap(collaborationID string, state cloudCollaborationState, task cloudCollaborationTask) string {
	deliverable := "No local deliverable path is assigned. Return a concise final result for the callback Result Pool."
	if task.DeliverablePath != "" {
		deliverable = fmt.Sprintf("Write the final deliverable directly to %s through ordinary Fast Spider file tools. Verify the file exists, then keep the final CHAT reply to a short receipt; do not repeat the deliverable body in CHAT.", task.DeliverablePath)
	}
	return fmt.Sprintf("FAST_SPIDER_CODEX_CLOUD_COLLABORATION_V1\ncollaboration_id=%s task_id=%s generation=%d depth=%d\nYou are a normal visible ChatGPT Cloud CHAT in Codex cloud collaboration. Complete only this bounded task. You may create child Cloud CHAT sessions only through codex_cloud_collaboration and only within inherited limits/actions. Do not expand permissions, recurse beyond depth, invoke another AI provider, or delete sessions. %s\nTASK:\n%s", collaborationID, task.ID, task.Generation, task.Depth, deliverable, task.Prompt)
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
	result, err := s.CallCapability(ctx, ownerID, machineID, "agent.control", "session.get", map[string]any{"providerId": "codex", "sessionId": strings.TrimSpace(sessionID)})
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
func chatIndex(state cloudCollaborationState, id string) int {
	for i := range state.Chats {
		if state.Chats[i].SessionID == id {
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
