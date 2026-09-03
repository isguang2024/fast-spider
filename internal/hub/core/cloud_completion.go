package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/store"
	"github.com/isguang2024/fast-spider/internal/security"
)

const (
	cloudCompletionNotificationKind = "completion"
	cloudCompletionClaimLease       = 5 * time.Minute
	cloudCompletionMaxClaimBatch    = 64
)

type CloudCompletionAckItem struct {
	NotificationID    string
	ResultID          string
	ResultStatus      string
	ResultBytes       int64
	ResultSHA256      string
	DeliverableStatus string
}

type CloudCompletionRequest struct {
	Action           string
	CollaborationID  string
	TaskID           string
	ActorSessionID   string
	SourceSessionID  string
	Outcome          string
	ClaimID          string
	Limit            int
	Acknowledgements []CloudCompletionAckItem
}

// CloudCompletion is the lightweight completion-notification channel. It is
// deliberately independent from collaboration revision and dispatcher lease:
// the producer writes its result first and this API only persists a notification.
func (s *Service) CloudCompletion(ctx context.Context, ownerID string, req CloudCompletionRequest) (map[string]any, error) {
	switch strings.TrimSpace(req.Action) {
	case "notify":
		return s.notifyCloudCompletion(ctx, ownerID, req)
	case "claim":
		return s.claimCloudCompletions(ctx, ownerID, req)
	case "ack":
		return s.ackCloudCompletions(ctx, ownerID, req)
	default:
		return nil, store.ErrNotFound
	}
}

func (s *Service) notifyCloudCompletion(ctx context.Context, ownerID string, req CloudCompletionRequest) (map[string]any, error) {
	rec, state, err := s.loadCloudCollaboration(ctx, ownerID, strings.TrimSpace(req.CollaborationID))
	if err != nil {
		return nil, err
	}
	idx := taskIndex(state, strings.TrimSpace(req.TaskID))
	if idx < 0 {
		return nil, store.ErrNotFound
	}
	task := state.Tasks[idx]
	if task.ChatSessionID == "" || task.Generation < 1 {
		return nil, store.ErrConflict
	}
	outcome := strings.TrimSpace(req.Outcome)
	if outcome != "completed" && outcome != "blocked" && outcome != "failed" {
		return nil, store.ErrConflict
	}
	actorSessionID := strings.TrimSpace(req.ActorSessionID)
	sourceSessionID := task.ChatSessionID
	switch actorSessionID {
	case cloudCollaborationSelfActor:
		if strings.TrimSpace(req.SourceSessionID) != "" {
			return nil, store.ErrUnauthorized
		}
	case state.DispatcherSessionID:
		// The dispatcher may replay a durable Node fallback notification. The
		// source remains the CHAT already bound to this exact task generation.
		if strings.TrimSpace(req.SourceSessionID) != task.ChatSessionID {
			return nil, store.ErrUnauthorized
		}
	default:
		return nil, store.ErrUnauthorized
	}
	if task.Status != "active" && task.Status != "completion_reported" && task.Status != "verifying" && task.Status != "result_available" && task.Status != "done" && task.Status != "blocked" {
		return nil, store.ErrConflict
	}
	now := s.now().UTC()
	notification := store.CloudCompletionNotificationRecord{
		NotificationID:   cloudCompletionNotificationID(rec.CollaborationID, task.ID, task.Generation),
		OwnerID:          ownerID,
		CollaborationID:  rec.CollaborationID,
		TaskID:           task.ID,
		Generation:       task.Generation,
		NotificationKind: cloudCompletionNotificationKind,
		Outcome:          outcome,
		SourceSessionID:  sourceSessionID,
		TargetSessionID:  state.DispatcherSessionID,
		DeliverablePath:  cloudCollaborationTaskResultPath(task),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	stored, replayed, err := s.store.EnqueueCloudCompletionNotification(ctx, notification)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"notification":      cloudCompletionNotificationMap(stored, now),
		"notificationId":    stored.NotificationID,
		"replayed":          replayed,
		"deliveryPolicy":    "durable-batch-claim",
		"maxClaimBatch":     cloudCompletionMaxClaimBatch,
		"claimLeaseSeconds": int64(cloudCompletionClaimLease / time.Second),
	}, nil
}

