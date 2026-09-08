package node

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	_ "modernc.org/sqlite"
)

func TestCollaborationControlIsLocalOnlyAndClosesDispatchReceipt(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "chat-target")
	createCollaborationTestLedger(t, dbPath, packet)
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	params := map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"expectedRevision": 1, "itemId": "task-1",
	}

	remote := client.handleCapabilityRequest(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "remote-collaboration", Capability: "collaboration.control", Action: "claim", Params: params,
	})
	if remote.Error == nil || remote.Error.Code != "UNSUPPORTED_CAPABILITY" {
		t.Fatalf("remote collaboration control=%#v", remote)
	}

	claimed := callCollaborationTest(t, client, "claim", params)
	token, _ := claimed["dispatchToken"].(string)
	if len(token) != 64 || claimed["packetSHA256"] == "" {
		t.Fatalf("claim=%#v", claimed)
	}
	dispatchRequest, _ := claimed["dispatchRequest"].(map[string]any)
	if dispatchRequest["action"] != "dispatch" || !collaborationMapsEqual(dispatchRequest["params"].(map[string]any), packet) {
		t.Fatalf("dispatch request=%#v", dispatchRequest)
	}

	verified := callCollaborationTest(t, client, "verify", map[string]any{"dispatchToken": token})
	if !collaborationMapsEqual(verified["dispatchRequest"].(map[string]any), dispatchRequest) {
		t.Fatalf("verify=%#v", verified)
	}
	receipt := map[string]any{"structuredContent": map[string]any{"result": map[string]any{
		"chatSessionId": "chat-target", "collaborationId": "collaboration-1", "taskRef": "task-ref-1",
		"callbackSessionId": "controller-1",
	}}}
	completed := callCollaborationTest(t, client, "receipt", map[string]any{"dispatchToken": token, "dispatchResult": receipt})
	if completed["phase"] != "active" {
		t.Fatalf("receipt=%#v", completed)
	}
	replayed := callCollaborationTest(t, client, "receipt", map[string]any{"dispatchToken": token, "dispatchResult": map[string]any{"invalid": true}})
	if !collaborationMapsEqual(completed, replayed) {
		t.Fatalf("completed token was not idempotent: first=%#v second=%#v", completed, replayed)
	}
	item := readCollaborationTestItem(t, dbPath)
	if item["phase"] != "active" || item["claim"] == nil {
		t.Fatalf("stored item=%#v", item)
	}
	binding, _ := item["binding"].(map[string]any)
	if binding["chatSessionId"] != "chat-target" || binding["idempotencyKey"] != packet["idempotencyKey"] {
		t.Fatalf("binding=%#v", binding)
	}
}

func TestCollaborationControlConcurrentReceiptsShareCompletedResult(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "chat-target")
	createCollaborationTestLedger(t, dbPath, packet)
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	claimed := callCollaborationTest(t, client, "claim", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"expectedRevision": 1, "itemId": "task-1",
	})
	token := claimed["dispatchToken"].(string)
	dispatchResult := map[string]any{"chatSessionId": "chat-target", "collaborationId": "collaboration-concurrent", "taskRef": "task-ref-concurrent", "callbackSessionId": "controller-1"}

	start := make(chan struct{})
	responses := make(chan protocolv1.CapabilityResponse, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			responses <- client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
				RequestId: "concurrent-receipt", Capability: "collaboration.control", Action: "receipt",
				Params: map[string]any{"dispatchToken": token, "dispatchResult": dispatchResult},
			})
		}()
	}
	close(start)
	first, second := <-responses, <-responses
	if first.Error != nil || second.Error != nil {
		t.Fatalf("concurrent receipts failed: first=%#v second=%#v", first, second)
	}
	delete(first.Result, "timing")
	delete(second.Result, "timing")
	if !collaborationMapsEqual(first.Result, second.Result) || first.Result["phase"] != "active" {
		t.Fatalf("concurrent receipt results diverged: first=%#v second=%#v", first.Result, second.Result)
	}
	completed, err := client.readCollaborationToken(token)
	if err != nil || completed.Completed == nil {
		t.Fatalf("completed receipt was not persisted: token=%#v err=%v", completed, err)
	}
	if !collaborationMapsEqual(completed.Completed, first.Result) {
		t.Fatalf("persisted completed receipt changed: persisted=%#v response=%#v", completed.Completed, first.Result)
	}
	if item := readCollaborationTestItem(t, dbPath); item["phase"] != "active" {
		t.Fatalf("concurrent receipt regressed ledger: %#v", item)
	}
}

func TestCollaborationControlReceiptAndUncertainDoNotRegress(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "chat-target")
	createCollaborationTestLedger(t, dbPath, packet)
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	claimed := callCollaborationTest(t, client, "claim", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"expectedRevision": 1, "itemId": "task-1",
	})
	token := claimed["dispatchToken"].(string)
	uncertain := callCollaborationTest(t, client, "uncertain", map[string]any{"dispatchToken": token, "evidenceRef": "fs:uncertain-first"})
	if uncertain["phase"] != "in_doubt" {
		t.Fatalf("uncertain transition=%#v", uncertain)
	}
	receipt := callCollaborationTest(t, client, "receipt", map[string]any{
		"dispatchToken":  token,
		"dispatchResult": map[string]any{"chatSessionId": "chat-target", "collaborationId": "collaboration-after-uncertain", "taskRef": "task-ref-after-uncertain", "callbackSessionId": "controller-1"},
	})
	if receipt["phase"] != "active" {
		t.Fatalf("receipt did not advance in-doubt state: %#v", receipt)
	}
	replayed := callCollaborationTest(t, client, "uncertain", map[string]any{"dispatchToken": token, "evidenceRef": "fs:late-uncertain"})
	if !collaborationMapsEqual(receipt, replayed) {
		t.Fatalf("late uncertain receipt overwrote completed active result: receipt=%#v late=%#v", receipt, replayed)
	}
	if item := readCollaborationTestItem(t, dbPath); item["phase"] != "active" {
		t.Fatalf("receipt/uncertain sequence regressed ledger: %#v", item)
	}
}

