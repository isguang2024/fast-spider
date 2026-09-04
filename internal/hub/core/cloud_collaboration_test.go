package core

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/store"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/isguang2024/fast-spider/internal/security"
)

type cloudCollaborationTestNode struct {
	mu        sync.Mutex
	calls     []protocolv1.CapabilityRequest
	onCall    func(protocolv1.CapabilityRequest)
	responses map[string]map[string]any
	creates   int
}

func (n *cloudCollaborationTestNode) respond(req protocolv1.CapabilityRequest) map[string]any {
	n.mu.Lock()
	n.calls = append(n.calls, req)
	hook := n.onCall
	n.onCall = nil
	response := n.responses[req.Action]
	n.mu.Unlock()
	if hook != nil {
		hook(req)
	}
	if response != nil {
		return response
	}
	switch req.Action {
	case "provider.readiness":
		return map[string]any{"ready": true, "chatgptCloudAvailable": true, "reasonCode": "READY"}
	case "session.get":
		return map[string]any{"session": map[string]any{"sessionId": req.Params["sessionId"], "providerId": "codex", "backend": "codex_local"}}
	case "session.create":
		n.mu.Lock()
		n.creates++
		created := n.creates
		n.mu.Unlock()
		return map[string]any{"sessionId": fmt.Sprintf("cloud-chat-%d", created), "backend": "chatgpt_cloud", "visibility": "visible", "externalIdType": "chatgpt_conversation"}
	case "session.callback.register":
		return map[string]any{"registered": true}
	case "session.watch":
		return map[string]any{"nextCursor": int64(1)}
	case "session.archive":
		return map[string]any{"archived": true}
	default:
		return map[string]any{}
	}
}

func (n *cloudCollaborationTestNode) setOneShotHook(hook func(protocolv1.CapabilityRequest)) {
	n.mu.Lock()
	n.onCall = hook
	n.mu.Unlock()
}

func (n *cloudCollaborationTestNode) setResponse(action string, response map[string]any) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.responses == nil {
		n.responses = map[string]map[string]any{}
	}
	n.responses[action] = response
}

func (n *cloudCollaborationTestNode) snapshotCalls() []protocolv1.CapabilityRequest {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]protocolv1.CapabilityRequest(nil), n.calls...)
}

func newCloudCollaborationTestService(t *testing.T) (*Service, string, string, *cloudCollaborationTestNode) {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	service, err := New(st, reg, Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	bootstrap, err := service.EnsureBootstrap(ctx)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	account, err := service.BootstrapAccount(ctx, bootstrap, "cloud-collab-owner", "Owner", "correct horse battery staple", "127.0.0.1")
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	machine, err := service.RegisterMachine(ctx, account.OwnerID, MachineRegistrationRequest{DisplayName: "cloud-collab-node", OS: "windows", Arch: "amd64", NodeVersion: "test", PublicKey: security.EncodePublicKey(publicKey)}, "127.0.0.1")
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	testNode := &cloudCollaborationTestNode{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, acceptErr := websocket.Accept(w, r, nil)
		if acceptErr != nil {
			t.Errorf("accept test node: %v", acceptErr)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "test complete")
		for {
			var req protocolv1.CapabilityRequest
			if readErr := wsjson.Read(ctx, conn, &req); readErr != nil {
				return
			}
			response := protocolv1.CapabilityResponse{MessageType: protocolv1.MessageCapabilityResponse, RequestId: req.RequestId, TraceId: req.TraceId, Result: testNode.respond(req), Timestamp: protocolv1.Timestamp(time.Now())}
			if writeErr := wsjson.Write(ctx, conn, response); writeErr != nil {
				return
			}
		}
	}))
	wsConn, _, err := websocket.Dial(ctx, server.URL, nil)
	if err != nil {
		server.Close()
		st.Close()
		t.Fatal(err)
	}
	connection := registry.NewConnection(machine.MachineID, "conn-cloud-collab", 1, time.Now(), wsConn)
	connection.Capabilities = append([]protocolv1.CapabilityDescriptor(nil), protocolv1.NodeCapabilities...)
	if _, accepted := reg.Register(connection); !accepted {
		wsConn.Close(websocket.StatusNormalClosure, "not accepted")
		server.Close()
		st.Close()
		t.Fatal("test node connection not accepted")
	}
	go func() {
		for {
			var response protocolv1.CapabilityResponse
			if readErr := wsjson.Read(ctx, wsConn, &response); readErr != nil {
				return
			}
			connection.DeliverResponse(response)
		}
	}()
	t.Cleanup(func() {
		reg.Remove(machine.MachineID, 1)
		_ = wsConn.Close(websocket.StatusNormalClosure, "test complete")
		server.Close()
		_ = st.Close()
	})
	return service, account.OwnerID, machine.MachineID, testNode
}

