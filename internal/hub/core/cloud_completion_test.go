package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/store"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestCloudCompletionDistinguishesMissingCollaborationAndTask(t *testing.T) {
	service, ownerID, machineID, _ := newCloudCollaborationTestService(t)
	ctx := context.Background()
	_, err := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{Action: "notify", CollaborationID: "missing-collaboration", TaskID: "task-1", ActorSessionID: "$self", Outcome: "completed"})
	var capabilityErr *CapabilityCallError
	if !errors.As(err, &capabilityErr) || capabilityErr.Code != "COLLABORATION_NOT_FOUND" {
		t.Fatalf("missing collaboration error=%v", err)
	}

	created, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "create", MachineID: machineID, IdempotencyKey: "completion-missing-task-001",
		ControllerSessionID: "codex-controller", DispatcherSessionID: "codex-dispatcher",
		Title: "Missing task", Goal: "Classify callback routes", DoneWhen: "Missing task is explicit",
		WorkingDirectory: t.TempDir(), AllowedActions: []string{"chat.create", "file.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{Action: "notify", CollaborationID: created["collaborationId"].(string), TaskID: "missing-task", ActorSessionID: "$self", Outcome: "completed"})
	capabilityErr = nil
	if !errors.As(err, &capabilityErr) || capabilityErr.Code != "TASK_NOT_FOUND" {
		t.Fatalf("missing task error=%v", err)
	}
}

func TestCloudCompletionSupportsBoundedTextAndStatusCallbacks(t *testing.T) {
	service, ownerID, machineID, _ := newCloudCollaborationTestService(t)
	ctx := context.Background()
	created, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "create", MachineID: machineID, IdempotencyKey: "completion-typed-callbacks-001",
		ControllerSessionID: "codex-controller", DispatcherSessionID: "codex-dispatcher",
		Title: "Typed callbacks", Goal: "Return bounded results", DoneWhen: "Both callbacks are acknowledged",
		WorkingDirectory: t.TempDir(), AllowedActions: []string{"chat.create", "file.read"}, MaxActiveChats: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	collaborationID := created["collaborationId"].(string)
	current, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "lease.acquire", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(created, "revision"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []struct {
		id           string
		callbackType string
	}{
		{id: "task-text", callbackType: protocolv1.CloudCallbackTypeText},
		{id: "task-status", callbackType: protocolv1.CloudCallbackTypeStatus},
	} {
		current, err = service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
			Action: "task.add", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(current, "revision"),
			TaskID: task.id, Title: task.id, Prompt: "Return the requested callback.", AccessMode: "read_only", CallbackType: task.callbackType, AllowedActions: []string{"chat.create", "file.read"},
		})
		if err != nil {
			t.Fatalf("add %s: %v", task.id, err)
		}
		current, err = service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
			Action: "task.dispatch", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(current, "revision"), TaskID: task.id,
		})
		if err != nil {
			t.Fatalf("dispatch %s: %v", task.id, err)
		}
	}
	_, err = service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{
		Action: "notify", CollaborationID: collaborationID, TaskID: "task-text", ActorSessionID: "$self", Outcome: "completed", CallbackType: protocolv1.CloudCallbackTypeText, Text: strings.Repeat("界", protocolv1.CloudCallbackTextMaxRunes+1),
	})
	var capabilityErr *CapabilityCallError
	if !errors.As(err, &capabilityErr) || capabilityErr.Code != "CALLBACK_TEXT_TOO_LARGE" {
		t.Fatalf("oversized text error=%v", err)
	}
	textResult := "已完成，结果保存在短文本回调中。"
	notified, err := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{
		Action: "notify", CollaborationID: collaborationID, TaskID: "task-text", ActorSessionID: "$self", Outcome: "completed", CallbackType: protocolv1.CloudCallbackTypeText, Text: textResult,
	})
	if err != nil {
		t.Fatal(err)
	}
	notification := notified["notification"].(map[string]any)
	if notification["textAvailable"] != true || notification["text"] != nil {
		t.Fatalf("notify response leaked text=%#v", notification)
	}
	if _, err := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{
		Action: "notify", CollaborationID: collaborationID, TaskID: "task-status", ActorSessionID: "$self", Outcome: "completed", CallbackType: protocolv1.CloudCallbackTypeStatus, Text: "not allowed",
	}); err == nil {
		t.Fatal("status callback accepted text")
	}
	if _, err := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{
		Action: "notify", CollaborationID: collaborationID, TaskID: "task-status", ActorSessionID: "$self", Outcome: "completed", CallbackType: protocolv1.CloudCallbackTypeStatus,
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{Action: "claim", ActorSessionID: "codex-dispatcher", ClaimID: "claim-typed-callbacks", Limit: 2})
	if err != nil || claimed["claimedCount"] != 2 {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	items := claimed["claimed"].([]map[string]any)
	acks := make([]CloudCompletionAckItem, 0, len(items))
	seenText, seenStatus := false, false
	for _, item := range items {
		acks = append(acks, CloudCompletionAckItem{NotificationID: item["notificationId"].(string)})
		switch item["callbackType"] {
		case protocolv1.CloudCallbackTypeText:
			seenText = item["text"] == textResult && item["deliverablePath"] == nil
		case protocolv1.CloudCallbackTypeStatus:
			_, hasText := item["text"]
			_, hasPath := item["deliverablePath"]
			seenStatus = !hasText && !hasPath
		}
	}
	if !seenText || !seenStatus {
		t.Fatalf("typed claim=%#v", items)
	}
	if _, err := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{Action: "ack", ActorSessionID: "codex-dispatcher", ClaimID: "claim-typed-callbacks", Acknowledgements: acks}); err != nil {
		t.Fatal(err)
	}
	_, state, err := service.loadCloudCollaboration(ctx, ownerID, collaborationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range state.Tasks {
		if task.Status != "done" {
			t.Fatalf("task=%+v", task)
		}
		if task.CallbackType == protocolv1.CloudCallbackTypeText && (task.ResultText != textResult || task.ResultSHA256 == "") {
			t.Fatalf("text task=%+v", task)
		}
		if task.CallbackType == protocolv1.CloudCallbackTypeStatus && (task.ResultText != "" || task.ResultSHA256 != "") {
			t.Fatalf("status task=%+v", task)
		}
	}
}