func TestCollaborationControlReceiptAndNotCreatedDoNotOverwriteCompletedState(t *testing.T) {
	for _, firstAction := range []string{"not_created", "receipt"} {
		t.Run(firstAction+"-first", func(t *testing.T) {
			root := t.TempDir()
			dbPath := filepath.Join(root, "collaboration.sqlite3")
			packet := collaborationTestPacket(root, "chat-target")
			createCollaborationTestLedger(t, dbPath, packet)
			client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
			claimed := callCollaborationTest(t, client, "claim", map[string]any{
				"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
				"expectedRevision": 1, "itemId": "task-1",
			})
			token := claimed["dispatchToken"].(string)
			receiptParams := map[string]any{
				"dispatchToken":  token,
				"dispatchResult": map[string]any{"chatSessionId": "chat-target", "collaborationId": "collaboration-order", "taskRef": "task-ref-order", "callbackSessionId": "controller-1"},
			}
			notCreatedParams := map[string]any{"dispatchToken": token, "noTaskCreated": true, "evidenceRef": "fs:not-created-order"}

			var first, second map[string]any
			if firstAction == "not_created" {
				first = callCollaborationTest(t, client, "not_created", notCreatedParams)
				second = callCollaborationTest(t, client, "receipt", receiptParams)
				if first["phase"] != "dispatch_rejected" || second["phase"] != "dispatch_rejected" {
					t.Fatalf("not_created then receipt changed terminal state: first=%#v second=%#v", first, second)
				}
			} else {
				first = callCollaborationTest(t, client, "receipt", receiptParams)
				second = callCollaborationTest(t, client, "not_created", notCreatedParams)
				if first["phase"] != "active" || second["phase"] != "active" {
					t.Fatalf("receipt then not_created changed active state: first=%#v second=%#v", first, second)
				}
			}
			completed, err := client.readCollaborationToken(token)
			if err != nil || completed.Completed == nil {
				t.Fatalf("terminal result was not persisted: token=%#v err=%v", completed, err)
			}
			if !collaborationMapsEqual(completed.Completed, second) {
				t.Fatalf("terminal result was overwritten: persisted=%#v second=%#v", completed.Completed, second)
			}
		})
	}
}

func TestCollaborationControlSerializesClaimsAndPreservesUncertainPacket(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "chat-target")
	createCollaborationTestLedger(t, dbPath, packet)
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	params := map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"expectedRevision": 1, "itemId": "task-1",
	}

	var wg sync.WaitGroup
	responses := make(chan protocolv1.CapabilityResponse, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
				RequestId: "claim-race", Capability: "collaboration.control", Action: "claim", Params: params,
			})
		}()
	}
	wg.Wait()
	close(responses)
	var claimed map[string]any
	var success, rejected int
	for response := range responses {
		if response.Error != nil {
			rejected++
			continue
		}
		success++
		claimed = response.Result
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("claim race success=%d rejected=%d", success, rejected)
	}
	token := claimed["dispatchToken"].(string)
	uncertain := callCollaborationTest(t, client, "receipt", map[string]any{
		"dispatchToken":  token,
		"dispatchResult": map[string]any{"structuredContent": map[string]any{"result": map[string]any{"deliveryInDoubt": true}}},
	})
	if uncertain["phase"] != "in_doubt" || uncertain["dispatchToken"] != token {
		t.Fatalf("uncertain=%#v", uncertain)
	}
	recovered := callCollaborationTest(t, client, "recover", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1",
	})
	if recovered["dispatchToken"] != token || !collaborationMapsEqual(recovered["dispatchRequest"].(map[string]any), claimed["dispatchRequest"].(map[string]any)) {
		t.Fatalf("recover=%#v claimed=%#v", recovered, claimed)
	}
}

func TestCollaborationControlRejectsReceiptIdentityDrift(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "chat-target")
	createCollaborationTestLedger(t, dbPath, packet)
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	claimed := callCollaborationTest(t, client, "claim", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"expectedRevision": 1, "itemId": "task-1",
	})
	token := claimed["dispatchToken"].(string)
	response := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "receipt-drift", Capability: "collaboration.control", Action: "receipt",
		Params: map[string]any{"dispatchToken": token, "dispatchResult": map[string]any{
			"chatSessionId": "wrong-chat", "collaborationId": "collaboration-1", "taskRef": "task-ref-1",
		}},
	})
	if response.Error == nil {
		t.Fatalf("drifted receipt succeeded: %#v", response.Result)
	}
	item := readCollaborationTestItem(t, dbPath)
	if item["phase"] != "dispatching" || item["binding"] != nil {
		t.Fatalf("drifted receipt changed item=%#v", item)
	}

	missingRevision := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "missing-revision", Capability: "collaboration.control", Action: "claim",
		Params: map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"},
	})
	if missingRevision.Error == nil {
		t.Fatal("missing expectedRevision was accepted")
	}
}