func TestCodexCloudCollaborationRequiresLocalCodexAndUsesDeliverableCallback(t *testing.T) {
	service, ownerID, machineID, node := newCloudCollaborationTestService(t)
	root := filepath.Join(t.TempDir(), "project")
	output := filepath.Join(root, "outputs", "report.md")
	createReq := CloudCollaborationRequest{
		Action: "create", MachineID: machineID, IdempotencyKey: "codex-collab-create-001", ControllerSessionID: "codex-controller", DispatcherSessionID: "codex-dispatcher",
		Title: "Research", Goal: "Produce a report", DoneWhen: "Report exists", WorkingDirectory: root, AllowedActions: []string{"chat.create", "file.read", "file.write"},
	}
	created, err := service.CloudCollaboration(context.Background(), ownerID, createReq)
	if err != nil || numberField(created, "revision") != 1 {
		t.Fatalf("create=%#v err=%v", created, err)
	}
	initialCallCount := len(node.snapshotCalls())
	for _, call := range node.snapshotCalls() {
		if call.Action == "session.get" && call.Params["metadataOnly"] != true {
			t.Fatalf("local Codex validation loaded turn history: %#v", call.Params)
		}
	}
	if repeated, err := service.CloudCollaboration(context.Background(), ownerID, createReq); err != nil || repeated["collaborationId"] != created["collaborationId"] || len(node.snapshotCalls()) != initialCallCount {
		t.Fatalf("idempotent replay=%#v err=%v calls=%d/%d", repeated, err, len(node.snapshotCalls()), initialCallCount)
	}
	conflicting := createReq
	conflicting.DispatcherSessionID = "different-dispatcher"
	if _, err := service.CloudCollaboration(context.Background(), ownerID, conflicting); !errors.Is(err, store.ErrConflict) || len(node.snapshotCalls()) != initialCallCount {
		t.Fatalf("conflicting replay err=%v calls=%d/%d", err, len(node.snapshotCalls()), initialCallCount)
	}
	collaborationID, _ := created["collaborationId"].(string)
	controllerView, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "get", CollaborationID: collaborationID, ActorSessionID: "codex-controller", ActorRole: "controller"})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"machineId", "workingDirectory", "tasks", "chats", "events", "controllerSessionId", "dispatcherSessionId"} {
		if _, exists := controllerView[forbidden]; exists {
			t.Fatalf("controller view leaked %s: %#v", forbidden, controllerView)
		}
	}
	leased, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "lease.acquire", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	added, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "task.add", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(leased, "revision"),
		TaskID: "task-report", Title: "Write report", Prompt: "Research and write the final report.", AccessMode: "write", WriteScope: filepath.Join(root, "outputs"), DeliverablePath: output, AllowedActions: []string{"chat.create", "file.read", "file.write"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var concurrentMutationErr error
	node.setOneShotHook(func(call protocolv1.CapabilityRequest) {
		if call.Action != "provider.readiness" {
			return
		}
		node.setOneShotHook(func(call protocolv1.CapabilityRequest) {
			if call.Action != "session.create" {
				return
			}
			_, concurrentMutationErr = service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "goal.add", CollaborationID: collaborationID, ActorSessionID: "codex-controller", ActorRole: "controller", ExpectedRevision: numberField(added, "revision") + 1, GoalID: "goal-concurrent", Title: "Preserve concurrent controller mutation"})
		})
	})
	dispatched, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "task.dispatch", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(added, "revision"), TaskID: "task-report"})
	if err != nil || concurrentMutationErr != nil {
		t.Fatalf("dispatch err=%v concurrent mutation err=%v", err, concurrentMutationErr)
	}
	if numberField(dispatched, "revision") < 6 {
		t.Fatalf("dispatch=%#v", dispatched)
	}
	var createPrompt string
	for _, call := range node.snapshotCalls() {
		if call.Action == "session.create" {
			createPrompt, _ = call.Params["prompt"].(string)
			if call.Params["mode"] != "quick_chat" {
				t.Fatalf("new CHAT was not dispatched in quick mode: %#v", call.Params)
			}
		}
	}
	if !strings.Contains(createPrompt, output) || !strings.Contains(createPrompt, "codex_cloud_completion") || !strings.Contains(createPrompt, "action=notify") || strings.Contains(createPrompt, "action=event.ingest") || !strings.Contains(createPrompt, "plan.init") || !strings.Contains(createPrompt, "initializeMarkdown=true") {
		t.Fatalf("prompt=%q", createPrompt)
	}
	if countCloudCollaborationCalls(node.snapshotCalls(), "session.list") != 0 {
		t.Fatalf("dispatch guessed an old CHAT through session.list: %#v", node.snapshotCalls())
	}
	goalCounts, _ := dispatched["goalCounts"].(map[string]int)
	if goalCounts["queued"] != 1 {
		t.Fatalf("dispatch lost concurrent goal: %#v", dispatched)
	}
	dispatchedChats, _ := dispatched["chats"].([]cloudCollaborationChat)
	if len(dispatchedChats) != 1 || !dispatchedChats[0].CallbackRegistered {
		t.Fatalf("dispatch did not persist callback registration: %#v", dispatched)
	}
	rec, state, err := service.loadCloudCollaboration(context.Background(), ownerID, collaborationID)
	if err != nil {
		t.Fatal(err)
	}
	state.Chats[0].CallbackRegistered = false
	retryBase, err := service.saveCloudCollaboration(context.Background(), ownerID, rec, state)
	if err != nil {
		t.Fatal(err)
	}
	tickBeforeRetry, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "tick", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher"})
	if err != nil || !strings.Contains(strings.Join(mapActionTypes(tickBeforeRetry["actions"]), ","), "ensure_callback") {
		t.Fatalf("tick before callback retry=%#v err=%v", tickBeforeRetry, err)
	}
	createCallsBeforeRetry := countCloudCollaborationCalls(node.snapshotCalls(), "session.create")
	dispatched, err = service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "task.dispatch", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(retryBase, "revision"), TaskID: "task-report"})
	if err != nil || countCloudCollaborationCalls(node.snapshotCalls(), "session.create") != createCallsBeforeRetry {
		t.Fatalf("callback retry dispatched=%#v err=%v", dispatched, err)
	}
	if _, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "event.ingest", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(dispatched, "revision"),
		TaskID: "task-report", EventID: "event-report-wrong-path", EventSequence: 1, EventGeneration: 1, EventType: "conversation.turn.complete", ResultStatus: "ready", ResultBytes: 123, ResultSHA256: "sha256:" + strings.Repeat("a", 64), DeliverablePath: filepath.Join(root, "outputs", "other.md"), DeliverableStatus: "ready",
	}); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatalf("mismatched deliverable path error=%v", err)
	}
	ingested, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "event.ingest", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(dispatched, "revision"),
		TaskID: "task-report", EventID: "event-report-1", EventSequence: 1, EventGeneration: 1, EventType: "conversation.turn.complete", ResultStatus: "ready", ResultBytes: 123, ResultSHA256: "sha256:" + strings.Repeat("a", 64), DeliverablePath: output, DeliverableStatus: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	tasks, _ := ingested["tasks"].([]cloudCollaborationTask)
	if len(tasks) != 1 || tasks[0].Status != "result_available" || tasks[0].DeliverablePath != output {
		t.Fatalf("tasks=%#v", ingested["tasks"])
	}
	if _, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "task.update", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(ingested, "revision"), TaskID: "task-report", TaskStatus: "done"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("unacknowledged result completed task error=%v", err)
	}
	if _, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "close", CollaborationID: collaborationID, ActorSessionID: "codex-controller", ActorRole: "controller", ExpectedRevision: numberField(ingested, "revision")}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("unfinished collaboration close error=%v", err)
	}
	if _, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "event.ack", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(ingested, "revision"), EventID: "event-report-1", ResultSHA256: "sha256:" + strings.Repeat("b", 64)}); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatalf("wrong verification hash error=%v", err)
	}
	acked, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "event.ack", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(ingested, "revision"), EventID: "event-report-1", ResultSHA256: "sha256:" + strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if acked["status"] != "completed" {
		t.Fatalf("ack did not close collaboration: %#v", acked)
	}
	tasks, _ = acked["tasks"].([]cloudCollaborationTask)
	goals, _ := acked["goalCounts"].(map[string]int)
	if len(tasks) != 1 || tasks[0].Status != "done" || goals["done"] != 1 {
		t.Fatalf("automatic terminal state tasks=%#v goals=%#v", tasks, goals)
	}
	if countCloudCollaborationCalls(node.snapshotCalls(), "session.archive") != 1 {
		t.Fatalf("ack did not archive Cloud CHAT: %#v", node.snapshotCalls())
	}
}

