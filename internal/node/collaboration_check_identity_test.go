package node

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollaborationCheckBudgetSurvivesEditsAndLegacyMigration(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "current"
		if legacy {
			name = "pre-upgrade"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			db := filepath.Join(root, "mission.sqlite3")
			a := &collaborationTestAgent{}
			c := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node"), Agent: a})
			item := collaborationStateLocalItem("local-1", "planned")
			item["execution_ref"] = "codex-thread:worker-1"
			current := callCollaborationTest(t, c, "init", collaborationStateInitParams(db, []any{item}))
			apply := func(patch map[string]any) {
				t.Helper()
				patch["id"] = "local-1"
				p := collaborationStateIdentity(db, "controller-1")
				p["expectedRevision"], p["items"] = current["revision"], []any{patch}
				current = callCollaborationTest(t, c, "apply", p)
			}
			readDue := func(actor string, now int64) map[string]any {
				p := collaborationStateIdentity(db, actor)
				p["now"] = now
				return callCollaborationTest(t, c, "next_actions", p)
			}
			apply(map[string]any{"phase": "active", "next_check_at": int64(1000)})
			var actionID string
			for index, now := range []int64{1000, 1900, 3700} {
				due := readDue("coordinator-1", now)
				id := collaborationTestActionID(due, "check_execution")
				if id == "" || index > 0 && id != actionID {
					t.Fatalf("execution check identity drifted: %#v", due)
				}
				actionID = id
				for _, action := range collaborationTestActions(due) {
					if action["actionId"] == id && action["notify"] != (index == 0) {
						t.Fatalf("notification dedupe lost: %#v", action)
					}
				}
				p := collaborationStateIdentity(db, "coordinator-1")
				p["expectedRevision"], p["expectedObservationRevision"], p["actionId"] = due["revision"], due["observationRevision"], id
				p["outcome"], p["evidenceRef"], p["notified"], p["now"] = "unavailable", "native:unknown", index == 0, now
				callCollaborationTest(t, c, "record_check", p)
				if legacy && index == 0 {
					// Seed the exact persisted format produced by 0.4.76. The first
					// post-upgrade action is an item edit, not another provider check.
					resolvedDB, err := validateCollaborationBaseIdentity(db, "mission-1", "controller-1")
					if err != nil {
						t.Fatal(err)
					}
					l, err := openCollaborationLedger(context.Background(), resolvedDB, "mission-1", "controller-1", true)
					if err != nil {
						t.Fatal(err)
					}
					defer l.rollback()
					observation, err := l.readObservation(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					key, _ := encodeCollaborationJSON([]any{"coordinator", "check_execution", "local-1"})
					oldID, _ := collaborationActionHash([]any{"mission-1", key, current["revision"], int64(1000)})
					observation["action_checks"].(map[string]any)[key].(map[string]any)["action_id"] = oldID
					raw, _ := encodeCollaborationJSON(observation)
					if _, err := l.conn.ExecContext(context.Background(), "UPDATE observation SET data=? WHERE singleton=1", raw); err != nil {
						t.Fatal(err)
					}
					if err := l.commit(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				apply(map[string]any{"next_action": "Wait for existing worker", "next_check_at": now + 1, "priority": int64(index + 1)})
				if collaborationTestHasActionKind(readDue("coordinator-1", now+1), "check_execution") {
					t.Fatal("item edit bypassed check backoff")
				}
			}
			if collaborationTestHasActionKind(readDue("coordinator-1", 10000), "check_execution") || !collaborationTestHasActionKind(readDue("controller-1", 10000), "decide_stalled_check") {
				t.Fatal("edits reset the exhausted budget or lost controller handoff")
			}
			// A legacy scheduling call must not turn an exhausted check back on.
			due := readDue("coordinator-1", 10000)
			p := collaborationStateIdentity(db, "coordinator-1")
			p["expectedRevision"], p["expectedObservationRevision"], p["actionId"] = due["revision"], due["observationRevision"], actionID
			p["evidenceRef"], p["retryAt"], p["now"] = "schedule:only", int64(11000), int64(10000)
			callCollaborationTest(t, c, "record_action", p)
			if collaborationTestHasActionKind(readDue("coordinator-1", 12000), "check_execution") {
				t.Fatal("record_action erased record_check budget")
			}
			// A genuinely new execution binding starts its own budget.
			apply(map[string]any{"execution_ref": "codex-thread:worker-2", "next_check_at": int64(12000)})
			newID := collaborationTestActionID(readDue("coordinator-1", 12000), "check_execution")
			if newID == "" || newID == actionID {
				t.Fatal("new execution inherited the old exhausted budget")
			}
			// A formal retry also changes identity even if the native task is reused.
			apply(map[string]any{"phase": "returned", "result": "completed", "terminal_ref": "test:exit", "evidence": []any{"test:exit"}})
			apply(map[string]any{"phase": "rework"})
			r := collaborationStateIdentity(db, "controller-1")
			r["expectedRevision"], r["itemId"], r["evidenceRef"], r["item"] = current["revision"], "local-1", "test:retry", map[string]any{"execution_ref": "codex-thread:worker-2"}
			current = callCollaborationTest(t, c, "retry", r)
			apply(map[string]any{"phase": "active", "next_check_at": int64(12000)})
			if id := collaborationTestActionID(readDue("coordinator-1", 12000), "check_execution"); id == "" || id == newID {
				t.Fatal("new attempt did not reset check identity")
			}
			if len(a.actions) != 0 {
				t.Fatal("check bookkeeping contacted provider")
			}
		})
	}
}

func TestCollaborationBriefExposesFrozenScopeAndNamesConflictingWriter(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "mission.sqlite3")
	c := NewLocalCapabilityClient(Config{DataDir: filepath.Join(root, "node")})
	item := collaborationStateCloudItem(root, "writer", "key-writer-001", "cloud-writer", root)
	init := callCollaborationTest(t, c, "init", collaborationStateInitParams(db, []any{item}))
	p := collaborationStateIdentity(db, "coordinator-1")
	p["itemId"], p["expectedRevision"] = "writer", init["revision"]
	callCollaborationTest(t, c, "claim", p)
	var revision any
	for _, actor := range []string{"controller-1", "coordinator-1"} {
		brief := callCollaborationTest(t, c, "brief", collaborationStateIdentity(db, actor))
		revision = brief["revision"]
		view := collaborationAnyList(brief["items"])[0].(map[string]any)
		scope := view["scope"].(map[string]any)
		if scope["writeScope"] != root || scope["workingDirectory"] != root || scope["prompt"] != nil || view["packet"] != nil || view["holdsExecution"] != true {
			t.Fatalf("brief lost real scope or included prompt: %#v", view)
		}
	}
	p = collaborationStateIdentity(db, "controller-1")
	p["expectedRevision"], p["items"] = revision, []any{collaborationStateCloudItem(root, "other", "key-other-002", "cloud-other", "src/independent")}
	result := c.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("apply", p))
	if result.Error == nil || !strings.Contains(result.Error.Message, "item=writer") || !strings.Contains(result.Error.Message, "scope="+collaborationScopeRoots(collaborationScopePacket(item))[0]) {
		t.Fatalf("conflict lacks actual occupying writer: %#v root=%s held=%v requested=%v", result.Error, root, collaborationScopeRoots(collaborationScopePacket(item)), collaborationScopeRoots(collaborationScopePacket(collaborationStateCloudItem(root, "other", "key-other-002", "cloud-other", "src/independent"))))
	}
	p["items"] = []any{map[string]any{"id": "writer", "packet": collaborationTestPacket(root, "cloud-writer")}}
	if result := c.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("apply", p)); result.Error == nil || !strings.Contains(result.Error.Message, "dispatched packet immutable") {
		t.Fatalf("running packet was allowed to shrink: %#v", result.Error)
	}
}