func TestCollaborationControlRecordsConfirmedNoCreate(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "chat-target")
	createCollaborationTestLedger(t, dbPath, packet)
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	claimed := callCollaborationTest(t, client, "claim", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"expectedRevision": 1, "itemId": "task-1",
	})
	token := claimed["dispatchToken"].(string)

	rejected := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "not-created-without-proof", Capability: "collaboration.control", Action: "not_created",
		Params: map[string]any{"dispatchToken": token, "noTaskCreated": false, "evidenceRef": "fs:request-rejected"},
	})
	if rejected.Error == nil {
		t.Fatal("not_created without an explicit confirmation was accepted")
	}
	if item := readCollaborationTestItem(t, dbPath); item["phase"] != "dispatching" {
		t.Fatalf("rejected not_created changed item=%#v", item)
	}

	completed := callCollaborationTest(t, client, "not_created", map[string]any{
		"dispatchToken": token, "noTaskCreated": true, "evidenceRef": "fs:request-rejected",
	})
	if completed["phase"] != "dispatch_rejected" || completed["evidenceRef"] != "fs:request-rejected" {
		t.Fatalf("not_created=%#v", completed)
	}
	item := readCollaborationTestItem(t, dbPath)
	if item["phase"] != "dispatch_rejected" || item["terminal_ref"] != "fs:request-rejected" {
		t.Fatalf("stored rejected item=%#v", item)
	}
	evidence := collaborationStringList(item["evidence"])
	if len(evidence) != 1 || evidence[0] != "fs:request-rejected" {
		t.Fatalf("stored evidence=%#v", evidence)
	}
	replayed := callCollaborationTest(t, client, "not_created", map[string]any{
		"dispatchToken": token, "noTaskCreated": true, "evidenceRef": "fs:request-rejected",
	})
	if !collaborationMapsEqual(completed, replayed) {
		t.Fatalf("completed not_created token was not idempotent: first=%#v second=%#v", completed, replayed)
	}
}

func TestCollaborationControlRecoverRequiresActiveBoundCoordinator(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "chat-target")
	createCollaborationTestLedger(t, dbPath, packet)
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	claimed := callCollaborationTest(t, client, "claim", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"expectedRevision": 1, "itemId": "task-1",
	})
	if claimed["dispatchToken"] == "" {
		t.Fatalf("claim=%#v", claimed)
	}

	foreign := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "recover-as-controller", Capability: "collaboration.control", Action: "recover",
		Params: map[string]any{
			"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "controller-1", "itemId": "task-1",
		},
	})
	if foreign.Error == nil {
		t.Fatalf("controller recovered coordinator dispatch=%#v", foreign.Result)
	}

	setCollaborationTestMissionDispatch(t, dbPath, "paused", false)
	paused := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "recover-while-paused", Capability: "collaboration.control", Action: "recover",
		Params: map[string]any{
			"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1",
		},
	})
	if paused.Error == nil {
		t.Fatalf("paused mission returned a dispatch request=%#v", paused.Result)
	}
}

func TestCollaborationTokenCleanupRemovesOnlyExpiredRecords(t *testing.T) {
	root := t.TempDir()
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	expiredToken := strings.Repeat("1", 64)
	currentToken := strings.Repeat("2", 64)
	expiredPath, err := client.collaborationTokenPath(expiredToken)
	if err != nil {
		t.Fatal(err)
	}
	expiredRaw, _ := json.Marshal(collaborationToken{Version: collaborationTokenVersion, Completed: map[string]any{"phase": "active"}, ExpiresAt: time.Now().Add(-time.Minute).Unix()})
	if err := os.WriteFile(expiredPath, expiredRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	current := collaborationToken{Version: collaborationTokenVersion, Completed: map[string]any{"phase": "active"}, ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if err := client.writeCollaborationToken(currentToken, current); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(expiredPath); !os.IsNotExist(err) {
		t.Fatalf("expired token still exists: %v", err)
	}
	currentPath, err := client.collaborationTokenPath(currentToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(currentPath); err != nil {
		t.Fatalf("current token was removed: %v", err)
	}
}

func TestCollaborationControlLocalDispatchCreatesAndArmsCloudChat(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "")
	createCollaborationTestLedger(t, dbPath, packet)
	agent := &collaborationTestAgent{results: map[string]map[string]any{"session.create": {"sessionId": "cloud-local-1"}}}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Agent: agent})
	claimed := callCollaborationTest(t, client, "claim", map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "expectedRevision": 1, "itemId": "task-1"})
	completed := callCollaborationTest(t, client, "dispatch", map[string]any{"dispatchToken": claimed["dispatchToken"]})
	if completed["phase"] != "active" || completed["callerShouldYield"] != true || completed["activePollingAllowed"] != false {
		t.Fatalf("local dispatch=%#v", completed)
	}
	if !agent.hasAction("session.create") || !agent.hasAction("session.callback.register") || !agent.hasAction("session.callback.arm") {
		t.Fatalf("local dispatch calls=%v", agent.actions)
	}
	item := readCollaborationTestItem(t, dbPath)
	if item["phase"] != "active" {
		t.Fatalf("stored item=%#v", item)
	}
}

func TestCollaborationControlDispatchClaimsIdentityInOneLocalCall(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "")
	createCollaborationTestLedger(t, dbPath, packet)
	agent := &collaborationTestAgent{results: map[string]map[string]any{"session.create": {"sessionId": "cloud-direct-1"}}}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Agent: agent})
	result, err := client.collaborationControl(context.Background(), "dispatch", map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"})
	if err != nil || result["phase"] != "active" {
		t.Fatalf("direct dispatch=%#v err=%v", result, err)
	}
	if !agent.hasAction("session.create") || readCollaborationTestItem(t, dbPath)["phase"] != "active" {
		t.Fatalf("direct dispatch did not claim and complete: calls=%v", agent.actions)
	}
}

func TestCollaborationControlDispatchIdentityRetryReplaysActiveReceipt(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "")
	createCollaborationTestLedger(t, dbPath, packet)
	agent := &collaborationTestAgent{results: map[string]map[string]any{"session.create": {"sessionId": "cloud-identity-replay-1"}}}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Agent: agent})
	identity := map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"}

	first := callCollaborationTest(t, client, "dispatch", identity)
	second := callCollaborationTest(t, client, "dispatch", identity)
	if !collaborationMapsEqual(first, second) {
		t.Fatalf("identity retry changed active receipt: first=%#v second=%#v", first, second)
	}
	if agent.actionCount("session.create") != 1 || agent.actionCount("session.send") != 0 {
		t.Fatalf("identity retry repeated provider work: %v", agent.actions)
	}
}