func TestCodexCloudCollaborationPollRecoversMissedCallbackAndCloses(t *testing.T) {
	service, ownerID, machineID, node := newCloudCollaborationTestService(t)
	created, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "create", MachineID: machineID, IdempotencyKey: "codex-collab-poll-recovery-001", ControllerSessionID: "codex-controller", DispatcherSessionID: "codex-dispatcher",
		Title: "Recover", Goal: "Recover callback result", DoneWhen: "Collaboration closes", WorkingDirectory: t.TempDir(), AllowedActions: []string{"chat.create", "file.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	collaborationID, _ := created["collaborationId"].(string)
	leased, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "lease.acquire", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(created, "revision")})
	if err != nil {
		t.Fatal(err)
	}
	goalAdded, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "goal.add", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(leased, "revision"), GoalID: "goal-recovery", Title: "Recover callback result"})
	if err != nil {
		t.Fatal(err)
	}
	taskAdded, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "task.add", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(goalAdded, "revision"),
		TaskID: "task-recovery", Title: "Recover", Prompt: "Return the recovery result.", AccessMode: "read_only", AllowedActions: []string{"chat.create", "file.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatched, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "task.dispatch", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(taskAdded, "revision"), TaskID: "task-recovery"})
	if err != nil {
		t.Fatal(err)
	}
	rec, state, err := service.loadCloudCollaboration(context.Background(), ownerID, collaborationID)
	if err != nil {
		t.Fatal(err)
	}
	state.Chats[0].CallbackRegistered = false
	missed, err := service.saveCloudCollaboration(context.Background(), ownerID, rec, state)
	if err != nil {
		t.Fatal(err)
	}
	node.setResponse("session.watch", map[string]any{
		"cursor": int64(7),
		"events": []any{map[string]any{"sequence": int64(7), "type": "conversation.turn.complete", "eventKey": "provider_evt_recovery", "sessionId": "cloud-chat-1"}},
	})
	recovered, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "status.poll", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(missed, "revision"), TaskID: "task-recovery"})
	if err != nil || recovered["status"] != "active" {
		t.Fatalf("recovered=%#v err=%v dispatched=%#v", recovered, err, dispatched)
	}
	tasks, _ := recovered["tasks"].([]cloudCollaborationTask)
	if len(tasks) != 1 || tasks[0].Status != "active" || recovered["completionNotification"] == nil {
		t.Fatalf("recovered=%#v tasks=%#v", recovered, tasks)
	}
	if countCloudCollaborationCalls(node.snapshotCalls(), "session.result") != 0 {
		t.Fatalf("status poll used the heavy result path: %#v", node.snapshotCalls())
	}
	claim, err := service.CloudCompletion(context.Background(), ownerID, CloudCompletionRequest{Action: "claim", ActorSessionID: "codex-dispatcher", ClaimID: "claim-recovery"})
	if err != nil || claim["claimedCount"] != 1 {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	claimed := claim["claimed"].([]map[string]any)
	acked, err := service.CloudCompletion(context.Background(), ownerID, CloudCompletionRequest{
		Action: "ack", ActorSessionID: "codex-dispatcher", ClaimID: "claim-recovery",
		Acknowledgements: []CloudCompletionAckItem{{NotificationID: claimed[0]["notificationId"].(string), ResultStatus: "ready", ResultBytes: 19, ResultSHA256: "sha256:" + strings.Repeat("c", 64), DeliverableStatus: "ready"}},
	})
	if err != nil || acked["ackedCount"] != 1 {
		t.Fatalf("ack=%#v err=%v", acked, err)
	}
}

