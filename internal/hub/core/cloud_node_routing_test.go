package core

import (
	"context"
	"testing"
	"time"
)

func TestCloudCollaborationUsesStoredMachineForNodeCalls(t *testing.T) {
	service, ownerID, machineID, node := newCloudCollaborationTestService(t)
	ctx := context.Background()
	created, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "create", MachineID: machineID, IdempotencyKey: "cloud-node-routing-machine-001",
		ControllerSessionID: "codex-controller", DispatcherSessionID: "codex-dispatcher",
		Title: "Node routing", Goal: "Use the assigned Node", DoneWhen: "The task is delivered",
		WorkingDirectory: t.TempDir(), AllowedActions: []string{"chat.create", "file.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "lease.acquire", CollaborationID: mapString(created, "collaborationId"),
		ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(created, "revision"),
	})
	if err != nil {
		t.Fatal(err)
	}
	added, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "task.add", CollaborationID: mapString(created, "collaborationId"),
		ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(leased, "revision"),
		TaskID: "task-node-routing", Title: "Route", Prompt: "Return the result.", AccessMode: "read_only",
		AllowedActions: []string{"chat.create", "file.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// MachineID is deliberately forged in the action request. Dispatch must use
	// the collaboration record's machine binding rather than this caller field.
	if _, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "task.dispatch", CollaborationID: mapString(created, "collaborationId"),
		MachineID: "forged-machine", ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher",
		ExpectedRevision: numberField(added, "revision"), TaskID: "task-node-routing",
	}); err != nil {
		t.Fatal(err)
	}
	if countCloudCollaborationCalls(node.snapshotCalls(), "session.create") != 1 {
		t.Fatalf("stored Node did not receive session.create: %#v", node.snapshotCalls())
	}
	for _, call := range node.snapshotCalls() {
		switch call.Action {
		case "session.get", "session.result", "session.watch":
			t.Fatalf("Hub read provider session action %s: %#v", call.Action, call.Params)
		}
	}
}

func TestCloudCollaborationStatusRecoveryIsNodeOwned(t *testing.T) {
	service, ownerID, machineID, node := newCloudCollaborationTestService(t)
	ctx := context.Background()
	dispatched, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "dispatch", MachineID: machineID, CallbackSessionID: "codex-dispatcher",
		WorkingDirectory: t.TempDir(), Prompt: "Return the result.", IdempotencyKey: "cloud-node-routing-recovery-001",
	})
	if err != nil {
		t.Fatal(err)
	}
	collaborationID := mapString(dispatched, "collaborationId")
	rec, state, err := service.loadCloudCollaboration(ctx, ownerID, collaborationID)
	if err != nil {
		t.Fatal(err)
	}
	state.Chats[0].LastObservedAt = time.Now().UTC().Add(-31 * time.Minute).Format(time.RFC3339)
	saved, err := service.saveCloudCollaboration(ctx, ownerID, rec, state)
	if err != nil {
		t.Fatal(err)
	}
	node.setResponse("session.callback.recover", map[string]any{"cursor": int64(3), "recoveryQueued": true, "executionOwner": "node"})
	result, err := service.CloudCollaboration(ctx, ownerID, CloudCollaborationRequest{
		Action: "status.poll", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher",
		ExpectedRevision: numberField(saved, "revision"), TaskID: "task",
	})
	if err != nil || result["recoveryOwner"] != "node" || result["recoveryQueued"] != true {
		t.Fatalf("Node-owned recovery result=%#v err=%v", result, err)
	}
	if countCloudCollaborationCalls(node.snapshotCalls(), "session.callback.recover") != 1 {
		t.Fatalf("recovery was not routed through Node: %#v", node.snapshotCalls())
	}
	for _, call := range node.snapshotCalls() {
		switch call.Action {
		case "session.get", "session.result", "session.watch":
			t.Fatalf("Hub read provider session action %s during recovery: %#v", call.Action, call.Params)
		}
	}
}