func TestCloudCompletionNotifyActivelyQueuesCodexCallbackWithoutProviderEvent(t *testing.T) {
	service, ownerID, machineID, node := newCloudCollaborationTestService(t)
	ctx := context.Background()
	dispatched, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "dispatch", MachineID: machineID, CallbackSessionID: "codex-callback-target",
		WorkingDirectory: t.TempDir(), Prompt: "Return a short result.", IdempotencyKey: "completion-active-callback-001",
	})
	if err != nil {
		t.Fatal(err)
	}
	collaborationID := mapString(dispatched, "collaborationId")
	_, state, err := service.loadCloudCollaboration(ctx, ownerID, collaborationID)
	if err != nil {
		t.Fatal(err)
	}
	tasks := state.Tasks
	if len(tasks) != 1 || tasks[0].ChatSessionID == "" {
		t.Fatalf("dispatch tasks=%#v", tasks)
	}
	result, err := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{
		Action: "notify", CollaborationID: collaborationID, TaskID: tasks[0].ID,
		ActorSessionID: "$self", Outcome: "completed", CallbackType: protocolv1.CloudCallbackTypeText, Text: "任务已经完成。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["activeCallbackState"] != "node_queued" || result["activeCallbackAccepted"] != true || result["callbackWakePolicy"] != "node-durable-immediate-when-idle" {
		t.Fatalf("notify result=%#v", result)
	}
	var enqueue protocolv1.CapabilityRequest
	for _, call := range node.snapshotCalls() {
		if call.Action == "session.callback.enqueue" {
			enqueue = call
		}
	}
	if enqueue.Action == "" {
		t.Fatalf("completion.notify did not push the Node callback: %#v", node.snapshotCalls())
	}
	for key, want := range map[string]any{
		"sessionId": tasks[0].ChatSessionID, "callbackTargetSessionId": "codex-callback-target",
		"callbackMissionId": collaborationID, "callbackTaskId": tasks[0].ID,
		"callbackType":    protocolv1.CloudCallbackTypeText,
		"callbackOutcome": "completed", "callbackText": "任务已经完成。",
	} {
		if got := enqueue.Params[key]; got != want {
			t.Fatalf("callback enqueue %s=%#v want=%#v params=%#v", key, got, want, enqueue.Params)
		}
	}
	if generation, _ := numericInt64(enqueue.Params["callbackGeneration"]); generation != tasks[0].Generation {
		t.Fatalf("callback enqueue generation=%d want=%d params=%#v", generation, tasks[0].Generation, enqueue.Params)
	}
	if countCloudCollaborationCalls(node.snapshotCalls(), "session.watch") != 0 || countCloudCollaborationCalls(node.snapshotCalls(), "session.result") != 0 {
		t.Fatalf("primary callback path polled the Provider: %#v", node.snapshotCalls())
	}
}

