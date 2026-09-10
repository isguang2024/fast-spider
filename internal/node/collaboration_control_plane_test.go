package node

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestCollaborationDispatchConditionalYieldUsesBoundedRefill(t *testing.T) {
	for _, tc := range []struct {
		name        string
		secondScope string
		wantYield   bool
		wantBlocked string
	}{
		{name: "independent ready continues", secondScope: "src/task-b", wantYield: false},
		{name: "writer fence yields", secondScope: "src/task-a/child", wantYield: true, wantBlocked: "write_scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dbPath := filepath.Join(root, "mission.sqlite3")
			first := collaborationStateCloudItem(root, "task-a", "mission-task-a-key-001", "", "src/task-a")
			second := collaborationStateCloudItem(root, "task-b", "mission-task-b-key-001", "", tc.secondScope)
			agent := &collaborationTestAgent{results: map[string]map[string]any{"session.create": {"sessionId": "cloud-created-a"}}}
			client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node"), Agent: agent})
			callCollaborationTest(t, client, "init", collaborationStateInitParams(dbPath, []any{first, second}))

			completed, err := client.collaborationControl(context.Background(), "dispatch", map[string]any{
				"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "itemId": "task-a",
			})
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := completed["callerShouldYield"].(bool); got != tc.wantYield {
				t.Fatalf("callerShouldYield=%v want=%v completed=%#v", got, tc.wantYield, completed)
			}
			if completed["activePollingAllowed"] != false {
				t.Fatalf("dispatch enabled active polling: %#v", completed)
			}

			next := callCollaborationTest(t, client, "next_actions", collaborationStateIdentity(dbPath, "coordinator-1"))
			refill := collaborationAnyList(next["refillInputs"])
			if tc.wantYield {
				if len(refill) != 0 {
					t.Fatalf("writer-conflicting READY leaked into refill: %#v", refill)
				}
				found := false
				for _, raw := range collaborationAnyList(next["readyBlocked"]) {
					blocked, _ := raw.(map[string]any)
					if blocked["itemId"] == "task-b" && blocked["kind"] == tc.wantBlocked {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing bounded READY diagnostic: %#v", next["readyBlocked"])
				}
			} else {
				if len(refill) != 1 || refill[0].(map[string]any)["itemId"] != "task-b" {
					t.Fatalf("pagination-safe refill=%#v", refill)
				}
				if completed["refillRecommended"] != true {
					t.Fatalf("independent READY did not recommend refill: %#v", completed)
				}
			}
		})
	}
}

