package core

import (
	"context"
	"testing"

	"github.com/isguang2024/fast-spider/internal/hub/store"
)

func TestCloudChatReuseRequiresAcknowledgedBlockAndDrainedQueue(t *testing.T) {
	s, owner, machine, node := newCloudCollaborationTestService(t)
	ctx := context.Background()
	dispatched, err := s.CloudCollaboration(ctx, owner, CloudCollaborationRequest{Action: "dispatch", MachineID: machine, CallbackSessionID: "target", WorkingDirectory: t.TempDir(), Prompt: "Work or report a decision blocker", IdempotencyKey: "blocked-chat-reuse-test-001", AccessMode: "read_only"})
	if err != nil {
		t.Fatal(err)
	}
	cid := dispatched["collaborationId"].(string)
	_, state, err := s.loadCloudCollaboration(ctx, owner, cid)
	if err != nil {
		t.Fatal(err)
	}
	task := state.Tasks[0]
	callback := map[string]any{"missionId": cid, "taskId": task.ID, "targetSessionId": "target", "generation": task.Generation, "pendingCount": 0}
	node.setResponse("session.callback.list", map[string]any{"callbacks": []any{callback}})
	reuse := func() error {
		return s.releaseCompletedCloudCallbackForReuse(ctx, owner, store.CloudCollaborationRecord{CollaborationID: "next-collaboration", MachineID: machine}, cloudCollaborationState{DispatcherSessionID: "target"}, cloudCollaborationTask{ID: "next-task", Generation: 1}, task.ChatSessionID)
	}
	if _, err := s.SubmitTaskResult(ctx, owner, dispatched["taskRef"].(string), "blocked", "Need Resource Center bind API; local work is saved"); err != nil {
		t.Fatal(err)
	}
	if err := reuse(); err == nil {
		t.Fatal("unacknowledged block released the CHAT")
	}
	claim, err := s.CloudCompletion(ctx, owner, CloudCompletionRequest{Action: "claim", ActorSessionID: "target"})
	if err != nil {
		t.Fatal(err)
	}
	items := claim["claimed"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("blocked callback missing: %v", items)
	}
	if _, err := s.CloudCompletion(ctx, owner, CloudCompletionRequest{Action: "ack", ActorSessionID: "target", ClaimID: claim["claimId"].(string), Acknowledgements: []CloudCompletionAckItem{{NotificationID: items[0]["notificationId"].(string)}}}); err != nil {
		t.Fatal(err)
	}
	callback["pendingCount"] = 1
	if err := reuse(); err == nil {
		t.Fatal("pending callback was discarded")
	}
	callback["pendingCount"] = 0
	callback["generation"] = task.Generation + 1
	if err := reuse(); err == nil {
		t.Fatal("different generation was released")
	}
	callback["generation"] = task.Generation
	if err := reuse(); err != nil {
		t.Fatalf("acknowledged blocked CHAT cannot be reused: %v", err)
	}
	if _, err := s.SubmitTaskResult(ctx, owner, dispatched["taskRef"].(string), "completed", "Changed old result"); err == nil {
		t.Fatal("old blocked result was overwritten")
	}
	_, retained, err := s.loadCloudCollaboration(ctx, owner, cid)
	if err != nil || retained.Tasks[0].Status != "blocked" {
		t.Fatalf("historical blocked fact changed: %v %v", retained.Tasks, err)
	}
}
