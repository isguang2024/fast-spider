package node

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func recoveryFixture(t *testing.T) (*Client, *collaborationTestAgent, string, map[string]any) {
	t.Helper()
	c, a, db, event := collaborationInboxFixture(t)
	event["outcome"], event["resultStatus"], event["callbackErrorCode"], event["callbackType"] = "failed", "failed", "CALLBACK_TEXT_TOO_LARGE", "text"
	if err := c.PersistCollaborationCallback(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	a.results = map[string]map[string]any{"session.result": {"sessionId": "cloud-1", "status": "completed", "resultStatus": "ready", "resultId": "res_verified", "resultBytes": 12051, "resultSHA256": "sha256:" + strings.Repeat("a", 64)}}
	box := callCollaborationTest(t, c, "inbox", collaborationStateIdentity(db, "controller-1"))
	entry := collaborationTestInboxResults(box)[0]
	p := collaborationStateIdentity(db, "controller-1")
	p["expectedRevision"], p["resultId"] = box["revision"], entry["resultId"]
	hint := entry["resultFetch"].(map[string]any)["params"].(map[string]any)
	if hint["sessionId"] != "cloud-1" || hint["idempotencyKey"] != "mission-key-001" {
		t.Fatalf("wrong result hint: %#v", hint)
	}
	recovery := entry["resultRecovery"].(map[string]any)["params"].(map[string]any)
	canonical, err := validateCollaborationBaseIdentity(db, "mission-1", "controller-1")
	if err != nil {
		t.Fatal(err)
	}
	p["dbPath"] = canonical
	if !collaborationValueEqual(p, recovery) {
		t.Fatalf("recovery hint differs: %#v %#v", p, recovery)
	}
	return c, a, db, p
}

func recoveryAudit(t *testing.T, db, id string) string {
	t.Helper()
	db, err := validateCollaborationBaseIdentity(db, "mission-1", "controller-1")
	if err != nil {
		t.Fatal(err)
	}
	l, err := openCollaborationLedger(context.Background(), db, "mission-1", "controller-1", false)
	if err != nil {
		t.Fatal(err)
	}
	defer l.rollback()
	var audit string
	if err := l.conn.QueryRowContext(context.Background(), "SELECT coalesce(resolution,'') || '|' || coalesce(resolved_at,'') FROM callback_inbox WHERE result_id=?", id).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	return audit
}

func TestResultRecoverPreservesBusinessDecision(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "returned", true: "resolved_blocked"}[blocked], func(t *testing.T) {
			c, a, db, p := recoveryFixture(t)
			if blocked {
				decision := cloneParams(p)
				decision["decision"], decision["evidenceRef"], decision["blocker"] = "block", "transport:overflow", map[string]any{"kind": "runtime", "reason": "callback overflow", "owner": "controller-1", "resume_when": "provider manifest verified", "next_check_at": int64(9999999999)}
				resolved := callCollaborationTest(t, c, "resolve", decision)
				p["expectedRevision"] = resolved["revision"]
			}
			before := readCollaborationTestItem(t, db)
			audit := recoveryAudit(t, db, p["resultId"].(string))
			got := callCollaborationTest(t, c, "result_recover", p)
			after := readCollaborationTestItem(t, db)
			if after["result"] != "completed" || after["phase"] != before["phase"] || after["terminal_ref"] != before["terminal_ref"] || !collaborationValueEqual(after["blocker"], before["blocker"]) || after["callback"] != before["callback"] {
				t.Fatalf("business state changed: %#v -> %#v", before, after)
			}
			if recoveryAudit(t, db, p["resultId"].(string)) != audit {
				t.Fatal("resolution audit changed")
			}
			replay := callCollaborationTest(t, c, "result_recover", p)
			if replay["replayed"] != true || replay["revision"] != got["revision"] || a.actionCount("session.result") != 1 {
				t.Fatalf("not idempotent: %#v", replay)
			}
			if a.hasAction("session.create") || a.hasAction("session.send") {
				t.Fatal("Cloud execution repeated")
			}
		})
	}
}