func TestCloudCompletionFailedTextWithoutBodyHasNoSyntheticDigest(t *testing.T) {
	notification := store.CloudCompletionNotificationRecord{
		CallbackType: protocolv1.CloudCallbackTypeText,
		Outcome:      "failed",
	}
	ack, err := resolveCloudCompletionAcknowledgement(notification, CloudCompletionAckItem{})
	if err != nil {
		t.Fatal(err)
	}
	if ack.ResultBytes != 0 || ack.ResultSHA256 != "" || ack.ResultStatus != "failed" {
		t.Fatalf("ack=%#v", ack)
	}
}

func TestCloudCollaborationBootstrapDefinesLocalFileCallbackSlot(t *testing.T) {
	resultPath := filepath.Join(t.TempDir(), ".fast-spider-result-test.md")
	prompt := cloudCollaborationBootstrap("collab-test", "machine-test", cloudCollaborationState{
		WorkingDirectory: t.TempDir(),
	}, cloudCollaborationTask{
		ID: "task-file", Generation: 1, CallbackType: protocolv1.CloudCallbackTypeLocalFile, ResultPath: resultPath, AccessMode: "read_only",
	})
	for _, needle := range []string{"FAST_SPIDER_CLOUD_TASK_V1", resultPath, "callback slot", "writable even for a read-only task", "no permission to write anywhere else", "without uploading"} {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("bootstrap missing %q: %s", needle, prompt)
		}
	}
}

func TestCloudCompletionConcurrentNotifyBatchClaimAndAck(t *testing.T) {
	service, ownerID, machineID, _ := newCloudCollaborationTestService(t)
	ctx := context.Background()
	created, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "create", MachineID: machineID, IdempotencyKey: "completion-concurrency-001",
		ControllerSessionID: "codex-controller", DispatcherSessionID: "codex-dispatcher",
		Title: "Completion queue", Goal: "Complete eight tasks", DoneWhen: "All results verified",
		WorkingDirectory: t.TempDir(), AllowedActions: []string{"chat.create", "file.read", "file.write"},
		MaxActiveChats: 8, MaxCreates: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	collaborationID := created["collaborationId"].(string)
	current, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "lease.acquire", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(created, "revision"),
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err = service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "goal.add", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(current, "revision"),
		GoalID: "goal-completion", Title: "Verify all completion records",
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		taskID := fmt.Sprintf("task-%d", i)
		current, err = service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
			Action: "task.add", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(current, "revision"),
			TaskID: taskID, Title: "Task " + taskID, Prompt: "Write the bounded result.", AccessMode: "read_only", AllowedActions: []string{"chat.create", "file.read"},
		})
		if err != nil {
			t.Fatalf("add %s: %v", taskID, err)
		}
		current, err = service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
			Action: "task.dispatch", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(current, "revision"), TaskID: taskID,
		})
		if err != nil {
			t.Fatalf("dispatch %s: %v", taskID, err)
		}
	}
	// A long-running CHAT must still notify after the dispatcher collaboration
	// lease has expired; completion notify has its own durable transaction.
	service.now = func() time.Time { return time.Now().UTC().Add(time.Hour) }
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(taskID string) {
			defer wg.Done()
			_, notifyErr := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{
				Action: "notify", CollaborationID: collaborationID, TaskID: taskID, ActorSessionID: "$self", Outcome: "completed",
			})
			errs <- notifyErr
		}(fmt.Sprintf("task-%d", i))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent notify: %v", err)
		}
	}
	for retry := 0; retry < 10; retry++ {
		result, err := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{
			Action: "notify", CollaborationID: collaborationID, TaskID: "task-0", ActorSessionID: "$self", Outcome: "completed",
		})
		if err != nil || result["replayed"] != true {
			t.Fatalf("notify retry %d result=%#v err=%v", retry, result, err)
		}
	}
	type claimWithAcknowledgements struct {
		claimID          string
		acknowledgements []CloudCompletionAckItem
	}
	claims := make([]claimWithAcknowledgements, 0, 2)
	for _, claimID := range []string{"claim-four-a", "claim-four-b"} {
		claimed, err := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{
			Action: "claim", ActorSessionID: "codex-dispatcher", ClaimID: claimID, Limit: 4,
		})
		if err != nil || claimed["claimedCount"] != 4 {
			t.Fatalf("claim %s=%#v err=%v", claimID, claimed, err)
		}
		items := claimed["claimed"].([]map[string]any)
		acknowledgements := make([]CloudCompletionAckItem, 0, len(items))
		for _, item := range items {
			acknowledgements = append(acknowledgements, CloudCompletionAckItem{
				NotificationID: item["notificationId"].(string), ResultStatus: "ready", ResultBytes: 10,
				ResultSHA256: "sha256:" + strings.Repeat("a", 64), DeliverableStatus: "ready",
			})
		}
		claims = append(claims, claimWithAcknowledgements{claimID: claimID, acknowledgements: acknowledgements})
	}
	type ackResult struct {
		result map[string]any
		err    error
	}
	ackResults := make(chan ackResult, len(claims))
	for _, claim := range claims {
		claim := claim
		go func() {
			result, ackErr := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{
				Action: "ack", ActorSessionID: "codex-dispatcher", ClaimID: claim.claimID, Acknowledgements: claim.acknowledgements,
			})
			ackResults <- ackResult{result: result, err: ackErr}
		}()
	}
	totalAcked := 0
	for range claims {
		acked := <-ackResults
		if acked.err != nil {
			t.Fatalf("concurrent ack result=%#v err=%v", acked.result, acked.err)
		}
		totalAcked += acked.result["ackedCount"].(int)
	}
	if totalAcked != 8 {
		t.Fatalf("concurrent acknowledged count=%d", totalAcked)
	}
	replayed, err := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{
		Action: "ack", ActorSessionID: "codex-dispatcher", ClaimID: claims[0].claimID, Acknowledgements: claims[0].acknowledgements,
	})
	if err != nil || replayed["replayed"] != true {
		t.Fatalf("replayed ack=%#v err=%v", replayed, err)
	}
	_, state, err := service.loadCloudCollaboration(ctx, ownerID, collaborationID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "closing" && state.Status != "completed" {
		t.Fatalf("collaboration status=%s", state.Status)
	}
	for _, task := range state.Tasks {
		if task.Status != "done" || task.ResultSHA256 == "" {
			t.Fatalf("task=%+v", task)
		}
	}
}

