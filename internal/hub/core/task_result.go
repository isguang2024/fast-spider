package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
)

// A task reference identifies an existing generation. It is not a credential;
// the authenticated owner is still checked when loading the collaboration.
type taskResultReference struct {
	CollaborationID string `json:"c"`
	TaskID          string `json:"t"`
	Generation      int64  `json:"g"`
}

func cloudTaskResultReference(collaborationID string, task cloudCollaborationTask) string {
	raw, _ := json.Marshal(taskResultReference{collaborationID, task.ID, task.Generation})
	return "task_" + base64.RawURLEncoding.EncodeToString(raw)
}

// SubmitTaskResult only reports an already assigned task. Neither its local
// output path nor its notification destination can be selected by the producer.
func (s *Service) SubmitTaskResult(ctx context.Context, ownerID, taskRef, status, text string) (map[string]any, error) {
	status = strings.TrimSpace(status)
	invalid := func() error {
		return &CapabilityCallError{Code: "INVALID_REQUEST", Message: "taskRef must be the reference supplied with the assigned task"}
	}
	if len(taskRef) > 2048 || !strings.HasPrefix(taskRef, "task_") {
		return nil, invalid()
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(taskRef, "task_"))
	if err != nil {
		return nil, invalid()
	}
	var ref taskResultReference
	if json.Unmarshal(raw, &ref) != nil || ref.Generation < 1 || validateCloudCompletionOpaqueID(ref.CollaborationID, "collaborationId") != nil || validateCloudCompletionOpaqueID(ref.TaskID, "taskId") != nil {
		return nil, invalid()
	}
	result, err := s.notifyCloudCompletion(ctx, ownerID, CloudCompletionRequest{
		Action: "notify", CollaborationID: ref.CollaborationID, TaskID: ref.TaskID,
		ActorSessionID: cloudCollaborationSelfActor, ExpectedGeneration: ref.Generation,
		Outcome: status, Text: text,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"taskRef": taskRef, "status": status, "accepted": true, "replayed": result["replayed"],
		"result": result["notification"], "notificationAccepted": result["activeCallbackAccepted"],
		"notificationPolicy": "notify-preassigned-codex-session-when-idle",
		"localFile":          result["localFile"],
	}, nil
}