func TestCollaborationControlDispatchIdentityRetryReusesInDoubtToken(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "cloud-existing-in-doubt-1")
	createCollaborationTestLedger(t, dbPath, packet)
	agent := &collaborationTestAgent{errors: map[string]error{"session.callback.register": errors.New("callback register unavailable")}}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Agent: agent})
	identity := map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"}

	first := callCollaborationTest(t, client, "dispatch", identity)
	second := callCollaborationTest(t, client, "dispatch", identity)
	if first["phase"] != "in_doubt" || second["phase"] != "in_doubt" || first["dispatchToken"] != second["dispatchToken"] {
		t.Fatalf("identity retry changed in-doubt state: first=%#v second=%#v", first, second)
	}
	if agent.actionCount("session.create") != 0 || agent.actionCount("session.send") != 0 {
		t.Fatalf("in-doubt identity retry created or sent a second task: %v", agent.actions)
	}
}

func TestCollaborationControlDispatchIdentityRetryUsesExistingDispatchingClaim(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "")
	createCollaborationTestLedger(t, dbPath, packet)
	agent := &collaborationTestAgent{results: map[string]map[string]any{"session.create": {"sessionId": "cloud-dispatching-retry-1"}}}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Agent: agent})
	claim := callCollaborationTest(t, client, "claim", map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "expectedRevision": 1, "itemId": "task-1"})
	identity := map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"}

	retried := callCollaborationTest(t, client, "dispatch", identity)
	if retried["phase"] != "active" {
		t.Fatalf("dispatching identity retry did not reuse claim: claim=%#v retry=%#v", claim, retried)
	}
	item := readCollaborationTestItem(t, dbPath)
	if item["claim"] != claim["claim"] {
		t.Fatalf("dispatching identity retry changed claim: item=%#v claim=%#v", item, claim)
	}
	if agent.actionCount("session.create") != 1 {
		t.Fatalf("dispatching identity retry created %d CHATs", agent.actionCount("session.create"))
	}
}

func TestCollaborationControlConcurrentDirectDispatchesReuseClaimAndReceipt(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "")
	createCollaborationTestLedger(t, dbPath, packet)
	agent := &collaborationTestAgent{results: map[string]map[string]any{"session.create": {"sessionId": "cloud-concurrent-direct-1"}}}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Agent: agent})
	identity := map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"}

	var barrierMu sync.Mutex
	arrived := 0
	release := make(chan struct{})
	collaborationDirectReadyHook = func() {
		barrierMu.Lock()
		arrived++
		if arrived == 2 {
			close(release)
		}
		barrierMu.Unlock()
		<-release
	}
	defer func() { collaborationDirectReadyHook = nil }()

	responses := make(chan protocolv1.CapabilityResponse, 2)
	for i := 0; i < 2; i++ {
		go func() {
			responses <- client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
				RequestId: "concurrent-direct-dispatch", Capability: "collaboration.control", Action: "dispatch", Params: identity,
			})
		}()
	}
	first, second := <-responses, <-responses
	if first.Error != nil || second.Error != nil {
		t.Fatalf("concurrent direct dispatch failed: first=%#v second=%#v", first, second)
	}
	delete(first.Result, "timing")
	delete(second.Result, "timing")
	if !collaborationMapsEqual(first.Result, second.Result) || first.Result["phase"] != "active" {
		t.Fatalf("concurrent direct dispatches diverged: first=%#v second=%#v", first.Result, second.Result)
	}
	if agent.actionCount("session.create") != 1 || agent.actionCount("session.callback.register") != 1 || agent.actionCount("session.callback.arm") != 1 {
		t.Fatalf("concurrent direct dispatch repeated provider/callback work: %v", agent.actions)
	}
	collaborationDispatchLocks.Lock()
	defer collaborationDispatchLocks.Unlock()
	if len(collaborationDispatchLocks.locks) != 0 {
		t.Fatalf("per-token dispatch lock was not cleaned up: %d locks remain", len(collaborationDispatchLocks.locks))
	}
}

func TestCollaborationControlReadinessFailureRejectsBeforeCreate(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "")
	createCollaborationTestLedger(t, dbPath, packet)
	agent := &collaborationTestAgent{errors: map[string]error{"provider.readiness": errors.New("not ready")}}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Agent: agent})
	result := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{RequestId: "dispatch-ready", Capability: "collaboration.control", Action: "dispatch", Params: map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"}})
	if result.Error != nil || result.Result["phase"] != "dispatch_rejected" {
		t.Fatalf("readiness result=%#v code=%v message=%v", result, result.Error.Code, result.Error.Message)
	}
	if agent.hasAction("session.create") || agent.hasAction("session.send") {
		t.Fatalf("Cloud action ran after readiness failure: %v", agent.actions)
	}
}

func TestCollaborationControlCreateInDoubtNeverBecomesRejected(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "")
	createCollaborationTestLedger(t, dbPath, packet)
	agent := &collaborationTestAgent{errors: map[string]error{"session.create": collaborationCreateInDoubtError{}}}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Agent: agent})
	identity := map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"}

	first := callCollaborationTest(t, client, "dispatch", identity)
	second := callCollaborationTest(t, client, "dispatch_recover", identity)
	if first["phase"] != "in_doubt" || second["phase"] != "in_doubt" {
		t.Fatalf("create in doubt was not preserved: first=%#v second=%#v", first, second)
	}
	if item := readCollaborationTestItem(t, dbPath); item["phase"] != "in_doubt" || item["terminal_ref"] != nil {
		t.Fatalf("create in doubt became terminal: %#v", item)
	}
	if agent.actionCount("session.create") != 2 {
		t.Fatalf("same-key recovery create calls=%v", agent.actions)
	}
}

