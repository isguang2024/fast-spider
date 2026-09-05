package core

import (
	"context"
	"errors"
	"testing"
)

func TestRecoveryReceiptNeverBlocksFormalTaskResult(t *testing.T) {
	for _, outcome := range []string{"failed", "completed"} {
		for _, acked := range []bool{false, true} {
			name := outcome
			if acked {
				name += "-acked"
			}
			t.Run(name, func(t *testing.T) {
				s, owner, machine, node := newCloudCollaborationTestService(t)
				ctx := context.Background()
				dispatched, err := s.CloudCollaboration(ctx, owner, CloudCollaborationRequest{Action: "dispatch", MachineID: machine, CallbackSessionID: "target", WorkingDirectory: t.TempDir(), Prompt: "result", IdempotencyKey: "recovery-result-test-001", AccessMode: "read_only"})
				if err != nil {
					t.Fatal(err)
				}
				cid := dispatched["collaborationId"].(string)
				_, state, err := s.loadCloudCollaboration(ctx, owner, cid)
				if err != nil {
					t.Fatal(err)
				}
				task := state.Tasks[0]
				text := ""
				if outcome == "completed" {
					text = "remaining work is not finished"
				}
				before := len(node.snapshotCalls())
				result, err := s.CloudCompletion(ctx, owner, CloudCompletionRequest{Action: "notify", CollaborationID: cid, TaskID: task.ID, ActorSessionID: "target", SourceSessionID: task.ChatSessionID, Outcome: outcome, Text: text})
				if err != nil || result["recoveryOnly"] != true {
					t.Fatalf("recovery=%v %v", result, err)
				}
				if len(node.snapshotCalls()) != before {
					t.Fatal("recovery replay created another Node wake")
				}
				claim, err := s.CloudCompletion(ctx, owner, CloudCompletionRequest{Action: "claim", ActorSessionID: "target"})
				if err != nil {
					t.Fatal(err)
				}
				if acked {
					item := claim["claimed"].([]map[string]any)[0]
					_, err = s.CloudCompletion(ctx, owner, CloudCompletionRequest{Action: "ack", ActorSessionID: "target", ClaimID: claim["claimId"].(string), Acknowledgements: []CloudCompletionAckItem{{NotificationID: item["notificationId"].(string)}}})
					if err != nil {
						t.Fatal(err)
					}
				}
				_, after, err := s.loadCloudCollaboration(ctx, owner, cid)
				if err != nil || after.Tasks[0].Status != task.Status || after.Status != state.Status {
					t.Fatalf("recovery finalized task: %+v %v", after, err)
				}
				if _, err := s.SubmitTaskResult(ctx, owner, dispatched["taskRef"].(string), "completed", "actual final result"); err != nil {
					t.Fatal(err)
				}
				if _, err := s.SubmitTaskResult(ctx, owner, dispatched["taskRef"].(string), "completed", "different final result"); err == nil {
					t.Fatal("inconsistent formal result swallowed")
				} else {
					var ce *CapabilityCallError
					if !errors.As(err, &ce) || ce.Code != "TASK_RESULT_CONFLICT" {
						t.Fatalf("conflict=%v", err)
					}
				}
			})
		}
	}
}