func TestCloudCompletionRejectsWrongTaskActorAndConflictingOutcome(t *testing.T) {
	service, ownerID, machineID, _ := newCloudCollaborationTestService(t)
	ctx := context.Background()
	created, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "create", MachineID: machineID, IdempotencyKey: "completion-auth-001",
		ControllerSessionID: "codex-controller", DispatcherSessionID: "codex-dispatcher",
		Title: "Completion auth", Goal: "Reject impersonation", DoneWhen: "Checks pass", WorkingDirectory: t.TempDir(),
		AllowedActions: []string{"chat.create", "file.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	collaborationID := created["collaborationId"].(string)
	current, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{Action: "lease.acquire", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(created, "revision")})
	if err != nil {
		t.Fatal(err)
	}
	current, err = service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "task.add", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(current, "revision"),
		TaskID: "task-auth", Title: "Auth", Prompt: "Finish.", AccessMode: "read_only", AllowedActions: []string{"chat.create", "file.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{Action: "task.dispatch", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(current, "revision"), TaskID: "task-auth"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{Action: "notify", CollaborationID: collaborationID, TaskID: "missing", ActorSessionID: "$self", Outcome: "completed"}); err == nil {
		t.Fatal("wrong task was accepted")
	}
	if _, err := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{Action: "notify", CollaborationID: collaborationID, TaskID: "task-auth", ActorSessionID: "impostor", Outcome: "completed"}); err == nil {
		t.Fatal("impostor was accepted")
	}
	if _, err := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{Action: "notify", CollaborationID: collaborationID, TaskID: "task-auth", ActorSessionID: "$self", Outcome: "completed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{Action: "notify", CollaborationID: collaborationID, TaskID: "task-auth", ActorSessionID: "$self", Outcome: "failed"}); err == nil {
		t.Fatal("conflicting terminal outcome was accepted")
	}
}

