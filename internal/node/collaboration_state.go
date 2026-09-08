package node

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var collaborationPhases = map[string]bool{
	"planned": true, "ready": true, "dispatching": true, "in_doubt": true,
	"dispatch_rejected": true, "active": true, "returned": true, "verifying": true,
	"blocked": true, "rework": true, "integrating": true, "accepted": true,
	"done": true, "canceled": true,
}

var collaborationFinalPhases = map[string]bool{"done": true, "canceled": true}

var collaborationKinds = map[string]bool{
	"explore": true, "implement": true, "validate": true,
	"integrate": true, "decision": true, "finding": true,
}

var collaborationItemFields = map[string]bool{
	"id": true, "kind": true, "phase": true, "owner": true, "executor": true,
	"next_action": true, "evidence": true, "depends_on": true, "packet": true,
	"binding": true, "callback": true, "result": true, "validation": true,
	"integration": true, "blocker": true, "claim": true, "source_ref": true,
	"dispatch_key": true, "terminal_ref": true, "validation_owner": true,
	"validation_started_at": true, "next_check_at": true, "started_at": true,
	"priority": true, "contract_refs": true, "acceptance_ref": true,
	"execution_ref": true, "local_scope": true,
	"title": true, "workstream_id": true, "archived": true, "current_attempt": true,
}

var collaborationTransitions = map[string]map[string]bool{
	"planned":           {"ready": true, "blocked": true, "done": true, "canceled": true},
	"ready":             {"planned": true, "blocked": true, "canceled": true},
	"dispatching":       {"returned": true, "blocked": true, "canceled": true},
	"in_doubt":          {"returned": true, "blocked": true, "canceled": true},
	"dispatch_rejected": {"blocked": true, "canceled": true},
	"active":            {"returned": true, "blocked": true},
	"returned":          {"verifying": true, "blocked": true, "rework": true, "integrating": true, "accepted": true, "done": true},
	"verifying":         {"blocked": true, "rework": true, "integrating": true, "accepted": true, "done": true},
	"blocked":           {"planned": true, "returned": true, "verifying": true, "rework": true, "integrating": true, "accepted": true, "done": true, "canceled": true},
	"rework":            {"blocked": true, "done": true, "canceled": true},
	"integrating":       {"blocked": true, "rework": true, "accepted": true, "done": true},
	"accepted":          {"done": true, "blocked": true, "rework": true},
	"done":              {},
	"canceled":          {},
}

const collaborationSchema = `
CREATE TABLE mission(singleton INTEGER PRIMARY KEY CHECK(singleton=1), data TEXT NOT NULL, revision INTEGER NOT NULL);
CREATE TABLE items(id TEXT PRIMARY KEY, phase TEXT NOT NULL, kind TEXT NOT NULL, revision INTEGER NOT NULL, data TEXT NOT NULL, dispatch_key TEXT UNIQUE, task_ref TEXT UNIQUE);
CREATE INDEX current_phase ON items(phase,id);
CREATE TABLE events(revision INTEGER PRIMARY KEY, object_id TEXT NOT NULL, phase TEXT NOT NULL);
CREATE TABLE observation(singleton INTEGER PRIMARY KEY CHECK(singleton=1), data TEXT NOT NULL);
` + collaborationInboxSchema + collaborationAttemptSchema + collaborationTreeSchema + collaborationTreeItemSchema

type collaborationIdentityParams struct {
	DBPath         string `json:"dbPath"`
	MissionID      string `json:"missionId"`
	ActorSessionID string `json:"actorSessionId"`
}

type collaborationInitParams struct {
	collaborationIdentityParams
	Coordinator     string           `json:"coordinator"`
	AuthorityRef    string           `json:"authorityRef"`
	NextAction      string           `json:"nextAction"`
	DispatchEnabled bool             `json:"dispatchEnabled"`
	Continuation    map[string]any   `json:"continuation"`
	Items           []map[string]any `json:"items,omitempty"`
	Goal            string           `json:"goal,omitempty"`
	StrategyRef     string           `json:"strategyRef,omitempty"`
	Capacity        map[string]any   `json:"capacity,omitempty"`
}

type collaborationBriefParams struct {
	collaborationIdentityParams
	Since *int64 `json:"since,omitempty"`
	Limit int64  `json:"limit,omitempty"`
	After string `json:"after,omitempty"`
	View  string `json:"view,omitempty"`
}

type collaborationGetParams struct {
	collaborationIdentityParams
	ItemID string `json:"itemId"`
	View   string `json:"view,omitempty"`
}

type collaborationNextActionsParams struct {
	collaborationIdentityParams
	Since *int64 `json:"since,omitempty"`
	Limit int64  `json:"limit,omitempty"`
	After string `json:"after,omitempty"`
	Now   *int64 `json:"now,omitempty"`
}

type collaborationRecordActionParams struct {
	collaborationIdentityParams
	ExpectedRevision            int64  `json:"expectedRevision"`
	ExpectedObservationRevision int64  `json:"expectedObservationRevision"`
	ActionID                    string `json:"actionId"`
	RetryAt                     int64  `json:"retryAt"`
	EvidenceRef                 string `json:"evidenceRef"`
	Notified                    bool   `json:"notified,omitempty"`
	Now                         *int64 `json:"now,omitempty"`
	Outcome                     string `json:"outcome,omitempty"`
	completeCheck               bool
}

type collaborationApplyParams struct {
	collaborationIdentityParams
	ExpectedRevision int64            `json:"expectedRevision"`
	Mission          map[string]any   `json:"mission,omitempty"`
	Items            []map[string]any `json:"items,omitempty"`
}

type collaborationTransferControlParams struct {
	collaborationIdentityParams
	ExpectedRevision  int64  `json:"expectedRevision"`
	NewController     string `json:"newController"`
	NewCoordinator    string `json:"newCoordinator"`
	EvidenceRef       string `json:"evidenceRef"`
	AutomationsPaused bool   `json:"automationsPaused"`
}

type collaborationObserveParams struct {
	collaborationIdentityParams
	ExpectedObservationRevision int64    `json:"expectedObservationRevision"`
	CheckedRevision             int64    `json:"checkedRevision"`
	Full                        bool     `json:"full"`
	Conflicts                   []string `json:"conflicts,omitempty"`
}

type collaborationCloseParams struct {
	collaborationIdentityParams
	ExpectedRevision  int64  `json:"expectedRevision"`
	AutomationStopped bool   `json:"automationStopped"`
	EvidenceRef       string `json:"evidenceRef"`
}

type collaborationCleanupParams struct {
	collaborationIdentityParams
	Apply          bool   `json:"apply,omitempty"`
	ConfirmMission string `json:"confirmMission,omitempty"`
}

type collaborationActionCandidate struct {
	Key          string
	ActionID     string
	Kind         string
	ItemID       any
	Owner        string
	SubjectOwner string
	DueAt        int64
	Priority     int64
}

func (c *Client) collaborationStateControl(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	switch action {
	case "init":
		if err := requireCollaborationParams(params, "dbPath", "missionId", "actorSessionId", "coordinator", "authorityRef", "nextAction", "dispatchEnabled", "continuation"); err != nil {
			return nil, err
		}
		var input collaborationInitParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid collaboration init params: %w", err)
		}
		return c.collaborationInitialize(ctx, input)
	case "brief":
		var input collaborationBriefParams
		if err := decodeCollaborationIdentityParams(params, &input); err != nil {
			return nil, err
		}
		return c.collaborationBrief(ctx, input)
	case "get":
		if err := requireCollaborationParams(params, "dbPath", "missionId", "actorSessionId", "itemId"); err != nil {
			return nil, err
		}
		var input collaborationGetParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid collaboration get params: %w", err)
		}
		return c.collaborationGet(ctx, input)
	case "next_actions":
		var input collaborationNextActionsParams
		if err := decodeCollaborationIdentityParams(params, &input); err != nil {
			return nil, err
		}
		return c.collaborationNextActions(ctx, input)
	case "record_action", "record_check":
		if action == "record_check" {
			if err := requireCollaborationParams(params, "expectedRevision", "expectedObservationRevision", "actionId", "outcome", "evidenceRef"); err != nil {
				return nil, collaborationRecordCheckParamError(err)
			}
			var input collaborationRecordActionParams
			if err := decodeCollaborationIdentityParams(params, &input); err != nil {
				return nil, collaborationRecordCheckParamError(err)
			}
			input.completeCheck = true
			return c.collaborationRecordAction(ctx, input)
		}
		if err := requireCollaborationParams(params, "dbPath", "missionId", "actorSessionId", "expectedRevision", "expectedObservationRevision", "actionId", "retryAt", "evidenceRef"); err != nil {
			return nil, err
		}
		var input collaborationRecordActionParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid collaboration record_action params: %w", err)
		}
		return c.collaborationRecordAction(ctx, input)
	case "apply":
		if err := requireCollaborationParams(params, "dbPath", "missionId", "actorSessionId", "expectedRevision"); err != nil {
			return nil, err
		}
		var input collaborationApplyParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid collaboration apply params: %w", err)
		}
		return c.collaborationApply(ctx, input)
	case "transfer_control":
		if err := requireCollaborationParams(params, "dbPath", "missionId", "actorSessionId", "expectedRevision", "newController", "newCoordinator", "evidenceRef", "automationsPaused"); err != nil {
			return nil, err
		}
		var input collaborationTransferControlParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid collaboration transfer_control params: %w", err)
		}
		return c.collaborationTransferControl(ctx, input)
	case "observe":
		if err := requireCollaborationParams(params, "dbPath", "missionId", "actorSessionId", "expectedObservationRevision", "checkedRevision", "full"); err != nil {
			return nil, err
		}
		var input collaborationObserveParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid collaboration observe params: %w", err)
		}
		return c.collaborationObserve(ctx, input)
	case "observation":
		var input collaborationIdentityParams
		if err := decodeCollaborationIdentityParams(params, &input); err != nil {
			return nil, err
		}
		return c.collaborationObservation(ctx, input)
	case "close":
		if err := requireCollaborationParams(params, "dbPath", "missionId", "actorSessionId", "expectedRevision", "automationStopped", "evidenceRef"); err != nil {
			return nil, err
		}
		var input collaborationCloseParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid collaboration close params: %w", err)
		}
		return c.collaborationClose(ctx, input)
	case "compact":
		var input collaborationIdentityParams
		if err := decodeCollaborationIdentityParams(params, &input); err != nil {
			return nil, err
		}
		return c.collaborationCompact(ctx, input)
	case "cleanup":
		var input collaborationCleanupParams
		if err := decodeCollaborationIdentityParams(params, &input); err != nil {
			return nil, err
		}
		return c.collaborationCleanup(ctx, input)
	default:
		return nil, fmt.Errorf("unsupported collaboration state action %q", action)
	}
}