func TestCodexCloudCollaborationPollRecoversCompletedDeliverableWithoutRealtimeEvent(t *testing.T) {
	service, ownerID, machineID, node := newCloudCollaborationTestService(t)
	created, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "create", MachineID: machineID, IdempotencyKey: "codex-collab-file-recovery-001", ControllerSessionID: "codex-controller", DispatcherSessionID: "codex-dispatcher",
		Title: "Recover file", Goal: "Recover a completed result file", DoneWhen: "Completion is queued", WorkingDirectory: t.TempDir(), AllowedActions: []string{"chat.create", "file.read"}, StallMinutes: 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	collaborationID := created["collaborationId"].(string)
	leased, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "lease.acquire", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(created, "revision")})
	if err != nil {
		t.Fatal(err)
	}
	added, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "task.add", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(leased, "revision"),
		TaskID: "task-file-recovery", Title: "Recover", Prompt: "Write the fixed result.", AccessMode: "read_only", AllowedActions: []string{"chat.create", "file.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "task.dispatch", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(added, "revision"), TaskID: "task-file-recovery"}); err != nil {
		t.Fatal(err)
	}
	rec, state, err := service.loadCloudCollaboration(context.Background(), ownerID, collaborationID)
	if err != nil {
		t.Fatal(err)
	}
	state.Chats[0].CallbackRegistered = false
	state.Chats[0].StalledNotified = true
	state.Chats[0].LastProgressAt = service.now().UTC().Add(-16 * time.Minute).Format(time.RFC3339)
	saved, err := service.saveCloudCollaboration(context.Background(), ownerID, rec, state)
	if err != nil {
		t.Fatal(err)
	}
	node.setResponse("session.watch", map[string]any{"cursor": int64(0), "events": []any{}})
	node.setResponse("session.result", map[string]any{
		"status": "completed", "resultStatus": "ready", "resultBytes": int64(17), "resultSHA256": "sha256:" + strings.Repeat("e", 64),
		"deliverablePath": state.Tasks[0].ResultPath, "deliverableStatus": "ready",
	})
	recovered, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "status.poll", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(saved, "revision"), TaskID: "task-file-recovery"})
	if err != nil || recovered["completionNotification"] == nil {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
	chats := recovered["chats"].([]cloudCollaborationChat)
	if len(chats) != 1 || !chats[0].CallbackRegistered || countCloudCollaborationCalls(node.snapshotCalls(), "session.result") != 1 {
		t.Fatalf("recovered=%#v calls=%#v", recovered, node.snapshotCalls())
	}
}

