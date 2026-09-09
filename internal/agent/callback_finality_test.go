package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestCallbackInboxRoutePersistsAcrossRegistrationAndPendingEvent(t *testing.T) {
	dataDir := t.TempDir()
	route := map[string]any{
		"dbPath":    filepath.Join(dataDir, "callback-inbox.sqlite3"),
		"missionId": "mission-route",
		"itemId":    "item-route",
		"claim":     "claim-route",
	}
	registration := testCallbackRegistration("route-source", "route-target", "route-task", 1)
	registration.CallbackClaimTransport = callbackClaimTransportLocal
	registration.CallbackInboxRoute = route
	store := newSessionCallbackStore(dataDir)
	if _, _, err := store.register(registration); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.enqueue(testCallbackEvent(registration.SourceSessionID, 1)); err != nil || !queued {
		t.Fatalf("enqueue queued=%v err=%v", queued, err)
	}

	reloaded := newSessionCallbackStore(dataDir)
	loaded, exists, err := reloaded.registrationFor(registration.SourceSessionID)
	if err != nil || !exists || !callbackInboxRoutesEqual(loaded.CallbackInboxRoute, route) {
		t.Fatalf("registration route=%#v exists=%v err=%v", loaded.CallbackInboxRoute, exists, err)
	}
	pending, err := reloaded.pendingSnapshot(registration.SourceSessionID, registration.TargetSessionID)
	if err != nil || len(pending) != 1 || !callbackInboxRoutesEqual(pending[0].CallbackInboxRoute, route) {
		t.Fatalf("pending route=%#v err=%v", pending, err)
	}
	metadata := collaborationResultMetadata(pending[0])
	if !callbackInboxRoutesEqual(metadata["callbackInboxRoute"].(map[string]any), route) {
		t.Fatalf("sink metadata route=%#v", metadata)
	}
	if _, ok := metadata["resultText"]; ok {
		t.Fatal("callback result text crossed sink metadata")
	}

	hubRoute := registration
	hubRoute.SourceSessionID = "route-hub-source"
	hubRoute.CallbackClaimTransport = callbackClaimTransportHub
	if _, _, err := reloaded.register(hubRoute); err == nil {
		t.Fatal("accepted callback inbox route on hub transport")
	}
}

func TestBindCollaborationInboxPreservesPendingClaimAndRollsBackAtomicFailure(t *testing.T) {
	dataDir := t.TempDir()
	store := newSessionCallbackStore(dataDir)
	registration := testCallbackRegistration("bind-source", "bind-target", "bind-task", 3)
	registration.CallbackClaimTransport = callbackClaimTransportLocal
	if _, _, err := store.register(registration); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.enqueue(testCallbackEvent(registration.SourceSessionID, 1)); err != nil || !queued {
		t.Fatalf("enqueue queued=%v err=%v", queued, err)
	}
	claimID, claimed, err := store.claim(registration.TargetSessionID, "bind-claim", 1, time.Now().UTC(), callbackClaimTransportLocal)
	if err != nil || claimID != "bind-claim" || len(claimed) != 1 {
		t.Fatalf("claim=%q events=%#v err=%v", claimID, claimed, err)
	}
	route := map[string]any{"dbPath": filepath.Join(dataDir, "mission.sqlite3"), "missionId": registration.MissionID, "itemId": "item-bind", "claim": "claim-bind"}
	binding := map[string]any{"sourceSessionId": registration.SourceSessionID, "targetSessionId": registration.TargetSessionID, "missionId": registration.MissionID, "taskId": registration.TaskID, "generation": registration.Generation, "callbackInboxRoute": route}
	var sinkEvents []map[string]any
	dispatcher := newSessionCallbackDispatcher(store, nil, nil, nil, nil)
	dispatcher.setCollaborationResultSink(func(_ context.Context, metadata map[string]any) error {
		sinkEvents = append(sinkEvents, metadata)
		return nil
	})
	manager := &AgentManager{callbackStore: store, callbackDispatcher: dispatcher}
	if err := manager.BindCollaborationInbox(context.Background(), []map[string]any{binding}); err != nil {
		t.Fatal(err)
	}
	if len(sinkEvents) != 1 || !callbackInboxRoutesEqual(sinkEvents[0]["callbackInboxRoute"].(map[string]any), route) {
		t.Fatalf("bind did not project pending event: %#v", sinkEvents)
	}
	if err := manager.BindCollaborationInbox(context.Background(), []map[string]any{binding}); err != nil {
		t.Fatalf("same binding was not idempotent: %v", err)
	}
	loaded, exists, err := store.registrationFor(registration.SourceSessionID)
	if err != nil || !exists || !callbackInboxRoutesEqual(loaded.CallbackInboxRoute, route) {
		t.Fatalf("bound registration=%#v exists=%v err=%v", loaded, exists, err)
	}
	pending, err := store.pendingClaimSnapshot(registration.TargetSessionID, claimID, callbackClaimTransportLocal)
	if err != nil || len(pending) != 1 || pending[0].ClaimID != claimID || !callbackInboxRoutesEqual(pending[0].CallbackInboxRoute, route) {
		t.Fatalf("bound pending=%#v err=%v", pending, err)
	}

	conflicting := make(map[string]any, len(binding))
	for key, value := range binding {
		conflicting[key] = value
	}
	conflicting["callbackInboxRoute"] = map[string]any{"dbPath": filepath.Join(dataDir, "other.sqlite3"), "missionId": registration.MissionID, "itemId": "item-bind", "claim": "claim-bind"}
	if err := manager.BindCollaborationInbox(context.Background(), []map[string]any{conflicting}); err == nil {
		t.Fatal("conflicting binding was accepted")
	}
	loaded, _, _ = store.registrationFor(registration.SourceSessionID)
	if !callbackInboxRoutesEqual(loaded.CallbackInboxRoute, route) {
		t.Fatalf("conflict changed registration route=%#v", loaded.CallbackInboxRoute)
	}

	failureStore := newSessionCallbackStore(t.TempDir())
	failureRegistration := testCallbackRegistration("bind-failure-source", "bind-failure-target", "bind-failure-task", 1)
	failureRegistration.CallbackClaimTransport = callbackClaimTransportLocal
	if _, _, err := failureStore.register(failureRegistration); err != nil {
		t.Fatal(err)
	}
	if queued, err := failureStore.enqueue(testCallbackEvent(failureRegistration.SourceSessionID, 1)); err != nil || !queued {
		t.Fatalf("failure enqueue queued=%v err=%v", queued, err)
	}
	failureStore.beforeCommitSaveOverride = func() error { return errors.New("injected persistence failure") }
	failureManager := &AgentManager{callbackStore: failureStore}
	failureRoute := map[string]any{"dbPath": filepath.Join(t.TempDir(), "failure.sqlite3"), "missionId": failureRegistration.MissionID, "itemId": "item-failure", "claim": "claim-failure"}
	failureBinding := map[string]any{"sourceSessionId": failureRegistration.SourceSessionID, "targetSessionId": failureRegistration.TargetSessionID, "missionId": failureRegistration.MissionID, "taskId": failureRegistration.TaskID, "generation": failureRegistration.Generation, "callbackInboxRoute": failureRoute}
	if err := failureManager.BindCollaborationInbox(context.Background(), []map[string]any{failureBinding}); err == nil {
		t.Fatal("persistence failure was hidden")
	}
	failureLoaded, _, _ := failureStore.registrationFor(failureRegistration.SourceSessionID)
	failurePending, _ := failureStore.pendingSnapshot(failureRegistration.SourceSessionID, failureRegistration.TargetSessionID)
	if failureLoaded.CallbackInboxRoute != nil || len(failurePending) != 1 || failurePending[0].CallbackInboxRoute != nil {
		t.Fatalf("failed bind partially mutated state: registration=%#v pending=%#v", failureLoaded, failurePending)
	}
}

