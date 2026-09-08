package node

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

const collaborationTokenVersion = 1

var collaborationTokenStoreMu sync.Mutex

type collaborationClaimParams struct {
	DBPath           string `json:"dbPath"`
	MissionID        string `json:"missionId"`
	ActorSessionID   string `json:"actorSessionId"`
	ExpectedRevision int64  `json:"expectedRevision"`
	ItemID           string `json:"itemId"`
}

type collaborationRecoverParams struct {
	DBPath         string `json:"dbPath"`
	MissionID      string `json:"missionId"`
	ActorSessionID string `json:"actorSessionId"`
	ItemID         string `json:"itemId"`
}

type collaborationTokenParams struct {
	DispatchToken  string         `json:"dispatchToken"`
	DispatchResult map[string]any `json:"dispatchResult,omitempty"`
	EvidenceRef    string         `json:"evidenceRef,omitempty"`
	NoTaskCreated  bool           `json:"noTaskCreated,omitempty"`
}

type collaborationToken struct {
	Version         int            `json:"version"`
	DBPath          string         `json:"dbPath,omitempty"`
	MissionID       string         `json:"missionId,omitempty"`
	ActorSessionID  string         `json:"actorSessionId,omitempty"`
	ItemID          string         `json:"itemId,omitempty"`
	Claim           string         `json:"claim,omitempty"`
	PacketSHA256    string         `json:"packetSHA256,omitempty"`
	DispatchRequest map[string]any `json:"dispatchRequest,omitempty"`
	Completed       map[string]any `json:"completed,omitempty"`
	ExpiresAt       int64          `json:"expiresAt"`
}

type collaborationLedger struct {
	db       *sql.DB
	conn     *sql.Conn
	mission  map[string]any
	revision int64
	closed   bool
}

func (c *Client) collaborationControl(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	action = strings.TrimSpace(action)
	if params == nil {
		params = map[string]any{}
	}
	switch action {
	case "claim":
		if err := requireCollaborationParams(params, "dbPath", "missionId", "actorSessionId", "expectedRevision", "itemId"); err != nil {
			return nil, err
		}
		var input collaborationClaimParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid collaboration control params: %w", err)
		}
		return c.collaborationClaim(ctx, input)
	case "recover":
		if err := requireCollaborationParams(params, "dbPath", "missionId", "actorSessionId", "itemId"); err != nil {
			return nil, err
		}
		var input collaborationRecoverParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid collaboration control params: %w", err)
		}
		return c.collaborationRecover(ctx, input)
	case "receipt", "uncertain", "not_created", "verify":
		required := []string{"dispatchToken"}
		if action == "receipt" {
			required = append(required, "dispatchResult")
		}
		if action == "uncertain" || action == "not_created" {
			required = append(required, "evidenceRef")
		}
		if action == "not_created" {
			required = append(required, "noTaskCreated")
		}
		if err := requireCollaborationParams(params, required...); err != nil {
			return nil, err
		}
		var input collaborationTokenParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid collaboration control params: %w", err)
		}
		return c.collaborationUseToken(ctx, action, input)
	default:
		return nil, fmt.Errorf("unsupported collaboration control action %q", action)
	}
}