func TestResultRecoverRejectsInvalidEvidence(t *testing.T) {
	for _, test := range []struct {
		name, key string
		value     any
	}{
		{"coordinator", "actorSessionId", "coordinator-1"}, {"stale", "expectedRevision", int64(0)},
		{"failed", "status", "failed"}, {"unready", "resultStatus", "pending"}, {"wrong_source", "sessionId", "cloud-other"}, {"bad_hash", "resultSHA256", "bad"}, {"empty", "resultBytes", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, a, db, p := recoveryFixture(t)
			before, _ := json.Marshal(readCollaborationTestItem(t, db))
			if test.key == "actorSessionId" || test.key == "expectedRevision" {
				p[test.key] = test.value
			} else {
				a.results["session.result"][test.key] = test.value
			}
			r := c.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("result_recover", p))
			if r.Error == nil {
				t.Fatal("invalid recovery accepted")
			}
			after, _ := json.Marshal(readCollaborationTestItem(t, db))
			if string(before) != string(after) {
				t.Fatal("rejection mutated item")
			}
		})
	}
}

type resultRecoveryRaceAgent struct {
	*collaborationTestAgent
	change func()
}

func (a *resultRecoveryRaceAgent) Control(ctx context.Context, action string, p map[string]any) (map[string]any, error) {
	if action == "session.result" {
		a.change()
	}
	return a.collaborationTestAgent.Control(ctx, action, p)
}

func TestResultRecoverRechecksRevisionAfterProviderRead(t *testing.T) {
	c, a, db, p := recoveryFixture(t)
	c.agent = &resultRecoveryRaceAgent{a, func() {
		q := collaborationStateIdentity(db, "controller-1")
		q["expectedRevision"], q["items"] = p["expectedRevision"], []any{map[string]any{"id": "task-1", "next_action": "Changed during provider read"}}
		callCollaborationTest(t, c, "apply", q)
	}}
	r := c.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("result_recover", p))
	if r.Error == nil || !strings.Contains(r.Error.Message, "revision conflict") {
		t.Fatalf("missing CAS: %#v", r.Error)
	}
	if readCollaborationTestItem(t, db)["result"] != "failed" {
		t.Fatal("stale provider read committed")
	}
}

func TestResultRecoverRejectsUnrelatedTerminal(t *testing.T) {
	for _, kind := range []string{"different_error", "old_attempt", "different_binding"} {
		t.Run(kind, func(t *testing.T) {
			c, a, db, p := recoveryFixture(t)
			l, err := openCollaborationLedger(context.Background(), p["dbPath"].(string), "mission-1", "controller-1", true)
			if err != nil {
				t.Fatal(err)
			}
			defer l.rollback()
			item, err := l.item(context.Background(), "task-1")
			if err != nil {
				t.Fatal(err)
			}
			if kind == "different_error" {
				var raw string
				if err := l.conn.QueryRowContext(context.Background(), "SELECT data FROM callback_inbox WHERE result_id=?", p["resultId"]).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				event := map[string]any{}
				if err := json.Unmarshal([]byte(raw), &event); err != nil {
					t.Fatal(err)
				}
				event["callbackErrorCode"] = "PROVIDER_FAILED"
				updated, _ := json.Marshal(event)
				if _, err := l.conn.ExecContext(context.Background(), "UPDATE callback_inbox SET data=? WHERE result_id=?", string(updated), p["resultId"]); err != nil {
					t.Fatal(err)
				}
			} else {
				if kind == "old_attempt" {
					item["terminal_ref"] = "inbox-new-attempt"
				} else {
					item["binding"].(map[string]any)["chatSessionId"] = "cloud-other"
				}
				if err := l.saveItem(context.Background(), item); err != nil {
					t.Fatal(err)
				}
			}
			if err := l.commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			p["expectedRevision"] = l.revision
			if r := c.HandleLocalCapability(context.Background(), collaborationCapabilityRequest("result_recover", p)); r.Error == nil {
				t.Fatal("unrelated result corrected")
			}
			if a.hasAction("session.result") || readCollaborationTestItem(t, db)["result"] != "failed" {
				t.Fatal("unrelated provider contacted or result changed")
			}
			if kind == "old_attempt" {
				q := collaborationStateIdentity(db, "controller-1")
				q["resultId"] = p["resultId"]
				box := callCollaborationTest(t, c, "inbox", q)
				if box["resultFetch"] != nil {
					t.Fatal("old attempt exposed current key")
				}
			}
		})
	}
}
