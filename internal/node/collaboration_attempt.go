package node

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

const collaborationAttemptSchema = `
CREATE TABLE execution_attempts(item_id TEXT NOT NULL, attempt INTEGER NOT NULL, claim TEXT NOT NULL,
 dispatch_key TEXT UNIQUE, task_ref TEXT UNIQUE, data TEXT NOT NULL,
 PRIMARY KEY(item_id,attempt), UNIQUE(item_id,claim));
`

func (l *collaborationLedger) hasAttempts(ctx context.Context) bool {
	var n int
	return l.conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='execution_attempts'").Scan(&n) == nil && n == 1
}

type collaborationRetryParams struct {
	collaborationIdentityParams
	ExpectedRevision int64          `json:"expectedRevision"`
	ItemID           string         `json:"itemId"`
	EvidenceRef      string         `json:"evidenceRef"`
	UserDecisionRef  string         `json:"userDecisionRef,omitempty"`
	Item             map[string]any `json:"item"`
}

// retry creates a new execution attempt, not a new business task. The new READY
// row is the durable dispatch intent; existing dispatch claim/CAS consumes it.
func (c *Client) collaborationRetry(ctx context.Context, p collaborationRetryParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(p.DBPath, p.MissionID, p.ActorSessionID)
	if err != nil {
		return nil, err
	}
	l, err := openCollaborationLedger(ctx, dbPath, p.MissionID, p.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer l.rollback()
	if mapStringValue(l.mission, "controller") != p.ActorSessionID {
		return nil, errors.New("controller-only retry decision")
	}
	if !l.hasAttempts(ctx) {
		return nil, errors.New("stable attempts require a new v3 mission")
	}
	if mapStringValue(l.mission, "status") != "active" {
		return nil, errors.New("paused mission cannot start a new attempt")
	}
	if err := validateCollaborationText(p.EvidenceRef, "retry evidence", 1024); err != nil {
		return nil, err
	}
	old, err := l.item(ctx, p.ItemID)
	if err != nil {
		return nil, err
	}
	if p.ExpectedRevision != l.revision {
		return nil, fmt.Errorf("revision conflict; current=%d", l.revision)
	}
	switch mapStringValue(old, "phase") {
	case "rework", "blocked", "dispatch_rejected", "canceled":
	default:
		return nil, errors.New("resolve the current result before retrying")
	}
	if collaborationHoldsExecution(old) {
		return nil, errors.New("unconfirmed writer; recover exact existing attempt")
	}
	if mapStringValue(old, "claim") == "" && old["binding"] == nil && old["started_at"] == nil {
		return nil, errors.New("no previous execution; update the planned task")
	}
	canceled := mapStringValue(old, "phase") == "canceled"
	if ref := mapStringValue(old, "terminal_ref"); ref != "" && l.hasInbox(ctx) {
		var raw string
		if err := l.conn.QueryRowContext(ctx, "SELECT data FROM callback_inbox WHERE result_id=?", ref).Scan(&raw); err == nil {
			var event map[string]any
			if err := json.Unmarshal([]byte(raw), &event); err != nil {
				return nil, err
			}
			canceled = canceled || mapStringValue(event, "callbackErrorCode") == "CLOUD_CHAT_CANCELED"
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	if canceled {
		if err := validateCollaborationText(p.UserDecisionRef, "explicit user resume decision", 1024); err != nil {
			return nil, err
		}
	}
	if mapStringValue(old, "executor") == "cloud" && old["binding"] != nil && mapStringValue(old, "callback") != "acked" {
		return nil, errors.New("settle callback transport before reusing the Cloud session")
	}
	if l.hasInbox(ctx) {
		var pending int
		if err := l.conn.QueryRowContext(ctx, "SELECT count(*) FROM callback_inbox WHERE item_id=? AND resolved_at IS NULL", p.ItemID).Scan(&pending); err != nil {
			return nil, err
		}
		if pending > 0 {
			return nil, errors.New("unresolved result remains")
		}
	}
	allowed := map[string]bool{"owner": true, "executor": true, "packet": true, "local_scope": true, "execution_ref": true, "next_action": true, "priority": true, "contract_refs": true}
	if err := validateCollaborationMapKeys(p.Item, allowed, "retry item"); err != nil {
		return nil, err
	}
	item := cloneParams(old)
	for _, key := range []string{"binding", "claim", "dispatch_key", "terminal_ref", "started_at", "next_check_at", "blocker", "validation_owner", "validation_claim", "validation_launch_ref", "validation_execution_ref", "validation_claimed_at", "validation_started_at", "acceptance_ref", "execution_ref", "packet", "local_scope"} {
		item[key] = nil
	}
	for key, value := range p.Item {
		item[key] = value
	}
	item["phase"], item["callback"], item["result"], item["validation"], item["integration"] = "planned", "none", "none", "pending", "pending"
	if mapStringValue(item, "executor") == "cloud" {
		item["phase"] = "ready"
	}
	item["evidence"] = []any{p.EvidenceRef}
	item["archived"] = false
	item["current_attempt"] = collaborationIntDefault(old, "current_attempt", 1) + 1
	if p.UserDecisionRef != "" {
		item["evidence"] = append(collaborationAnyList(item["evidence"]), p.UserDecisionRef)
	}
	if err := validateCollaborationItem(item, l.mission); err != nil {
		return nil, err
	}
	if err := l.checkDependencies(ctx, item); err != nil {
		return nil, err
	}
	if collaborationDispatchKey(item) != "" && collaborationDispatchKey(item) == collaborationDispatchKey(old) {
		return nil, errors.New("new attempt needs a new idempotency key")
	}
	if err := l.checkUnique(ctx, p.ItemID, item); err != nil {
		return nil, err
	}
	// Retain terminal facts and identities, not the old prompt/report body.
	snapshot := cloneParams(old)
	delete(snapshot, "packet")
	snapshot["dispatch_key"] = collaborationDispatchKey(old)
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	binding, _ := old["binding"].(map[string]any)
	oldClaim := mapStringValue(old, "claim")
	if oldClaim == "" {
		oldClaim = fmt.Sprintf("local-attempt-%d", collaborationIntDefault(old, "current_attempt", 1))
	}
	if _, err := l.conn.ExecContext(ctx, "INSERT INTO execution_attempts(item_id,attempt,claim,dispatch_key,task_ref,data) VALUES(?,?,?,?,?,?)", p.ItemID, collaborationIntDefault(old, "current_attempt", 1), oldClaim, nullableCollaborationString(collaborationDispatchKey(old)), nullableCollaborationString(mapStringValue(binding, "taskRef")), string(raw)); err != nil {
		return nil, err
	}
	if err := l.saveItem(ctx, item); err != nil {
		return nil, err
	}
	if err := c.enqueueCollaborationRoleWake(ctx, l, "coordinator", "retry_ready", p.ItemID, item["current_attempt"]); err != nil {
		return nil, err
	}
	if err := l.commit(ctx); err != nil {
		return nil, err
	}
	c.signalCollaborationRoleWake()
	return map[string]any{"revision": l.revision, "itemId": p.ItemID, "attempt": item["current_attempt"], "phase": item["phase"], "nextAction": "coordinator dispatches the persisted READY; do not create another business task"}, nil
}