func TestCollaborationValidationClaimCapacityAndLostReceiptRecovery(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "mission.sqlite3")
	items := make([]any, 0, 8)
	for i := 1; i <= 8; i++ {
		item := collaborationStateLocalItem(fmt.Sprintf("validation-%d", i), "verifying")
		item["source_ref"] = "test:validation-seed"
		item["validation_owner"] = fmt.Sprintf("validator-owner-%d", i)
		item["next_action"] = "Validate the returned result"
		items = append(items, item)
	}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node")})
	initialized := callCollaborationTest(t, client, "init", collaborationStateInitParams(dbPath, items))
	currentRevision, _ := collaborationInt64(initialized["revision"])

	var firstClaim map[string]any
	for i := 1; i <= 4; i++ {
		claim, err := client.collaborationControl(context.Background(), "validation_claim", map[string]any{
			"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
			"expectedRevision": currentRevision, "itemId": fmt.Sprintf("validation-%d", i),
			"launchRef": fmt.Sprintf("codex-agent:coordinator-1#/validators/%d", i),
		})
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			firstClaim = claim
		}
		currentRevision, _ = collaborationInt64(claim["revision"])
	}

	// A lost receipt must produce binding recovery, not a new validator launch
	// and not an unavailable-check budget against a synthetic logical owner.
	next := callCollaborationTest(t, client, "next_actions", collaborationStateIdentity(dbPath, "coordinator-1"))
	var recovery map[string]any
	for _, action := range collaborationTestActions(next) {
		if action["kind"] == "recover_validation_binding" && fmt.Sprint(action["itemId"]) == "validation-1" {
			recovery = action
		}
	}
	if recovery == nil || recovery["nativeBindingLookup"] == nil || recovery["validationReceipt"] == nil || recovery["recordCheck"] != nil {
		t.Fatalf("lost receipt recovery action=%#v firstClaim=%#v", recovery, firstClaim)
	}

	// Two contenders race for the fifth normal slot with the same revision.
	// BEGIN IMMEDIATE + revision CAS must cap held claims at exactly five.
	start := make(chan struct{})
	type claimResult struct {
		id    int
		value map[string]any
		err   error
	}
	results := make(chan claimResult, 2)
	for i := 5; i <= 6; i++ {
		i := i
		go func() {
			<-start
			value, err := client.collaborationControl(context.Background(), "validation_claim", map[string]any{
				"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
				"expectedRevision": currentRevision, "itemId": fmt.Sprintf("validation-%d", i),
				"launchRef": fmt.Sprintf("codex-agent:coordinator-1#/validators/%d", i),
			})
			results <- claimResult{id: i, value: value, err: err}
		}()
	}
	close(start)
	successes, loserID := 0, 0
	for i := 0; i < 2; i++ {
		result := <-results
		if result.err == nil {
			successes++
			currentRevision, _ = collaborationInt64(result.value["revision"])
		} else {
			loserID = result.id
		}
	}
	if successes != 1 {
		t.Fatalf("normal validation race successes=%d want=1", successes)
	}
	brief := callCollaborationTest(t, client, "brief", collaborationStateIdentity(dbPath, "coordinator-1"))
	heldNormal, _ := collaborationInt64(brief["validationHeld"])
	availableNormal, _ := collaborationInt64(brief["validationAvailable"])
	if heldNormal != 5 || availableNormal != 0 {
		t.Fatalf("normal validation capacity=%#v held=%v available=%v", brief["validationCapacity"], brief["validationHeld"], brief["validationAvailable"])
	}

	// Controller explicitly authorizes two burst slots. That authorization is
	// durable mission state; it is not an implicit relaxation of the normal 5.
	patch := collaborationStateIdentity(dbPath, "controller-1")
	patch["expectedRevision"] = brief["revision"]
	patch["mission"] = map[string]any{"capacity": map[string]any{"cloud": int64(4), "local": int64(2), "validation": int64(5), "validationBurst": int64(2)}}
	bursted := callCollaborationTest(t, client, "apply", patch)
	currentRevision, _ = collaborationInt64(bursted["revision"])
	for _, id := range []int{loserID, 7, 8} {
		value, err := client.collaborationControl(context.Background(), "validation_claim", map[string]any{
			"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1",
			"expectedRevision": currentRevision, "itemId": fmt.Sprintf("validation-%d", id),
			"launchRef": fmt.Sprintf("codex-agent:coordinator-1#/validators/burst-%d", id),
		})
		if id <= 7 {
			if err != nil {
				t.Fatalf("authorized burst claim %d: %v", id, err)
			}
			currentRevision, _ = collaborationInt64(value["revision"])
		} else if err == nil || !strings.Contains(err.Error(), "validation capacity exhausted") {
			t.Fatalf("eighth validation claim value=%#v err=%v", value, err)
		}
	}
	brief = callCollaborationTest(t, client, "brief", collaborationStateIdentity(dbPath, "coordinator-1"))
	capacity := brief["validationCapacity"].(map[string]any)
	heldBurst, _ := collaborationInt64(brief["validationHeld"])
	normal, _ := collaborationInt64(capacity["normal"])
	burst, _ := collaborationInt64(capacity["burst"])
	burstAuthorized, _ := capacity["burstAuthorized"].(bool)
	if heldBurst != 7 || normal != 5 || burst != 2 || !burstAuthorized {
		t.Fatalf("authorized validation burst=%#v", capacity)
	}
}

func TestCollaborationRoleWakeOutboxRetriesAcrossRestartAndRequiresConfirmedTurn(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "node")
	dbPath := filepath.Join(root, "mission.sqlite3")
	failedAgent := &collaborationTestAgent{errors: map[string]error{"session.send": context.DeadlineExceeded}}
	first := NewLocalCapabilityClient(Config{DataDir: dataDir, Agent: failedAgent})
	initialized := callCollaborationTest(t, first, "init", collaborationStateInitParams(dbPath, []any{collaborationStateCloudItem(root, "task-1", "mission-wake-key-001", "", "src/task-1")}))
	patch := collaborationStateIdentity(dbPath, "controller-1")
	patch["expectedRevision"] = initialized["revision"]
	patch["mission"] = map[string]any{"capacity": map[string]any{"cloud": int64(5), "local": int64(2), "validation": int64(5)}}
	callCollaborationTest(t, first, "apply", patch)

	first.drainCollaborationRoleWakes(context.Background())
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var attempts int64
	var delivered sql.NullInt64
	var lastError string
	if err := db.QueryRow("SELECT attempts,delivered_at,last_error FROM role_wakeup_outbox WHERE target_role='coordinator'").Scan(&attempts, &delivered, &lastError); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if attempts != 1 || delivered.Valid || lastError == "" {
		db.Close()
		t.Fatalf("failed wake attempts=%d delivered=%v lastError=%q", attempts, delivered, lastError)
	}
	// Make the durable retry due without sleeping; the next process owns no
	// in-memory state from the first client.
	if _, err := db.Exec("UPDATE role_wakeup_outbox SET next_attempt_at=0"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	_ = db.Close()

	successAgent := &collaborationTestAgent{results: map[string]map[string]any{"session.send": {
		"turnId": "turn-role-wake-1", "executionMode": "codex_app_server", "owner": "fast_spider_node",
	}}}
	restarted := NewLocalCapabilityClient(Config{DataDir: dataDir, Agent: successAgent})
	restarted.drainCollaborationRoleWakes(context.Background())
	if successAgent.localTurnDeliveries != 1 || failedAgent.localTurnDeliveries != 1 {
		t.Fatal("role wake bypassed the confirmed local notification path")
	}
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var turnID string
	if err := db.QueryRow("SELECT attempts,delivered_at,coalesce(delivery_turn_id,'') FROM role_wakeup_outbox WHERE target_role='coordinator'").Scan(&attempts, &delivered, &turnID); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || !delivered.Valid || turnID != "turn-role-wake-1" || successAgent.actionCount("session.send") != 1 {
		t.Fatalf("restart wake attempts=%d delivered=%v turn=%q sends=%d", attempts, delivered, turnID, successAgent.actionCount("session.send"))
	}
	if _, err := readCollaborationRoleWakeRoutes(filepath.Join(dataDir, "collaboration-role-wake-routes.json")); err != nil {
		t.Fatalf("durable wake route registry unreadable: %v", err)
	}
}