func TestBindCollaborationInboxRetriesPendingProjectionAfterSinkFailure(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	registration := testCallbackRegistration("bind-retry-source", "bind-retry-target", "bind-retry-task", 1)
	registration.CallbackClaimTransport = callbackClaimTransportLocal
	if _, _, err := store.register(registration); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.enqueue(testCallbackEvent(registration.SourceSessionID, 1)); err != nil || !queued {
		t.Fatalf("enqueue queued=%v err=%v", queued, err)
	}
	route := map[string]any{"dbPath": filepath.Join(t.TempDir(), "retry.sqlite3"), "missionId": registration.MissionID, "itemId": "item-retry", "claim": "claim-retry"}
	binding := map[string]any{"sourceSessionId": registration.SourceSessionID, "targetSessionId": registration.TargetSessionID, "missionId": registration.MissionID, "taskId": registration.TaskID, "generation": registration.Generation, "callbackInboxRoute": route}
	dispatcher := newSessionCallbackDispatcher(store, nil, nil, nil, nil)
	sinkCalls := 0
	dispatcher.setCollaborationResultSink(func(_ context.Context, metadata map[string]any) error {
		sinkCalls++
		if sinkCalls == 1 {
			return errors.New("inbox temporarily unavailable")
		}
		if !callbackInboxRoutesEqual(metadata["callbackInboxRoute"].(map[string]any), route) {
			t.Fatalf("retry metadata route=%#v", metadata)
		}
		return nil
	})
	manager := &AgentManager{callbackStore: store, callbackDispatcher: dispatcher}
	if err := manager.BindCollaborationInbox(context.Background(), []map[string]any{binding}); err == nil {
		t.Fatal("sink failure was hidden")
	}
	if err := manager.BindCollaborationInbox(context.Background(), []map[string]any{binding}); err != nil {
		t.Fatalf("same route retry failed: %v", err)
	}
	if sinkCalls != 2 {
		t.Fatalf("sink calls=%d", sinkCalls)
	}
	pending, err := store.pendingSnapshot(registration.SourceSessionID, registration.TargetSessionID)
	if err != nil || len(pending) != 1 || !callbackInboxRoutesEqual(pending[0].CallbackInboxRoute, route) {
		t.Fatalf("pending after sink retry=%#v err=%v", pending, err)
	}
}

func TestCallbackClaimAndCompletionAckRequireResultSinkBeforeMutation(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	registration := testCallbackRegistration("sink-claim-source", "sink-claim-target", "sink-claim-task", 1)
	if _, _, err := store.register(registration); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.enqueue(testCallbackEvent(registration.SourceSessionID, 1)); err != nil || !queued {
		t.Fatalf("enqueue queued=%v err=%v", queued, err)
	}
	dispatcher := newSessionCallbackDispatcher(store, nil, nil, nil, nil)
	manager := &AgentManager{callbackStore: store, callbackDispatcher: dispatcher}
	dispatcher.setCollaborationResultSink(func(context.Context, map[string]any) error {
		return errors.New("inbox unavailable")
	})
	_, err := manager.sessionCallbackClaimContext(context.Background(), agentControlParams{
		CallbackTargetSessionID: registration.TargetSessionID,
		CallbackClaimID:         "sink-claim",
		CallbackClaimLimit:      1,
	})
	if err == nil {
		t.Fatal("claim succeeded while result sink failed")
	}
	pending, err := store.pendingSnapshot(registration.SourceSessionID, registration.TargetSessionID)
	if err != nil || len(pending) != 1 || pending[0].ClaimID != "" {
		t.Fatalf("failed claim did not release pending event: %#v err=%v", pending, err)
	}

	completionStore := newSessionCallbackStore(t.TempDir())
	completionRegistration := testCallbackRegistration("sink-ack-source", "sink-ack-target", "sink-ack-task", 1)
	if _, _, err := completionStore.register(completionRegistration); err != nil {
		t.Fatal(err)
	}
	if queued, err := completionStore.enqueue(testCallbackEvent(completionRegistration.SourceSessionID, 1)); err != nil || !queued {
		t.Fatalf("completion enqueue queued=%v err=%v", queued, err)
	}
	completionDispatcher := newSessionCallbackDispatcher(completionStore, nil, nil, nil, nil)
	completionManager := &AgentManager{callbackStore: completionStore, callbackDispatcher: completionDispatcher}
	completionDispatcher.setCollaborationResultSink(func(context.Context, map[string]any) error {
		return errors.New("inbox unavailable")
	})
	_, err = completionManager.sessionCallbackAckContext(context.Background(), agentControlParams{
		Mode:                    "completion",
		SessionID:               completionRegistration.SourceSessionID,
		CallbackTargetSessionID: completionRegistration.TargetSessionID,
		CallbackMissionID:       completionRegistration.MissionID,
		CallbackTaskID:          completionRegistration.TaskID,
		CallbackGeneration:      completionRegistration.Generation,
	})
	if err == nil {
		t.Fatal("completion ACK succeeded while result sink failed")
	}
	if pending, pendingErr := completionStore.pendingSnapshot(completionRegistration.SourceSessionID, completionRegistration.TargetSessionID); pendingErr != nil || len(pending) != 1 {
		t.Fatalf("failed completion ACK removed pending event: %#v err=%v", pending, pendingErr)
	}
	if _, exists, registrationErr := completionStore.registrationFor(completionRegistration.SourceSessionID); registrationErr != nil || !exists {
		t.Fatalf("failed completion ACK retired route: exists=%v err=%v", exists, registrationErr)
	}
}

