package node

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const collaborationInboxSchema = `
CREATE TABLE callback_inbox(result_id TEXT PRIMARY KEY, item_id TEXT NOT NULL, data TEXT NOT NULL,
 received_at INTEGER NOT NULL, resolution TEXT, resolved_at INTEGER);
CREATE INDEX pending_callback_inbox ON callback_inbox(resolved_at, result_id);
`

func (l *collaborationLedger) hasInbox(ctx context.Context) bool {
	var n int
	return l.conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='callback_inbox'").Scan(&n) == nil && n == 1
}

// PersistCollaborationCallback is called in-process, after the Agent's durable
// callback append and before notification/ACK. It never calls a Cloud provider.
func (c *Client) PersistCollaborationCallback(ctx context.Context, event map[string]any) error {
	route, _ := event["callbackInboxRoute"].(map[string]any)
	if route == nil { // Legacy registrations retain their existing transport.
		return nil
	}
	dbPath, err := validateCollaborationBaseIdentity(mapStringValue(route, "dbPath"), mapStringValue(route, "missionId"), mapStringValue(event, "targetSessionId"))
	if err != nil {
		return err
	}
	l, err := openCollaborationLedgerAccess(ctx, dbPath, mapStringValue(route, "missionId"), mapStringValue(event, "targetSessionId"), true, true, true)
	if err != nil {
		return err
	}
	defer l.rollback()
	if !l.hasInbox(ctx) {
		return errors.New("mission has no durable inbox; explicit migration required")
	}
	item, err := l.item(ctx, mapStringValue(route, "itemId"))
	if err != nil {
		return err
	}
	claim := mapStringValue(route, "claim")
	historical := false
	if claim != mapStringValue(item, "claim") && l.hasAttempts(ctx) {
		var raw string
		if err := l.conn.QueryRowContext(ctx, "SELECT data FROM execution_attempts WHERE item_id=? AND claim=?", mapStringValue(route, "itemId"), claim).Scan(&raw); err == nil {
			if err := json.Unmarshal([]byte(raw), &item); err != nil {
				return err
			}
			historical = true
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	mission, task, generation := localCallbackIdentity(collaborationToken{DBPath: dbPath, MissionID: mapStringValue(route, "missionId"), ItemID: mapStringValue(route, "itemId"), Claim: claim})
	if claim == "" || claim != mapStringValue(item, "claim") || mapStringValue(event, "missionId") != mission || mapStringValue(event, "taskId") != task || collaborationIntDefault(event, "generation", 0) != generation || mapStringValue(event, "sourceSessionId") == "" {
		return errors.New("callback does not match exact execution attempt")
	}
	key := mapStringValue(event, "eventKey")
	if key == "" {
		return errors.New("callback eventKey required")
	}
	resultID := "inbox-" + stableCollaborationDigest(mission+"|"+task+"|"+key)
	var existing string
	err = l.conn.QueryRowContext(ctx, "SELECT result_id FROM callback_inbox WHERE result_id=?", resultID).Scan(&existing)
	if err == nil {
		return nil // Includes resolved tombstones; late duplicates cannot reopen work.
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	stored := cloneParams(event)
	stored["inboxId"] = resultID
	raw, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	if len(raw) > 256*1024 {
		return errors.New("callback too large; use result manifest or deliverable reference")
	}
	var resolution any
	var resolvedAt any
	// An old/final attempt is a deduplicated historical fact, not a new decision.
	if historical || collaborationFinalPhases[mapStringValue(item, "phase")] || collaborationExecutionEnded(item) {
		resolution, resolvedAt = `{"decision":"historical"}`, time.Now().Unix()
	} else {
		binding := map[string]any{"chatSessionId": event["sourceSessionId"], "collaborationId": mission, "taskRef": task, "callbackSessionId": event["targetSessionId"], "idempotencyKey": collaborationDispatchKey(item)}
		if old, _ := item["binding"].(map[string]any); old != nil && !collaborationValueEqual(old, binding) {
			return errors.New("callback source differs from execution binding")
		}
		item["binding"], item["callback"], item["terminal_ref"] = binding, "received", resultID
		result := "completed"
		if mapStringValue(event, "outcome") == "failed" || mapStringValue(event, "resultStatus") == "failed" {
			result = "failed"
		}
		if mapStringValue(event, "resultStatus") == "blocked" {
			result = "blocked"
		}
		item["result"], item["phase"] = result, "returned"
		item["evidence"] = []any{resultID}
		item["blocker"], item["next_check_at"] = nil, nil
		item["next_action"] = "Resolve durable inbox result; execution completion is not business acceptance"
		if err := validateCollaborationItem(item, l.mission); err != nil {
			return err
		}
		if err := l.saveItem(ctx, item); err != nil {
			return err
		}
		if err := c.enqueueCollaborationRoleWake(ctx, l, "coordinator", "callback_capacity_released", mapStringValue(item, "id"), resultID); err != nil {
			return err
		}
		if mapStringValue(l.mission, "delivery_coordinator") != "" {
			if err := c.enqueueCollaborationRoleWake(ctx, l, "delivery_coordinator", "result_received", mapStringValue(item, "id"), resultID); err != nil {
				return err
			}
		}
	}
	if _, err := l.conn.ExecContext(ctx, "INSERT INTO callback_inbox(result_id,item_id,data,received_at,resolution,resolved_at) VALUES(?,?,?,?,?,?)", resultID, mapStringValue(item, "id"), string(raw), time.Now().Unix(), resolution, resolvedAt); err != nil {
		return err
	}
	if err := l.commit(ctx); err != nil {
		return err
	}
	if !historical {
		c.signalCollaborationRoleWake()
	}
	return nil
}

type collaborationInboxParams struct {
	collaborationIdentityParams
	After    string `json:"after,omitempty"`
	Limit    int64  `json:"limit,omitempty"`
	ResultID string `json:"resultId,omitempty"`
}

func (c *Client) collaborationInbox(ctx context.Context, p collaborationInboxParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(p.DBPath, p.MissionID, p.ActorSessionID)
	if err != nil {
		return nil, err
	}
	l, err := openCollaborationLedgerMode(ctx, dbPath, p.MissionID, p.ActorSessionID, false, true)
	if err != nil {
		return nil, err
	}
	defer l.rollback()
	if !l.hasInbox(ctx) {
		return nil, errors.New("mission has no durable inbox; use legacy callback_claim/ack")
	}
	if p.ResultID != "" {
		var raw string
		var resolution sql.NullString
		if err := l.conn.QueryRowContext(ctx, "SELECT data,resolution FROM callback_inbox WHERE result_id=?", p.ResultID).Scan(&raw, &resolution); err != nil {
			return nil, err
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			return nil, err
		}
		return map[string]any{"revision": l.revision, "result": data, "resolved": resolution.Valid}, nil
	}
	if p.Limit == 0 {
		p.Limit = 20
	}
	if p.Limit < 1 || p.Limit > 50 {
		return nil, errors.New("limit must be 1..50")
	}
	rows, err := l.conn.QueryContext(ctx, "SELECT result_id,item_id,data,received_at FROM callback_inbox WHERE resolved_at IS NULL AND result_id>? ORDER BY result_id LIMIT ?", p.After, p.Limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []map[string]any{}
	for rows.Next() {
		var id, itemID, raw string
		var received int64
		if err := rows.Scan(&id, &itemID, &raw, &received); err != nil {
			return nil, err
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			return nil, err
		}
		entry := map[string]any{"resultId": id, "itemId": itemID, "receivedAt": received}
		for _, key := range []string{"outcome", "callbackErrorCode", "resultStatus", "resultId", "deliverablePath"} {
			if key == "resultId" {
				entry["providerResultId"] = data[key]
			} else if data[key] != nil {
				entry[key] = data[key]
			}
		}
		results = append(results, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var after any
	if int64(len(results)) > p.Limit {
		results = results[:p.Limit]
		after = results[len(results)-1]["resultId"]
	}
	return map[string]any{"revision": l.revision, "results": results, "nextAfter": after}, nil
}

type collaborationResolveParams struct {
	collaborationIdentityParams
	ExpectedRevision int64          `json:"expectedRevision"`
	ResultID         string         `json:"resultId"`
	Decision         string         `json:"decision"`
	EvidenceRef      string         `json:"evidenceRef"`
	Validation       string         `json:"validation,omitempty"`
	Integration      string         `json:"integration,omitempty"`
	ValidationOwner  string         `json:"validationOwner,omitempty"`
	Blocker          map[string]any `json:"blocker,omitempty"`
	Followup         string         `json:"followup,omitempty"`
}

type collaborationDecisionBatchParams struct {
	collaborationIdentityParams
	ExpectedRevision int64                        `json:"expectedRevision"`
	Decisions        []collaborationResolveParams `json:"decisions"`
}

// All business decisions commit together. Transport ACKs happen afterwards and
// replay the immutable result resolutions, so a failed ACK never reruns work.
func (c *Client) collaborationDecisionBatch(ctx context.Context, p collaborationDecisionBatchParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(p.DBPath, p.MissionID, p.ActorSessionID)
	if err != nil {
		return nil, err
	}
	if len(p.Decisions) == 0 || len(p.Decisions) > 20 {
		return nil, errors.New("decision batch must contain 1..20 results")
	}
	l, err := openCollaborationLedger(ctx, dbPath, p.MissionID, p.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer l.rollback()
	if mapStringValue(l.mission, "controller") != p.ActorSessionID {
		return nil, errors.New("controller-only result resolution")
	}
	baseRevision := l.revision
	seen := map[string]bool{}
	results := []map[string]any{}
	events := []map[string]any{}
	for _, decision := range p.Decisions {
		if seen[decision.ResultID] {
			return nil, errors.New("duplicate result in decision batch")
		}
		seen[decision.ResultID] = true
		decision.collaborationIdentityParams = p.collaborationIdentityParams
		decision.ExpectedRevision = p.ExpectedRevision
		if p.ExpectedRevision == baseRevision {
			decision.ExpectedRevision = l.revision
		}
		result, event, err := c.resolveCollaborationInTransaction(ctx, l, decision)
		if err != nil {
			return nil, err
		}
		results, events = append(results, result), append(events, event)
	}
	if err := l.commit(ctx); err != nil {
		return nil, err
	}
	c.signalCollaborationRoleWake()
	revision := l.revision
	identity := p.collaborationIdentityParams
	identity.DBPath = dbPath
	for i, result := range results {
		ackRevision, ackErr := c.ackCollaborationInbox(ctx, identity, mapStringValue(result, "itemId"), events[i])
		if ackRevision > revision {
			revision = ackRevision
		}
		result["transportAcked"] = ackErr == nil
		if ackErr != nil {
			result["ackPending"], result["ackError"] = true, ackErr.Error()
		}
	}
	return map[string]any{"revision": revision, "resolved": true, "results": results}, nil
}

func (c *Client) collaborationResolve(ctx context.Context, p collaborationResolveParams) (map[string]any, error) {
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
		return nil, errors.New("controller-only result resolution")
	}
	result, event, err := c.resolveCollaborationInTransaction(ctx, l, p)
	if err != nil {
		return nil, err
	}
	itemID := mapStringValue(result, "itemId")
	if err := l.commit(ctx); err != nil {
		return nil, err
	}
	c.signalCollaborationRoleWake()
	// Business decision is durable before transport ACK. A crash/error here is
	// recoverable by replaying this same resolve, never by repeating Cloud work.
	identity := p.collaborationIdentityParams
	identity.DBPath = dbPath
	ackRevision, ackErr := c.ackCollaborationInbox(ctx, identity, itemID, event)
	revision := l.revision
	if ackRevision > 0 {
		revision = ackRevision
	}
	result["transportAcked"], result["revision"] = ackErr == nil, revision
	if ackErr != nil {
		result["ackError"] = ackErr.Error()
		result["nextAction"] = "retry same resolve to settle callback transport"
		result["ackPending"] = true
	}
	return result, nil
}

func (c *Client) resolveCollaborationInTransaction(ctx context.Context, l *collaborationLedger, p collaborationResolveParams) (map[string]any, map[string]any, error) {
	var itemID, raw string
	var resolution sql.NullString
	if err := l.conn.QueryRowContext(ctx, "SELECT item_id,data,resolution FROM callback_inbox WHERE result_id=?", p.ResultID).Scan(&itemID, &raw, &resolution); err != nil {
		return nil, nil, err
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		return nil, nil, err
	}
	wantedFields := map[string]any{"decision": p.Decision, "evidenceRef": p.EvidenceRef, "validation": p.Validation, "integration": p.Integration, "validationOwner": p.ValidationOwner, "blocker": p.Blocker}
	if p.Followup != "" {
		if p.Followup != "prepare" || p.Decision != "accept" {
			return nil, nil, errors.New("followup=prepare requires an accept decision")
		}
		wantedFields["followup"] = p.Followup
	}
	wanted, _ := json.Marshal(wantedFields)
	duplicate := resolution.Valid
	if duplicate {
		if resolution.String != string(wanted) {
			return nil, nil, errors.New("result already resolved differently")
		}
	} else {
		if l.revision != p.ExpectedRevision {
			return nil, nil, fmt.Errorf("revision conflict; current=%d", l.revision)
		}
		if err := validateCollaborationText(p.EvidenceRef, "resolution evidence", 1024); err != nil {
			return nil, nil, err
		}
		item, err := l.item(ctx, itemID)
		if err != nil {
			return nil, nil, err
		}
		old := cloneParams(item)
		item["blocker"] = nil
		if p.Decision != "verify" && mapStringValue(item, "phase") == "verifying" {
			clearCollaborationValidationExecution(item)
		}
		switch p.Decision {
		case "accept":
			item["phase"], item["validation"], item["integration"] = "accepted", p.Validation, p.Integration
			if mapStringValue(item, "callback") == "acked" {
				item["phase"] = "done"
			}
			item["acceptance_ref"] = p.EvidenceRef
		case "verify":
			clearCollaborationValidationExecution(item)
			item["phase"], item["validation_owner"] = "verifying", p.ValidationOwner
		case "integrate":
			item["phase"], item["validation"] = "integrating", p.Validation
			if p.Validation != "passed" && p.Validation != "not_required" {
				return nil, nil, errors.New("integration needs completed validation")
			}
		case "rework":
			item["phase"] = "rework"
		case "block":
			item["phase"], item["blocker"] = "blocked", p.Blocker
		default:
			return nil, nil, errors.New("decision must be accept, verify, integrate, rework or block")
		}
		item["evidence"] = append(collaborationAnyList(item["evidence"]), p.EvidenceRef)
		item["next_action"] = "Controller decision: " + p.Decision
		if err := validateCollaborationItem(item, l.mission); err != nil {
			return nil, nil, err
		}
		if err := validateCollaborationItemUpdate(old, item); err != nil {
			return nil, nil, err
		}
		if err := l.saveItem(ctx, item); err != nil {
			return nil, nil, err
		}
		if p.Followup == "prepare" {
			if err := c.createCollaborationFollowup(ctx, l, item, p.ResultID, p.EvidenceRef); err != nil {
				return nil, nil, err
			}
		}
		wakeReason := "result_resolved_" + p.Decision
		if err := c.enqueueCollaborationRoleWake(ctx, l, "coordinator", wakeReason, itemID, p.ResultID); err != nil {
			return nil, nil, err
		}
		if _, err := l.conn.ExecContext(ctx, "UPDATE callback_inbox SET resolution=?,resolved_at=? WHERE result_id=?", string(wanted), time.Now().Unix(), p.ResultID); err != nil {
			return nil, nil, err
		}
	}
	result := map[string]any{"resolved": true, "duplicate": duplicate, "itemId": itemID}
	if p.Followup == "prepare" {
		result["followupItemId"] = "followup-" + stableCollaborationDigest(p.ResultID)[:24]
	}
	return result, event, nil
}

func (c *Client) ackCollaborationInbox(ctx context.Context, identity collaborationIdentityParams, itemID string, event map[string]any) (int64, error) {
	if c.agent == nil {
		return 0, ErrAgentProviderUnavailable
	}
	_, err := c.agent.Control(ctx, "session.callback.ack", map[string]any{"mode": "completion", "providerId": "codex", "backend": "chatgpt_cloud", "sessionId": event["sourceSessionId"], "callbackTargetSessionId": event["targetSessionId"], "callbackMissionId": event["missionId"], "callbackTaskId": event["taskId"], "callbackGeneration": event["generation"], "callbackClaimTransport": "local"})
	if err != nil {
		return 0, err
	}
	l, err := openCollaborationLedger(ctx, identity.DBPath, identity.MissionID, identity.ActorSessionID, true)
	if err != nil {
		return 0, err
	}
	defer l.rollback()
	item, err := l.item(ctx, itemID)
	if err != nil {
		return 0, err
	}
	route, _ := event["callbackInboxRoute"].(map[string]any)
	if mapStringValue(item, "claim") != mapStringValue(route, "claim") {
		// Replayed ACK belongs to an archived attempt, not the task's new run.
		return l.revision, nil
	}
	if mapStringValue(item, "callback") != "acked" {
		item["callback"] = "acked"
		if mapStringValue(item, "phase") == "accepted" {
			item["phase"] = "done"
		}
		if err := validateCollaborationItem(item, l.mission); err != nil {
			return 0, err
		}
		if err := l.saveItem(ctx, item); err != nil {
			return 0, err
		}
	}
	return l.revision, l.commit(ctx)
}
