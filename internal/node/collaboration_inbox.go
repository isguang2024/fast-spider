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
	}
	if _, err := l.conn.ExecContext(ctx, "INSERT INTO callback_inbox(result_id,item_id,data,received_at,resolution,resolved_at) VALUES(?,?,?,?,?,?)", resultID, mapStringValue(item, "id"), string(raw), time.Now().Unix(), resolution, resolvedAt); err != nil {
		return err
	}
	return l.commit(ctx)
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
	var itemID, raw string
	var resolution sql.NullString
	if err := l.conn.QueryRowContext(ctx, "SELECT item_id,data,resolution FROM callback_inbox WHERE result_id=?", p.ResultID).Scan(&itemID, &raw, &resolution); err != nil {
		return nil, err
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		return nil, err
	}
	wanted, _ := json.Marshal(map[string]any{"decision": p.Decision, "evidenceRef": p.EvidenceRef, "validation": p.Validation, "integration": p.Integration, "validationOwner": p.ValidationOwner, "blocker": p.Blocker})
	duplicate := resolution.Valid
	if duplicate {
		if resolution.String != string(wanted) {
			return nil, errors.New("result already resolved differently")
		}
	} else {
		if l.revision != p.ExpectedRevision {
			return nil, fmt.Errorf("revision conflict; current=%d", l.revision)
		}
		if err := validateCollaborationText(p.EvidenceRef, "resolution evidence", 1024); err != nil {
			return nil, err
		}
		item, err := l.item(ctx, itemID)
		if err != nil {
			return nil, err
		}
		old := cloneParams(item)
		item["blocker"] = nil
		switch p.Decision {
		case "accept":
			item["phase"], item["validation"], item["integration"] = "accepted", p.Validation, p.Integration
			if mapStringValue(item, "callback") == "acked" {
				item["phase"] = "done"
			}
			item["acceptance_ref"] = p.EvidenceRef
		case "verify":
			item["phase"], item["validation_owner"], item["validation_started_at"] = "verifying", p.ValidationOwner, time.Now().Unix()
		case "integrate":
			item["phase"], item["validation"] = "integrating", p.Validation
			if p.Validation != "passed" && p.Validation != "not_required" {
				return nil, errors.New("integration needs completed validation")
			}
		case "rework":
			item["phase"] = "rework"
		case "block":
			item["phase"], item["blocker"] = "blocked", p.Blocker
		default:
			return nil, errors.New("decision must be accept, verify, integrate, rework or block")
		}
		item["evidence"] = append(collaborationAnyList(item["evidence"]), p.EvidenceRef)
		item["next_action"] = "Controller decision: " + p.Decision
		if err := validateCollaborationItem(item, l.mission); err != nil {
			return nil, err
		}
		if err := validateCollaborationItemUpdate(old, item); err != nil {
			return nil, err
		}
		if err := l.saveItem(ctx, item); err != nil {
			return nil, err
		}
		if _, err := l.conn.ExecContext(ctx, "UPDATE callback_inbox SET resolution=?,resolved_at=? WHERE result_id=?", string(wanted), time.Now().Unix(), p.ResultID); err != nil {
			return nil, err
		}
	}
	if err := l.commit(ctx); err != nil {
		return nil, err
	}
	// Business decision is durable before transport ACK. A crash/error here is
	// recoverable by replaying this same resolve, never by repeating Cloud work.
	identity := p.collaborationIdentityParams
	identity.DBPath = dbPath
	ackRevision, ackErr := c.ackCollaborationInbox(ctx, identity, itemID, event)
	revision := l.revision
	if ackRevision > 0 {
		revision = ackRevision
	}
	result := map[string]any{"resolved": true, "duplicate": duplicate, "itemId": itemID, "transportAcked": ackErr == nil, "revision": revision}
	if ackErr != nil {
		result["ackError"] = ackErr.Error()
		result["nextAction"] = "retry same resolve to settle callback transport"
		result["ackPending"] = true
	}
	return result, nil
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