type callbackSinkCaptureAgent struct {
	registerParams map[string]any
}

func (a *callbackSinkCaptureAgent) Control(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	if action == "session.callback.register" {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &a.registerParams); err != nil {
			return nil, err
		}
	}
	switch action {
	case "provider.readiness":
		return map[string]any{"ready": true, "readyForSessionCreate": true}, nil
	case "session.create":
		return map[string]any{"sessionId": "cloud-sink-source"}, nil
	case "session.send":
		return map[string]any{"sessionId": "cloud-sink-source", "phase": "running"}, nil
	default:
		return map[string]any{"prepared": true}, nil
	}
}

func (*callbackSinkCaptureAgent) Close(context.Context) error { return nil }

func TestAgentManagerNodeLocalSinkProjectsInjectedMetadataToSQLite(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "mission.sqlite3")
	fakeAgent := &callbackSinkCaptureAgent{}
	fakeNode := node.NewLocalCapabilityClient(node.Config{DataDir: filepath.Join(root, "capture-node"), Agent: fakeAgent})
	item := map[string]any{
		"id": "task-sink", "kind": "implement", "phase": "ready", "owner": "owner-1", "executor": "cloud",
		"next_action": "Wait", "evidence": []any{}, "depends_on": []any{}, "packet": map[string]any{
			"machineId": "machine-1", "callbackSessionId": "controller-1", "workingDirectory": root,
			"prompt": "Exercise the local callback sink.", "idempotencyKey": "sink-integration-key-001",
			"targetSessionId": "cloud-sink-target", "accessMode": "write", "writeScope": "src/task", "callbackType": "text",
		},
		"binding": nil, "callback": "none", "result": "none", "validation": "pending", "integration": "pending",
		"blocker": nil, "claim": nil, "source_ref": nil, "dispatch_key": nil, "terminal_ref": nil,
		"validation_owner": nil, "validation_started_at": nil, "next_check_at": nil, "started_at": nil,
		"priority": int64(100), "contract_refs": []any{}, "acceptance_ref": nil, "execution_ref": nil, "local_scope": nil,
	}
	initResponse := fakeNode.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "sink-init", Capability: "collaboration.control", Action: "init",
		Params: map[string]any{
			"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "controller-1", "coordinator": "coordinator-1",
			"authorityRef": "user-scope-1", "goal": "Deliver the bounded mission", "strategyRef": "strategy-1",
			"nextAction": "Process current work", "dispatchEnabled": true, "continuation": map[string]any{"enabled": false},
			"capacity": map[string]any{"cloud": int64(4), "local": int64(2)}, "items": []any{item},
		},
	})
	if initResponse.Error != nil {
		t.Fatalf("init failed: %#v", initResponse.Error)
	}
	claimResponse := fakeNode.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "sink-claim", Capability: "collaboration.control", Action: "claim",
		Params: map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-sink", "expectedRevision": initResponse.Result["revision"]},
	})
	if claimResponse.Error != nil {
		t.Fatalf("claim failed: %#v", claimResponse.Error)
	}
	dispatchResponse := fakeNode.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "sink-dispatch", Capability: "collaboration.control", Action: "dispatch",
		Params: map[string]any{"dispatchToken": claimResponse.Result["dispatchToken"]},
	})
	if dispatchResponse.Error != nil {
		t.Fatalf("dispatch failed: %#v", dispatchResponse.Error)
	}
	if fakeAgent.registerParams == nil {
		t.Fatalf("dispatch did not capture callback registration: response=%#v", dispatchResponse.Result)
	}
	route, ok := fakeAgent.registerParams["callbackInboxRoute"].(map[string]any)
	if !ok {
		t.Fatalf("callback inbox route missing from registration: %#v", fakeAgent.registerParams)
	}

	manager := New(filepath.Join(root, "agent"), nil)
	defer manager.Close(context.Background())
	localNode := node.NewLocalCapabilityClient(node.Config{DataDir: filepath.Join(root, "real-node"), Agent: manager})
	if manager.callbackDispatcher.collaborationResultSink() == nil {
		t.Fatal("NewLocalCapabilityClient did not inject collaboration result sink")
	}
	generation, ok := fakeAgent.registerParams["callbackGeneration"].(float64)
	if !ok {
		t.Fatalf("callback generation missing from registration: %#v", fakeAgent.registerParams)
	}
	event := sessionCallbackEvent{
		SourceSessionID:        fakeAgent.registerParams["sessionId"].(string),
		TargetSessionID:        fakeAgent.registerParams["callbackTargetSessionId"].(string),
		MissionID:              fakeAgent.registerParams["callbackMissionId"].(string),
		TaskID:                 fakeAgent.registerParams["callbackTaskId"].(string),
		Generation:             int64(generation),
		EventSequence:          1,
		EventKey:               "submitted_sink_integration",
		EventType:              "conversation.turn.complete",
		CompletionSource:       "local-submission",
		OccurredAt:             time.Now().UTC(),
		CallbackType:           "text",
		CallbackClaimTransport: callbackClaimTransportLocal,
		CallbackInboxRoute:     route,
		CallbackOutcome:        "completed",
		ResultStatus:           "completed",
	}
	if err := manager.persistCollaborationCallbackEvents(context.Background(), []sessionCallbackEvent{event}); err != nil {
		t.Fatalf("AgentManager sink projection failed: %v", err)
	}
	inbox := localNode.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "sink-inbox", Capability: "collaboration.control", Action: "inbox",
		Params: map[string]any{"dbPath": route["dbPath"], "missionId": route["missionId"], "actorSessionId": event.TargetSessionID},
	})
	if inbox.Error != nil {
		t.Fatalf("inbox read failed: %#v", inbox.Error)
	}
	results, ok := inbox.Result["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("projected inbox=%#v", inbox.Result)
	}
	resultSummary, ok := results[0].(map[string]any)
	if !ok {
		t.Fatalf("projected inbox result=%#v", results[0])
	}
	detail := localNode.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "sink-inbox-detail", Capability: "collaboration.control", Action: "inbox",
		Params: map[string]any{"dbPath": route["dbPath"], "missionId": route["missionId"], "actorSessionId": event.TargetSessionID, "resultId": resultSummary["resultId"]},
	})
	if detail.Error != nil {
		t.Fatalf("inbox detail failed: %#v", detail.Error)
	}
	stored, ok := detail.Result["result"].(map[string]any)
	if !ok || stored["eventKey"] != event.EventKey || stored["callbackInboxRoute"] == nil || stored["callbackOutcome"] != event.CallbackOutcome {
		t.Fatalf("stored metadata=%#v", stored)
	}
}