func TestCollaborationControlMissingCreateIdentityStaysInDoubt(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "")
	createCollaborationTestLedger(t, dbPath, packet)
	agent := &collaborationTestAgent{results: map[string]map[string]any{"session.create": {}}}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Agent: agent})
	identity := map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"}

	uncertain := callCollaborationTest(t, client, "dispatch", identity)
	if uncertain["phase"] != "in_doubt" || agent.actionCount("session.create") != 1 {
		t.Fatalf("missing create identity=%#v calls=%v", uncertain, agent.actions)
	}
	if agent.hasAction("session.callback.register") || agent.hasAction("session.callback.arm") {
		t.Fatalf("callback was registered without a CHAT identity: %v", agent.actions)
	}
	recovered := callCollaborationTest(t, client, "dispatch_recover", identity)
	if recovered["phase"] != "in_doubt" || agent.actionCount("session.create") != 2 {
		t.Fatalf("missing identity recovery=%#v calls=%v", recovered, agent.actions)
	}
	var creates []collaborationTestCall
	for _, call := range agent.calls {
		if call.action == "session.create" {
			creates = append(creates, call)
		}
	}
	if len(creates) != 2 || creates[0].params["idempotencyKey"] != creates[1].params["idempotencyKey"] || creates[0].params["prompt"] != creates[1].params["prompt"] {
		t.Fatalf("create recovery drifted: %#v", creates)
	}
}

func TestCollaborationControlBootstrapIncludesLocalFileContract(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "")
	packet["callbackType"] = "local_file"
	createCollaborationTestLedger(t, dbPath, packet)
	agent := &collaborationTestAgent{results: map[string]map[string]any{"session.create": {"sessionId": "cloud-bootstrap-1"}}}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Agent: agent})
	callCollaborationTest(t, client, "dispatch", map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"})
	resolvedRoot, err := ResolveMachinePath(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range agent.calls {
		if call.action == "session.create" {
			prompt, _ := call.params["prompt"].(string)
			for _, required := range []string{
				"FAST_SPIDER_LOCAL_COLLABORATION_V1",
				"MACHINE_ID: machine-1",
				"WORKING_DIRECTORY: " + resolvedRoot,
				"ACCESS_MODE: write",
				"WRITE_SCOPE: src/task",
				"CALLBACK_TYPE: local_file",
				"DELIVERABLE_PATH: " + resolvedRoot,
				"TASK:\nImplement and test the bounded task.",
				"Do not call remote task_result_submit",
				"completed work, blockers, and validation evidence",
			} {
				if !strings.Contains(prompt, required) {
					t.Fatalf("bootstrap missing %q: %q", required, prompt)
				}
			}
			return
		}
	}
	t.Fatal("session.create call was not recorded")
}

func TestCollaborationControlLocalDispatchReusesTargetAndPreservesTokenState(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "cloud-existing-1")
	createCollaborationTestLedger(t, dbPath, packet)
	agent := &collaborationTestAgent{results: map[string]map[string]any{}}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Agent: agent})
	claimed := callCollaborationTest(t, client, "claim", map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "expectedRevision": 1, "itemId": "task-1"})
	completed := callCollaborationTest(t, client, "dispatch", map[string]any{"dispatchToken": claimed["dispatchToken"]})
	if completed["phase"] != "active" {
		t.Fatalf("reuse dispatch=%#v", completed)
	}
	if agent.hasAction("session.create") || !agent.hasAction("session.send") {
		t.Fatalf("reuse dispatch calls=%v", agent.actions)
	}
	for _, call := range agent.calls {
		if call.action == "session.callback.register" && call.params["callbackClaimTransport"] != "local" {
			t.Fatalf("callback transport=%#v", call.params)
		}
	}
}

func TestCollaborationControlRecoverReusedSessionRegisterFailure(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "cloud-existing-register-1")
	createCollaborationTestLedger(t, dbPath, packet)
	agent := &collaborationTestAgent{errors: map[string]error{"session.callback.register": errors.New("callback register unavailable")}}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Agent: agent})
	identity := map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"}

	uncertain := callCollaborationTest(t, client, "dispatch", identity)
	if uncertain["phase"] != "in_doubt" || agent.actionCount("session.create") != 0 || agent.actionCount("session.send") != 0 {
		t.Fatalf("reuse register failure=%#v calls=%v", uncertain, agent.actions)
	}
	delete(agent.errors, "session.callback.register")
	completed := callCollaborationTest(t, client, "dispatch_recover", identity)
	if completed["phase"] != "active" {
		t.Fatalf("reuse register recovery=%#v", completed)
	}
	if agent.actionCount("session.create") != 0 || agent.actionCount("session.callback.register") != 2 || agent.actionCount("session.send") != 1 || agent.actionCount("session.callback.arm") != 1 {
		t.Fatalf("reuse register recovery calls=%v", agent.actions)
	}
}

func TestCollaborationControlRecoverKeepsCreatedSessionIdentity(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "")
	createCollaborationTestLedger(t, dbPath, packet)
	agent := &collaborationTestAgent{
		results: map[string]map[string]any{"session.create": {"sessionId": "cloud-recover-1"}},
		errors:  map[string]error{"session.callback.register": errors.New("callback store unavailable")},
	}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Agent: agent})
	identity := map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"}
	uncertain := callCollaborationTest(t, client, "dispatch", identity)
	if uncertain["phase"] != "in_doubt" {
		t.Fatalf("dispatch=%#v", uncertain)
	}
	recovered := callCollaborationTest(t, client, "recover", map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"})
	if recovered["dispatchToken"] != uncertain["dispatchToken"] {
		t.Fatalf("recover changed token=%#v uncertain=%#v", recovered, uncertain)
	}
	delete(agent.errors, "session.callback.register")
	completed := callCollaborationTest(t, client, "dispatch_recover", identity)
	if completed["phase"] != "active" {
		t.Fatalf("dispatch_recover=%#v", completed)
	}
	binding, _ := completed["binding"].(map[string]any)
	if binding["chatSessionId"] != "cloud-recover-1" {
		t.Fatalf("recovery created a second CHAT: %#v", binding)
	}
	if agent.actionCount("session.create") != 1 {
		t.Fatalf("recovery created %d CHATs", agent.actionCount("session.create"))
	}
}