func TestCodexCloudCollaborationContinuesStalledChatOnceWithoutReplacement(t *testing.T) {
	service, ownerID, machineID, node := newCloudCollaborationTestService(t)
	created, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "create", MachineID: machineID, IdempotencyKey: "codex-collab-continue-001", ControllerSessionID: "codex-controller", DispatcherSessionID: "codex-dispatcher",
		Title: "Continue", Goal: "Recover a stalled chat", DoneWhen: "The chat resumes", WorkingDirectory: t.TempDir(), AllowedActions: []string{"chat.create", "file.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	collaborationID, _ := created["collaborationId"].(string)
	leased, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "lease.acquire", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(created, "revision")})
	if err != nil {
		t.Fatal(err)
	}
	added, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "task.add", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(leased, "revision"),
		TaskID: "task-continue", Title: "Continue", Prompt: "Complete the bounded task.", AccessMode: "read_only", AllowedActions: []string{"chat.create", "file.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "task.dispatch", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(added, "revision"), TaskID: "task-continue"})
	if err != nil {
		t.Fatal(err)
	}
	rec, state, err := service.loadCloudCollaboration(context.Background(), ownerID, collaborationID)
	if err != nil {
		t.Fatal(err)
	}
	state.Chats[0].StalledNotified = true
	state.Chats[0].QuietChecks = 2
	saved, err := service.saveCloudCollaboration(context.Background(), ownerID, rec, state)
	if err != nil {
		t.Fatal(err)
	}

	node.setResponse("session.get", map[string]any{"session": map[string]any{"sessionId": "cloud-chat-1", "providerId": "codex", "backend": "chatgpt_cloud", "status": "completed"}})
	completed, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "chat.continue", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(saved, "revision"), TaskID: "task-continue",
	})
	if err != nil || completed["continueSent"] != false || completed["recoveryAction"] != "status_poll" {
		t.Fatalf("completed chat recovery=%#v err=%v", completed, err)
	}
	if countCloudCollaborationCalls(node.snapshotCalls(), "session.send") != 0 {
		t.Fatalf("completed chat received a continue send: %#v", node.snapshotCalls())
	}

	node.setResponse("session.get", map[string]any{"session": map[string]any{"sessionId": "cloud-chat-1", "providerId": "codex", "backend": "chatgpt_cloud", "status": "unknown"}})
	continued, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "chat.continue", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(saved, "revision"), TaskID: "task-continue",
	})
	if err != nil || continued["continueSent"] != true || continued["continuePrompt"] != "请继续" || continued["automaticReplacement"] != false || continued["recordIssuesTo"] != cloudCollaborationIssueMarkdownPath {
		t.Fatalf("continue recovery=%#v err=%v", continued, err)
	}
	if countCloudCollaborationCalls(node.snapshotCalls(), "session.send") != 1 || countCloudCollaborationCalls(node.snapshotCalls(), "session.create") != 1 {
		t.Fatalf("continue calls=%#v", node.snapshotCalls())
	}
	var sendPrompt string
	for _, call := range node.snapshotCalls() {
		if call.Action == "session.send" {
			sendPrompt, _ = call.Params["prompt"].(string)
		}
	}
	if sendPrompt != "请继续" {
		t.Fatalf("continue prompt=%q", sendPrompt)
	}
	repeated, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "chat.continue", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(continued, "revision"), TaskID: "task-continue",
	})
	if err != nil || repeated["continueSent"] != false || repeated["recoveryAction"] != "controller_decision" || repeated["automaticReplacement"] != false {
		t.Fatalf("repeated continue recovery=%#v err=%v", repeated, err)
	}
	if countCloudCollaborationCalls(node.snapshotCalls(), "session.send") != 1 {
		t.Fatalf("repeated recovery sent another continue: %#v", node.snapshotCalls())
	}

	tick, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "tick", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher"})
	if err != nil {
		t.Fatal(err)
	}
	actions := strings.Join(mapActionTypes(tick["actions"]), ",")
	if !strings.Contains(actions, "chat_recovery_decision") || strings.Contains(actions, "continue_chat") {
		t.Fatalf("post-continue actions=%s %#v", actions, tick)
	}
}