func TestCallbackStoreSchema3DefaultsTransportToHubAndSchema4PersistsLocal(t *testing.T) {
	dir := t.TempDir()
	seed := newSessionCallbackStore(dir)
	reg := testCallbackRegistration("legacy-source", "legacy-target", "legacy-task", 1)
	registered, _, err := seed.register(reg)
	if err != nil {
		t.Fatal(err)
	}
	registered.CallbackClaimTransport = ""
	legacy, err := json.Marshal(sessionCallbackIndex{SchemaVersion: 3, Registrations: []sessionCallbackRegistration{registered}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent", "session-callbacks.json")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := newSessionCallbackStore(dir)
	legacyLoaded, exists, err := loaded.registrationFor("legacy-source")
	if err != nil || !exists || callbackTransportForRegistration(legacyLoaded) != callbackClaimTransportHub {
		t.Fatalf("schema3 legacy transport=%q exists=%v err=%v", legacyLoaded.CallbackClaimTransport, exists, err)
	}
	local := testCallbackRegistration("local-schema4", "local-target", "local-task", 1)
	local.CallbackClaimTransport = callbackClaimTransportLocal
	if _, _, err := loaded.register(local); err != nil {
		t.Fatal(err)
	}
	reloaded := newSessionCallbackStore(dir)
	localLoaded, exists, err := reloaded.registrationFor("local-schema4")
	if err != nil || !exists || callbackTransportForRegistration(localLoaded) != callbackClaimTransportLocal {
		t.Fatalf("schema4 local transport=%q exists=%v err=%v", localLoaded.CallbackClaimTransport, exists, err)
	}
}

func TestSubmittedCallbackNudgeHasOneDirectReceiveCall(t *testing.T) {
	event := sessionCallbackEvent{CompletionSource: "submission"}
	prompt := buildSessionCallbackNudge("codex-target", "callback-envelope", event)
	want := `Call FastSpider_FS codex_cloud_collaboration({"action":"completion.claim","params":{"actorSessionId":"codex-target","claimId":"callback-envelope"}}).`
	if prompt != want {
		t.Fatalf("normal callback must contain only the receive call: %s", prompt)
	}
	event.CompletionSource = "recovery"
	if prompt := buildSessionCallbackNudge("codex-target", "callback-envelope", event); !strings.Contains(prompt, "cloud-callback-recovery") {
		t.Fatalf("missing on-demand recovery entry: %s", prompt)
	}
}

func TestLocalCallbackNudgeUsesLocalControlPlane(t *testing.T) {
	event := sessionCallbackEvent{CompletionSource: "submission"}
	prompt := buildSessionCallbackNudgeForTransport("codex-target", "callback-envelope", callbackClaimTransportLocal, event)
	if !strings.Contains(prompt, "FastSpider_Local collaboration_control") || !strings.Contains(prompt, "callback_claim") || !strings.Contains(prompt, "callback_ack") || strings.Contains(prompt, "FastSpider_FS") {
		t.Fatalf("local callback must stay on the local control plane: %s", prompt)
	}
	if !strings.Contains(prompt, "resume any unfinished authorized validation/next_actions") || !strings.Contains(prompt, "Empty or already-consumed") {
		t.Fatalf("transport notification can discard unfinished business work: %s", prompt)
	}
}

func TestLocalCallbackNudgeJSONDrivesClaimAndAck(t *testing.T) {
	s := newSessionCallbackStore(t.TempDir())
	reg := testCallbackRegistration("local-json-source", "local-json-target", "local-json-task", 1)
	reg.CallbackClaimTransport = callbackClaimTransportLocal
	if _, _, err := s.register(reg); err != nil {
		t.Fatal(err)
	}
	if queued, err := s.enqueue(testCallbackEvent("local-json-source", 1)); err != nil || !queued {
		t.Fatalf("enqueue queued=%v err=%v", queued, err)
	}
	grouped, err := s.pendingForNudgeByTransport()
	if err != nil {
		t.Fatal(err)
	}
	group := sessionCallbackNudgeGroup{TargetSessionID: "local-json-target", Transport: callbackClaimTransportLocal}
	envelope := sessionCallbackEnvelopeIDForTransport(group.TargetSessionID, group.Transport, grouped[group])
	prompt := buildSessionCallbackNudgeForTransport(group.TargetSessionID, envelope, group.Transport, grouped[group]...)
	parts := strings.Split(prompt, "FastSpider_Local collaboration_control(")
	if len(parts) != 3 {
		t.Fatalf("expected claim and ack calls, prompt=%q", prompt)
	}
	manager := &AgentManager{callbackStore: s}
	for i, raw := range parts[1:] {
		end := strings.Index(raw, "})")
		if end < 0 {
			t.Fatalf("call %d has no JSON terminator: %q", i, raw)
		}
		var call struct {
			Action string         `json:"action"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal([]byte(raw[:end+1]), &call); err != nil {
			t.Fatalf("call %d JSON decode: %v", i, err)
		}
		if call.Action != []string{"callback_claim", "callback_ack"}[i] {
			t.Fatalf("call %d action=%q", i, call.Action)
		}
		if call.Params["callbackTargetSessionId"] != group.TargetSessionID || call.Params["callbackClaimId"] != envelope || call.Params["callbackClaimTransport"] != callbackClaimTransportLocal {
			t.Fatalf("call %d params=%#v", i, call.Params)
		}
		paramsJSON, err := json.Marshal(call.Params)
		if err != nil {
			t.Fatalf("call %d params encode: %v", i, err)
		}
		var decoded agentControlParams
		if err := json.Unmarshal(paramsJSON, &decoded); err != nil {
			t.Fatalf("call %d params decode: %v", i, err)
		}
		if i == 0 {
			if _, err := manager.sessionCallbackClaim(decoded); err != nil {
				t.Fatalf("claim from decoded JSON failed: %v", err)
			}
		} else {
			if _, err := manager.sessionCallbackAck(decoded); err != nil {
				t.Fatalf("ack from decoded JSON failed: %v", err)
			}
		}
	}
	if pending, _ := s.pendingSnapshot("local-json-source", group.TargetSessionID); len(pending) != 0 {
		t.Fatalf("local callback remained pending after decoded claim/ack: %+v", pending)
	}
	if _, exists, _ := s.registrationFor("local-json-source"); exists {
		t.Fatal("local callback route remained after decoded ack")
	}
}

func TestCallbackClaimTransportIsolatedAndLocalAckRetiresRoute(t *testing.T) {
	s := newSessionCallbackStore(t.TempDir())
	hub := testCallbackRegistration("hub-source", "same-target", "hub-task", 1)
	local := testCallbackRegistration("local-source", "same-target", "local-task", 1)
	local.CallbackClaimTransport = callbackClaimTransportLocal
	if _, _, err := s.register(hub); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.register(local); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"hub-source", "local-source"} {
		if queued, err := s.enqueue(testCallbackEvent(source, 1)); err != nil || !queued {
			t.Fatalf("enqueue %s queued=%v err=%v", source, queued, err)
		}
	}
	localClaim, localEvents, err := s.claim("same-target", "local-claim", 10, time.Now(), callbackClaimTransportLocal)
	if err != nil || localClaim != "local-claim" || len(localEvents) != 1 || localEvents[0].SourceSessionID != "local-source" {
		t.Fatalf("local claim=%q events=%+v err=%v", localClaim, localEvents, err)
	}
	hubClaim, hubEvents, err := s.claim("same-target", "hub-claim", 10, time.Now(), callbackClaimTransportHub)
	if err != nil || hubClaim != "hub-claim" || len(hubEvents) != 1 || hubEvents[0].SourceSessionID != "hub-source" {
		t.Fatalf("hub claim=%q events=%+v err=%v", hubClaim, hubEvents, err)
	}
	acked, retired, err := s.acknowledgeClaimAndRetire("same-target", localClaim, time.Now(), callbackClaimTransportLocal)
	if err != nil || acked != 1 || len(retired) != 1 || retired[0].SourceSessionID != "local-source" {
		t.Fatalf("local ack=%d retired=%+v err=%v", acked, retired, err)
	}
	if _, exists, err := s.registrationFor("local-source"); err != nil || exists {
		t.Fatalf("local route remained after local ack: exists=%v err=%v", exists, err)
	}
	if _, exists, err := s.registrationFor("hub-source"); err != nil || !exists {
		t.Fatalf("hub route was affected by local ack: exists=%v err=%v", exists, err)
	}
	if retryCount, _, retryErr := s.acknowledgeClaimAndRetire("same-target", localClaim, time.Now(), callbackClaimTransportLocal); retryErr != nil || retryCount != 0 {
		t.Fatalf("local ack retry was not idempotent: count=%d err=%v", retryCount, retryErr)
	}
	reloaded := newSessionCallbackStore(filepath.Dir(filepath.Dir(s.path)))
	if retryCount, _, retryErr := reloaded.acknowledgeClaimAndRetire("same-target", localClaim, time.Now(), callbackClaimTransportLocal); retryErr != nil || retryCount != 0 {
		t.Fatalf("local ack retry after restart was not idempotent: count=%d err=%v", retryCount, retryErr)
	}
}

func TestLocalAckValidatesWholeClaimBeforeMutation(t *testing.T) {
	s := newSessionCallbackStore(t.TempDir())
	for _, source := range []string{"local-atomic-a", "local-atomic-b"} {
		reg := testCallbackRegistration(source, "local-atomic-target", source, 1)
		reg.CallbackClaimTransport = callbackClaimTransportLocal
		if _, _, err := s.register(reg); err != nil {
			t.Fatal(err)
		}
		if queued, err := s.enqueue(testCallbackEvent(source, 1)); err != nil || !queued {
			t.Fatalf("enqueue %s queued=%v err=%v", source, queued, err)
		}
	}
	claimID, events, err := s.claim("local-atomic-target", "local-atomic-claim", 2, time.Now(), callbackClaimTransportLocal)
	if err != nil || claimID != "local-atomic-claim" || len(events) != 2 {
		t.Fatalf("claim=%q events=%d err=%v", claimID, len(events), err)
	}
	// Simulate a corrupted/missing route discovered during the all-or-nothing
	// validation pass. The other claimed item must remain untouched.
	s.mu.Lock()
	delete(s.registrations, "local-atomic-b")
	s.mu.Unlock()
	if _, _, err := s.acknowledgeClaimAndRetire("local-atomic-target", claimID, time.Now(), callbackClaimTransportLocal); err == nil {
		t.Fatal("accepted a claim with a missing route")
	}
	if pending, _ := s.pendingSnapshot("local-atomic-a", "local-atomic-target"); len(pending) != 1 || pending[0].ClaimID != claimID {
		t.Fatalf("first claimed item was partially removed: %+v", pending)
	}
}

func TestLocalFileClaimRefreshesDeliverableAndRejectsInvalidAck(t *testing.T) {
	dataDir := t.TempDir()
	store := newSessionCallbackStore(dataDir)
	missingPath := filepath.Join(dataDir, "missing-report.md")
	reg := testCallbackRegistration("local-file-missing", "local-file-target", "local-file-task", 1)
	reg.CallbackClaimTransport = callbackClaimTransportLocal
	reg.CallbackType = "local_file"
	reg.DeliverablePath = missingPath
	if _, _, err := store.register(reg); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.enqueue(testCallbackEvent(reg.SourceSessionID, 1)); err != nil || !queued {
		t.Fatalf("enqueue queued=%v err=%v", queued, err)
	}

	manager := &AgentManager{callbackStore: store}
	claimed, err := manager.sessionCallbackClaim(agentControlParams{
		CallbackTargetSessionID: reg.TargetSessionID,
		CallbackClaimID:         "local-file-missing-claim",
		CallbackClaimLimit:      1,
		CallbackClaimTransport:  callbackClaimTransportLocal,
	})
	if err != nil {
		t.Fatal(err)
	}
	items, ok := claimed["claimed"].([]map[string]any)
	if !ok || len(items) != 1 {
		t.Fatalf("claimed=%#v", claimed)
	}
	if items[0]["deliverableStatus"] != "missing" || items[0]["resultStatus"] != "failed" {
		t.Fatalf("missing file was exposed as ready: %#v", items[0])
	}

	if _, err := manager.sessionCallbackAck(agentControlParams{
		CallbackTargetSessionID: reg.TargetSessionID,
		CallbackClaimID:         "local-file-missing-claim",
		CallbackClaimTransport:  callbackClaimTransportLocal,
	}); err == nil {
		t.Fatal("missing local_file was successfully acknowledged")
	}
	if pending, err := store.pendingSnapshot(reg.SourceSessionID, reg.TargetSessionID); err != nil || len(pending) != 1 {
		t.Fatalf("invalid local_file ACK removed pending event: pending=%#v err=%v", pending, err)
	}
	if _, exists, err := store.registrationFor(reg.SourceSessionID); err != nil || !exists {
		t.Fatalf("invalid local_file ACK retired route: exists=%v err=%v", exists, err)
	}
}

func TestLocalFileBatchAckValidatesAllDeliverablesBeforeRetire(t *testing.T) {
	dataDir := t.TempDir()
	store := newSessionCallbackStore(dataDir)
	readyPath := filepath.Join(dataDir, "ready-report.md")
	if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	registrations := []sessionCallbackRegistration{
		func() sessionCallbackRegistration {
			reg := testCallbackRegistration("local-file-ready", "local-file-batch-target", "local-file-ready-task", 1)
			reg.CallbackClaimTransport = callbackClaimTransportLocal
			reg.CallbackType = "local_file"
			reg.DeliverablePath = readyPath
			return reg
		}(),
		func() sessionCallbackRegistration {
			reg := testCallbackRegistration("local-file-missing-batch", "local-file-batch-target", "local-file-missing-task", 1)
			reg.CallbackClaimTransport = callbackClaimTransportLocal
			reg.CallbackType = "local_file"
			reg.DeliverablePath = filepath.Join(dataDir, "missing-batch-report.md")
			return reg
		}(),
	}
	for i, reg := range registrations {
		if _, _, err := store.register(reg); err != nil {
			t.Fatal(err)
		}
		if queued, err := store.enqueue(testCallbackEvent(reg.SourceSessionID, int64(i+1))); err != nil || !queued {
			t.Fatalf("enqueue %s queued=%v err=%v", reg.SourceSessionID, queued, err)
		}
	}

	manager := &AgentManager{callbackStore: store}
	claimed, err := manager.sessionCallbackClaim(agentControlParams{
		CallbackTargetSessionID: "local-file-batch-target",
		CallbackClaimID:         "local-file-batch-claim",
		CallbackClaimLimit:      2,
		CallbackClaimTransport:  callbackClaimTransportLocal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if count, _ := claimed["claimedCount"].(int); count != 2 {
		t.Fatalf("claimed=%#v", claimed)
	}
	if _, err := manager.sessionCallbackAck(agentControlParams{
		CallbackTargetSessionID: "local-file-batch-target",
		CallbackClaimID:         "local-file-batch-claim",
		CallbackClaimTransport:  callbackClaimTransportLocal,
	}); err == nil {
		t.Fatal("mixed ready/missing local_file batch was partially acknowledged")
	}
	for _, reg := range registrations {
		if pending, err := store.pendingSnapshot(reg.SourceSessionID, reg.TargetSessionID); err != nil || len(pending) != 1 {
			t.Fatalf("batch ACK removed pending %s: pending=%#v err=%v", reg.SourceSessionID, pending, err)
		}
		if _, exists, err := store.registrationFor(reg.SourceSessionID); err != nil || !exists {
			t.Fatalf("batch ACK retired route %s: exists=%v err=%v", reg.SourceSessionID, exists, err)
		}
	}
}

func TestLocalFileIdempotentClaimRefreshesChangedDeliverable(t *testing.T) {
	dataDir := t.TempDir()
	store := newSessionCallbackStore(dataDir)
	path := filepath.Join(dataDir, "retry-report.md")
	if err := os.WriteFile(path, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := testCallbackRegistration("local-file-retry", "local-file-retry-target", "local-file-retry-task", 1)
	reg.CallbackClaimTransport = callbackClaimTransportLocal
	reg.CallbackType = "local_file"
	reg.DeliverablePath = path
	if _, _, err := store.register(reg); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.enqueue(testCallbackEvent(reg.SourceSessionID, 1)); err != nil || !queued {
		t.Fatalf("enqueue queued=%v err=%v", queued, err)
	}
	manager := &AgentManager{callbackStore: store}
	first, err := manager.sessionCallbackClaim(agentControlParams{
		CallbackTargetSessionID: reg.TargetSessionID,
		CallbackClaimID:         "local-file-retry-claim",
		CallbackClaimLimit:      1,
		CallbackClaimTransport:  callbackClaimTransportLocal,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstItems := first["claimed"].([]map[string]any)
	if firstItems[0]["deliverableStatus"] != "ready" || firstItems[0]["resultStatus"] != "ready" {
		t.Fatalf("initial claim=%#v", first)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	second, err := manager.sessionCallbackClaim(agentControlParams{
		CallbackTargetSessionID: reg.TargetSessionID,
		CallbackClaimID:         "local-file-retry-claim",
		CallbackClaimLimit:      1,
		CallbackClaimTransport:  callbackClaimTransportLocal,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondItems := second["claimed"].([]map[string]any)
	if secondItems[0]["deliverableStatus"] != "missing" || secondItems[0]["resultStatus"] != "failed" {
		t.Fatalf("idempotent claim returned stale metadata=%#v", second)
	}
}

func TestLocalProviderCompletionIsFormalWhileHubRecoveryRemainsRecovery(t *testing.T) {
	s := newSessionCallbackStore(t.TempDir())
	local := testCallbackRegistration("local-provider-source", "local-provider-target", "local-provider-task", 1)
	local.CallbackClaimTransport = callbackClaimTransportLocal
	if _, _, err := s.register(local); err != nil {
		t.Fatal(err)
	}
	if queued, err := s.enqueue(testCallbackEvent("local-provider-source", 1)); err != nil || !queued {
		t.Fatalf("local enqueue queued=%v err=%v", queued, err)
	}
	localPending, err := s.pendingSnapshot("local-provider-source", "local-provider-target")
	if err != nil || len(localPending) != 1 || localPending[0].CompletionSource != "local-submission" {
		t.Fatalf("local provider source=%+v err=%v", localPending, err)
	}
	if mapped := sessionCallbackEventMap(localPending[0], time.Now().UTC(), false); mapped["recoveryOnly"] != false {
		t.Fatalf("local provider completion incorrectly marked recovery-only: %#v", mapped)
	}
	hub := testCallbackRegistration("hub-provider-source", "hub-provider-target", "hub-provider-task", 1)
	if _, _, err := s.register(hub); err != nil {
		t.Fatal(err)
	}
	if queued, err := s.enqueue(testCallbackEvent("hub-provider-source", 1)); err != nil || !queued {
		t.Fatalf("hub enqueue queued=%v err=%v", queued, err)
	}
	hubPending, err := s.pendingSnapshot("hub-provider-source", "hub-provider-target")
	if err != nil || len(hubPending) != 1 || hubPending[0].CompletionSource != "recovery" {
		t.Fatalf("hub provider source=%+v err=%v", hubPending, err)
	}
}

func TestHubCompletionAcknowledgesOnlyBoundNodeGeneration(t *testing.T) {
	dir := t.TempDir()
	s := newSessionCallbackStore(dir)
	reg := testCallbackRegistration("source", "target", "task", 1)
	if _, _, err := s.register(reg); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.register(testCallbackRegistration("other-source", "target", "other-task", 1)); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"source", "other-source"} {
		event := testCallbackEvent(source, 1)
		event.EventType = "hub-completion-notify"
		if queued, err := s.enqueue(event); err != nil || !queued {
			t.Fatalf("enqueue=%v %v", queued, err)
		}
	}
	// Also handles the previously observed case: a Node claim was never acked.
	if _, _, err := s.claim("target", "old-node-claim", 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	wrong := reg
	wrong.TargetSessionID = "wrong-target"
	if _, err := s.acknowledgeCompletion(wrong, time.Now()); err == nil {
		t.Fatal("accepted a mismatched callback owner")
	}
	manager := &AgentManager{callbackStore: s}
	out, err := manager.sessionCallbackAck(agentControlParams{Mode: "completion", SessionID: "source", CallbackTargetSessionID: reg.TargetSessionID, CallbackMissionID: reg.MissionID, CallbackTaskID: reg.TaskID, CallbackGeneration: reg.Generation})
	if err != nil || out["acked"] != true || out["ackedCount"] != 1 {
		t.Fatalf("ack=%v %v", out, err)
	}
	s = newSessionCallbackStore(dir)
	pending, err := s.pendingSnapshot("", "target")
	if err != nil || len(pending) != 1 || pending[0].SourceSessionID != "other-source" {
		t.Fatalf("wrong callback cleared: %+v %v", pending, err)
	}
	if _, exists, err := s.registrationFor("source"); err != nil || exists {
		t.Fatalf("acknowledged route remained active: %v %v", exists, err)
	}
	if count, err := s.acknowledgeCompletion(reg, time.Now()); err != nil || count != 0 {
		t.Fatalf("retry=%d %v", count, err)
	}
	if queued, err := s.enqueue(testCallbackEvent("source", 2)); err != nil || queued {
		t.Fatalf("recovery requeued an acknowledged submission: %v %v", queued, err)
	}
	newReg := reg
	newReg.Generation++
	if _, _, err := s.register(newReg); err != nil {
		t.Fatal(err)
	}
	if queued, err := s.enqueue(testCallbackEvent("source", 3)); err != nil || !queued {
		t.Fatalf("new generation=%v %v", queued, err)
	}
	if count, err := s.acknowledgeCompletion(reg, time.Now()); err != nil || count != 0 {
		t.Fatalf("old generation affected new result: %d %v", count, err)
	}
	if pending, err := s.pendingSnapshot("source", "target"); err != nil || len(pending) != 1 || pending[0].Generation != 2 {
		t.Fatalf("new result lost: %+v %v", pending, err)
	}
}

func TestCloudCallbackFinalityRejectsIntermediateMessages(t *testing.T) {
	for _, tc := range []struct {
		name, role, channel, recipient, status, async string
		end                                           bool
		want                                          string
	}{
		{"final", "assistant", "final", "all", "finished_successfully", "", true, "completed"},
		{"old async completed with progress", "assistant", "commentary", "all", "finished_successfully", "completed", false, "unknown"},
		{"tool", "tool", "", "all", "finished_successfully", "", true, "unknown"},
		{"tool call", "assistant", "analysis", "python", "finished_successfully", "", false, "unknown"},
		{"analysis end", "assistant", "analysis", "all", "finished_successfully", "", true, "unknown"},
		{"user", "user", "", "all", "finished_successfully", "", true, "unknown"},
		{"async running wins", "assistant", "final", "all", "finished_successfully", "running", true, "running"},
		{"streaming final", "assistant", "final", "all", "in_progress", "completed", true, "running"},
		{"failed async beats old final", "assistant", "final", "all", "finished_successfully", "failed", true, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := map[string]any{"author": map[string]any{"role": tc.role}, "channel": tc.channel, "recipient": tc.recipient, "status": tc.status, "end_turn": tc.end}
			old := map[string]any{"author": map[string]any{"role": "assistant"}, "channel": "final", "status": "finished_successfully", "end_turn": true}
			detail := map[string]any{"async_status": tc.async, "current_node": "current", "mapping": map[string]any{"current": map[string]any{"message": message, "parent": "old"}, "old": map[string]any{"message": old}}}
			if got := chatgptCloudConversationStatus(detail); got != tc.want {
				t.Fatalf("status=%s want=%s", got, tc.want)
			}
		})
	}
}

func TestCallbackSubmissionSupersedesClaimedRecoveryAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s := newSessionCallbackStore(dir)
	if _, _, err := s.register(testCallbackRegistration("source", "target", "task", 1)); err != nil {
		t.Fatal(err)
	}
	e := testCallbackEvent("source", 1)
	if _, err := s.enqueue(e); err != nil {
		t.Fatal(err)
	}
	claim, _, err := s.claim("target", "", 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.Sequence = 2
	e.EventType = "hub-completion-notify"
	if queued, err := s.enqueue(e); err != nil || !queued {
		t.Fatalf("formal queued=%v err=%v", queued, err)
	}
	s = newSessionCallbackStore(dir)
	pending, err := s.pendingSnapshot("source", "target")
	if err != nil || len(pending) != 1 || pending[0].CompletionSource != "submission" || pending[0].ClaimID != "" {
		t.Fatalf("replacement=%+v err=%v", pending, err)
	}
	if _, err := s.acknowledgeClaim("target", claim, time.Now()); err == nil {
		t.Fatal("old claim acknowledged replacement")
	}
	e.Sequence = 3
	e.EventType = "conversation-turn-complete"
	if queued, err := s.enqueue(e); err != nil || queued {
		t.Fatalf("recovery overwrote formal: %v %v", queued, err)
	}
}

func TestRecoveryNudgeOnceButRemainsClaimable(t *testing.T) {
	s := newSessionCallbackStore(t.TempDir())
	if _, _, err := s.register(testCallbackRegistration("source", "target", "task", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.enqueue(testCallbackEvent("source", 1)); err != nil {
		t.Fatal(err)
	}
	grouped, _ := s.pendingForNudge()
	if err := s.recordNudge("target", sessionCallbackEnvelopeID("target", grouped["target"]), testAppServerCallbackDelivery(), time.Now()); err != nil {
		t.Fatal(err)
	}
	grouped, _ = s.pendingForNudge()
	if len(grouped) != 0 {
		t.Fatal("recovery will nudge again")
	}
	_, items, err := s.claim("target", "", 1, time.Now())
	if err != nil || len(items) != 1 {
		t.Fatalf("recovery receipt lost: %v %v", items, err)
	}
	e := testCallbackEvent("source", 2)
	e.EventType = "hub-completion-notify"
	if queued, err := s.enqueue(e); err != nil || !queued {
		t.Fatalf("submitted=%v %v", queued, err)
	}
	if due, _, err := s.nudgeSchedule("target", time.Now(), time.Hour); err != nil || !due {
		t.Fatalf("formal result delayed by recovery nudge: %v %v", due, err)
	}
}

func TestLegacyCallbackDedupCannotSwallowFormalSubmission(t *testing.T) {
	s := newSessionCallbackStore(t.TempDir())
	reg, _, err := s.register(testCallbackRegistration("source", "target", "task", 1))
	if err != nil {
		t.Fatal(err)
	}
	// Old versions used this same key for already-consumed failed observations.
	reg.LastEventKey = sessionCallbackCompletionEventKey(reg)
	reg.RecentEventKeys = []string{reg.LastEventKey}
	reg.LastEventSequence = 10
	s.registrations["source"] = reg
	e := testCallbackEvent("source", 11)
	e.EventType = "hub-completion-notify"
	if queued, err := s.enqueue(e); err != nil || !queued {
		t.Fatalf("formal swallowed by legacy key: %v %v", queued, err)
	}
	e.Sequence = 12
	e.CallbackOutcome = "failed"
	if _, err := s.enqueue(e); err == nil {
		t.Fatal("different formal result swallowed")
	}
}

func TestRecoveryClaimDuringDeliveryDoesNotRepeatNudge(t *testing.T) {
	s := newSessionCallbackStore(t.TempDir())
	if _, _, err := s.register(testCallbackRegistration("source", "target", "task", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.enqueue(testCallbackEvent("source", 1)); err != nil {
		t.Fatal(err)
	}
	grouped, _ := s.pendingForNudge()
	sent := grouped["target"]
	now := time.Now()
	if _, _, err := s.claim("target", "", 1, now); err != nil {
		t.Fatal(err)
	}
	if err := s.recordNudge("target", sessionCallbackEnvelopeID("target", sent), testAppServerCallbackDelivery(), now, sent...); err != nil {
		t.Fatal(err)
	}
	if _, err := s.releaseExpiredClaims(now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	grouped, _ = s.pendingForNudge()
	if len(grouped) != 0 {
		t.Fatal("concurrent claim caused a repeat recovery nudge")
	}
}