func (s *Service) claimCloudCompletions(ctx context.Context, ownerID string, req CloudCompletionRequest) (map[string]any, error) {
	targetSessionID := strings.TrimSpace(req.ActorSessionID)
	if err := validateCloudCompletionOpaqueID(targetSessionID, "actorSessionId"); err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit == 0 {
		limit = cloudCompletionMaxClaimBatch
	}
	if limit < 1 || limit > cloudCompletionMaxClaimBatch {
		return nil, store.ErrConflict
	}
	claimID := strings.TrimSpace(req.ClaimID)
	if claimID == "" {
		var err error
		claimID, err = security.RandomOpaque("cclaim_")
		if err != nil {
			return nil, err
		}
	} else if err := validateCloudCompletionOpaqueID(claimID, "claimId"); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	claimed, err := s.store.ClaimCloudCompletionNotifications(ctx, ownerID, targetSessionID, claimID, limit, now, cloudCompletionClaimLease)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(claimed))
	for _, notification := range claimed {
		items = append(items, cloudCompletionNotificationMap(notification, now))
	}
	return map[string]any{
		"claimId":           claimID,
		"claimed":           items,
		"claimedCount":      len(items),
		"claimLeaseSeconds": int64(cloudCompletionClaimLease / time.Second),
		"deliveryPolicy":    "durable-batch-claim",
		"queueText":         cloudCompletionQueueText(targetSessionID, claimID, claimed),
	}, nil
}

func (s *Service) ackCloudCompletions(ctx context.Context, ownerID string, req CloudCompletionRequest) (map[string]any, error) {
	targetSessionID := strings.TrimSpace(req.ActorSessionID)
	claimID := strings.TrimSpace(req.ClaimID)
	if err := validateCloudCompletionOpaqueID(targetSessionID, "actorSessionId"); err != nil {
		return nil, err
	}
	if err := validateCloudCompletionOpaqueID(claimID, "claimId"); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	claimed, alreadyAcked, err := s.store.GetCloudCompletionClaim(ctx, ownerID, targetSessionID, claimID, now, cloudCompletionClaimLease)
	if err != nil {
		return nil, err
	}
	if alreadyAcked {
		return map[string]any{"claimId": claimID, "acked": true, "ackedCount": 0, "replayed": true, "deliveryPolicy": "durable-batch-claim"}, nil
	}
	ackByID := make(map[string]CloudCompletionAckItem, len(req.Acknowledgements))
	for _, ack := range req.Acknowledgements {
		ack.NotificationID = strings.TrimSpace(ack.NotificationID)
		if ack.NotificationID == "" || ackByID[ack.NotificationID].NotificationID != "" {
			return nil, store.ErrConflict
		}
		ackByID[ack.NotificationID] = ack
	}
	if len(ackByID) != len(claimed) {
		return nil, store.ErrConflict
	}
	for _, notification := range claimed {
		ack, ok := ackByID[notification.NotificationID]
		if !ok {
			return nil, store.ErrConflict
		}
		if err := validateCloudCompletionAcknowledgement(notification, ack); err != nil {
			return nil, err
		}
	}
	archiveIDs := map[string]bool{}
	for _, notification := range claimed {
		closing, err := s.applyCloudCompletionAcknowledgement(ctx, ownerID, notification, ackByID[notification.NotificationID], now)
		if err != nil {
			return nil, err
		}
		if closing {
			archiveIDs[notification.CollaborationID] = true
		}
	}
	acked, err := s.store.AcknowledgeCloudCompletionClaim(ctx, ownerID, targetSessionID, claimID, now, cloudCompletionClaimLease)
	if err != nil {
		return nil, err
	}
	for collaborationID := range archiveIDs {
		s.archiveCloudCollaborationAsync(ownerID, collaborationID)
	}
	return map[string]any{
		"claimId":        claimID,
		"acked":          true,
		"ackedCount":     acked,
		"archivePending": len(archiveIDs) > 0,
		"deliveryPolicy": "durable-batch-claim",
	}, nil
}