func TestCodexCloudCollaborationReusesOnlyExplicitChatAndReleasesIt(t *testing.T) {
	service, ownerID, machineID, node := newCloudCollaborationTestService(t)
	created, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "create", MachineID: machineID, IdempotencyKey: "codex-collab-explicit-chat-001", ControllerSessionID: "codex-controller", DispatcherSessionID: "codex-dispatcher",
		Title: "Reuse", Goal: "Reuse only the selected CHAT", DoneWhen: "Task is complete", WorkingDirectory: t.TempDir(), AllowedActions: []string{"chat.create", "chat.send", "file.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	collaborationID := created["collaborationId"].(string)
	leased, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "lease.acquire", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(created, "revision")})
	if err != nil {
		t.Fatal(err)
	}
	added, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "task.add", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(leased, "revision"),
		TaskID: "task-reuse", Title: "Reuse selected CHAT", Prompt: "Complete this isolated task.", AccessMode: "read_only", TargetSessionID: "shared-cloud-chat", AllowedActions: []string{"chat.send", "file.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	node.setResponse("session.get", map[string]any{"session": map[string]any{
		"sessionId": "shared-cloud-chat", "providerId": "codex", "backend": "chatgpt_cloud", "visibility": "visible", "externalIdType": "chatgpt_conversation", "status": "completed", "createdBy": "another-codex-session",
	}})
	node.setResponse("session.callback.list", map[string]any{"callbacks": []any{}})
	dispatched, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "task.dispatch", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(added, "revision"), TaskID: "task-reuse",
	})
	if err != nil {
		t.Fatal(err)
	}
	if numberField(dispatched, "createCount") != 0 || countCloudCollaborationCalls(node.snapshotCalls(), "session.create") != 0 || countCloudCollaborationCalls(node.snapshotCalls(), "session.list") != 0 || countCloudCollaborationCalls(node.snapshotCalls(), "session.send") != 1 {
		t.Fatalf("explicit reuse calls=%#v state=%#v", node.snapshotCalls(), dispatched)
	}
	tasks := dispatched["tasks"].([]cloudCollaborationTask)
	chats := dispatched["chats"].([]cloudCollaborationChat)
	if len(tasks) != 1 || tasks[0].TargetSessionID != "shared-cloud-chat" || tasks[0].SessionMode != "reuse" || tasks[0].DeliveryStatus != "delivered" || len(chats) != 1 || !chats[0].Reused || !chats[0].CallbackRegistered {
		t.Fatalf("reused state tasks=%#v chats=%#v", tasks, chats)
	}
	registerIndex, sendIndex := -1, -1
	for i, call := range node.snapshotCalls() {
		if call.Action == "session.callback.register" {
			registerIndex = i
		}
		if call.Action == "session.send" {
			sendIndex = i
			if call.Params["sessionId"] != "shared-cloud-chat" || call.Params["mode"] != "quick_chat" {
				t.Fatalf("explicit CHAT send params=%#v", call.Params)
			}
		}
	}
	if registerIndex < 0 || sendIndex < 0 || registerIndex > sendIndex {
		t.Fatalf("callback ownership was not reserved before send: %#v", node.snapshotCalls())
	}

	rotated, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "chat.rotate", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(dispatched, "revision"), TaskID: "task-reuse", InactiveVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	rotatedTasks := rotated["tasks"].([]cloudCollaborationTask)
	rotatedChats := rotated["chats"].([]cloudCollaborationChat)
	if rotatedTasks[0].TargetSessionID != "" || rotatedTasks[0].ChatSessionID != "" || rotatedTasks[0].Status != "queued" || rotatedChats[0].Status != "released" || rotatedChats[0].CallbackRegistered {
		t.Fatalf("rotated tasks=%#v chats=%#v", rotatedTasks, rotatedChats)
	}
	if countCloudCollaborationCalls(node.snapshotCalls(), "session.callback.unregister") != 1 {
		t.Fatalf("rotate did not release callback: %#v", node.snapshotCalls())
	}
	released, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "lease.acquire", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(rotated, "revision"),
	})
	if err != nil {
		t.Fatal(err)
	}

	redispatched, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "task.dispatch", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(released, "revision"), TaskID: "task-reuse", TargetSessionID: "shared-cloud-chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	if countCloudCollaborationCalls(node.snapshotCalls(), "session.send") != 2 || countCloudCollaborationCalls(node.snapshotCalls(), "session.create") != 0 {
		t.Fatalf("same CHAT could not be explicitly reused after rotate: %#v", node.snapshotCalls())
	}
	rec, state, err := service.loadCloudCollaboration(context.Background(), ownerID, collaborationID)
	if err != nil {
		t.Fatal(err)
	}
	state.Tasks[0].Status = "done"
	state.Chats[len(state.Chats)-1].Status = "completed"
	readyToClose, err := service.saveCloudCollaboration(context.Background(), ownerID, rec, state)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{
		Action: "close", CollaborationID: collaborationID, ActorSessionID: "codex-controller", ActorRole: "controller", ExpectedRevision: numberField(readyToClose, "revision"),
	})
	if err != nil || closed["status"] != "completed" {
		t.Fatalf("close=%#v err=%v redispatched=%#v", closed, err, redispatched)
	}
	if countCloudCollaborationCalls(node.snapshotCalls(), "session.archive") != 0 || countCloudCollaborationCalls(node.snapshotCalls(), "session.delete") != 0 || countCloudCollaborationCalls(node.snapshotCalls(), "session.callback.unregister") != 2 {
		t.Fatalf("reused CHAT lifecycle calls=%#v", node.snapshotCalls())
	}
}