func TestCollaborationBootstrapFileResponsibilityAndReadOnlyException(t *testing.T) {
	c := NewLocalCapabilityClient(Config{DataDir: t.TempDir()})
	for _, mode := range []string{"read_only", "write"} {
		for _, callbackType := range []string{"local_file", "text", "status"} {
			packet := collaborationTestPacket(t.TempDir(), "")
			packet["accessMode"], packet["callbackType"] = mode, callbackType
			if mode == "read_only" {
				delete(packet, "writeScope")
			}
			runtimePacket, err := c.localCollaborationRuntimePacket(packet)
			if err != nil {
				t.Fatal(err)
			}
			prompt := localCollaborationBootstrap(runtimePacket, collaborationToken{})
			isFile := callbackType == "local_file"
			if strings.Contains(prompt, "save the final report as a UTF-8 file at the exact DELIVERABLE_PATH") != isFile || strings.Contains(prompt, "Node-assigned report file is permitted") != (isFile && mode == "read_only") {
				t.Fatalf("wrong deliverable contract for %s/%s: %s", mode, callbackType, prompt)
			}
			if isFile && (!strings.Contains(prompt, "DELIVERABLE_PATH: "+mapStringValue(runtimePacket, "deliverablePath")) || !strings.Contains(prompt, "does not write this report for you")) {
				t.Fatal("report ownership/path is ambiguous")
			}
		}
	}
}