func validateCloudCompletionAcknowledgement(notification store.CloudCompletionNotificationRecord, ack CloudCompletionAckItem) error {
	if ack.ResultBytes < 0 || ack.ResultBytes > 256<<20 {
		return store.ErrConflict
	}
	if ack.ResultSHA256 != "" && !validCloudCompletionSHA256(ack.ResultSHA256) {
		return store.ErrConflict
	}
	switch notification.Outcome {
	case "completed":
		if ack.ResultStatus != "ready" && ack.ResultStatus != "completed" {
			return store.ErrConflict
		}
		if ack.ResultSHA256 == "" {
			return store.ErrConflict
		}
		if notification.DeliverablePath != "" {
			if ack.DeliverableStatus != "ready" {
				return store.ErrConflict
			}
		} else if strings.TrimSpace(ack.ResultID) == "" {
			return store.ErrConflict
		}
	case "blocked", "failed":
		if ack.DeliverableStatus != "" && ack.DeliverableStatus != "missing" && ack.DeliverableStatus != "invalid" && ack.DeliverableStatus != "unreadable" && ack.DeliverableStatus != "too_large" {
			return store.ErrConflict
		}
	default:
		return store.ErrConflict
	}
	if len(ack.ResultID) > 256 || strings.ContainsAny(ack.ResultID, "\x00\r\n") {
		return store.ErrConflict
	}
	return nil
}

func (s *Service) applyCloudCompletionAcknowledgement(ctx context.Context, ownerID string, notification store.CloudCompletionNotificationRecord, ack CloudCompletionAckItem, now time.Time) (bool, error) {
	for range 5 {
		rec, state, err := s.loadCloudCollaboration(ctx, ownerID, notification.CollaborationID)
		if err != nil {
			return false, err
		}
		idx := taskIndex(state, notification.TaskID)
		if idx < 0 {
			return false, store.ErrNotFound
		}
		task := &state.Tasks[idx]
		if task.Generation != notification.Generation || task.ChatSessionID != notification.SourceSessionID || cloudCollaborationTaskResultPath(*task) != notification.DeliverablePath || state.DispatcherSessionID != notification.TargetSessionID {
			return false, store.ErrUnauthorized
		}
		resultStatus := strings.TrimSpace(ack.ResultStatus)
		if resultStatus == "" {
			if notification.Outcome == "completed" {
				resultStatus = "completed"
			} else {
				resultStatus = "failed"
			}
		}
		signatureRaw, _ := json.Marshal([]any{notification.TaskID, notification.SourceSessionID, notification.Generation, notification.Outcome, ack.ResultID, resultStatus, ack.ResultBytes, ack.ResultSHA256, notification.DeliverablePath, ack.DeliverableStatus})
		sum := sha256.Sum256(signatureRaw)
		signature := hex.EncodeToString(sum[:])
		eventIdx := -1
		for i := range state.Events {
			if state.Events[i].ID == notification.NotificationID {
				eventIdx = i
				break
			}
		}
		if eventIdx >= 0 {
			event := &state.Events[eventIdx]
			if event.Signature != signature || event.TaskID != task.ID || event.Generation != task.Generation || event.SessionID != task.ChatSessionID {
				return false, store.ErrConflict
			}
			event.Status = "acked"
		} else {
			if len(state.Events) >= 512 {
				state.Events = append([]cloudCollaborationEvent(nil), state.Events[len(state.Events)-384:]...)
			}
			state.Events = append(state.Events, cloudCollaborationEvent{
				ID: notification.NotificationID, TaskID: task.ID, SessionID: task.ChatSessionID, Generation: task.Generation,
				Sequence: 1, Type: "completion.notification", ResultID: ack.ResultID, ResultStatus: resultStatus,
				ResultBytes: ack.ResultBytes, ResultSHA256: ack.ResultSHA256, Status: "acked",
				DeliverablePath: notification.DeliverablePath, DeliverableStatus: ack.DeliverableStatus,
				Signature: signature, CreatedAt: now.Format(time.RFC3339),
			})
		}
		task.ResultID, task.ResultStatus, task.ResultSHA256 = ack.ResultID, resultStatus, ack.ResultSHA256
		if notification.Outcome == "completed" {
			task.Status = "done"
		} else {
			task.Status = "blocked"
		}
		task.UpdatedAt = now.Format(time.RFC3339)
		if chatIdx := chatIndex(state, task.ChatSessionID); chatIdx >= 0 {
			if notification.Outcome == "completed" {
				state.Chats[chatIdx].Status = "completed"
			} else {
				state.Chats[chatIdx].Status = "failed"
			}
			state.Chats[chatIdx].LastObservedAt = now.Format(time.RFC3339)
		}
		allTasksDone := len(state.Tasks) > 0
		for _, current := range state.Tasks {
			if current.Status != "done" && current.Status != "dropped" {
				allTasksDone = false
				break
			}
		}
		if allTasksDone {
			for i := range state.Goals {
				if state.Goals[i].Status != "dropped" {
					state.Goals[i].Status = "done"
				}
			}
		}
		closing := cloudCollaborationReadyToClose(state)
		if closing {
			state.Status = "closing"
		}
		raw, err := json.Marshal(state)
		if err != nil {
			return false, err
		}
		if _, err := s.store.UpdateCloudCollaboration(ctx, ownerID, rec.CollaborationID, state.Status, string(raw), rec.Revision, now); err == nil {
			return closing, nil
		} else if !errors.Is(err, store.ErrConflict) {
			return false, err
		}
	}
	return false, store.ErrConflict
}