func TestCollaborationControlRecoverRetriesUncertainSendWithSameKey(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "cloud-existing-1")
	createCollaborationTestLedger(t, dbPath, packet)
	agent := &collaborationTestAgent{errors: map[string]error{"session.send": context.DeadlineExceeded}}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Agent: agent})
	identity := map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"}

	uncertain := callCollaborationTest(t, client, "dispatch", identity)
	if uncertain["phase"] != "in_doubt" || agent.actionCount("session.callback.arm") != 1 {
		t.Fatalf("uncertain send=%#v calls=%v", uncertain, agent.actions)
	}
	delete(agent.errors, "session.send")
	completed := callCollaborationTest(t, client, "dispatch_recover", identity)
	if completed["phase"] != "active" {
		t.Fatalf("dispatch_recover=%#v", completed)
	}
	if agent.actionCount("session.send") != 2 || agent.actionCount("session.create") != 0 || agent.actionCount("session.callback.arm") != 1 {
		t.Fatalf("send reconciliation calls=%v", agent.actions)
	}
	var sends []collaborationTestCall
	for _, call := range agent.calls {
		if call.action == "session.send" {
			sends = append(sends, call)
		}
	}
	if len(sends) != 2 || sends[0].params["idempotencyKey"] != sends[1].params["idempotencyKey"] || sends[0].params["prompt"] != sends[1].params["prompt"] {
		t.Fatalf("send retry drifted: %#v", sends)
	}
}

func TestCollaborationControlRecoverCleansRejectedSendWithoutResending(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "cloud-existing-1")
	createCollaborationTestLedger(t, dbPath, packet)
	agent := &collaborationTestAgent{errors: map[string]error{
		"session.send":                testAgentCapabilityError{},
		"session.callback.unregister": errors.New("callback unregister unavailable"),
	}}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Agent: agent})
	identity := map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"}

	uncertain := callCollaborationTest(t, client, "dispatch", identity)
	if uncertain["phase"] != "in_doubt" || agent.actionCount("session.send") != 1 {
		t.Fatalf("rejected send cleanup=%#v calls=%v", uncertain, agent.actions)
	}
	delete(agent.errors, "session.callback.unregister")
	completed := callCollaborationTest(t, client, "dispatch_recover", identity)
	if completed["phase"] != "dispatch_rejected" {
		t.Fatalf("rejected send recovery=%#v", completed)
	}
	if agent.actionCount("session.send") != 1 || agent.actionCount("session.callback.unregister") != 2 {
		t.Fatalf("rejected send was resent: %v", agent.actions)
	}
}

func TestCollaborationControlRecoverRetriesArmWithoutSecondCreate(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "")
	createCollaborationTestLedger(t, dbPath, packet)
	agent := &collaborationTestAgent{
		results: map[string]map[string]any{"session.create": {"sessionId": "cloud-arm-recover-1"}},
		errors:  map[string]error{"session.callback.arm": errors.New("callback arm unavailable")},
	}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data"), Agent: agent})
	identity := map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"}

	uncertain := callCollaborationTest(t, client, "dispatch", identity)
	if uncertain["phase"] != "in_doubt" {
		t.Fatalf("arm failure=%#v", uncertain)
	}
	delete(agent.errors, "session.callback.arm")
	completed := callCollaborationTest(t, client, "dispatch_recover", identity)
	if completed["phase"] != "active" {
		t.Fatalf("arm recovery=%#v", completed)
	}
	if agent.actionCount("session.create") != 1 || agent.actionCount("session.callback.register") != 1 || agent.actionCount("session.callback.arm") != 2 {
		t.Fatalf("arm recovery calls=%v", agent.actions)
	}
}

func TestCollaborationControlDispatchRecoverRejectsBareToken(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "")
	createCollaborationTestLedger(t, dbPath, packet)
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	claimed := callCollaborationTest(t, client, "claim", map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "expectedRevision": 1, "itemId": "task-1"})
	if _, err := client.collaborationControl(context.Background(), "dispatch_recover", map[string]any{"dispatchToken": claimed["dispatchToken"]}); err == nil {
		t.Fatal("dispatch_recover accepted a bare token without checking the ledger recovery phase")
	}
}

func TestCollaborationControlCompletedTokenStillHonorsProjectBoundary(t *testing.T) {
	projectRoot := t.TempDir()
	outsideRoot := t.TempDir()
	outsideDB := filepath.Join(outsideRoot, "collaboration.sqlite3")
	if err := os.WriteFile(outsideDB, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(projectRoot, "node-data"), ProjectRoot: projectRoot})
	tokenID := strings.Repeat("a", 64)
	if err := client.writeCollaborationToken(tokenID, collaborationToken{
		Version: collaborationTokenVersion, DBPath: outsideDB, MissionID: "mission-1", ActorSessionID: "coordinator-1",
		ItemID: "task-1", Claim: "claim-1", PacketSHA256: "sha256:" + strings.Repeat("b", 64),
		Completed: map[string]any{"phase": "active"}, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.collaborationControl(context.Background(), "dispatch", map[string]any{"dispatchToken": tokenID}); !errors.Is(err, ErrProjectPathForbidden) {
		t.Fatalf("completed token outside project error=%v", err)
	}
}

func TestCollaborationControlRejectsCorruptCompletedTokenBeforeShortCircuit(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "")
	createCollaborationTestLedger(t, dbPath, packet)
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node-data")})
	claimed := callCollaborationTest(t, client, "claim", map[string]any{
		"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
		"expectedRevision": 1, "itemId": "task-1",
	})
	tokenID := claimed["dispatchToken"].(string)
	record, err := client.readCollaborationToken(tokenID)
	if err != nil {
		t.Fatal(err)
	}
	record.Completed = map[string]any{"phase": "active"}

	t.Run("missing identity", func(t *testing.T) {
		corrupt := record
		corrupt.ActorSessionID = ""
		if err := client.writeCollaborationToken(tokenID, corrupt); err != nil {
			t.Fatal(err)
		}
		if _, err := client.collaborationControl(context.Background(), "dispatch", map[string]any{"dispatchToken": tokenID}); err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("missing completed identity was accepted: %v", err)
		}
	})

	t.Run("token identity", func(t *testing.T) {
		alias := strings.Repeat("c", 64)
		if err := client.writeCollaborationToken(alias, record); err != nil {
			t.Fatal(err)
		}
		if _, err := client.collaborationControl(context.Background(), "dispatch", map[string]any{"dispatchToken": alias}); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("completed token id drift was accepted: %v", err)
		}
	})

	t.Run("packet digest", func(t *testing.T) {
		corrupt := record
		corrupt.PacketSHA256 = "sha256:" + strings.Repeat("0", 64)
		if err := client.writeCollaborationToken(tokenID, corrupt); err != nil {
			t.Fatal(err)
		}
		if _, err := client.collaborationControl(context.Background(), "dispatch", map[string]any{"dispatchToken": tokenID}); err == nil || !strings.Contains(err.Error(), "digest") {
			t.Fatalf("completed packet digest drift was accepted: %v", err)
		}
	})

	t.Run("dispatch request", func(t *testing.T) {
		corrupt := record
		corrupt.DispatchRequest = map[string]any{"action": "dispatch"}
		if err := client.writeCollaborationToken(tokenID, corrupt); err != nil {
			t.Fatal(err)
		}
		if _, err := client.collaborationControl(context.Background(), "dispatch", map[string]any{"dispatchToken": tokenID}); err == nil || !strings.Contains(err.Error(), "dispatchRequest") {
			t.Fatalf("completed dispatchRequest corruption was accepted: %v", err)
		}
	})
}