func TestValidateReusableCloudCollaborationSession(t *testing.T) {
	base := map[string]any{"session": map[string]any{"sessionId": "chat-1", "providerId": "codex", "backend": "chatgpt_cloud", "visibility": "visible", "externalIdType": "chatgpt_conversation", "status": "completed"}}
	if err := validateReusableCloudCollaborationSession(base, "chat-1"); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(map[string]any){
		"busy":        func(session map[string]any) { session["status"] = "running" },
		"not cloud":   func(session map[string]any) { session["backend"] = "codex_local" },
		"not visible": func(session map[string]any) { session["visibility"] = "internal" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			session := map[string]any{"sessionId": "chat-1", "providerId": "codex", "backend": "chatgpt_cloud", "visibility": "visible", "externalIdType": "chatgpt_conversation", "status": "completed"}
			mutate(session)
			if err := validateReusableCloudCollaborationSession(map[string]any{"session": session}, "chat-1"); err == nil {
				t.Fatalf("invalid reusable session accepted: %#v", session)
			}
		})
	}
}

func countCloudCollaborationCalls(calls []protocolv1.CapabilityRequest, action string) int {
	count := 0
	for _, call := range calls {
		if call.Action == action {
			count++
		}
	}
	return count
}

func mapActionTypes(value any) []string {
	actions, _ := value.([]map[string]any)
	out := make([]string, 0, len(actions))
	for _, action := range actions {
		if kind, _ := action["type"].(string); kind != "" {
			out = append(out, kind)
		}
	}
	return out
}

func TestCodexCloudCollaborationRejectsInvalidLimitsAndMissingLease(t *testing.T) {
	if err := validateCloudCollaborationLimits(CloudCollaborationRequest{MaxDepth: 9}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("invalid limit error=%v", err)
	}
	state := cloudCollaborationState{Generation: 1, DispatcherSessionID: "dispatcher"}
	if err := requireCloudCollaborationLease(state, "dispatcher", "dispatcher", time.Now()); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatalf("missing lease error=%v", err)
	}
}

