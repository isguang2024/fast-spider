package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestTaskResultSubmitTextIsOwnerAndGenerationBound(t *testing.T) {
	s, owner, machine, node := newCloudCollaborationTestService(t)
	ctx := context.Background()
	r, err := s.CloudCollaboration(ctx, owner, CloudCollaborationRequest{Action: "dispatch", MachineID: machine, CallbackSessionID: "codex-target", WorkingDirectory: t.TempDir(), Prompt: "Return a short result", IdempotencyKey: "task-result-text-0001", AccessMode: "read_only"})
	if err != nil {
		t.Fatal(err)
	}
	ref := r["taskRef"].(string)
	for _, bad := range []string{"", "task_!", cloudTaskResultReference(r["collaborationId"].(string), cloudCollaborationTask{ID: "task", Generation: 2})} {
		if _, err := s.SubmitTaskResult(ctx, owner, bad, "completed", "OK"); err == nil {
			t.Fatalf("invalid/stale ref accepted: %s", bad)
		}
	}
	if _, err := s.SubmitTaskResult(ctx, "another-owner", ref, "completed", "OK"); err == nil {
		t.Fatal("another owner accepted")
	}
	if _, err := s.SubmitTaskResult(ctx, owner, ref, "completed", strings.Repeat("界", 2001)); err == nil {
		t.Fatal("oversized text accepted")
	}
	for i := 0; i < 2; i++ {
		out, err := s.SubmitTaskResult(ctx, owner, ref, "completed", "OK")
		if err != nil || out["accepted"] != true || out["replayed"] != (i == 1) {
			t.Fatalf("submit %d=%v err=%v", i, out, err)
		}
	}
	for _, call := range node.snapshotCalls() {
		if call.Action == "session.callback.enqueue" && call.Params["callbackTargetSessionId"] != "codex-target" {
			t.Fatalf("destination changed: %v", call.Params)
		}
	}
	claim, err := s.CloudCompletion(ctx, owner, CloudCompletionRequest{Action: "claim", ActorSessionID: "codex-target"})
	if err != nil || claim["claimedCount"] != 1 {
		t.Fatalf("claim=%v err=%v", claim, err)
	}
}

