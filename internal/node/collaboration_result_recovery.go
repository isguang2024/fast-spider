package node

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type collaborationResultRecoverParams struct {
	collaborationIdentityParams
	ExpectedRevision int64  `json:"expectedRevision"`
	ResultID         string `json:"resultId"`
}

// Repairs a transport-only result misclassification. Existing business
// decisions and phases remain untouched; only the controller can adopt it.
func (c *Client) collaborationResultRecover(ctx context.Context, p collaborationResultRecoverParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(p.DBPath, p.MissionID, p.ActorSessionID)
	if err != nil {
		return nil, err
	}
	l, err := openCollaborationLedger(ctx, dbPath, p.MissionID, p.ActorSessionID, false)
	if err != nil {
		return nil, err
	}
	defer l.rollback()
	if mapStringValue(l.mission, "controller") != p.ActorSessionID {
		return nil, errors.New("controller-only result recovery")
	}
	var itemID, raw string
	var resolution sql.NullString
	if err := l.conn.QueryRowContext(ctx, "SELECT item_id,data,resolution FROM callback_inbox WHERE result_id=?", p.ResultID).Scan(&itemID, &raw, &resolution); err != nil {
		return nil, err
	}
	event := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		return nil, err
	}
	item, err := l.item(ctx, itemID)
	if err != nil {
		return nil, err
	}
	if mapStringValue(item, "terminal_ref") != p.ResultID {
		return nil, errors.New("result no longer belongs to the current execution")
	}
	if event["transportRecovery"] != nil && mapStringValue(item, "result") == "completed" {
		return map[string]any{"revision": l.revision, "itemId": itemID, "resultId": p.ResultID, "providerResultId": event["resultId"], "replayed": true, "phase": item["phase"]}, nil
	}
	if mapStringValue(event, "callbackErrorCode") != "CALLBACK_TEXT_TOO_LARGE" || mapStringValue(item, "result") != "failed" {
		return nil, errors.New("only callback text overflow can be recovered by this action")
	}
	if resolution.Valid {
		decision := map[string]any{}
		if err := json.Unmarshal([]byte(resolution.String), &decision); err != nil {
			return nil, err
		}
		if mapStringValue(decision, "decision") != "block" || mapStringValue(item, "phase") != "blocked" {
			return nil, errors.New("existing business decision cannot be replaced by result recovery")
		}
	} else if mapStringValue(item, "phase") != "returned" {
		return nil, errors.New("result recovery requires an undecided returned result or a preserved blocked decision")
	}
	if l.revision != p.ExpectedRevision {
		return nil, fmt.Errorf("revision conflict; current=%d", l.revision)
	}
	sourceID := mapStringValue(event, "sourceSessionId")
	if sourceID == "" || mapStringValue(collaborationOptionalMap(item["binding"]), "chatSessionId") != sourceID {
		return nil, errors.New("result source does not match its execution binding")
	}
	key := collaborationDispatchKey(item)
	if key == "" {
		return nil, errors.New("original result idempotency key is unavailable")
	}
	l.rollback() // Never hold the mission transaction while reading the provider.
	if c.agent == nil {
		return nil, ErrAgentProviderUnavailable
	}
	verified, err := c.agent.Control(ctx, "session.result", map[string]any{"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": sourceID, "resultMode": "manifest", "idempotencyKey": key})
	if err != nil {
		return nil, err
	}
	providerID := mapStringValue(verified, "resultId")
	digest := mapStringValue(verified, "resultSHA256")
	decoded, decodeErr := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if mapStringValue(verified, "status") != "completed" || mapStringValue(verified, "resultStatus") != "ready" || !strings.HasPrefix(providerID, "res_") || decodeErr != nil || len(decoded) != 32 || collaborationIntDefault(verified, "resultBytes", 0) <= 0 {
		return nil, errors.New("provider did not return a complete ready result manifest")
	}
	if returned := mapStringValue(verified, "sessionId"); returned != "" && returned != sourceID {
		return nil, errors.New("provider result belongs to another session")
	}
	w, err := openCollaborationLedger(ctx, dbPath, p.MissionID, p.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer w.rollback()
	if mapStringValue(w.mission, "controller") != p.ActorSessionID {
		return nil, errors.New("controller changed during result recovery")
	}
	if w.revision != p.ExpectedRevision {
		return nil, fmt.Errorf("revision conflict; current=%d", w.revision)
	}
	item, err = w.item(ctx, itemID)
	if err != nil {
		return nil, err
	}
	event["transportRecovery"] = map[string]any{"originalCallbackErrorCode": event["callbackErrorCode"], "originalOutcome": event["outcome"], "verifiedAt": time.Now().Unix(), "providerResultId": providerID}
	delete(event, "callbackErrorCode")
	event["outcome"], event["callbackOutcome"], event["resultStatus"], event["resultId"] = "completed", "completed", "ready", providerID
	event["resultBytes"], event["resultSHA256"], event["resultPageCount"] = verified["resultBytes"], digest, verified["resultPageCount"]
	item["result"] = "completed"
	item["evidence"] = append(collaborationAnyList(item["evidence"]), "provider-result:"+providerID+" "+digest)
	if err := validateCollaborationItem(item, w.mission); err != nil {
		return nil, err
	}
	if err := w.saveItem(ctx, item); err != nil {
		return nil, err
	}
	updated, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	if _, err := w.conn.ExecContext(ctx, "UPDATE callback_inbox SET data=? WHERE result_id=?", string(updated), p.ResultID); err != nil {
		return nil, err
	}
	if err := c.enqueueCollaborationRoleWake(ctx, w, collaborationDeliveryRole(w.mission), "result_recovered", itemID, p.ResultID); err != nil {
		return nil, err
	}
	if err := w.commit(ctx); err != nil {
		return nil, err
	}
	c.signalCollaborationRoleWake()
	nextAction := "Provider result facts restored. Controller resolves this undecided inbox using the complete result evidence; do not retry Cloud execution."
	if resolution.Valid {
		nextAction = "Provider result facts restored. Preserve the original decision audit; controller applies the next business disposition using this evidence, without retrying Cloud execution or resolving the old inbox again."
	}
	return map[string]any{"revision": w.revision, "itemId": itemID, "resultId": p.ResultID, "providerResultId": providerID, "resultSHA256": digest, "phase": item["phase"], "businessDecisionPreserved": resolution.Valid, "nextAction": nextAction}, nil
}

func (l *collaborationLedger) addCollaborationResultHints(ctx context.Context, itemID, resultID string, event, entry map[string]any) error {
	callbackType := mapStringValue(event, "callbackType")
	if callbackType != "status" && callbackType != "text" && mapStringValue(event, "callbackErrorCode") != "CALLBACK_TEXT_TOO_LARGE" {
		return nil
	}
	item, err := l.item(ctx, itemID)
	if err != nil {
		return err
	}
	if mapStringValue(item, "terminal_ref") != resultID {
		return nil
	}
	entry["resultFetch"] = map[string]any{"capability": "agent.control", "action": "session.result", "params": map[string]any{"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": event["sourceSessionId"], "resultMode": "manifest", "idempotencyKey": collaborationDispatchKey(item)}}
	if mapStringValue(event, "callbackErrorCode") == "CALLBACK_TEXT_TOO_LARGE" {
		entry["resultRecovery"] = map[string]any{"controllerOnly": true, "action": "result_recover", "params": map[string]any{"dbPath": l.mission["db_path"], "missionId": l.mission["id"], "actorSessionId": l.mission["controller"], "expectedRevision": l.revision, "resultId": resultID}}
	}
	return nil
}