func TestCodexCloudCollaborationRequiresReadyCodexAndLocalSessions(t *testing.T) {
	t.Run("codex unavailable", func(t *testing.T) {
		service, ownerID, machineID, node := newCloudCollaborationTestService(t)
		node.setResponse("provider.readiness", map[string]any{"ready": false, "chatgptCloudAvailable": false, "reasonCode": "NOT_LOGGED_IN"})
		_, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "create", MachineID: machineID, IdempotencyKey: "codex-collab-unready-001", ControllerSessionID: "codex-controller", DispatcherSessionID: "codex-dispatcher", Title: "Research", Goal: "Report", DoneWhen: "Report exists", WorkingDirectory: t.TempDir(), AllowedActions: []string{"chat.create"}})
		var capabilityErr *CapabilityCallError
		if !errors.As(err, &capabilityErr) || capabilityErr.Code != "RUNTIME_UNAVAILABLE" {
			t.Fatalf("unready create error=%v", err)
		}
		if countCloudCollaborationCalls(node.snapshotCalls(), "session.create") != 0 {
			t.Fatalf("unready create reached Cloud CHAT creation: %#v", node.snapshotCalls())
		}
	})

	t.Run("controller is not local codex", func(t *testing.T) {
		service, ownerID, machineID, node := newCloudCollaborationTestService(t)
		node.setResponse("session.get", map[string]any{"session": map[string]any{"sessionId": "cloud-controller", "providerId": "codex", "backend": "chatgpt_cloud"}})
		_, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "create", MachineID: machineID, IdempotencyKey: "codex-collab-cloud-controller-001", ControllerSessionID: "cloud-controller", DispatcherSessionID: "codex-dispatcher", Title: "Research", Goal: "Report", DoneWhen: "Report exists", WorkingDirectory: t.TempDir(), AllowedActions: []string{"chat.create"}})
		var capabilityErr *CapabilityCallError
		if !errors.As(err, &capabilityErr) || capabilityErr.Code != "INVALID_REQUEST" {
			t.Fatalf("non-local controller error=%v", err)
		}
		if countCloudCollaborationCalls(node.snapshotCalls(), "session.create") != 0 {
			t.Fatalf("invalid controller reached Cloud CHAT creation: %#v", node.snapshotCalls())
		}
	})
}

func TestCodexCloudCollaborationCreateInDoubtReusesOriginalIdempotencyKey(t *testing.T) {
	service, ownerID, machineID, node := newCloudCollaborationTestService(t)
	created, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "create", MachineID: machineID, IdempotencyKey: "codex-collab-in-doubt-001", ControllerSessionID: "codex-controller", DispatcherSessionID: "codex-dispatcher", Title: "Research", Goal: "Report", DoneWhen: "Report exists", WorkingDirectory: t.TempDir(), AllowedActions: []string{"chat.create", "file.read"}})
	if err != nil {
		t.Fatal(err)
	}
	collaborationID, _ := created["collaborationId"].(string)
	leased, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "lease.acquire", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(created, "revision")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "task.add", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(leased, "revision"), TaskID: "task-retry", Title: "Retry", Prompt: "Complete the task.", AccessMode: "read_only", AllowedActions: []string{"chat.create", "file.read"}})
	if err != nil {
		t.Fatal(err)
	}
	rec, state, err := service.loadCloudCollaboration(context.Background(), ownerID, collaborationID)
	if err != nil {
		t.Fatal(err)
	}
	originalKey := state.Tasks[0].IdempotencyKey
	state.Tasks[0].Status = "create_in_doubt"
	state.CreateCount = 1
	retryBase, err := service.saveCloudCollaboration(context.Background(), ownerID, rec, state)
	if err != nil {
		t.Fatal(err)
	}
	dispatched, err := service.CloudCollaboration(context.Background(), ownerID, CloudCollaborationRequest{Action: "task.dispatch", CollaborationID: collaborationID, ActorSessionID: "codex-dispatcher", ActorRole: "dispatcher", ExpectedRevision: numberField(retryBase, "revision"), TaskID: "task-retry"})
	if err != nil {
		t.Fatal(err)
	}
	if numberField(dispatched, "createCount") != 1 {
		t.Fatalf("create-in-doubt retry consumed another create: %#v", dispatched)
	}
	var createKey string
	for _, call := range node.snapshotCalls() {
		if call.Action == "session.create" {
			createKey, _ = call.Params["idempotencyKey"].(string)
		}
	}
	if createKey != originalKey {
		t.Fatalf("create-in-doubt key=%q want=%q", createKey, originalKey)
	}
}