func TestCloudCompletionAckRecoversAfterStateUpdateBeforeQueueAck(t *testing.T) {
	service, ownerID, machineID, _ := newCloudCollaborationTestService(t)
	ctx := context.Background()
	created, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "create", MachineID: machineID, IdempotencyKey: "completion-crash-recovery-001",
		ControllerSessionID: "codex-controller", DispatcherSessionID: "codex-dispatcher",
		Title: "Completion crash recovery", Goal: "Recover an interrupted acknowledgement", DoneWhen: "Task is durably acknowledged",
		WorkingDirectory: t.TempDir(), AllowedActions: []string{"chat.create", "file.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	collaborationID := created["collaborationId"].(string)
	current, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "lease.acquire", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(created, "revision"),
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err = service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "task.add", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(current, "revision"),
		TaskID: "task-crash", Title: "Crash recovery", Prompt: "Write the fixed result.", AccessMode: "read_only", AllowedActions: []string{"chat.create", "file.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "task.dispatch", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(current, "revision"), TaskID: "task-crash",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{
		Action: "notify", CollaborationID: collaborationID, TaskID: "task-crash", ActorSessionID: "$self", Outcome: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	claimedResult, err := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{
		Action: "claim", ActorSessionID: "codex-dispatcher", ClaimID: "claim-crash-recovery", Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, alreadyAcked, err := service.store.GetCloudCompletionClaim(ctx, ownerID, "codex-dispatcher", "claim-crash-recovery", service.now().UTC(), cloudCompletionClaimLease)
	if err != nil || alreadyAcked || len(claimed) != 1 {
		t.Fatalf("claimed=%#v alreadyAcked=%v err=%v", claimed, alreadyAcked, err)
	}
	ack := CloudCompletionAckItem{
		NotificationID: claimed[0].NotificationID, ResultStatus: "ready", ResultBytes: 17,
		ResultSHA256: "sha256:" + strings.Repeat("d", 64), DeliverableStatus: "ready",
	}
	if closing, err := service.applyCloudCompletionAcknowledgement(ctx, ownerID, claimed[0], ack, service.now().UTC()); err != nil || !closing {
		t.Fatalf("state-only acknowledgement closing=%v err=%v", closing, err)
	}
	stillClaimed, alreadyAcked, err := service.store.GetCloudCompletionClaim(ctx, ownerID, "codex-dispatcher", "claim-crash-recovery", service.now().UTC(), cloudCompletionClaimLease)
	if err != nil || alreadyAcked || len(stillClaimed) != 1 || stillClaimed[0].State != "claimed" {
		t.Fatalf("queue changed before durable ack: rows=%#v alreadyAcked=%v err=%v", stillClaimed, alreadyAcked, err)
	}
	acked, err := service.CloudCompletion(ctx, ownerID, CloudCompletionRequest{
		Action: "ack", ActorSessionID: "codex-dispatcher", ClaimID: "claim-crash-recovery", Acknowledgements: []CloudCompletionAckItem{ack},
	})
	if err != nil || acked["ackedCount"] != 1 || claimedResult["claimedCount"] != 1 {
		t.Fatalf("recovered ack=%#v claim=%#v err=%v", acked, claimedResult, err)
	}
	_, state, err := service.loadCloudCollaboration(ctx, ownerID, collaborationID)
	if err != nil || len(state.Tasks) != 1 || state.Tasks[0].Status != "done" {
		t.Fatalf("recovered state=%#v err=%v", state, err)
	}
}

func TestCloudCollaborationLoadBackfillsManagedResultPath(t *testing.T) {
	service, ownerID, machineID, _ := newCloudCollaborationTestService(t)
	ctx := context.Background()
	workingDirectory := t.TempDir()
	created, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "create", MachineID: machineID, IdempotencyKey: "completion-result-path-upgrade-001",
		ControllerSessionID: "codex-controller", DispatcherSessionID: "codex-dispatcher",
		Title: "Upgrade result path", Goal: "Keep an in-flight task recoverable", DoneWhen: "Stable result path is restored",
		WorkingDirectory: workingDirectory, AllowedActions: []string{"chat.create", "file.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	collaborationID := created["collaborationId"].(string)
	current, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "lease.acquire", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(created, "revision"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "task.add", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(current, "revision"),
		TaskID: "task-upgrade", Title: "Upgrade", Prompt: "Return a result.", AccessMode: "read_only", AllowedActions: []string{"chat.create", "file.read"},
	}); err != nil {
		t.Fatal(err)
	}
	rec, state, err := service.loadCloudCollaboration(ctx, ownerID, collaborationID)
	if err != nil {
		t.Fatal(err)
	}
	state.Tasks[0].ResultPath = ""
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.store.UpdateCloudCollaboration(ctx, ownerID, collaborationID, state.Status, string(raw), rec.Revision, service.now().UTC()); err != nil {
		t.Fatal(err)
	}
	_, reloaded, err := service.loadCloudCollaboration(ctx, ownerID, collaborationID)
	if err != nil {
		t.Fatal(err)
	}
	want := cloudCollaborationManagedResultPath(workingDirectory, collaborationID, "task-upgrade", reloaded.Tasks[0].Generation)
	if got := reloaded.Tasks[0].ResultPath; filepath.Clean(got) != filepath.Clean(want) || !filepath.IsAbs(got) {
		t.Fatalf("backfilled resultPath=%q want=%q", got, want)
	}
}