func TestCollaborationControlRejectsFrozenPathsOutsideTheirBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		packet func(projectRoot, outsideRoot string) map[string]any
	}{
		{
			name: "working directory outside project",
			packet: func(_ string, outsideRoot string) map[string]any {
				return collaborationTestPacket(outsideRoot, "")
			},
		},
		{
			name: "write scope outside working directory",
			packet: func(projectRoot, outsideRoot string) map[string]any {
				packet := collaborationTestPacket(projectRoot, "")
				packet["writeScope"] = outsideRoot
				return packet
			},
		},
		{
			name: "deliverable outside write scope",
			packet: func(projectRoot, _ string) map[string]any {
				packet := collaborationTestPacket(projectRoot, "")
				packet["writeScope"] = "src/task"
				packet["callbackType"] = "local_file"
				packet["deliverablePath"] = filepath.Join(projectRoot, "reports", "result.md")
				return packet
			},
		},
		{
			name: "read only explicit deliverable",
			packet: func(projectRoot, _ string) map[string]any {
				packet := collaborationTestPacket(projectRoot, "")
				packet["accessMode"] = "read_only"
				delete(packet, "writeScope")
				packet["callbackType"] = "local_file"
				packet["deliverablePath"] = filepath.Join(projectRoot, "report.md")
				return packet
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot := t.TempDir()
			outsideRoot := t.TempDir()
			dbPath := filepath.Join(projectRoot, "collaboration.sqlite3")
			packet := tc.packet(projectRoot, outsideRoot)
			createCollaborationTestLedger(t, dbPath, packet)
			agent := &collaborationTestAgent{}
			client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(projectRoot, "node-data"), ProjectRoot: projectRoot, Agent: agent})
			response := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
				RequestId: "frozen-path-boundary", Capability: "collaboration.control", Action: "dispatch",
				Params: map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"},
			})
			if response.Error == nil {
				t.Fatalf("unsafe packet was dispatched: %#v", response.Result)
			}
			if len(agent.actions) != 0 {
				t.Fatalf("provider was called before path rejection: %v", agent.actions)
			}
		})
	}
}

func TestCollaborationControlRejectsFrozenMachineMismatch(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "node-data")
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "")
	createCollaborationTestLedger(t, dbPath, packet)
	client := NewLocalCapabilityClient(Config{DataDir: dataDir, Agent: &collaborationTestAgent{}})
	if err := SaveState(client.statePath, State{
		HubURL: "https://hub.example", MachineID: "machine-other", CredentialID: "credential-1",
		HubPublicKey: "public-key", HubFingerprint: "sha256:fingerprint",
	}); err != nil {
		t.Fatal(err)
	}
	response := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "frozen-machine-mismatch", Capability: "collaboration.control", Action: "dispatch",
		Params: map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"},
	})
	if response.Error == nil || response.Error.Code != "INVALID_REQUEST" {
		t.Fatalf("machine mismatch response=%#v", response)
	}
}

func TestCollaborationControlRecoversActiveLedgerAfterCompletedTokenWriteFailure(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "node-data")
	dbPath := filepath.Join(root, "collaboration.sqlite3")
	packet := collaborationTestPacket(root, "")
	createCollaborationTestLedger(t, dbPath, packet)
	agent := &collaborationTestAgent{results: map[string]map[string]any{"session.create": {"sessionId": "cloud-active-repair-1"}}}
	client := NewLocalCapabilityClient(Config{DataDir: dataDir, Agent: agent})
	injected := errors.New("injected completed token write failure")
	client.beforeCollaborationTokenWriteOverride = func(record collaborationToken) error {
		if record.Completed != nil {
			return injected
		}
		return nil
	}
	identity := map[string]any{"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-1"}
	failed := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "active-token-failure", Capability: "collaboration.control", Action: "dispatch", Params: identity,
	})
	if failed.Error == nil {
		t.Fatalf("completed token failure=%#v", failed)
	}
	item := readCollaborationTestItem(t, dbPath)
	if item["phase"] != "active" {
		t.Fatalf("ledger was not committed before token failure: %#v", item)
	}

	restartedAgent := &collaborationTestAgent{}
	restarted := NewLocalCapabilityClient(Config{DataDir: dataDir, Agent: restartedAgent})
	identityRepaired := callCollaborationTest(t, restarted, "dispatch", identity)
	if identityRepaired["phase"] != "active" || identityRepaired["replayed"] != true {
		t.Fatalf("active receipt was not repaired by identity retry: %#v", identityRepaired)
	}
	if len(restartedAgent.actions) != 0 {
		t.Fatalf("active receipt repair repeated provider work: %v", restartedAgent.actions)
	}
	repaired := callCollaborationTest(t, restarted, "dispatch_recover", identity)
	if !collaborationMapsEqual(identityRepaired, repaired) {
		t.Fatalf("explicit recovery changed identity-repaired receipt: identity=%#v recover=%#v", identityRepaired, repaired)
	}
	binding, _ := repaired["binding"].(map[string]any)
	if binding["chatSessionId"] != "cloud-active-repair-1" {
		t.Fatalf("repaired binding=%#v", binding)
	}
	retried := callCollaborationTest(t, restarted, "dispatch", identity)
	if !collaborationMapsEqual(repaired, retried) {
		t.Fatalf("identity retry changed repaired active receipt: repaired=%#v retry=%#v", repaired, retried)
	}
	if len(restartedAgent.actions) != 0 {
		t.Fatalf("identity retry repeated provider work after receipt repair: %v", restartedAgent.actions)
	}
}