func TestCollaborationValidationReceiptSchedulesDurableRoleWakes(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "mission.sqlite3")
	item := collaborationStateLocalItem("validation-wake", "verifying")
	item["source_ref"] = "test:validation-wake"
	item["validation_owner"] = "validator-owner-wake"
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node")})
	initialized := callCollaborationTest(t, client, "init", collaborationStateInitParams(dbPath, []any{item}))
	claim := collaborationStateIdentity(dbPath, "coordinator-1")
	claim["expectedRevision"], claim["itemId"], claim["launchRef"] = initialized["revision"], "validation-wake", "codex-agent:coordinator-1#/validators/wake"
	claimed := callCollaborationTest(t, client, "validation_claim", claim)
	receipt := collaborationStateIdentity(dbPath, "coordinator-1")
	receipt["expectedRevision"], receipt["itemId"] = claimed["revision"], "validation-wake"
	receipt["validationClaim"], receipt["executionRef"] = claimed["validationClaim"], "codex-thread:validator-wake-1"
	callCollaborationTest(t, client, "validation_receipt", receipt)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT target_role,reason,next_attempt_at FROM role_wakeup_outbox WHERE item_id='validation-wake' ORDER BY reason")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	now := time.Now().Unix()
	got := map[string]struct {
		role string
		due  int64
	}{}
	for rows.Next() {
		var role, reason string
		var due int64
		if err := rows.Scan(&role, &reason, &due); err != nil {
			t.Fatal(err)
		}
		got[reason] = struct {
			role string
			due  int64
		}{role: role, due: due}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if row := got["validation_execution_bound"]; row.role != "controller" || row.due != 0 {
		t.Fatalf("immediate validation wake=%#v", row)
	}
	if row := got["validation_check_due"]; row.role != "controller" || row.due < now+540 {
		t.Fatalf("controller validation due wake=%#v now=%d", row, now)
	}
	if row := got["validation_notify_due"]; row.role != "coordinator" || row.due < now+540 {
		t.Fatalf("coordinator validation due wake=%#v now=%d", row, now)
	}
}

func TestCollaborationValidationClaimsRemainBoundedUnderRepeatedConcurrentCAS(t *testing.T) {
	// This extra stress case catches accidental count-then-commit races if the
	// validation claim transaction ever stops using BEGIN IMMEDIATE.
	root := t.TempDir()
	dbPath := filepath.Join(root, "mission.sqlite3")
	items := make([]any, 0, 6)
	for i := 0; i < 6; i++ {
		item := collaborationStateLocalItem(fmt.Sprintf("v-%d", i), "verifying")
		item["source_ref"] = "test:concurrent-validation"
		item["validation_owner"] = fmt.Sprintf("owner-%d", i)
		items = append(items, item)
	}
	client := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node")})
	initialized := callCollaborationTest(t, client, "init", collaborationStateInitParams(dbPath, items))
	revision, _ := collaborationInt64(initialized["revision"])
	var wg sync.WaitGroup
	wg.Add(6)
	for i := 0; i < 6; i++ {
		i := i
		go func() {
			defer wg.Done()
			_, _ = client.collaborationControl(context.Background(), "validation_claim", map[string]any{
				"dbPath": dbPath, "missionId": "mission-1", "actorSessionId": "coordinator-1", "expectedRevision": revision,
				"itemId": fmt.Sprintf("v-%d", i), "launchRef": fmt.Sprintf("codex-agent:coordinator-1#/stress/%d", i),
			})
		}()
	}
	wg.Wait()
	brief := callCollaborationTest(t, client, "brief", collaborationStateIdentity(dbPath, "coordinator-1"))
	held, _ := collaborationInt64(brief["validationHeld"])
	if held > 5 {
		t.Fatalf("validation claims exceeded normal capacity: %d", held)
	}
	// Stale CAS means typically one succeeds; the invariant under test is the
	// hard upper bound, independent of scheduling order.
	if held < 1 {
		t.Fatalf("all concurrent claims failed unexpectedly: %#v", brief["validationCapacity"])
	}
}