func (s *Service) archiveCloudCollaborationAsync(ownerID, collaborationID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		rec, state, err := s.loadCloudCollaboration(ctx, ownerID, collaborationID)
		if err != nil || !cloudCollaborationReadyToClose(state) {
			return
		}
		_, _ = s.cloudCollaborationClose(ctx, ownerID, rec, state)
	}()
}

func cloudCompletionNotificationID(collaborationID, taskID string, generation int64) string {
	sum := sha256.Sum256([]byte(collaborationID + "\x00" + taskID + "\x00" + fmt.Sprintf("%d", generation) + "\x00" + cloudCompletionNotificationKind))
	return "completion_" + hex.EncodeToString(sum[:])[:48]
}

func cloudCompletionNotificationMap(rec store.CloudCompletionNotificationRecord, now time.Time) map[string]any {
	out := map[string]any{
		"notificationId": rec.NotificationID, "collaborationId": rec.CollaborationID, "taskId": rec.TaskID,
		"generation": rec.Generation, "outcome": rec.Outcome, "sourceSessionId": rec.SourceSessionID,
		"targetSessionId": rec.TargetSessionID, "state": rec.State, "createdAt": rec.CreatedAt.Format(time.RFC3339),
	}
	if rec.DeliverablePath != "" {
		out["deliverablePath"] = rec.DeliverablePath
	}
	if rec.ClaimID != "" && rec.ClaimedAt != nil {
		out["claimId"] = rec.ClaimID
		out["claimedAt"] = rec.ClaimedAt.Format(time.RFC3339)
		out["claimExpiresAt"] = rec.ClaimedAt.Add(cloudCompletionClaimLease).Format(time.RFC3339)
		if !rec.ClaimedAt.Add(cloudCompletionClaimLease).After(now.UTC()) && rec.State == "claimed" {
			out["state"] = "claim_expired"
		}
	}
	return out
}

func cloudCompletionQueueText(targetSessionID, claimID string, records []store.CloudCompletionNotificationRecord) string {
	var builder strings.Builder
	builder.WriteString("FAST_SPIDER_CLOUD_COMPLETION_QUEUE_V1\nTARGET_SESSION_ID: ")
	builder.WriteString(targetSessionID)
	builder.WriteString("\nCLAIM_ID: ")
	builder.WriteString(claimID)
	builder.WriteString("\nNOTIFICATIONS:\n")
	ordered := append([]store.CloudCompletionNotificationRecord(nil), records...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].NotificationID < ordered[j].NotificationID })
	for _, rec := range ordered {
		_, _ = fmt.Fprintf(&builder, "- notification=%s collaboration=%s task=%s generation=%d outcome=%s source_session=%s", rec.NotificationID, rec.CollaborationID, rec.TaskID, rec.Generation, rec.Outcome, rec.SourceSessionID)
		if rec.DeliverablePath != "" {
			_, _ = fmt.Fprintf(&builder, " deliverable_path=%s", rec.DeliverablePath)
		}
		builder.WriteByte('\n')
	}
	builder.WriteString("INSTRUCTIONS:\nThis queue contains notifications only, never CHAT result bodies. Verify each fixed local deliverable or Result Pool manifest, then acknowledge the whole claim with codex_cloud_completion action=ack and one acknowledgement per notification.\n")
	return builder.String()
}

func validateCloudCompletionOpaqueID(value, field string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || strings.ContainsAny(value, "\x00\r\n\t ") {
		return fmt.Errorf("%s is invalid", field)
	}
	return nil
}

func validCloudCompletionSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