func (c *Client) collaborationClaim(ctx context.Context, input collaborationClaimParams) (map[string]any, error) {
	if input.ExpectedRevision < 0 {
		return nil, errors.New("expectedRevision must be nonnegative")
	}
	dbPath, err := validateCollaborationIdentity(input.DBPath, input.MissionID, input.ActorSessionID, input.ItemID)
	if err != nil {
		return nil, err
	}
	ledger, err := openCollaborationLedger(ctx, dbPath, input.MissionID, input.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	if mapStringValue(ledger.mission, "coordinator") != input.ActorSessionID {
		return nil, errors.New("only bound coordinator claims READY")
	}
	if mapStringValue(ledger.mission, "status") != "active" || !mapBoolValue(ledger.mission, "dispatch_enabled") {
		return nil, errors.New("dispatch is paused/disabled")
	}
	if ledger.revision != input.ExpectedRevision {
		return nil, fmt.Errorf("revision conflict; current=%d", ledger.revision)
	}
	item, err := ledger.item(ctx, input.ItemID)
	if err != nil {
		return nil, err
	}
	if mapStringValue(item, "phase") != "ready" {
		return nil, errors.New("not READY; reconcile existing claim, never redispatch blindly")
	}
	if mapStringValue(item, "executor") != "cloud" {
		return nil, errors.New("Cloud dispatch requires a frozen packet")
	}
	packet, ok := item["packet"].(map[string]any)
	if !ok {
		return nil, errors.New("Cloud dispatch requires a frozen packet")
	}
	if err := validateCollaborationPacket(packet, ledger.mission, true); err != nil {
		return nil, err
	}
	if err := ledger.checkDependencies(ctx, item); err != nil {
		return nil, err
	}
	if err := ledger.checkUnique(ctx, input.ItemID, item); err != nil {
		return nil, err
	}
	if err := ledger.checkCloudCapacity(ctx); err != nil {
		return nil, err
	}
	claim, err := randomCollaborationClaim()
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	item["phase"] = "dispatching"
	item["claim"] = claim
	item["started_at"] = now
	if _, ok := item["next_check_at"]; !ok || item["next_check_at"] == nil {
		item["next_check_at"] = now + 900
	}
	item["next_action"] = "Await dispatch receipt; uncertainty requires original-key reconciliation"
	if err := ledger.saveItem(ctx, item); err != nil {
		return nil, err
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return c.storeCollaborationClaim(dbPath, input.MissionID, input.ActorSessionID, input.ItemID, claim, packet, ledger.revision)
}

func (c *Client) collaborationRecover(ctx context.Context, input collaborationRecoverParams) (map[string]any, error) {
	dbPath, err := validateCollaborationIdentity(input.DBPath, input.MissionID, input.ActorSessionID, input.ItemID)
	if err != nil {
		return nil, err
	}
	ledger, err := openCollaborationLedger(ctx, dbPath, input.MissionID, input.ActorSessionID, false)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	if mapStringValue(ledger.mission, "coordinator") != input.ActorSessionID {
		return nil, errors.New("only bound coordinator recovers a dispatch token")
	}
	if mapStringValue(ledger.mission, "status") != "active" || !mapBoolValue(ledger.mission, "dispatch_enabled") {
		return nil, errors.New("dispatch is paused/disabled")
	}
	item, err := ledger.item(ctx, input.ItemID)
	if err != nil {
		return nil, err
	}
	phase := mapStringValue(item, "phase")
	if phase != "dispatching" && phase != "in_doubt" {
		return nil, fmt.Errorf("item phase %q cannot recover a dispatch token", phase)
	}
	claim := mapStringValue(item, "claim")
	packet, ok := item["packet"].(map[string]any)
	if claim == "" || !ok {
		return nil, errors.New("ledger item has no recoverable frozen packet")
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return c.storeCollaborationClaim(dbPath, input.MissionID, input.ActorSessionID, input.ItemID, claim, packet, ledger.revision)
}

func (c *Client) storeCollaborationClaim(dbPath, missionID, actorSessionID, itemID, claim string, packet map[string]any, revision int64) (map[string]any, error) {
	packetBytes, err := json.Marshal(packet)
	if err != nil {
		return nil, err
	}
	digest := "sha256:" + hex.EncodeToString(collaborationHash(packetBytes))
	tokenID := collaborationTokenID(dbPath, missionID, itemID, claim)
	record := collaborationToken{
		Version: collaborationTokenVersion, DBPath: dbPath, MissionID: missionID, ActorSessionID: actorSessionID,
		ItemID: itemID, Claim: claim, PacketSHA256: digest,
		DispatchRequest: map[string]any{"action": "dispatch", "params": packet},
		ExpiresAt:       time.Now().Add(24 * time.Hour).Unix(),
	}
	if err := c.writeCollaborationToken(tokenID, record); err != nil {
		return nil, err
	}
	return map[string]any{
		"dispatchToken": tokenID, "itemId": itemID, "claim": claim, "packetSHA256": digest,
		"ledgerRevision": revision, "dispatchRequest": record.DispatchRequest,
		"nextAction": "Pass dispatchRequest unchanged to FastSpider_FS once, then pass its untouched structured result to receipt. Do not wait or poll.",
	}, nil
}

func (c *Client) collaborationUseToken(ctx context.Context, action string, input collaborationTokenParams) (map[string]any, error) {
	token, err := c.readCollaborationToken(input.DispatchToken)
	if err != nil {
		return nil, err
	}
	if token.Completed != nil {
		return token.Completed, nil
	}
	if token.ExpiresAt <= time.Now().Unix() {
		return nil, errors.New("dispatch token expired; use recover with the exact ledger item")
	}
	if action == "verify" {
		return map[string]any{"dispatchToken": input.DispatchToken, "itemId": token.ItemID, "claim": token.Claim, "packetSHA256": token.PacketSHA256, "dispatchRequest": token.DispatchRequest}, nil
	}
	if action == "uncertain" {
		if strings.TrimSpace(input.EvidenceRef) == "" {
			return nil, errors.New("evidenceRef is required for an uncertain dispatch")
		}
		return c.recordCollaborationUncertain(ctx, input.DispatchToken, token, input.EvidenceRef)
	}
	if action == "not_created" {
		if !input.NoTaskCreated || strings.TrimSpace(input.EvidenceRef) == "" {
			return nil, errors.New("not_created requires explicit noTaskCreated=true and evidenceRef")
		}
		return c.recordCollaborationNotCreated(ctx, input.DispatchToken, token, input.EvidenceRef)
	}
	dispatch, err := unwrapCollaborationDispatchResult(input.DispatchResult)
	if err != nil {
		return nil, err
	}
	if collaborationDispatchUncertain(dispatch) {
		return c.recordCollaborationUncertain(ctx, input.DispatchToken, token, "fast-spider:structured-dispatch-uncertain")
	}
	packet, ok := token.DispatchRequest["params"].(map[string]any)
	if !ok {
		return nil, errors.New("dispatchToken record is incomplete")
	}
	if value := mapStringValue(dispatch, "callbackSessionId"); value != "" && value != mapStringValue(packet, "callbackSessionId") {
		return nil, errors.New("dispatch callbackSessionId differs from the frozen packet")
	}
	if value := mapStringValue(dispatch, "idempotencyKey"); value != "" && value != mapStringValue(packet, "idempotencyKey") {
		return nil, errors.New("dispatch idempotencyKey differs from the frozen packet")
	}
	if target := mapStringValue(packet, "targetSessionId"); target != "" && mapStringValue(dispatch, "chatSessionId") != target {
		return nil, errors.New("dispatch chatSessionId differs from the frozen target")
	}
	binding := map[string]any{
		"chatSessionId": mapStringValue(dispatch, "chatSessionId"), "collaborationId": mapStringValue(dispatch, "collaborationId"),
		"taskRef": mapStringValue(dispatch, "taskRef"), "callbackSessionId": mapStringValue(packet, "callbackSessionId"),
		"idempotencyKey": mapStringValue(packet, "idempotencyKey"),
	}
	result, err := c.recordCollaborationActive(ctx, token, binding)
	if err != nil {
		return nil, err
	}
	completed := map[string]any{
		"itemId": token.ItemID, "binding": binding, "packetSHA256": token.PacketSHA256,
		"ledgerRevision": result["revision"], "phase": result["phase"],
		"nextAction": "End the current turn and await the formal callback.",
	}
	if replayed, ok := result["replayed"]; ok {
		completed["replayed"] = replayed
	}
	return c.finishCollaborationToken(input.DispatchToken, token, completed)
}

func (c *Client) recordCollaborationNotCreated(ctx context.Context, dispatchToken string, token collaborationToken, evidenceRef string) (map[string]any, error) {
	ledger, err := openCollaborationLedger(ctx, token.DBPath, token.MissionID, token.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	if mapStringValue(ledger.mission, "coordinator") != token.ActorSessionID {
		return nil, errors.New("only bound coordinator records dispatch receipt")
	}
	item, err := ledger.item(ctx, token.ItemID)
	if err != nil {
		return nil, err
	}
	if mapStringValue(item, "claim") != token.Claim {
		return nil, errors.New("wrong dispatch claim")
	}
	phase := mapStringValue(item, "phase")
	if phase == "dispatch_rejected" && mapStringValue(item, "terminal_ref") == evidenceRef {
		if err := ledger.commit(ctx); err != nil {
			return nil, err
		}
		return c.finishCollaborationToken(dispatchToken, token, map[string]any{
			"itemId": token.ItemID, "phase": phase, "ledgerRevision": ledger.revision,
			"replayed": true, "evidenceRef": evidenceRef,
			"nextAction": "Controller closes the rejected round, then may freeze a corrected new round.",
		})
	}
	if phase != "dispatching" && phase != "in_doubt" {
		return nil, errors.New("not-created receipt conflicts with current state")
	}
	item["phase"] = "dispatch_rejected"
	item["terminal_ref"] = evidenceRef
	item["evidence"] = appendUniqueCollaborationString(collaborationStringList(item["evidence"]), evidenceRef)
	item["next_action"] = "Controller closes rejected round, then may freeze a corrected new round"
	if err := ledger.saveItem(ctx, item); err != nil {
		return nil, err
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return c.finishCollaborationToken(dispatchToken, token, map[string]any{
		"itemId": token.ItemID, "phase": "dispatch_rejected", "ledgerRevision": ledger.revision,
		"evidenceRef": evidenceRef,
		"nextAction":  "Controller closes the rejected round, then may freeze a corrected new round.",
	})
}

func (c *Client) finishCollaborationToken(dispatchToken string, token collaborationToken, completed map[string]any) (map[string]any, error) {
	completed["packetSHA256"] = token.PacketSHA256
	finished := collaborationToken{Version: collaborationTokenVersion, Completed: completed, ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if err := c.writeCollaborationToken(dispatchToken, finished); err != nil {
		return nil, err
	}
	return completed, nil
}

func (c *Client) recordCollaborationUncertain(ctx context.Context, dispatchToken string, token collaborationToken, evidenceRef string) (map[string]any, error) {
	ledger, err := openCollaborationLedger(ctx, token.DBPath, token.MissionID, token.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	if mapStringValue(ledger.mission, "coordinator") != token.ActorSessionID {
		return nil, errors.New("only bound coordinator records dispatch receipt")
	}
	item, err := ledger.item(ctx, token.ItemID)
	if err != nil {
		return nil, err
	}
	if mapStringValue(item, "claim") != token.Claim {
		return nil, errors.New("wrong dispatch claim")
	}
	phase := mapStringValue(item, "phase")
	if phase != "dispatching" && phase != "in_doubt" {
		return nil, errors.New("late uncertain receipt conflicts with current state")
	}
	if phase == "dispatching" || mapStringValue(item, "next_action") != "Reconcile original key and frozen packet; do not create another round" {
		item["phase"] = "in_doubt"
		item["next_action"] = "Reconcile original key and frozen packet; do not create another round"
		if err := ledger.saveItem(ctx, item); err != nil {
			return nil, err
		}
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{
		"revision": ledger.revision, "phase": "in_doubt", "dispatchToken": dispatchToken,
		"packetSHA256": token.PacketSHA256, "evidenceRef": evidenceRef,
		"nextAction": "Keep the original token, packet and key; schedule one bounded reconciliation and end the turn.",
	}, nil
}

func (c *Client) recordCollaborationActive(ctx context.Context, token collaborationToken, binding map[string]any) (map[string]any, error) {
	ledger, err := openCollaborationLedger(ctx, token.DBPath, token.MissionID, token.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	if mapStringValue(ledger.mission, "coordinator") != token.ActorSessionID {
		return nil, errors.New("only bound coordinator records dispatch receipt")
	}
	item, err := ledger.item(ctx, token.ItemID)
	if err != nil {
		return nil, err
	}
	if mapStringValue(item, "claim") != token.Claim {
		return nil, errors.New("wrong dispatch claim")
	}
	if err := validateCollaborationBinding(binding, item, ledger.mission); err != nil {
		return nil, err
	}
	phase := mapStringValue(item, "phase")
	if phase == "blocked" && item["binding"] == nil && collaborationHoldsExecution(item) {
		item["binding"] = binding
		if err := ledger.checkUnique(ctx, token.ItemID, item); err != nil {
			return nil, err
		}
		if err := ledger.saveItem(ctx, item); err != nil {
			return nil, err
		}
		if err := ledger.commit(ctx); err != nil {
			return nil, err
		}
		return map[string]any{"revision": ledger.revision, "phase": "blocked"}, nil
	}
	if phase != "dispatching" && phase != "in_doubt" {
		stored, _ := item["binding"].(map[string]any)
		if !collaborationMapsEqual(stored, binding) {
			return nil, errors.New("late receipt conflicts with current state")
		}
		if err := ledger.commit(ctx); err != nil {
			return nil, err
		}
		return map[string]any{"revision": ledger.revision, "phase": phase, "replayed": true}, nil
	}
	item["binding"] = binding
	item["phase"] = "active"
	item["next_action"] = "Await formal callback at original controller"
	if err := ledger.checkUnique(ctx, token.ItemID, item); err != nil {
		return nil, err
	}
	if err := ledger.saveItem(ctx, item); err != nil {
		return nil, err
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"revision": ledger.revision, "phase": "active"}, nil
}

func openCollaborationLedger(ctx context.Context, dbPath, missionID, actorSessionID string, writable bool) (*collaborationLedger, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open collaboration database: %w", err)
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		conn.Close()
		db.Close()
		return nil, err
	}
	begin := "BEGIN"
	if writable {
		begin = "BEGIN IMMEDIATE"
	}
	if _, err := conn.ExecContext(ctx, begin); err != nil {
		conn.Close()
		db.Close()
		return nil, fmt.Errorf("begin collaboration transaction: %w", err)
	}
	ledger := &collaborationLedger{db: db, conn: conn}
	var raw string
	if err := conn.QueryRowContext(ctx, "SELECT data, revision FROM mission WHERE singleton=1").Scan(&raw, &ledger.revision); err != nil {
		ledger.rollback()
		return nil, fmt.Errorf("read collaboration mission: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &ledger.mission); err != nil {
		ledger.rollback()
		return nil, errors.New("collaboration mission is invalid")
	}
	if mapStringValue(ledger.mission, "id") != missionID || !samePath(mapStringValue(ledger.mission, "db_path"), dbPath) {
		ledger.rollback()
		return nil, errors.New("wrong mission/database identity")
	}
	if schema, ok := collaborationInt64(ledger.mission["schema"]); !ok || schema != 1 {
		ledger.rollback()
		return nil, errors.New("collaboration mission schema is unsupported")
	}
	controller := mapStringValue(ledger.mission, "controller")
	coordinator := mapStringValue(ledger.mission, "coordinator")
	if actorSessionID != controller && actorSessionID != coordinator {
		ledger.rollback()
		return nil, errors.New("actor not bound to this task")
	}
	if writable && mapStringValue(ledger.mission, "status") == "closed" {
		ledger.rollback()
		return nil, errors.New("mission closed; no more writes")
	}
	return ledger, nil
}

func (l *collaborationLedger) rollback() {
	if l == nil || l.closed {
		return
	}
	_, _ = l.conn.ExecContext(context.Background(), "ROLLBACK")
	_ = l.conn.Close()
	_ = l.db.Close()
	l.closed = true
}

func (l *collaborationLedger) commit(ctx context.Context) error {
	if l.closed {
		return errors.New("collaboration transaction is closed")
	}
	if _, err := l.conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	_ = l.conn.Close()
	_ = l.db.Close()
	l.closed = true
	return nil
}

func (l *collaborationLedger) item(ctx context.Context, itemID string) (map[string]any, error) {
	var raw string
	if err := l.conn.QueryRowContext(ctx, "SELECT data FROM items WHERE id=?", itemID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("unknown item")
		}
		return nil, err
	}
	var item map[string]any
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return nil, errors.New("collaboration item is invalid")
	}
	return item, nil
}

func (l *collaborationLedger) saveItem(ctx context.Context, item map[string]any) error {
	itemID := mapStringValue(item, "id")
	phase := mapStringValue(item, "phase")
	kind := mapStringValue(item, "kind")
	if itemID == "" || phase == "" || kind == "" {
		return errors.New("collaboration item identity is incomplete")
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if len(raw) > 48000 {
		return errors.New("item too large; store logs in evidence files")
	}
	dispatchKey := collaborationDispatchKey(item)
	taskRef := ""
	if binding, _ := item["binding"].(map[string]any); binding != nil {
		taskRef = mapStringValue(binding, "taskRef")
	}
	l.revision++
	if _, err := l.conn.ExecContext(ctx, `INSERT INTO items(id,phase,kind,revision,data,dispatch_key,task_ref) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET phase=excluded.phase,kind=excluded.kind,revision=excluded.revision,data=excluded.data,dispatch_key=excluded.dispatch_key,task_ref=excluded.task_ref`,
		itemID, phase, kind, l.revision, string(raw), nullableCollaborationString(dispatchKey), nullableCollaborationString(taskRef)); err != nil {
		return err
	}
	if _, err := l.conn.ExecContext(ctx, "INSERT INTO events(revision,object_id,phase) VALUES(?,?,?)", l.revision, itemID, phase); err != nil {
		return err
	}
	if _, err := l.conn.ExecContext(ctx, "DELETE FROM events WHERE revision <= ?", l.revision-100); err != nil {
		return err
	}
	missionRaw, err := json.Marshal(l.mission)
	if err != nil {
		return err
	}
	_, err = l.conn.ExecContext(ctx, "UPDATE mission SET revision=?,data=? WHERE singleton=1", l.revision, string(missionRaw))
	return err
}

func (l *collaborationLedger) checkDependencies(ctx context.Context, item map[string]any) error {
	for _, dep := range collaborationStringList(item["depends_on"]) {
		dependency, err := l.item(ctx, dep)
		if err != nil {
			return err
		}
		phase := mapStringValue(dependency, "phase")
		if phase != "accepted" && phase != "done" {
			return fmt.Errorf("dependency not done: %s", dep)
		}
	}
	return nil
}

func (l *collaborationLedger) checkCloudCapacity(ctx context.Context) error {
	capacity, _ := l.mission["capacity"].(map[string]any)
	limit, ok := collaborationInt64(capacity["cloud"])
	if !ok {
		return nil
	}
	rows, err := l.conn.QueryContext(ctx, "SELECT data FROM items WHERE phase NOT IN ('done','canceled')")
	if err != nil {
		return err
	}
	defer rows.Close()
	var held int64
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var item map[string]any
		if json.Unmarshal([]byte(raw), &item) != nil {
			return errors.New("collaboration item is invalid")
		}
		if mapStringValue(item, "executor") == "cloud" && collaborationHoldsExecution(item) {
			held++
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if held >= limit {
		return errors.New("registered Cloud capacity exhausted")
	}
	return nil
}

func (l *collaborationLedger) checkUnique(ctx context.Context, itemID string, item map[string]any) error {
	key := collaborationDispatchKey(item)
	if key != "" {
		var found int
		err := l.conn.QueryRowContext(ctx, "SELECT 1 FROM items WHERE id<>? AND dispatch_key=? LIMIT 1", itemID, key).Scan(&found)
		if err == nil {
			return errors.New("idempotency key already belongs to another round")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	binding, _ := item["binding"].(map[string]any)
	if taskRef := mapStringValue(binding, "taskRef"); taskRef != "" {
		var found int
		err := l.conn.QueryRowContext(ctx, "SELECT 1 FROM items WHERE id<>? AND task_ref=? LIMIT 1", itemID, taskRef).Scan(&found)
		if err == nil {
			return errors.New("taskRef already registered")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if mapStringValue(item, "phase") != "ready" && !collaborationHoldsExecution(item) {
		return nil
	}
	packet := collaborationScopePacket(item)
	rows, err := l.conn.QueryContext(ctx, "SELECT data FROM items WHERE id<>? AND phase NOT IN ('done','canceled')", itemID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var other map[string]any
		if json.Unmarshal([]byte(raw), &other) != nil {
			return errors.New("collaboration item is invalid")
		}
		if !collaborationHoldsExecution(other) {
			continue
		}
		otherPacket := collaborationScopePacket(other)
		chat := collaborationChatBinding(item, packet)
		otherChat := collaborationChatBinding(other, otherPacket)
		if chat != "" && chat == otherChat {
			return errors.New("CHAT has an active or uncertain round; reconcile original")
		}
		if mapStringValue(packet, "machineId") != mapStringValue(otherPacket, "machineId") {
			continue
		}
		for _, a := range collaborationScopeRoots(packet) {
			for _, b := range collaborationScopeRoots(otherPacket) {
				if pathWithin(a, b) || pathWithin(b, a) {
					return errors.New("write scope held by active/uncertain round")
				}
			}
		}
	}
	return rows.Err()
}

func validateCollaborationIdentity(dbPath, missionID, actorSessionID, itemID string) (string, error) {
	if !filepath.IsAbs(dbPath) || !strings.EqualFold(filepath.Ext(dbPath), ".sqlite3") {
		return "", errors.New("dbPath must be an absolute .sqlite3 path")
	}
	info, err := os.Lstat(dbPath)
	if err != nil {
		return "", errors.New("database missing; initialize explicitly")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("database must be a regular non-symlink file")
	}
	resolved, err := ResolveMachinePath(dbPath)
	if err != nil {
		return "", err
	}
	for name, value := range map[string]string{"missionId": missionID, "actorSessionId": actorSessionID, "itemId": itemID} {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 256 || strings.ContainsAny(value, "\x00\r\n\t ") {
			return "", fmt.Errorf("%s must be a bounded opaque ID", name)
		}
	}
	return resolved, nil
}

func validateCollaborationPacket(packet, mission map[string]any, dispatchable bool) error {
	allowed := map[string]bool{"machineId": true, "callbackSessionId": true, "workingDirectory": true, "prompt": true, "idempotencyKey": true, "accessMode": true, "writeScope": true, "callbackType": true, "targetSessionId": true, "deliverablePath": true}
	for key := range packet {
		if !allowed[key] {
			return errors.New("unknown packet fields")
		}
	}
	for _, key := range []string{"machineId", "callbackSessionId", "workingDirectory", "idempotencyKey"} {
		if err := validateCollaborationText(packet[key], key, 1024); err != nil {
			return err
		}
	}
	callback := mapStringValue(packet, "callbackSessionId")
	if dispatchable && callback != mapStringValue(mission, "controller") {
		return errors.New("new READY must callback to current controller")
	}
	key := mapStringValue(packet, "idempotencyKey")
	if len(key) < 12 || len(key) > 128 {
		return errors.New("invalid idempotency key")
	}
	if !filepath.IsAbs(mapStringValue(packet, "workingDirectory")) {
		return errors.New("workingDirectory must be absolute")
	}
	accessMode := mapStringValue(packet, "accessMode")
	if accessMode != "read_only" && accessMode != "write" {
		return errors.New("explicit accessMode required")
	}
	if accessMode == "write" {
		if dispatchable {
			if err := validateCollaborationText(packet["writeScope"], "writeScope", 1024); err != nil {
				return errors.New("FS writeScope must be one string; split independent paths into separate task rounds")
			}
		}
	}
	callbackType := mapStringValue(packet, "callbackType")
	if callbackType != "text" && callbackType != "local_file" && callbackType != "status" {
		return errors.New("callbackType required")
	}
	prompt, ok := packet["prompt"].(string)
	if !ok || prompt == "" || utf8.RuneCountInString(prompt) > 16000 {
		return errors.New("prompt too large or absent")
	}
	return nil
}

func validateCollaborationBinding(binding, item, mission map[string]any) error {
	allowed := map[string]bool{"chatSessionId": true, "collaborationId": true, "taskRef": true, "callbackSessionId": true, "idempotencyKey": true}
	if len(binding) != len(allowed) {
		return errors.New("binding must contain the exact dispatch identity")
	}
	for key := range binding {
		if !allowed[key] {
			return errors.New("binding must contain the exact dispatch identity")
		}
		if err := validateCollaborationText(binding[key], key, 1024); err != nil {
			return err
		}
	}
	callback := mapStringValue(binding, "callbackSessionId")
	validCallback := callback == mapStringValue(mission, "controller")
	for _, legacy := range collaborationStringList(mission["legacy_callback_sessions"]) {
		validCallback = validCallback || callback == legacy
	}
	if !validCallback {
		return errors.New("foreign callback owner")
	}
	packet, _ := item["packet"].(map[string]any)
	if packet != nil {
		if mapStringValue(binding, "idempotencyKey") != mapStringValue(packet, "idempotencyKey") {
			return errors.New("binding key differs from packet")
		}
		if target := mapStringValue(packet, "targetSessionId"); target != "" && mapStringValue(binding, "chatSessionId") != target {
			return errors.New("receipt differs from frozen target CHAT")
		}
	}
	return nil
}

func validateCollaborationText(value any, name string, limit int) error {
	text, ok := value.(string)
	if !ok || text == "" || len(text) > limit {
		return fmt.Errorf("invalid %s", name)
	}
	for _, char := range text {
		if char < 32 {
			return fmt.Errorf("invalid %s", name)
		}
	}
	return nil
}

func collaborationScopePacket(item map[string]any) map[string]any {
	if local, _ := item["local_scope"].(map[string]any); local != nil {
		return local
	}
	packet, _ := item["packet"].(map[string]any)
	return packet
}

func collaborationScopeRoots(packet map[string]any) []string {
	if mapStringValue(packet, "accessMode") != "write" {
		return nil
	}
	var scopes []string
	switch value := packet["writeScope"].(type) {
	case string:
		scopes = []string{value}
	case []any:
		for _, item := range value {
			if text, ok := item.(string); ok {
				scopes = append(scopes, text)
			}
		}
	}
	workDir := mapStringValue(packet, "workingDirectory")
	roots := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		prefix := scope
		if index := strings.IndexAny(prefix, "*?["); index >= 0 {
			prefix = prefix[:index]
			if prefix != scope && !strings.HasSuffix(prefix, "/") && !strings.HasSuffix(prefix, "\\") {
				prefix = filepath.Dir(prefix)
			}
		}
		root := prefix
		if !filepath.IsAbs(root) {
			root = filepath.Join(workDir, root)
		}
		if absolute, err := filepath.Abs(root); err == nil {
			root = filepath.Clean(absolute)
		}
		if resolved, err := ResolveMachinePath(root); err == nil {
			root = resolved
		}
		roots = append(roots, root)
	}
	return roots
}

func collaborationChatBinding(item, packet map[string]any) string {
	if binding, _ := item["binding"].(map[string]any); binding != nil {
		if chat := mapStringValue(binding, "chatSessionId"); chat != "" {
			return chat
		}
	}
	return mapStringValue(packet, "targetSessionId")
}

func collaborationExecutionEnded(item map[string]any) bool {
	if mapStringValue(item, "terminal_ref") != "" {
		return true
	}
	result := mapStringValue(item, "result")
	if result == "" || result == "none" || len(collaborationAnyList(item["evidence"])) == 0 {
		return false
	}
	if mapStringValue(item, "executor") != "cloud" {
		return true
	}
	callback := mapStringValue(item, "callback")
	return callback == "received" || callback == "acked"
}

func collaborationHoldsExecution(item map[string]any) bool {
	started := mapStringValue(item, "claim") != "" || item["binding"] != nil || item["started_at"] != nil || mapStringValue(item, "phase") == "active"
	return started && mapStringValue(item, "phase") != "dispatch_rejected" && !collaborationExecutionEnded(item)
}

func collaborationDispatchKey(item map[string]any) string {
	if packet, _ := item["packet"].(map[string]any); packet != nil {
		if key := mapStringValue(packet, "idempotencyKey"); key != "" {
			return key
		}
	}
	if key := mapStringValue(item, "dispatch_key"); key != "" {
		return key
	}
	if binding, _ := item["binding"].(map[string]any); binding != nil {
		return mapStringValue(binding, "idempotencyKey")
	}
	return ""
}

func (c *Client) collaborationTokenPath(token string) (string, error) {
	if len(token) != 64 {
		return "", errors.New("invalid dispatchToken")
	}
	if _, err := hex.DecodeString(token); err != nil {
		return "", errors.New("invalid dispatchToken")
	}
	dir := filepath.Join(c.cfg.DataDir, "collaboration-control", "tokens")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, token+".json"), nil
}

func (c *Client) writeCollaborationToken(token string, record collaborationToken) error {
	collaborationTokenStoreMu.Lock()
	defer collaborationTokenStoreMu.Unlock()

	path, err := c.collaborationTokenPath(token)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "token-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if err := json.NewEncoder(temp).Encode(record); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceFile(tempPath, path); err != nil {
		return err
	}
	if err := syncParentDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	cleanupExpiredCollaborationTokens(filepath.Dir(path), time.Now().Unix())
	return nil
}

func (c *Client) readCollaborationToken(token string) (collaborationToken, error) {
	collaborationTokenStoreMu.Lock()
	defer collaborationTokenStoreMu.Unlock()

	path, err := c.collaborationTokenPath(token)
	if err != nil {
		return collaborationToken{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return collaborationToken{}, errors.New("dispatchToken was not found; use recover with the exact ledger item")
	}
	var record collaborationToken
	if json.Unmarshal(data, &record) != nil || record.Version != collaborationTokenVersion || record.ExpiresAt <= 0 {
		return collaborationToken{}, errors.New("dispatchToken record is invalid")
	}
	if record.Completed == nil && (record.DBPath == "" || record.MissionID == "" || record.ActorSessionID == "" || record.ItemID == "" || record.Claim == "" || record.PacketSHA256 == "" || record.DispatchRequest == nil) {
		return collaborationToken{}, errors.New("dispatchToken record is incomplete")
	}
	return record, nil
}

func cleanupExpiredCollaborationTokens(dir string, now int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var record collaborationToken
		if json.Unmarshal(data, &record) == nil && record.ExpiresAt > 0 && record.ExpiresAt <= now {
			_ = os.Remove(path)
		}
	}
}

func unwrapCollaborationDispatchResult(input map[string]any) (map[string]any, error) {
	current := input
	for range 8 {
		if completeCollaborationDispatch(current) || collaborationDispatchUncertain(current) {
			return current, nil
		}
		advanced := false
		for _, key := range []string{"result", "structuredContent", "data"} {
			if child, ok := current[key].(map[string]any); ok {
				current = child
				advanced = true
				break
			}
		}
		if !advanced {
			break
		}
	}
	return nil, errors.New("dispatch result has no complete binding; record uncertain instead")
}

func completeCollaborationDispatch(value map[string]any) bool {
	return mapStringValue(value, "chatSessionId") != "" && mapStringValue(value, "collaborationId") != "" && mapStringValue(value, "taskRef") != ""
}

func collaborationDispatchUncertain(value map[string]any) bool {
	for _, key := range []string{"createInDoubt", "deliveryInDoubt", "callbackPending"} {
		if mapBoolValue(value, key) {
			return true
		}
	}
	return false
}

func requireCollaborationParams(input map[string]any, names ...string) error {
	for _, name := range names {
		if _, ok := input[name]; !ok {
			return fmt.Errorf("%s is required", name)
		}
	}
	return nil
}

func randomCollaborationClaim() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func collaborationTokenID(dbPath, missionID, itemID, claim string) string {
	return hex.EncodeToString(collaborationHash([]byte(strings.Join([]string{dbPath, missionID, itemID, claim}, "\x00"))))
}

func collaborationHash(value []byte) []byte {
	digest := sha256.Sum256(value)
	return digest[:]
}

func collaborationMapsEqual(left, right map[string]any) bool {
	a, errA := json.Marshal(left)
	b, errB := json.Marshal(right)
	return errA == nil && errB == nil && string(a) == string(b)
}

func collaborationStringList(value any) []string {
	items := collaborationAnyList(value)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func appendUniqueCollaborationString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func collaborationAnyList(value any) []any {
	items, _ := value.([]any)
	return items
}

func collaborationInt64(value any) (int64, bool) {
	switch number := value.(type) {
	case float64:
		return int64(number), number == float64(int64(number))
	case int64:
		return number, true
	case int:
		return int64(number), true
	default:
		return 0, false
	}
}

func mapStringValue(value map[string]any, key string) string {
	text, _ := value[key].(string)
	return text
}

func mapBoolValue(value map[string]any, key string) bool {
	flag, _ := value[key].(bool)
	return flag
}

func nullableCollaborationString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