type collaborationTestCall struct {
	action string
	params map[string]any
}

type collaborationCreateInDoubtError struct{}

func (collaborationCreateInDoubtError) Error() string { return "prior create remains unresolved" }
func (collaborationCreateInDoubtError) CapabilityError() (string, string, bool) {
	return "AGENT_CREATE_IN_DOUBT", "prior create remains unresolved", false
}

type collaborationTestAgent struct {
	mu      sync.Mutex
	actions []string
	calls   []collaborationTestCall
	results map[string]map[string]any
	errors  map[string]error
}

func (a *collaborationTestAgent) Control(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.actions = append(a.actions, action)
	a.calls = append(a.calls, collaborationTestCall{action: action, params: params})
	if err := a.errors[action]; err != nil {
		return nil, err
	}
	if result := a.results[action]; result != nil {
		return result, nil
	}
	return map[string]any{"prepared": true}, nil
}

func (a *collaborationTestAgent) Close(context.Context) error { return nil }

func (a *collaborationTestAgent) hasAction(action string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, got := range a.actions {
		if got == action {
			return true
		}
	}
	return false
}

func (a *collaborationTestAgent) actionCount(action string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	count := 0
	for _, got := range a.actions {
		if got == action {
			count++
		}
	}
	return count
}

func callCollaborationTest(t *testing.T, client *Client, action string, params map[string]any) map[string]any {
	t.Helper()
	response := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "local-collaboration-" + action, Capability: "collaboration.control", Action: action, Params: params,
	})
	if response.Error != nil {
		t.Fatalf("%s failed: %#v", action, response.Error)
	}
	delete(response.Result, "timing")
	return response.Result
}

func collaborationTestPacket(root, target string) map[string]any {
	return map[string]any{
		"machineId": "machine-1", "callbackSessionId": "controller-1", "workingDirectory": root,
		"prompt": "Implement and test the bounded task.", "idempotencyKey": "mission-task-key-001",
		"targetSessionId": target, "accessMode": "write", "writeScope": "src/task",
		"callbackType": "text",
	}
}

func createCollaborationTestLedger(t *testing.T, dbPath string, packet map[string]any) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE mission(singleton INTEGER PRIMARY KEY CHECK(singleton=1), data TEXT NOT NULL, revision INTEGER NOT NULL);
		CREATE TABLE items(id TEXT PRIMARY KEY, phase TEXT NOT NULL, kind TEXT NOT NULL, revision INTEGER NOT NULL, data TEXT NOT NULL, dispatch_key TEXT UNIQUE, task_ref TEXT UNIQUE);
		CREATE TABLE events(revision INTEGER PRIMARY KEY, object_id TEXT NOT NULL, phase TEXT NOT NULL);
		CREATE TABLE observation(singleton INTEGER PRIMARY KEY CHECK(singleton=1), data TEXT NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveMachinePath(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	mission := map[string]any{
		"id": "mission-1", "controller": "controller-1", "coordinator": "coordinator-1", "db_path": resolved,
		"status": "active", "dispatch_enabled": true, "capacity": map[string]any{"cloud": 2, "local": 1},
		"legacy_callback_sessions": []any{}, "schema": 1,
	}
	item := map[string]any{
		"id": "task-1", "kind": "implement", "phase": "ready", "owner": "owner-1", "executor": "cloud",
		"next_action": "dispatch", "evidence": []any{}, "depends_on": []any{}, "contract_refs": []any{},
		"packet": packet, "callback": "none", "result": "none", "validation": "pending", "integration": "pending",
		"blocker": nil, "claim": nil, "source_ref": nil,
	}
	missionRaw, _ := json.Marshal(mission)
	itemRaw, _ := json.Marshal(item)
	if _, err := db.Exec("INSERT INTO mission VALUES(1,?,1)", string(missionRaw)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO items VALUES(?,?,?,?,?,?,?)", "task-1", "ready", "implement", 1, string(itemRaw), packet["idempotencyKey"], nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO events VALUES(1,'task-1','ready')"); err != nil {
		t.Fatal(err)
	}
}

func readCollaborationTestItem(t *testing.T, dbPath string) map[string]any {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw string
	if err := db.QueryRow("SELECT data FROM items WHERE id='task-1'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var item map[string]any
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatal(err)
	}
	return item
}

func setCollaborationTestMissionDispatch(t *testing.T, dbPath, status string, enabled bool) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw string
	var revision int64
	if err := db.QueryRow("SELECT data, revision FROM mission WHERE singleton=1").Scan(&raw, &revision); err != nil {
		t.Fatal(err)
	}
	var mission map[string]any
	if err := json.Unmarshal([]byte(raw), &mission); err != nil {
		t.Fatal(err)
	}
	mission["status"] = status
	mission["dispatch_enabled"] = enabled
	updated, err := json.Marshal(mission)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE mission SET data=? WHERE singleton=1", string(updated)); err != nil {
		t.Fatal(err)
	}
}