func TestTaskResultSubmitRepairsMissingRouteBeforeReturning(t *testing.T) {
	s, owner, machine, node := newCloudCollaborationTestService(t)
	ctx := context.Background()
	req := CloudCollaborationRequest{Action: "dispatch", MachineID: machine, CallbackSessionID: "codex-restarted-target", WorkingDirectory: t.TempDir(), Prompt: "Return the result", IdempotencyKey: "task-result-route-repair-001", AccessMode: "read_only"}
	dispatched, err := s.CloudCollaboration(ctx, owner, req)
	if err != nil {
		t.Fatal(err)
	}
	registerBefore := countCloudCollaborationCalls(node.snapshotCalls(), "session.callback.register")
	armBefore := countCloudCollaborationCalls(node.snapshotCalls(), "session.callback.arm")
	node.setErrorSequence("session.callback.enqueue", &protocolv1.ProtocolError{Code: "CALLBACK_ROUTE_NOT_FOUND", Message: "route lost after restart", Retryable: true}, nil)
	out, err := s.SubmitTaskResult(ctx, owner, dispatched["taskRef"].(string), "completed", "durable result")
	if err != nil || out["accepted"] != true || out["notificationAccepted"] != true {
		t.Fatalf("submit=%#v err=%v", out, err)
	}
	calls := node.snapshotCalls()
	if countCloudCollaborationCalls(calls, "session.callback.enqueue") != 2 || countCloudCollaborationCalls(calls, "session.callback.register") != registerBefore+1 || countCloudCollaborationCalls(calls, "session.callback.arm") != armBefore+1 {
		t.Fatalf("route was not repaired before deterministic replay: %#v", calls)
	}
	claim, err := s.CloudCompletion(ctx, owner, CloudCompletionRequest{Action: "claim", ActorSessionID: "codex-restarted-target"})
	if err != nil || claim["claimedCount"] != 1 || claim["claimed"].([]map[string]any)[0]["text"] != "durable result" {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
}

func TestDispatchReplayRestoresRouteAndRequeuesDurableSubmission(t *testing.T) {
	s, owner, machine, node := newCloudCollaborationTestService(t)
	ctx := context.Background()
	req := CloudCollaborationRequest{Action: "dispatch", MachineID: machine, CallbackSessionID: "codex-replay-target", WorkingDirectory: t.TempDir(), Prompt: "Return the result", IdempotencyKey: "task-result-durable-replay-001", AccessMode: "read_only"}
	dispatched, err := s.CloudCollaboration(ctx, owner, req)
	if err != nil {
		t.Fatal(err)
	}
	node.setError("session.callback.enqueue", &protocolv1.ProtocolError{Code: "CALLBACK_ROUTE_NOT_FOUND", Message: "route missing", Retryable: true})
	node.setError("session.callback.register", &protocolv1.ProtocolError{Code: "RESOURCE_LIMIT", Message: "legacy route capacity full"})
	_, err = s.SubmitTaskResult(ctx, owner, dispatched["taskRef"].(string), "completed", "stored once")
	var capabilityErr *CapabilityCallError
	if !errors.As(err, &capabilityErr) || capabilityErr.Code != "CALLBACK_DELIVERY_PENDING" {
		t.Fatalf("submit err=%v", err)
	}
	node.setError("session.callback.enqueue", nil)
	node.setError("session.callback.register", nil)
	replayed, err := s.CloudCollaboration(ctx, owner, req)
	if err != nil || replayed["replayed"] != true {
		t.Fatalf("dispatch replay=%#v err=%v", replayed, err)
	}
	claim, err := s.CloudCompletion(ctx, owner, CloudCompletionRequest{Action: "claim", ActorSessionID: "codex-replay-target"})
	if err != nil || claim["claimedCount"] != 1 {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	item := claim["claimed"].([]map[string]any)[0]
	if item["text"] != "stored once" || item["recoveryOnly"] != false {
		t.Fatalf("durable formal result=%#v", item)
	}
	second, err := s.SubmitTaskResult(ctx, owner, dispatched["taskRef"].(string), "completed", "stored once")
	if err != nil || second["replayed"] != true {
		t.Fatalf("duplicate submit=%#v err=%v", second, err)
	}
}

func TestTaskResultSubmitLocalFileStaysOnNode(t *testing.T) {
	s, owner, machine, node := newCloudCollaborationTestService(t)
	ctx := context.Background()
	root := t.TempDir()
	output := filepath.Join(root, "summary.md")
	r, err := s.CloudCollaboration(ctx, owner, CloudCollaborationRequest{Action: "dispatch", MachineID: machine, CallbackSessionID: "codex-file-target", WorkingDirectory: root, Prompt: "Write a long local report", IdempotencyKey: "task-result-file-0001", CallbackType: protocolv1.CloudCallbackTypeLocalFile, DeliverablePath: output})
	if err != nil {
		t.Fatal(err)
	}
	if r["resultPath"] != output || r["resultType"] != protocolv1.CloudCallbackTypeLocalFile {
		t.Fatalf("receipt=%v", r)
	}
	ref := r["taskRef"].(string)
	node.mu.Lock()
	node.errors = map[string]*protocolv1.ProtocolError{"session.callback.prepare": {Code: "NOT_FOUND", Message: "missing"}}
	node.mu.Unlock()
	if _, err := s.SubmitTaskResult(ctx, owner, ref, "completed", ""); err == nil {
		t.Fatal("missing file reported complete")
	}
	claim, err := s.CloudCompletion(ctx, owner, CloudCompletionRequest{Action: "claim", ActorSessionID: "codex-file-target"})
	if err != nil || claim["claimedCount"] != 0 {
		t.Fatalf("missing file persisted completion: %v %v", claim, err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	node.mu.Lock()
	node.errors = nil
	node.responses = map[string]map[string]any{"session.callback.prepare": {"size": int64(9000), "fileSha256": digest}}
	node.mu.Unlock()
	if _, err := s.SubmitTaskResult(ctx, owner, ref, "completed", "do not upload this body"); err == nil {
		t.Fatal("file body accepted by result submission")
	}
	out, err := s.SubmitTaskResult(ctx, owner, ref, "completed", "")
	if err != nil {
		t.Fatal(err)
	}
	file := out["localFile"].(map[string]any)
	if file["path"] != output || file["sha256"] != digest || file["bytes"] != int64(9000) {
		t.Fatalf("file=%v", file)
	}
	for _, call := range node.snapshotCalls() {
		if call.Action == "session.callback.prepare" && call.Params["mode"] == "result" {
			generation, _ := numericInt64(call.Params["callbackGeneration"])
			if call.Params["mode"] != "result" || call.Params["sessionId"] != r["chatSessionId"] || call.Params["callbackTargetSessionId"] != "codex-file-target" || call.Params["callbackMissionId"] != r["collaborationId"] || call.Params["callbackTaskId"] != "task" || generation != 1 {
				t.Fatalf("result metadata route was not Node-owned: %v", call.Params)
			}
			if _, hasPath := call.Params["path"]; hasPath {
				t.Fatalf("Hub supplied a caller-selected path to result verification: %v", call.Params)
			}
		}
	}
	claim, err = s.CloudCompletion(ctx, owner, CloudCompletionRequest{Action: "claim", ActorSessionID: "codex-file-target"})
	if err != nil || claim["claimedCount"] != 1 {
		t.Fatalf("claim=%v err=%v", claim, err)
	}
	item := claim["claimed"].([]map[string]any)[0]
	if item["deliverablePath"] != output || item["text"] != nil {
		t.Fatalf("notification=%v", item)
	}
	_, err = s.CloudCompletion(ctx, owner, CloudCompletionRequest{Action: "ack", ActorSessionID: "codex-file-target", ClaimID: claim["claimId"].(string), Acknowledgements: []CloudCompletionAckItem{{NotificationID: item["notificationId"].(string), ResultStatus: "ready", ResultBytes: 9000, ResultSHA256: digest, DeliverableStatus: "ready"}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTaskResultSubmitRejectsInvalidFileMetadata(t *testing.T) {
	s, owner, machine, node := newCloudCollaborationTestService(t)
	ctx := context.Background()
	r, err := s.CloudCollaboration(ctx, owner, CloudCollaborationRequest{Action: "dispatch", MachineID: machine, CallbackSessionID: "codex-file-target", WorkingDirectory: t.TempDir(), Prompt: "Write report", IdempotencyKey: "task-result-file-invalid", CallbackType: protocolv1.CloudCallbackTypeLocalFile, AccessMode: "read_only"})
	if err != nil {
		t.Fatal(err)
	}
	for _, metadata := range []map[string]any{{}, {"size": int64(256<<20) + 1, "fileSha256": "sha256:" + strings.Repeat("a", 64)}, {"size": int64(10), "fileSha256": "invalid"}} {
		node.mu.Lock()
		node.responses = map[string]map[string]any{"session.callback.prepare": metadata}
		node.mu.Unlock()
		_, err := s.SubmitTaskResult(ctx, owner, r["taskRef"].(string), "completed", "")
		var ce *CapabilityCallError
		if !errors.As(err, &ce) || ce.Code != "TASK_RESULT_FILE_INVALID" {
			t.Fatalf("metadata=%v err=%v", metadata, err)
		}
	}
}