func decodeCollaborationIdentityParams(params map[string]any, output any) error {
	if err := requireCollaborationParams(params, "dbPath", "missionId", "actorSessionId"); err != nil {
		return err
	}
	if err := decodeParams(params, output); err != nil {
		return fmt.Errorf("invalid collaboration control params: %w", err)
	}
	return nil
}

func collaborationNow(value *int64) (int64, error) {
	if value == nil {
		return time.Now().Unix(), nil
	}
	if *value < 0 {
		return 0, errors.New("invalid current time")
	}
	return *value, nil
}

func (c *Client) collaborationInitialize(ctx context.Context, input collaborationInitParams) (result map[string]any, err error) {
	path, err := validateNewCollaborationDatabasePath(input.DBPath, input.MissionID, input.ActorSessionID)
	if err != nil {
		return nil, err
	}
	if err := validateCollaborationText(input.Coordinator, "coordinator", 128); err != nil {
		return nil, err
	}
	if input.Coordinator == input.ActorSessionID {
		return nil, errors.New("small direct mode should use notes, not this ledger")
	}
	if err := validateCollaborationText(input.AuthorityRef, "authorityRef", 1024); err != nil {
		return nil, err
	}
	if err := validateCollaborationText(input.NextAction, "nextAction", 1024); err != nil {
		return nil, err
	}
	if err := validateCollaborationContinuation(input.Continuation); err != nil {
		return nil, err
	}
	if input.Goal != "" {
		if err := validateCollaborationText(input.Goal, "goal", 1024); err != nil {
			return nil, err
		}
	}
	if input.StrategyRef != "" {
		if err := validateCollaborationText(input.StrategyRef, "strategyRef", 1024); err != nil {
			return nil, err
		}
	}
	if input.Capacity != nil {
		if err := validateCollaborationCapacity(input.Capacity); err != nil {
			return nil, err
		}
	}
	if len(input.Items) > 100 {
		return nil, errors.New("initial snapshot must be bounded to 100 items")
	}

	mission := map[string]any{
		"id": input.MissionID, "controller": input.ActorSessionID, "coordinator": input.Coordinator,
		"db_path": path, "status": "active", "schema": int64(1),
		"authority_ref": input.AuthorityRef, "next_action": input.NextAction,
		"dispatch_enabled": input.DispatchEnabled, "continuation": input.Continuation,
	}
	if input.Goal != "" {
		mission["goal"] = input.Goal
	}
	if input.StrategyRef != "" {
		mission["strategy_ref"] = input.StrategyRef
	}
	if input.Capacity != nil {
		mission["capacity"] = input.Capacity
	}
	seen := map[string]bool{}
	for _, source := range input.Items {
		item := cloneParams(source)
		if err := validateCollaborationItem(item, mission); err != nil {
			return nil, err
		}
		id := mapStringValue(item, "id")
		if seen[id] {
			return nil, errors.New("duplicate imported item")
		}
		seen[id] = true
		if phase := mapStringValue(item, "phase"); phase != "planned" && phase != "ready" {
			if err := validateCollaborationText(item["source_ref"], "verified migration sourceRef", 1024); err != nil {
				return nil, err
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, errors.New("database already exists; never overwrite another mission")
		}
		return nil, err
	}
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(path)
		return nil, closeErr
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(path)
		}
	}()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err = db.ExecContext(ctx, collaborationSchema); err != nil {
		_ = db.Close()
		return nil, err
	}
	missionRaw, err := encodeCollaborationJSON(mission)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err = db.ExecContext(ctx, "INSERT INTO mission(singleton,data,revision) VALUES(1,?,0)", missionRaw); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err = db.Close(); err != nil {
		return nil, err
	}

	ledger, err := openCollaborationLedger(ctx, path, input.MissionID, input.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	for _, source := range input.Items {
		item := cloneParams(source)
		phase := mapStringValue(item, "phase")
		if (phase == "dispatching" || phase == "in_doubt") && mapStringValue(item, "claim") == "" {
			claim, claimErr := randomCollaborationClaim()
			if claimErr != nil {
				return nil, claimErr
			}
			item["claim"] = claim
		}
		if err := ledger.checkUnique(ctx, mapStringValue(item, "id"), item); err != nil {
			return nil, err
		}
		if err := ledger.saveItem(ctx, item); err != nil {
			return nil, err
		}
	}
	for _, source := range input.Items {
		if mapStringValue(source, "phase") != "ready" {
			continue
		}
		item, itemErr := ledger.item(ctx, mapStringValue(source, "id"))
		if itemErr != nil {
			return nil, itemErr
		}
		if err := ledger.checkDependencies(ctx, item); err != nil {
			return nil, err
		}
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	keep = true
	return map[string]any{"dbPath": path, "revision": ledger.revision, "imported": len(input.Items)}, nil
}

func validateNewCollaborationDatabasePath(value, missionID, actorSessionID string) (string, error) {
	if !filepath.IsAbs(value) || !strings.EqualFold(filepath.Ext(value), ".sqlite3") {
		return "", errors.New("dbPath must be an absolute .sqlite3 path")
	}
	if err := validateCollaborationOpaqueID(missionID, "missionId"); err != nil {
		return "", err
	}
	if err := validateCollaborationOpaqueID(actorSessionID, "actorSessionId"); err != nil {
		return "", err
	}
	resolved, err := resolveLocalCollaborationPath("", value, true)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(resolved); err == nil {
		return "", errors.New("database already exists; never overwrite another mission")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return resolved, nil
}

func validateCollaborationOpaqueID(value, name string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || strings.ContainsAny(value, "\x00\r\n\t ") {
		return fmt.Errorf("%s must be a bounded opaque ID", name)
	}
	return nil
}

func validateCollaborationCapacity(value map[string]any) error {
	if err := validateCollaborationMapKeys(value, map[string]bool{"cloud": true, "local": true}, "capacity"); err != nil {
		return err
	}
	for _, raw := range value {
		number, ok := collaborationInt64(raw)
		if !ok || number < 0 {
			return errors.New("capacity must be a nonnegative integer")
		}
	}
	return nil
}

func validateCollaborationContinuation(value map[string]any) error {
	if value == nil {
		return errors.New("continuation is required")
	}
	if err := validateCollaborationMapKeys(value, map[string]bool{"enabled": true, "next_action": true, "not_before": true, "plan_ref": true}, "continuation"); err != nil {
		return err
	}
	enabled, ok := value["enabled"].(bool)
	if !ok {
		return errors.New("explicit continuation enabled required")
	}
	if !enabled {
		return nil
	}
	if err := validateCollaborationText(value["next_action"], "continuation.nextAction", 1024); err != nil {
		return err
	}
	if err := validateCollaborationText(value["plan_ref"], "continuation.planRef", 1024); err != nil {
		return err
	}
	due, ok := collaborationInt64(value["not_before"])
	if !ok || due < 0 {
		return errors.New("invalid continuation due time")
	}
	return nil
}

func validateCollaborationMapKeys(value map[string]any, allowed map[string]bool, name string) error {
	for key := range value {
		if !allowed[key] {
			return fmt.Errorf("unknown %s fields", name)
		}
	}
	return nil
}

func validateCollaborationItem(item, mission map[string]any) error {
	for _, key := range []string{"title", "workstream_id"} {
		if value := item[key]; value != nil && value != "" {
			if err := validateCollaborationText(value, key, 2048); err != nil {
				return err
			}
		}
	}
	if value, exists := item["archived"]; exists {
		archived, ok := value.(bool)
		if !ok {
			return errors.New("archived must be boolean")
		}
		if archived && !collaborationFinalPhases[mapStringValue(item, "phase")] {
			return errors.New("only settled tasks can be archived")
		}
	}
	if value, exists := item["current_attempt"]; exists {
		if attempt, ok := collaborationInt64(value); !ok || attempt < 1 {
			return errors.New("current_attempt must be positive")
		}
	}
	if err := validateCollaborationMapKeys(item, collaborationItemFields, "item"); err != nil {
		return err
	}
	for _, name := range []string{"id", "owner", "next_action"} {
		if err := validateCollaborationText(item[name], name, 500); err != nil {
			return err
		}
	}
	phase := mapStringValue(item, "phase")
	if !collaborationPhases[phase] || !collaborationKinds[mapStringValue(item, "kind")] {
		return errors.New("invalid kind/phase")
	}
	executor := mapStringValue(item, "executor")
	if executor != "cloud" && executor != "local" && executor != "none" {
		return errors.New("invalid executor")
	}
	for _, name := range []string{"evidence", "depends_on", "contract_refs"} {
		values, err := strictCollaborationStringList(item[name], name, 20)
		if err != nil {
			return err
		}
		if name == "depends_on" {
			for _, dependency := range values {
				if dependency == mapStringValue(item, "id") {
					return errors.New("self dependency")
				}
			}
		}
	}
	if value := collaborationStringDefault(item, "callback", "none"); value != "none" && value != "received" && value != "acked" {
		return errors.New("invalid callback")
	}
	if value := collaborationStringDefault(item, "result", "none"); value != "none" && value != "completed" && value != "blocked" && value != "failed" {
		return errors.New("invalid result")
	}
	if value := collaborationStringDefault(item, "validation", "pending"); value != "pending" && value != "passed" && value != "failed" && value != "not_required" {
		return errors.New("invalid validation")
	}
	if value := collaborationStringDefault(item, "integration", "pending"); value != "pending" && value != "done" && value != "not_required" {
		return errors.New("invalid integration")
	}
	priority := int64(100)
	if raw, exists := item["priority"]; exists {
		value, ok := collaborationInt64(raw)
		if !ok {
			return errors.New("invalid priority")
		}
		priority = value
	}
	if priority < 0 || priority > 1000 {
		return errors.New("invalid priority")
	}
	for _, name := range []string{"next_check_at", "started_at"} {
		if item[name] == nil {
			continue
		}
		value, ok := collaborationInt64(item[name])
		if !ok || value <= 0 {
			return fmt.Errorf("invalid %s", name)
		}
	}
	for _, name := range []string{"terminal_ref", "acceptance_ref", "execution_ref"} {
		if item[name] != nil {
			if err := validateCollaborationText(item[name], name, 1024); err != nil {
				return err
			}
		}
	}
	if localScope, ok := item["local_scope"].(map[string]any); ok && localScope != nil {
		if err := validateCollaborationLocalScope(localScope, executor); err != nil {
			return err
		}
	} else if item["local_scope"] != nil {
		return errors.New("invalid local_scope")
	}
	packet, packetOK := item["packet"].(map[string]any)
	if item["packet"] != nil && !packetOK {
		return errors.New("invalid packet")
	}
	if packet != nil {
		if err := validateCollaborationPacket(packet, mission, phase == "ready"); err != nil {
			return err
		}
	}
	if phase == "ready" || phase == "dispatching" || phase == "in_doubt" {
		if executor != "cloud" || packet == nil {
			return errors.New("Cloud dispatch requires a frozen packet")
		}
	}
	if phase == "planned" || phase == "ready" {
		if mapStringValue(item, "claim") != "" || item["binding"] != nil || item["started_at"] != nil || mapStringValue(item, "terminal_ref") != "" || collaborationStringDefault(item, "result", "none") != "none" {
			return errors.New("executed round cannot return to planning; use a new round")
		}
	}
	if phase == "dispatching" || phase == "in_doubt" || phase == "active" {
		if collaborationExecutionEnded(item) {
			return errors.New("terminal execution must move out of the running phase")
		}
	}
	binding, bindingOK := item["binding"].(map[string]any)
	if item["binding"] != nil && !bindingOK {
		return errors.New("invalid binding")
	}
	if binding != nil {
		if err := validateCollaborationBinding(binding, item, mission); err != nil {
			return err
		}
	}
	if executor == "cloud" && (phase == "active" || phase == "returned" || phase == "verifying" || phase == "integrating" || phase == "accepted" || phase == "done") && binding == nil {
		return errors.New("exact Cloud binding required")
	}
	if collaborationStringDefault(item, "callback", "none") != "none" {
		if binding == nil || len(collaborationStringList(item["evidence"])) == 0 || collaborationStringDefault(item, "result", "none") == "none" {
			return errors.New("callback needs binding and formal result evidence")
		}
	}
	if executor == "cloud" && (phase == "returned" || phase == "verifying" || phase == "integrating" || phase == "accepted" || phase == "done") {
		if !collaborationExecutionEnded(item) || collaborationStringDefault(item, "result", "none") == "none" || len(collaborationStringList(item["evidence"])) == 0 {
			return errors.New("Cloud result and execution-terminal evidence required; never infer from empty queues")
		}
	}
	if phase == "verifying" {
		if err := validateCollaborationText(item["validation_owner"], "exact validation owner", 1024); err != nil {
			return err
		}
		started, ok := collaborationInt64(item["validation_started_at"])
		if !ok || started <= 0 {
			return errors.New("validation start time required")
		}
	}
	if phase == "blocked" {
		blocker, ok := item["blocker"].(map[string]any)
		if !ok || blocker == nil {
			return errors.New("blocked item requires blocker")
		}
		if err := validateCollaborationBlocker(blocker); err != nil {
			return err
		}
	} else if item["blocker"] != nil {
		return errors.New("resolved blocker must be removed from current state")
	}
	if collaborationFinalPhases[phase] && len(collaborationStringList(item["evidence"])) == 0 {
		return errors.New("closure requires evidence references")
	}
	if phase == "accepted" || phase == "done" {
		if phase == "accepted" && executor != "cloud" {
			return errors.New("accepted is Cloud business acceptance pending transport closure")
		}
		validation := collaborationStringDefault(item, "validation", "pending")
		if validation != "passed" && validation != "not_required" {
			return errors.New("validation not closed")
		}
		integration := collaborationStringDefault(item, "integration", "pending")
		if integration != "done" && integration != "not_required" {
			return errors.New("integration not closed")
		}
		if executor == "cloud" {
			if collaborationStringDefault(item, "result", "none") != "completed" {
				return errors.New("failed/blocked Cloud result is not task completion")
			}
			if phase == "done" && collaborationStringDefault(item, "callback", "none") != "acked" {
				return errors.New("business accepted but transport unsettled; use accepted")
			}
		}
	}
	if phase == "canceled" {
		if collaborationHoldsExecution(item) {
			return errors.New("cannot cancel an unconfirmed writer")
		}
		if executor == "cloud" && binding != nil && collaborationStringDefault(item, "callback", "none") != "acked" {
			return errors.New("canceling business work does not erase a pending Cloud callback")
		}
	}
	raw, err := encodeCollaborationJSON(item)
	if err != nil {
		return err
	}
	if len([]byte(raw)) > 48000 {
		return errors.New("item too large; store logs in evidence files")
	}
	return nil
}

func validateCollaborationLocalScope(scope map[string]any, executor string) error {
	if err := validateCollaborationMapKeys(scope, map[string]bool{"machineId": true, "workingDirectory": true, "accessMode": true, "writeScope": true}, "local_scope"); err != nil {
		return err
	}
	if executor != "local" {
		return errors.New("local_scope only belongs to local executors")
	}
	if err := validateCollaborationText(scope["machineId"], "local machineId", 1024); err != nil {
		return err
	}
	workingDirectory, ok := scope["workingDirectory"].(string)
	if !ok || !filepath.IsAbs(workingDirectory) {
		return errors.New("local workingDirectory must be absolute")
	}
	accessMode := mapStringValue(scope, "accessMode")
	if accessMode != "read_only" && accessMode != "write" {
		return errors.New("local accessMode required")
	}
	if accessMode == "write" {
		values, ok := scope["writeScope"].([]any)
		if !ok || len(values) == 0 {
			return errors.New("local writeScope required")
		}
		for _, value := range values {
			if err := validateCollaborationText(value, "local writeScope", 1024); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCollaborationBlocker(blocker map[string]any) error {
	allowed := map[string]bool{"kind": true, "owner": true, "reason": true, "resume_when": true, "next_check_at": true, "userActionThreadId": true}
	if err := validateCollaborationMapKeys(blocker, allowed, "blocker"); err != nil {
		return err
	}
	for _, name := range []string{"kind", "owner", "reason", "resume_when"} {
		if err := validateCollaborationText(blocker[name], "blocker."+name, 500); err != nil {
			return err
		}
	}
	if mapStringValue(blocker, "kind") == "user" {
		return validateCollaborationText(blocker["userActionThreadId"], "userActionThreadId", 1024)
	}
	next, ok := collaborationInt64(blocker["next_check_at"])
	if !ok || next <= 0 {
		return errors.New("non-user blocker needs nextCheckAt")
	}
	return nil
}

func strictCollaborationStringList(value any, name string, limit int) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok || len(items) > limit {
		return nil, fmt.Errorf("invalid %s", name)
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if err := validateCollaborationText(item, name, 1024); err != nil {
			return nil, err
		}
		result = append(result, item.(string))
	}
	return result, nil
}

func collaborationStringDefault(value map[string]any, key, fallback string) string {
	if text, ok := value[key].(string); ok {
		return text
	}
	return fallback
}

func collaborationCallbackSessions(mission map[string]any) (map[string]bool, error) {
	values := []string{mapStringValue(mission, "controller")}
	values = append(values, collaborationStringList(mission["legacy_callback_sessions"])...)
	if len(values) > 10 {
		return nil, errors.New("invalid callback session history")
	}
	result := make(map[string]bool, len(values))
	for _, value := range values {
		if err := validateCollaborationText(value, "callback session", 1024); err != nil {
			return nil, err
		}
		if result[value] {
			return nil, errors.New("invalid callback session history")
		}
		result[value] = true
	}
	return result, nil
}

func encodeCollaborationJSON(value any) (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buffer.String(), "\n"), nil
}

func collaborationValueEqual(left, right any) bool {
	a, errA := encodeCollaborationJSON(left)
	b, errB := encodeCollaborationJSON(right)
	return errA == nil && errB == nil && a == b
}

func collaborationActionHash(value any) (string, error) {
	raw, err := encodeCollaborationJSON(value)
	if err != nil {
		return "", err
	}
	digest := collaborationHash([]byte(raw))
	return hex.EncodeToString(digest)[:32], nil
}

func collaborationIntDefault(value map[string]any, key string, fallback int64) int64 {
	if number, ok := collaborationInt64(value[key]); ok {
		return number
	}
	return fallback
}

func collaborationBoolDefault(value map[string]any, key string, fallback bool) bool {
	if flag, ok := value[key].(bool); ok {
		return flag
	}
	return fallback
}

func collaborationOptionalMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func collaborationAppendUnique(values []string, additions ...string) []string {
	seen := make(map[string]bool, len(values)+len(additions))
	result := make([]string, 0, len(values)+len(additions))
	for _, value := range append(values, additions...) {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func collaborationStringListAny(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func (c *Client) collaborationApply(ctx context.Context, input collaborationApplyParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(input.DBPath, input.MissionID, input.ActorSessionID)
	if err != nil {
		return nil, err
	}
	ledger, err := openCollaborationLedger(ctx, dbPath, input.MissionID, input.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	if mapStringValue(ledger.mission, "controller") != input.ActorSessionID {
		return nil, errors.New("controller-only mutation")
	}
	if input.ExpectedRevision != ledger.revision {
		return nil, fmt.Errorf("revision conflict; current=%d", ledger.revision)
	}
	if len(input.Items) > 50 {
		return nil, errors.New("at most 50 item patches")
	}
	missionPatch := input.Mission
	if missionPatch == nil {
		missionPatch = map[string]any{}
	}
	if err := validateCollaborationMissionPatch(missionPatch); err != nil {
		return nil, err
	}
	if ledger.hasInbox(ctx) {
		for _, key := range []string{"goal", "authority_ref"} {
			if value, exists := missionPatch[key]; exists && !collaborationValueEqual(value, ledger.mission[key]) {
				return nil, errors.New("use tree_update with authorityRef to change the user target")
			}
		}
	}
	missionChanged := false
	for key, value := range missionPatch {
		if !collaborationValueEqual(ledger.mission[key], value) {
			ledger.mission[key] = value
			missionChanged = true
		}
	}
	if missionChanged {
		if err := ledger.saveMissionEvent(ctx, mapStringValue(ledger.mission, "status")); err != nil {
			return nil, err
		}
	}

	seen := map[string]bool{}
	for _, update := range input.Items {
		if err := validateCollaborationMapKeys(update, collaborationItemFields, "item patch"); err != nil {
			return nil, err
		}
		id, ok := update["id"].(string)
		if !ok {
			return nil, errors.New("invalid id")
		}
		if err := validateCollaborationText(id, "id", 500); err != nil {
			return nil, err
		}
		if seen[id] {
			return nil, errors.New("duplicate item in batch")
		}
		seen[id] = true
		old, oldErr := ledger.optionalItem(ctx, id)
		if oldErr != nil {
			return nil, oldErr
		}
		item := map[string]any{}
		if old != nil {
			item = cloneParams(old)
		}
		for key, value := range update {
			item[key] = value
		}
		if old != nil && mapStringValue(old, "phase") != "active" && mapStringValue(item, "phase") == "active" && mapStringValue(item, "executor") == "local" {
			item["started_at"] = time.Now().Unix()
		}
		if err := validateCollaborationItem(item, ledger.mission); err != nil {
			return nil, err
		}
		if old != nil && collaborationValueEqual(item, old) {
			continue
		}
		if old != nil {
			if err := validateCollaborationItemUpdate(old, item); err != nil {
				return nil, err
			}
		} else {
			phase := mapStringValue(item, "phase")
			if phase != "planned" && phase != "ready" && phase != "blocked" {
				return nil, errors.New("use init snapshot for existing in-flight tasks")
			}
			if mapStringValue(item, "claim") != "" {
				return nil, errors.New("claim is generated by the ledger")
			}
		}
		if mapStringValue(item, "phase") == "ready" {
			if err := ledger.checkDependencies(ctx, item); err != nil {
				return nil, err
			}
		}
		if err := ledger.checkUnique(ctx, id, item); err != nil {
			return nil, err
		}
		if err := ledger.saveItem(ctx, item); err != nil {
			return nil, err
		}
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"revision": ledger.revision}, nil
}

func validateCollaborationMissionPatch(patch map[string]any) error {
	allowed := map[string]bool{"status": true, "dispatch_enabled": true, "authority_ref": true, "next_action": true, "continuation": true, "goal": true, "strategy_ref": true, "capacity": true}
	if err := validateCollaborationMapKeys(patch, allowed, "mission"); err != nil {
		return err
	}
	if raw, exists := patch["status"]; exists {
		status, ok := raw.(string)
		if !ok || status != "active" && status != "paused" {
			return errors.New("use close for final closure")
		}
	}
	if raw, exists := patch["dispatch_enabled"]; exists {
		if _, ok := raw.(bool); !ok {
			return errors.New("invalid dispatchEnabled")
		}
	}
	for _, key := range []string{"authority_ref", "next_action", "goal", "strategy_ref"} {
		if raw, exists := patch[key]; exists {
			if err := validateCollaborationText(raw, key, 1024); err != nil {
				return err
			}
		}
	}
	if raw, exists := patch["continuation"]; exists {
		value, ok := raw.(map[string]any)
		if !ok {
			return errors.New("invalid continuation")
		}
		if err := validateCollaborationContinuation(value); err != nil {
			return err
		}
	}
	if raw, exists := patch["capacity"]; exists {
		value, ok := raw.(map[string]any)
		if !ok {
			return errors.New("invalid capacity")
		}
		if err := validateCollaborationCapacity(value); err != nil {
			return err
		}
	}
	return nil
}

func validateCollaborationItemUpdate(old, item map[string]any) error {
	if !collaborationValueEqual(old["current_attempt"], item["current_attempt"]) {
		return errors.New("use retry to advance an execution attempt")
	}
	oldPhase := mapStringValue(old, "phase")
	newPhase := mapStringValue(item, "phase")
	if collaborationFinalPhases[oldPhase] {
		return errors.New("closed item immutable; use a new round ID")
	}
	if mapStringValue(item, "owner") != mapStringValue(old, "owner") || mapStringValue(item, "executor") != mapStringValue(old, "executor") {
		return errors.New("owner/executor immutable; use new round")
	}
	if newPhase != oldPhase {
		allowed := collaborationTransitions[oldPhase][newPhase]
		if mapStringValue(item, "executor") == "local" && oldPhase == "planned" && newPhase == "active" {
			allowed = true
		}
		if !allowed {
			return errors.New("invalid transition; use claim/receipt or a new round")
		}
	}
	if old["binding"] != nil && !collaborationValueEqual(item["binding"], old["binding"]) {
		return errors.New("binding immutable")
	}
	if !collaborationValueEqual(item["claim"], old["claim"]) {
		return errors.New("claim immutable")
	}
	if !collaborationValueEqual(item["dispatch_key"], old["dispatch_key"]) {
		return errors.New("dispatch key immutable")
	}
	if oldPhase != "planned" && oldPhase != "ready" {
		if !collaborationValueEqual(item["packet"], old["packet"]) {
			return errors.New("dispatched packet immutable")
		}
		if !collaborationValueEqual(item["local_scope"], old["local_scope"]) {
			return errors.New("running local scope immutable")
		}
	}
	if collaborationHoldsExecution(old) && !collaborationHoldsExecution(item) && !collaborationExecutionEnded(item) {
		return errors.New("do not release an uncertain writer without terminal evidence")
	}
	if collaborationExecutionEnded(old) && !collaborationExecutionEnded(item) {
		return errors.New("terminal execution fact cannot regress")
	}
	callbackOrder := map[string]int{"none": 0, "received": 1, "acked": 2}
	if callbackOrder[collaborationStringDefault(item, "callback", "none")] < callbackOrder[collaborationStringDefault(old, "callback", "none")] {
		return errors.New("callback fact cannot regress")
	}
	if collaborationExecutionEnded(old) && collaborationStringDefault(old, "result", "none") != "none" && collaborationStringDefault(item, "result", "none") != collaborationStringDefault(old, "result", "none") {
		return errors.New("terminal result immutable; use new round for rework")
	}
	return nil
}

func (c *Client) collaborationTransferControl(ctx context.Context, input collaborationTransferControlParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(input.DBPath, input.MissionID, input.ActorSessionID)
	if err != nil {
		return nil, err
	}
	ledger, err := openCollaborationLedger(ctx, dbPath, input.MissionID, input.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	if mapStringValue(ledger.mission, "controller") != input.ActorSessionID {
		return nil, errors.New("controller-only mutation")
	}
	if input.ExpectedRevision != ledger.revision {
		return nil, fmt.Errorf("revision conflict; current=%d", ledger.revision)
	}
	if mapStringValue(ledger.mission, "status") != "paused" || mapBoolValue(ledger.mission, "dispatch_enabled") {
		return nil, errors.New("pause mission dispatch before transferring control")
	}
	if !input.AutomationsPaused {
		return nil, errors.New("pause old controller and coordinator automations before transfer")
	}
	for name, value := range map[string]string{"new controller": input.NewController, "new coordinator": input.NewCoordinator, "control transfer evidence": input.EvidenceRef} {
		if err := validateCollaborationText(value, name, 1024); err != nil {
			return nil, err
		}
	}
	if input.NewController == input.NewCoordinator {
		return nil, errors.New("controller and coordinator must differ")
	}
	oldController := mapStringValue(ledger.mission, "controller")
	oldCoordinator := mapStringValue(ledger.mission, "coordinator")
	if input.NewController == oldController || input.NewController == oldCoordinator || input.NewCoordinator == oldController || input.NewCoordinator == oldCoordinator {
		return nil, errors.New("use newly created control tasks")
	}
	legacy := collaborationAppendUnique([]string{oldController}, collaborationStringList(ledger.mission["legacy_callback_sessions"])...)
	filtered := legacy[:0]
	for _, value := range legacy {
		if value != input.NewController {
			filtered = append(filtered, value)
		}
	}
	legacy = filtered
	if len(legacy) > 9 {
		return nil, errors.New("callback session history is full; close legacy rounds first")
	}
	ledger.mission["controller"] = input.NewController
	ledger.mission["coordinator"] = input.NewCoordinator
	ledger.mission["legacy_callback_sessions"] = collaborationStringListAny(legacy)
	ledger.mission["control_handoff_ref"] = input.EvidenceRef
	ledger.mission["previous_control"] = map[string]any{"controller": oldController, "coordinator": oldCoordinator}
	if err := ledger.saveMissionEvent(ctx, "control_transferred"); err != nil {
		return nil, err
	}
	observation, err := ledger.readObservation(ctx)
	if err != nil {
		return nil, err
	}
	observation["revision"] = collaborationIntDefault(observation, "revision", 0) + 1
	observation["action_checks"] = map[string]any{}
	if err := ledger.storeObservation(ctx, observation); err != nil {
		return nil, err
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"revision": ledger.revision, "controller": input.NewController, "coordinator": input.NewCoordinator, "legacyCallbackSessions": legacy}, nil
}

func (c *Client) collaborationClose(ctx context.Context, input collaborationCloseParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(input.DBPath, input.MissionID, input.ActorSessionID)
	if err != nil {
		return nil, err
	}
	ledger, err := openCollaborationLedger(ctx, dbPath, input.MissionID, input.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	if mapStringValue(ledger.mission, "controller") != input.ActorSessionID {
		return nil, errors.New("controller-only mutation")
	}
	if input.ExpectedRevision != ledger.revision {
		return nil, fmt.Errorf("revision conflict; current=%d", ledger.revision)
	}
	var unsettled int64
	if err := ledger.conn.QueryRowContext(ctx, "SELECT count(*) FROM items WHERE phase NOT IN ('done','canceled')").Scan(&unsettled); err != nil {
		return nil, err
	}
	if unsettled != 0 {
		return nil, errors.New("unsettled work remains")
	}
	if ledger.hasInbox(ctx) {
		var pending int
		if err := ledger.conn.QueryRowContext(ctx, "SELECT count(*) FROM callback_inbox WHERE resolved_at IS NULL").Scan(&pending); err != nil {
			return nil, err
		}
		if pending != 0 {
			return nil, errors.New("unresolved inbox results remain")
		}
	}
	continuation := collaborationOptionalMap(ledger.mission["continuation"])
	if collaborationBoolDefault(continuation, "enabled", false) {
		return nil, errors.New("authorized continuation is still enabled")
	}
	if !input.AutomationStopped {
		return nil, errors.New("stop actual automations before closing")
	}
	if err := validateCollaborationText(input.EvidenceRef, "closure evidence", 1024); err != nil {
		return nil, err
	}
	ledger.mission["status"] = "closed"
	ledger.mission["dispatch_enabled"] = false
	ledger.mission["closure_ref"] = input.EvidenceRef
	if err := ledger.saveMissionEvent(ctx, "closed"); err != nil {
		return nil, err
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"revision": ledger.revision, "status": "closed"}, nil
}

func (l *collaborationLedger) optionalItem(ctx context.Context, itemID string) (map[string]any, error) {
	var raw string
	err := l.conn.QueryRowContext(ctx, "SELECT data FROM items WHERE id=?", itemID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var item map[string]any
	if json.Unmarshal([]byte(raw), &item) != nil {
		return nil, errors.New("collaboration item is invalid")
	}
	return item, nil
}

func (l *collaborationLedger) saveMissionEvent(ctx context.Context, phase string) error {
	l.revision++
	if _, err := l.conn.ExecContext(ctx, "INSERT INTO events(revision,object_id,phase) VALUES(?,?,?)", l.revision, "mission", phase); err != nil {
		return err
	}
	if _, err := l.conn.ExecContext(ctx, "DELETE FROM events WHERE revision <= ?", l.revision-100); err != nil {
		return err
	}
	raw, err := encodeCollaborationJSON(l.mission)
	if err != nil {
		return err
	}
	_, err = l.conn.ExecContext(ctx, "UPDATE mission SET revision=?,data=? WHERE singleton=1", l.revision, raw)
	return err
}

func (l *collaborationLedger) currentItems(ctx context.Context) ([]map[string]any, error) {
	rows, err := l.conn.QueryContext(ctx, "SELECT data FROM items WHERE phase NOT IN ('done','canceled')")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []map[string]any
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var item map[string]any
		if json.Unmarshal([]byte(raw), &item) != nil {
			return nil, errors.New("collaboration item is invalid")
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (l *collaborationLedger) readObservation(ctx context.Context) (map[string]any, error) {
	var raw string
	err := l.conn.QueryRowContext(ctx, "SELECT data FROM observation WHERE singleton=1").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{"revision": int64(0), "light_rounds": int64(0), "last_full_at": nil, "conflicts": []any{}, "action_checks": map[string]any{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var observation map[string]any
	if json.Unmarshal([]byte(raw), &observation) != nil {
		return nil, errors.New("collaboration observation is invalid")
	}
	if observation["action_checks"] == nil {
		observation["action_checks"] = map[string]any{}
	}
	return observation, nil
}

func (l *collaborationLedger) storeObservation(ctx context.Context, observation map[string]any) error {
	live, err := l.actionCandidates(ctx, observation)
	if err != nil {
		return err
	}
	liveActions := make(map[string]string, len(live))
	for _, action := range live {
		liveActions[action.Key] = action.ActionID
	}
	checks, _ := observation["action_checks"].(map[string]any)
	pruned := map[string]any{}
	for key, raw := range checks {
		check, ok := raw.(map[string]any)
		if !ok || mapStringValue(check, "action_id") != liveActions[key] {
			continue
		}
		pruned[key] = check
	}
	observation["action_checks"] = pruned
	raw, err := encodeCollaborationJSON(observation)
	if err != nil {
		return err
	}
	_, err = l.conn.ExecContext(ctx, `INSERT INTO observation(singleton,data) VALUES(1,?)
		ON CONFLICT(singleton) DO UPDATE SET data=excluded.data`, raw)
	return err
}

// Convert still-current revision-based check IDs before an item changes. This
// is an internal identity migration, not a new check or a business-state write.
func (l *collaborationLedger) migrateLegacyExecutionChecks(ctx context.Context) error {
	observation, err := l.readObservation(ctx)
	if err != nil {
		return err
	}
	checks, _ := observation["action_checks"].(map[string]any)
	for key, raw := range checks {
		var parts []string
		if json.Unmarshal([]byte(key), &parts) != nil || len(parts) != 3 || !collaborationExecutionCheckKind(parts[1]) {
			continue
		}
		if check, ok := raw.(map[string]any); ok && !strings.HasPrefix(mapStringValue(check, "action_id"), "check-v2-") {
			// actionCandidates adopts only an exact live legacy ID; stale checks
			// are pruned as before. Counters and observation CAS remain intact.
			return l.storeObservation(ctx, observation)
		}
	}
	return nil
}

func collaborationExecutionCheckKind(kind string) bool {
	switch kind {
	case "check_execution", "reconcile_dispatch", "check_validation", "notify_validation_due":
		return true
	}
	return false
}

func (l *collaborationLedger) role(view string) (string, error) {
	actual := "coordinator"
	if mapStringValue(l.mission, "controller") == lActorSession(l) {
		actual = "controller"
	}
	if view != "" && view != "controller" && view != "coordinator" && view != "worker" {
		return "", errors.New("invalid view")
	}
	if actual != "controller" && view != "" && view != actual {
		return "", errors.New("only controller exports other role views")
	}
	if view != "" {
		return view, nil
	}
	return actual, nil
}

func lActorSession(l *collaborationLedger) string {
	// openCollaborationLedger has already proved that the actor is one of the two
	// bound control tasks. Keep the actor on the transaction rather than infer it
	// from mutable mission fields during a control handoff.
	return l.actorSessionID
}

func (c *Client) collaborationBrief(ctx context.Context, input collaborationBriefParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(input.DBPath, input.MissionID, input.ActorSessionID)
	if err != nil {
		return nil, err
	}
	limit := input.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return nil, errors.New("limit must be 1..50")
	}
	ledger, err := openCollaborationLedger(ctx, dbPath, input.MissionID, input.ActorSessionID, false)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	role, err := ledger.role(input.View)
	if err != nil {
		return nil, err
	}
	if role == "worker" {
		return nil, errors.New("worker handoff requires get with itemId and view=worker")
	}
	if input.Since != nil {
		if *input.Since < 0 || *input.Since > ledger.revision {
			return nil, errors.New("invalid cursor")
		}
		if *input.Since == ledger.revision {
			if err := ledger.commit(ctx); err != nil {
				return nil, err
			}
			return map[string]any{"revision": ledger.revision, "changed": false, "dueActionsRequire": "next_actions"}, nil
		}
	}
	counts := map[string]any{}
	rows, err := ledger.conn.QueryContext(ctx, "SELECT phase,count(*) FROM items GROUP BY phase")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var phase string
		var count int64
		if err := rows.Scan(&phase, &count); err != nil {
			_ = rows.Close()
			return nil, err
		}
		counts[phase] = count
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	mission := cloneParams(ledger.mission)
	if role == "coordinator" {
		mission = selectCollaborationFields(ledger.mission, "id", "controller", "coordinator", "status", "dispatch_enabled", "capacity", "authority_ref", "legacy_callback_sessions", "control_handoff_ref")
	}
	result := map[string]any{"mission": mission, "role": role, "revision": ledger.revision, "counts": counts}
	if input.Since != nil {
		var oldest sql.NullInt64
		if err := ledger.conn.QueryRowContext(ctx, "SELECT min(revision) FROM events").Scan(&oldest); err != nil {
			return nil, err
		}
		result["snapshotRequired"] = oldest.Valid && *input.Since < oldest.Int64-1
	}
	itemRows, err := ledger.conn.QueryContext(ctx, "SELECT data FROM items WHERE phase NOT IN ('done','canceled') AND id>? ORDER BY id LIMIT ?", input.After, limit+1)
	if err != nil {
		return nil, err
	}
	var all []map[string]any
	for itemRows.Next() {
		var raw string
		if err := itemRows.Scan(&raw); err != nil {
			_ = itemRows.Close()
			return nil, err
		}
		var item map[string]any
		if json.Unmarshal([]byte(raw), &item) != nil {
			_ = itemRows.Close()
			return nil, errors.New("collaboration item is invalid")
		}
		all = append(all, item)
	}
	if err := itemRows.Close(); err != nil {
		return nil, err
	}
	visible := all
	if int64(len(visible)) > limit {
		visible = visible[:limit]
	}
	views := make([]any, 0, len(visible))
	for _, item := range visible {
		fields := []string{"id", "kind", "phase", "owner", "executor", "next_action", "blocker", "priority", "depends_on", "execution_ref", "next_check_at"}
		if role == "controller" {
			fields = append(fields, "validation", "integration", "callback", "result", "contract_refs", "acceptance_ref")
		}
		view := selectCollaborationFields(item, fields...)
		view["holdsExecution"] = collaborationHoldsExecution(item)
		scope := collaborationScopePacket(item)
		view["scope"] = selectCollaborationFields(scope, "machineId", "workingDirectory", "accessMode", "writeScope")
		if role == "coordinator" {
			view["binding"] = item["binding"]
		}
		if mapStringValue(item, "phase") == "verifying" {
			view["validationOwner"] = item["validation_owner"]
			view["validationStartedAt"] = item["validation_started_at"]
		}
		views = append(views, view)
	}
	result["items"] = views
	if int64(len(all)) > limit && len(visible) > 0 {
		result["nextAfter"] = mapStringValue(visible[len(visible)-1], "id")
	} else {
		result["nextAfter"] = nil
	}
	items, err := ledger.currentItems(ctx)
	if err != nil {
		return nil, err
	}
	executionHeld := map[string]any{"cloud": int64(0), "local": int64(0)}
	for _, item := range items {
		executor := mapStringValue(item, "executor")
		if (executor == "cloud" || executor == "local") && collaborationHoldsExecution(item) {
			executionHeld[executor] = executionHeld[executor].(int64) + 1
		}
	}
	result["executionHeld"] = executionHeld
	if role == "controller" {
		pressure, err := ledger.dependencyPressure(ctx, items)
		if err != nil {
			return nil, err
		}
		result["dependencyPressure"] = pressure
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) collaborationGet(ctx context.Context, input collaborationGetParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(input.DBPath, input.MissionID, input.ActorSessionID)
	if err != nil {
		return nil, err
	}
	if err := validateCollaborationOpaqueID(input.ItemID, "itemId"); err != nil {
		return nil, err
	}
	ledger, err := openCollaborationLedger(ctx, dbPath, input.MissionID, input.ActorSessionID, false)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	role, err := ledger.role(input.View)
	if err != nil {
		return nil, err
	}
	item, err := ledger.item(ctx, input.ItemID)
	if err != nil {
		return nil, err
	}
	if role == "worker" {
		item = selectCollaborationFields(item, "id", "kind", "owner", "executor", "next_action", "depends_on", "contract_refs", "acceptance_ref", "evidence", "packet", "local_scope")
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"revision": ledger.revision, "item": item}, nil
}

func selectCollaborationFields(source map[string]any, fields ...string) map[string]any {
	result := map[string]any{}
	if source == nil {
		return result
	}
	for _, field := range fields {
		if value, ok := source[field]; ok {
			result[field] = value
		}
	}
	return result
}

func (l *collaborationLedger) dependencyPressure(ctx context.Context, items []map[string]any) ([]any, error) {
	rows, err := l.conn.QueryContext(ctx, "SELECT id,phase FROM items")
	if err != nil {
		return nil, err
	}
	phases := map[string]string{}
	for rows.Next() {
		var id, phase string
		if err := rows.Scan(&id, &phase); err != nil {
			_ = rows.Close()
			return nil, err
		}
		phases[id] = phase
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, item := range items {
		for _, dependency := range collaborationStringList(item["depends_on"]) {
			if phases[dependency] != "accepted" && phases[dependency] != "done" {
				counts[dependency]++
			}
		}
	}
	type pair struct {
		id    string
		count int64
	}
	pairs := make([]pair, 0, len(counts))
	for id, count := range counts {
		pairs = append(pairs, pair{id: id, count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].id < pairs[j].id
	})
	if len(pairs) > 10 {
		pairs = pairs[:10]
	}
	result := make([]any, 0, len(pairs))
	for _, value := range pairs {
		result = append(result, []any{value.id, value.count})
	}
	return result, nil
}

func (l *collaborationLedger) actionCandidates(ctx context.Context, observation map[string]any) ([]collaborationActionCandidate, error) {
	if observation == nil {
		var err error
		observation, err = l.readObservation(ctx)
		if err != nil {
			return nil, err
		}
	}
	rows, err := l.conn.QueryContext(ctx, "SELECT revision,data FROM items WHERE phase NOT IN ('done','canceled')")
	if err != nil {
		return nil, err
	}
	type revisionItem struct {
		revision int64
		item     map[string]any
	}
	var values []revisionItem
	for rows.Next() {
		var revision int64
		var raw string
		if err := rows.Scan(&revision, &raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		var item map[string]any
		if json.Unmarshal([]byte(raw), &item) != nil {
			_ = rows.Close()
			return nil, errors.New("collaboration item is invalid")
		}
		values = append(values, revisionItem{revision: revision, item: item})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	actions := []collaborationActionCandidate{}
	add := func(kind, role string, itemID any, version any, due, priority int64, subjectOwner string) error {
		key, err := encodeCollaborationJSON([]any{role, kind, itemID})
		if err != nil {
			return err
		}
		actionID, err := collaborationActionHash([]any{mapStringValue(l.mission, "id"), key, version, due})
		if err != nil {
			return err
		}
		actions = append(actions, collaborationActionCandidate{
			Key: key, ActionID: actionID, Kind: kind, ItemID: itemID,
			Owner: mapStringValue(l.mission, role), SubjectOwner: subjectOwner,
			DueAt: due, Priority: priority,
		})
		return nil
	}
	active := mapStringValue(l.mission, "status") == "active"
	canDispatch := active && mapBoolValue(l.mission, "dispatch_enabled")
	capacity := collaborationOptionalMap(l.mission["capacity"])
	cloudLimit, hasCloudLimit := collaborationInt64(capacity["cloud"])
	var cloudHeld int64
	for _, value := range values {
		if mapStringValue(value.item, "executor") == "cloud" && collaborationHoldsExecution(value.item) {
			cloudHeld++
		}
	}
	for _, value := range values {
		item := value.item
		phase := mapStringValue(item, "phase")
		id := mapStringValue(item, "id")
		priority := collaborationIntDefault(item, "priority", 100)
		task := func(kind, role string, due int64) error {
			if err := add(kind, role, id, value.revision, due, priority, mapStringValue(item, "owner")); err != nil {
				return err
			}
			if !collaborationExecutionCheckKind(kind) {
				return nil
			}
			action := &actions[len(actions)-1]
			// Scheduling and descriptive edits do not start a new execution.
			// Keep the budget/notification identity separate from dueAt and CAS.
			identity := []any{collaborationIntDefault(item, "current_attempt", 1), item["executor"]}
			if kind == "check_validation" || kind == "notify_validation_due" {
				identity = append(identity, item["validation_owner"], item["validation_started_at"])
			} else if mapStringValue(item, "executor") == "cloud" {
				identity = append(identity, item["claim"], collaborationDispatchKey(item))
			} else {
				identity = append(identity, item["execution_ref"], item["started_at"])
			}
			hash, err := collaborationActionHash([]any{mapStringValue(l.mission, "id"), action.Key, identity})
			if err != nil {
				return err
			}
			stableID := "check-v2-" + hash
			checks, _ := observation["action_checks"].(map[string]any)
			if prior, ok := checks[action.Key].(map[string]any); ok && mapStringValue(prior, "action_id") == action.ActionID {
				prior["action_id"] = stableID
			}
			action.ActionID = stableID
			return nil
		}
		if phase == "ready" && canDispatch && (!hasCloudLimit || cloudHeld < cloudLimit) {
			if dependenciesErr := l.checkDependencies(ctx, item); dependenciesErr == nil {
				if uniqueErr := l.checkUnique(ctx, id, item); uniqueErr == nil {
					if err := task("dispatch_ready", "coordinator", 0); err != nil {
						return nil, err
					}
				}
			}
		}
		if phase == "planned" && canDispatch && l.checkDependencies(ctx, item) == nil {
			if err := task("prepare_task", "controller", 0); err != nil {
				return nil, err
			}
		}
		controllerKinds := map[string]string{
			"returned": "review_result", "rework": "prepare_rework",
			"integrating": "review_integration", "dispatch_rejected": "close_rejected_round",
		}
		if kind := controllerKinds[phase]; kind != "" {
			if err := task(kind, "controller", 0); err != nil {
				return nil, err
			}
		}
		if active && (phase == "dispatching" || phase == "in_doubt" || collaborationHoldsExecution(item)) {
			due := collaborationIntDefault(item, "next_check_at", 0)
			if due == 0 {
				due = collaborationIntDefault(item, "started_at", 0) + 900
			}
			kind := "check_execution"
			if phase == "dispatching" || phase == "in_doubt" {
				kind = "reconcile_dispatch"
			}
			if err := task(kind, "coordinator", due); err != nil {
				return nil, err
			}
		}
		if active && phase == "verifying" {
			due := collaborationIntDefault(item, "next_check_at", 0)
			if due == 0 {
				due = collaborationIntDefault(item, "validation_started_at", 0) + 600
			}
			if err := task("check_validation", "controller", due); err != nil {
				return nil, err
			}
			if err := task("notify_validation_due", "coordinator", due); err != nil {
				return nil, err
			}
		}
		if active && phase == "blocked" {
			blocker := collaborationOptionalMap(item["blocker"])
			if mapStringValue(blocker, "kind") != "user" {
				if err := task("recheck_blocker", "coordinator", collaborationIntDefault(blocker, "next_check_at", 0)); err != nil {
					return nil, err
				}
			}
		}
		if mapStringValue(item, "executor") == "cloud" && collaborationExecutionEnded(item) && item["binding"] != nil && collaborationStringDefault(item, "callback", "none") != "acked" {
			if err := task("recover_callback", "controller", 0); err != nil {
				return nil, err
			}
		}
		if phase == "accepted" && collaborationStringDefault(item, "callback", "none") == "acked" {
			if err := task("finalize_accepted", "controller", 0); err != nil {
				return nil, err
			}
		}
	}
	if active {
		continuation := collaborationOptionalMap(l.mission["continuation"])
		var totalItems int64
		if err := l.conn.QueryRowContext(ctx, "SELECT count(*) FROM items").Scan(&totalItems); err != nil {
			return nil, err
		}
		if totalItems > 0 && len(values) == 0 && !collaborationBoolDefault(continuation, "enabled", false) {
			if err := add("stop_automations_and_close", "controller", nil, l.revision, 0, 0, ""); err != nil {
				return nil, err
			}
		} else {
			lastFull := collaborationIntDefault(observation, "last_full_at", 0)
			var version any
			if observation["last_full_at"] != nil {
				version = lastFull
			}
			due := int64(0)
			if version != nil {
				due = lastFull + 3600
			}
			if err := add("consistency_audit", "coordinator", nil, version, due, 100, ""); err != nil {
				return nil, err
			}
			if canDispatch && collaborationBoolDefault(continuation, "enabled", false) {
				canPlan := true
				for _, value := range values {
					phase := mapStringValue(value.item, "phase")
					if phase != "blocked" && phase != "accepted" {
						canPlan = false
						break
					}
				}
				if canPlan {
					version := []any{continuation, l.mission["authority_ref"]}
					if err := add("plan_continuation", "controller", nil, version, collaborationIntDefault(continuation, "not_before", 0), 100, ""); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	if active {
		checks, _ := observation["action_checks"].(map[string]any)
		for _, action := range actions {
			record, _ := checks[action.Key].(map[string]any)
			if mapStringValue(record, "action_id") == action.ActionID && collaborationBoolDefault(record, "exhausted", false) {
				if err := add("decide_stalled_check", "controller", action.ItemID, action.ActionID, 0, 0, action.SubjectOwner); err != nil {
					return nil, err
				}
			}
		}
	}
	return actions, nil
}

func (c *Client) collaborationNextActions(ctx context.Context, input collaborationNextActionsParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(input.DBPath, input.MissionID, input.ActorSessionID)
	if err != nil {
		return nil, err
	}
	limit := input.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return nil, errors.New("limit must be 1..50")
	}
	now, err := collaborationNow(input.Now)
	if err != nil {
		return nil, err
	}
	ledger, err := openCollaborationLedger(ctx, dbPath, input.MissionID, input.ActorSessionID, false)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	if input.Since != nil && (*input.Since < 0 || *input.Since > ledger.revision) {
		return nil, errors.New("invalid cursor")
	}
	observation, err := ledger.readObservation(ctx)
	if err != nil {
		return nil, err
	}
	actions, err := ledger.actionCandidates(ctx, observation)
	if err != nil {
		return nil, err
	}
	checks, _ := observation["action_checks"].(map[string]any)
	due := []map[string]any{}
	future := []int64{}
	for _, action := range actions {
		if action.Owner != input.ActorSessionID {
			continue
		}
		record, _ := checks[action.Key].(map[string]any)
		if mapStringValue(record, "action_id") != action.ActionID {
			record = nil
		}
		if collaborationBoolDefault(record, "exhausted", false) {
			continue // Only the controller decision below remains actionable.
		}
		at := action.DueAt
		if retry := collaborationIntDefault(record, "retry_at", 0); retry > at {
			at = retry
		}
		if at > now {
			future = append(future, at)
			continue
		}
		entry := map[string]any{
			"actionId": action.ActionID, "kind": action.Kind, "itemId": action.ItemID,
			"owner": action.Owner, "dueAt": action.DueAt, "priority": action.Priority,
			"notify": !collaborationBoolDefault(record, "notified", false),
		}
		if action.SubjectOwner != "" {
			entry["subjectOwner"] = action.SubjectOwner
		}
		if err := ledger.addCollaborationCheckCalls(ctx, input, action, observation, entry); err != nil {
			return nil, err
		}
		due = append(due, entry)
	}
	sort.Slice(due, func(i, j int) bool {
		pi, _ := collaborationInt64(due[i]["priority"])
		pj, _ := collaborationInt64(due[j]["priority"])
		if pi != pj {
			return pi < pj
		}
		di, _ := collaborationInt64(due[i]["dueAt"])
		dj, _ := collaborationInt64(due[j]["dueAt"])
		if di != dj {
			return di < dj
		}
		return mapStringValue(due[i], "actionId") < mapStringValue(due[j], "actionId")
	})
	start := 0
	if input.After != "" {
		found := false
		for index, action := range due {
			if mapStringValue(action, "actionId") == input.After {
				start = index + 1
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("action cursor changed; restart bounded current snapshot")
		}
	}
	end := start + int(limit)
	if end > len(due) {
		end = len(due)
	}
	page := due[start:end]
	var nextAfter any
	if end < len(due) && len(page) > 0 {
		nextAfter = mapStringValue(page[len(page)-1], "actionId")
	}
	var nextDueAt any
	if len(due) > 0 {
		nextDueAt = now
	} else if len(future) > 0 {
		sort.Slice(future, func(i, j int) bool { return future[i] < future[j] })
		nextDueAt = future[0]
	}
	changed := true
	if input.Since != nil {
		changed = *input.Since != ledger.revision
	}
	result := map[string]any{
		"revision": ledger.revision, "changed": changed,
		"observationRevision": collaborationIntDefault(observation, "revision", 0),
		"actions":             page, "totalDue": len(due), "nextAfter": nextAfter, "nextDueAt": nextDueAt,
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) collaborationRecordAction(ctx context.Context, input collaborationRecordActionParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(input.DBPath, input.MissionID, input.ActorSessionID)
	if err != nil {
		return nil, err
	}
	now, err := collaborationNow(input.Now)
	if err != nil {
		return nil, err
	}
	ledger, err := openCollaborationLedger(ctx, dbPath, input.MissionID, input.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	if input.ExpectedRevision != ledger.revision {
		return nil, fmt.Errorf("revision conflict; current=%d", ledger.revision)
	}
	observation, err := ledger.readObservation(ctx)
	if err != nil {
		return nil, err
	}
	if input.ExpectedObservationRevision != collaborationIntDefault(observation, "revision", 0) {
		return nil, errors.New("observation CAS conflict")
	}
	actions, err := ledger.actionCandidates(ctx, observation)
	if err != nil {
		return nil, err
	}
	var selected *collaborationActionCandidate
	for index := range actions {
		if actions[index].ActionID == input.ActionID && actions[index].Owner == input.ActorSessionID {
			selected = &actions[index]
			break
		}
	}
	if selected == nil || selected.DueAt > now {
		return nil, errors.New("action stale, not due, or owned by another role; refresh local next_actions once and continue remaining due actions. Do not retry an absent action or repeat executor checks. A successful observe(full=true) already closes its prior consistency audit")
	}
	if !input.completeCheck && input.RetryAt <= now {
		return nil, errors.New("record a finite future retry time, not permanent suppression")
	}
	if err := validateCollaborationText(input.EvidenceRef, "action evidence", 1024); err != nil {
		return nil, err
	}
	checks, _ := observation["action_checks"].(map[string]any)
	if checks == nil {
		checks = map[string]any{}
	}
	prior, _ := checks[selected.Key].(map[string]any)
	if mapStringValue(prior, "action_id") != selected.ActionID {
		prior = nil
	}
	var count int64
	if input.completeCheck {
		if mapStringValue(prior, "outcome") == input.Outcome && mapStringValue(prior, "evidence_ref") == input.EvidenceRef && (collaborationIntDefault(prior, "retry_at", 0) > now || collaborationBoolDefault(prior, "exhausted", false)) {
			return map[string]any{"revision": ledger.revision, "observationRevision": observation["revision"], "duplicate": true}, nil
		}
		if selected.Kind == "consistency_audit" {
			if input.Outcome != "completed" {
				return nil, errors.New("consistency audit requires outcome completed")
			}
			observation["last_full_at"], observation["light_rounds"], observation["checked_revision"] = now, int64(0), ledger.revision
			input.RetryAt = now + 3600
		} else {
			switch selected.Kind {
			case "check_execution", "reconcile_dispatch", "check_validation", "notify_validation_due", "recheck_blocker":
			default:
				return nil, errors.New("record_check only closes due checks; use resolve/apply for business decisions")
			}
			if input.Outcome != "unchanged" && input.Outcome != "unavailable" {
				return nil, errors.New("check outcome must be unchanged or unavailable; persist new facts through resolve/apply")
			}
			count = collaborationIntDefault(prior, "attempts", 0) + 1
			if count > 3 {
				return nil, errors.New("check budget exhausted; controller decision required")
			}
			interval := int64(900)
			if selected.Kind == "check_execution" {
				item, err := ledger.item(ctx, fmt.Sprint(selected.ItemID))
				if err != nil {
					return nil, err
				}
				if mapStringValue(item, "executor") == "cloud" {
					interval = 1800 // Provider recovery is not a 15-minute ledger poll.
				}
			}
			backoff := now + interval*(1<<(count-1))
			if input.RetryAt < backoff {
				input.RetryAt = backoff
			}
		}
	}
	notified := input.Notified || mapStringValue(prior, "action_id") == selected.ActionID && collaborationBoolDefault(prior, "notified", false)
	checks[selected.Key] = map[string]any{
		"action_id": selected.ActionID, "retry_at": input.RetryAt, "checked_at": now,
		"evidence_ref": input.EvidenceRef, "notified": notified,
	}
	if input.completeCheck {
		record := checks[selected.Key].(map[string]any)
		record["attempts"], record["outcome"], record["exhausted"] = count, input.Outcome, count >= 3
	} else {
		// Legacy scheduling calls must not erase a record_check budget.
		record := checks[selected.Key].(map[string]any)
		for _, key := range []string{"attempts", "outcome", "exhausted"} {
			if value, ok := prior[key]; ok {
				record[key] = value
			}
		}
	}
	observation["action_checks"] = checks
	observation["revision"] = collaborationIntDefault(observation, "revision", 0) + 1
	if err := ledger.storeObservation(ctx, observation); err != nil {
		return nil, err
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"revision": ledger.revision, "observationRevision": observation["revision"], "retryAt": input.RetryAt}, nil
}

func (c *Client) collaborationObserve(ctx context.Context, input collaborationObserveParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(input.DBPath, input.MissionID, input.ActorSessionID)
	if err != nil {
		return nil, err
	}
	ledger, err := openCollaborationLedger(ctx, dbPath, input.MissionID, input.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	if mapStringValue(ledger.mission, "coordinator") != input.ActorSessionID {
		return nil, errors.New("only coordinator writes observation")
	}
	if input.CheckedRevision != ledger.revision {
		return nil, errors.New("observation based on stale state")
	}
	if len(input.Conflicts) > 20 {
		return nil, errors.New("at most 20 conflict fingerprints")
	}
	for _, conflict := range input.Conflicts {
		if err := validateCollaborationText(conflict, "conflict", 500); err != nil {
			return nil, err
		}
	}
	old, err := ledger.readObservation(ctx)
	if err != nil {
		return nil, err
	}
	if input.ExpectedObservationRevision != collaborationIntDefault(old, "revision", 0) {
		return nil, errors.New("observation CAS conflict")
	}
	lastFull := old["last_full_at"]
	lightRounds := collaborationIntDefault(old, "light_rounds", 0) + 1
	if input.Full {
		lastFull = time.Now().Unix()
		lightRounds = 0
	}
	newObservation := map[string]any{
		"revision":         collaborationIntDefault(old, "revision", 0) + 1,
		"checked_revision": ledger.revision, "light_rounds": lightRounds,
		"last_full_at": lastFull, "conflicts": collaborationStringListAny(input.Conflicts),
		"action_checks": old["action_checks"],
	}
	if err := ledger.storeObservation(ctx, newObservation); err != nil {
		return nil, err
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	result := collaborationObservationOutput(newObservation)
	if input.Full {
		result["auditRecorded"] = true
		result["recordCheckRequired"] = false
		result["nextAction"] = "refresh_next_actions"
		result["completionRule"] = "Full audit is already recorded. Do not record_check the old consistency action; continue other current due actions. For v3 use record_check(completed) alone on future audits."
	}
	return result, nil
}

func (c *Client) collaborationObservation(ctx context.Context, input collaborationIdentityParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(input.DBPath, input.MissionID, input.ActorSessionID)
	if err != nil {
		return nil, err
	}
	ledger, err := openCollaborationLedger(ctx, dbPath, input.MissionID, input.ActorSessionID, false)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	observation, err := ledger.readObservation(ctx)
	if err != nil {
		return nil, err
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return collaborationObservationOutput(observation), nil
}

func collaborationObservationOutput(observation map[string]any) map[string]any {
	return map[string]any{
		"revision":        collaborationIntDefault(observation, "revision", 0),
		"checkedRevision": observation["checked_revision"],
		"lightRounds":     collaborationIntDefault(observation, "light_rounds", 0),
		"lastFullAt":      observation["last_full_at"],
		"conflicts":       observation["conflicts"],
	}
}

func (c *Client) collaborationCompact(ctx context.Context, input collaborationIdentityParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(input.DBPath, input.MissionID, input.ActorSessionID)
	if err != nil {
		return nil, err
	}
	ledger, err := openCollaborationLedgerMode(ctx, dbPath, input.MissionID, input.ActorSessionID, true, true)
	if err != nil {
		return nil, err
	}
	if mapStringValue(ledger.mission, "controller") != input.ActorSessionID {
		ledger.rollback()
		return nil, errors.New("controller-only mutation")
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
		return nil, err
	}
	return map[string]any{"retainedEventLimit": 100, "retainedTaskIdentities": true}, nil
}

func (c *Client) collaborationCleanup(ctx context.Context, input collaborationCleanupParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(input.DBPath, input.MissionID, input.ActorSessionID)
	if err != nil {
		return nil, err
	}
	ledger, err := openCollaborationLedgerMode(ctx, dbPath, input.MissionID, input.ActorSessionID, true, true)
	if err != nil {
		return nil, err
	}
	if mapStringValue(ledger.mission, "controller") != input.ActorSessionID {
		ledger.rollback()
		return nil, errors.New("controller-only mutation")
	}
	if mapStringValue(ledger.mission, "status") != "closed" {
		ledger.rollback()
		return nil, errors.New("cannot clean a live mission")
	}
	var unsettled int64
	if err := ledger.conn.QueryRowContext(ctx, "SELECT count(*) FROM items WHERE phase NOT IN ('done','canceled')").Scan(&unsettled); err != nil {
		ledger.rollback()
		return nil, err
	}
	continuation := collaborationOptionalMap(ledger.mission["continuation"])
	if unsettled != 0 {
		ledger.rollback()
		return nil, errors.New("unsettled work remains")
	}
	if mapBoolValue(ledger.mission, "dispatch_enabled") || collaborationBoolDefault(continuation, "enabled", false) {
		ledger.rollback()
		return nil, errors.New("mission not stopped")
	}
	if input.Apply && input.ConfirmMission != input.MissionID {
		ledger.rollback()
		return nil, errors.New("confirmMission must match exactly")
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	if input.Apply {
		if err := os.Remove(dbPath); err != nil {
			return nil, err
		}
	}
	return map[string]any{"applied": input.Apply, "onlyTarget": dbPath}, nil
}
