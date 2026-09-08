package node

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The tree is deliberately kept in the existing mission database.  These
// columns are denormalized projections used for bounded tree reads; the full
// item remains in data so existing state operations keep their identity.
const collaborationTreeSchema = `
CREATE TABLE IF NOT EXISTS workstreams(
	id TEXT PRIMARY KEY,
	title TEXT NOT NULL,
	objective TEXT NOT NULL DEFAULT '',
	owner TEXT NOT NULL DEFAULT '',
	archived INTEGER NOT NULL DEFAULT 0,
	revision INTEGER NOT NULL DEFAULT 0,
	data TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS workstreams_archived_id ON workstreams(archived,id);
`

// collaborationTreeItemSchema is consumed by the mission initializer. Existing
// databases are never altered implicitly by a tree read or update.
const collaborationTreeItemSchema = `
ALTER TABLE items ADD COLUMN title TEXT NOT NULL DEFAULT '';
ALTER TABLE items ADD COLUMN workstream_id TEXT NOT NULL DEFAULT '';
ALTER TABLE items ADD COLUMN archived INTEGER NOT NULL DEFAULT 0;
`

type collaborationTreeIdentityParams struct {
	DBPath         string `json:"dbPath"`
	MissionID      string `json:"missionId"`
	ActorSessionID string `json:"actorSessionId"`
}

type collaborationTreeParams struct {
	collaborationTreeIdentityParams
	After                      string `json:"after,omitempty"`
	WorkstreamAfter            string `json:"workstreamAfter,omitempty"`
	Limit                      int64  `json:"limit,omitempty"`
	IncludeArchived            bool   `json:"includeArchived,omitempty"`
	IncludeArchivedWorkstreams bool   `json:"includeArchivedWorkstreams,omitempty"`
}

type collaborationTreeUpdateParams struct {
	collaborationTreeIdentityParams
	ExpectedRevision int64            `json:"expectedRevision"`
	AuthorityRef     string           `json:"authorityRef,omitempty"`
	Mission          map[string]any   `json:"mission,omitempty"`
	Workstreams      []map[string]any `json:"workstreams,omitempty"`
	Items            []map[string]any `json:"items,omitempty"`
}

type collaborationTreeArchiveParams struct {
	collaborationTreeIdentityParams
	ExpectedRevision int64  `json:"expectedRevision"`
	ItemID           string `json:"itemId"`
	Archived         bool   `json:"archived"`
}

// collaborationTreeControl is intentionally separate from the older state
// switch. The controller can wire actions "tree", "tree_update", and
// "archive" to this function without changing the existing state contract.
func (c *Client) collaborationTreeControl(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	if params == nil {
		params = map[string]any{}
	}
	switch action {
	case "tree":
		var input collaborationTreeParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid collaboration tree params: %w", err)
		}
		if err := validateCollaborationTreeIdentity(input.DBPath, input.MissionID, input.ActorSessionID); err != nil {
			return nil, err
		}
		return c.collaborationTree(ctx, input)
	case "tree_update":
		if err := requireCollaborationParams(params, "dbPath", "missionId", "actorSessionId", "expectedRevision"); err != nil {
			return nil, err
		}
		var input collaborationTreeUpdateParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid collaboration tree_update params: %w", err)
		}
		if err := validateCollaborationTreeIdentity(input.DBPath, input.MissionID, input.ActorSessionID); err != nil {
			return nil, err
		}
		return c.collaborationTreeUpdate(ctx, input)
	case "archive":
		if err := requireCollaborationParams(params, "dbPath", "missionId", "actorSessionId", "expectedRevision", "itemId", "archived"); err != nil {
			return nil, err
		}
		var input collaborationTreeArchiveParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid collaboration archive params: %w", err)
		}
		if err := validateCollaborationTreeIdentity(input.DBPath, input.MissionID, input.ActorSessionID); err != nil {
			return nil, err
		}
		return c.collaborationTreeArchive(ctx, input)
	default:
		return nil, fmt.Errorf("unsupported collaboration tree action %q", action)
	}
}

func validateCollaborationTreeIdentity(dbPath, missionID, actor string) error {
	if _, err := validateCollaborationBaseIdentity(dbPath, missionID, actor); err != nil {
		return err
	}
	return nil
}

func ensureCollaborationTreeSchema(ctx context.Context, dbPath string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open collaboration tree database: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	columns, err := collaborationTreeColumns(ctx, db, "items")
	if err != nil {
		return err
	}
	for _, name := range []string{"title", "workstream_id", "archived"} {
		if !columns[name] {
			return errors.New("collaboration tree schema is unavailable; initialize a new mission with tree schema")
		}
	}
	var table string
	err = db.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name='workstreams'").Scan(&table)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("collaboration tree schema is unavailable; initialize a new mission with tree schema")
	}
	return err
}

func collaborationTreeColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func collaborationTreeLimit(value int64) (int, error) {
	if value == 0 {
		return 50, nil
	}
	if value < 1 || value > 50 {
		return 0, errors.New("limit must be 1..50")
	}
	return int(value), nil
}

func (c *Client) collaborationTree(ctx context.Context, input collaborationTreeParams) (map[string]any, error) {
	limit, err := collaborationTreeLimit(input.Limit)
	if err != nil {
		return nil, err
	}
	if input.After != "" {
		if err := validateCollaborationOpaqueID(input.After, "after"); err != nil {
			return nil, err
		}
	}
	if input.WorkstreamAfter != "" {
		if err := validateCollaborationOpaqueID(input.WorkstreamAfter, "workstreamAfter"); err != nil {
			return nil, err
		}
	}
	dbPath, err := validateCollaborationBaseIdentity(input.DBPath, input.MissionID, input.ActorSessionID)
	if err != nil {
		return nil, err
	}
	if err := ensureCollaborationTreeSchema(ctx, dbPath); err != nil {
		return nil, err
	}
	ledger, err := openCollaborationLedger(ctx, dbPath, input.MissionID, input.ActorSessionID, false)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()

	workstreams, nextWorkstreamAfter, err := collaborationTreeWorkstreams(ctx, ledger, input.WorkstreamAfter, limit, input.IncludeArchivedWorkstreams)
	if err != nil {
		return nil, err
	}
	counts, err := collaborationTreeCounts(ctx, ledger, workstreams)
	if err != nil {
		return nil, err
	}
	query := `SELECT id,data,title,workstream_id,archived FROM items WHERE id>?`
	args := []any{input.After}
	if !input.IncludeArchived {
		query += " AND archived=0 AND phase NOT IN ('done','canceled')"
	}
	query += " ORDER BY id LIMIT ?"
	args = append(args, limit+1)
	rows, err := ledger.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit+1)
	for rows.Next() {
		var id, raw, title, workstreamID string
		var archived int
		if err := rows.Scan(&id, &raw, &title, &workstreamID, &archived); err != nil {
			return nil, err
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return nil, errors.New("collaboration item is invalid")
		}
		item["id"] = id
		if title == "" {
			title = mapStringValue(item, "title")
		}
		if workstreamID == "" {
			workstreamID = mapStringValue(item, "workstream_id")
		}
		item["title"] = title
		item["workstream_id"] = workstreamID
		item["archived"] = archived != 0
		items = append(items, collaborationTreeTaskView(item))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var nextAfter any
	if len(items) > limit {
		nextAfter = items[limit-1]["id"]
		items = items[:limit]
	}
	mission := collaborationTreeMissionView(ledger.mission)
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{
		"revision":            ledger.revision,
		"mission":             mission,
		"workstreams":         workstreams,
		"counts":              counts,
		"items":               items,
		"nextAfter":           nextAfter,
		"nextWorkstreamAfter": nextWorkstreamAfter,
	}, nil
}

func collaborationTreeMissionView(mission map[string]any) map[string]any {
	return selectCollaborationFields(mission, "id", "goal", "objective", "non_goals", "mode", "status", "controller", "coordinator", "authority_ref", "tree_update_authority_ref")
}

func collaborationTreeTaskView(item map[string]any) map[string]any {
	return selectCollaborationFields(item, "id", "current_attempt", "title", "kind", "phase", "owner", "executor", "workstream_id", "archived", "priority", "next_action", "depends_on", "blocker", "validation", "integration", "callback", "result", "evidence", "next_check_at")
}

func collaborationTreeWorkstreams(ctx context.Context, ledger *collaborationLedger, after string, limit int, includeArchived bool) ([]any, any, error) {
	query := "SELECT id,title,objective,owner,archived FROM workstreams WHERE id>?"
	args := []any{after}
	if !includeArchived {
		query += " AND archived=0"
	}
	query += " ORDER BY id LIMIT ?"
	args = append(args, limit+1)
	rows, err := ledger.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	result := make([]any, 0, limit+1)
	for rows.Next() {
		var id, title, objective, owner string
		var archived int
		if err := rows.Scan(&id, &title, &objective, &owner, &archived); err != nil {
			return nil, nil, err
		}
		result = append(result, map[string]any{
			"id": id, "title": title, "objective": objective,
			"owner": owner, "archived": archived != 0,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var next any
	if len(result) > limit {
		next = result[limit-1].(map[string]any)["id"]
		result = result[:limit]
	}
	return result, next, nil
}

func collaborationTreeCounts(ctx context.Context, ledger *collaborationLedger, workstreams []any) (map[string]any, error) {
	pageIDs := map[string]bool{"": true}
	for _, raw := range workstreams {
		if stream, ok := raw.(map[string]any); ok {
			pageIDs[mapStringValue(stream, "id")] = true
		}
	}
	rows, err := ledger.conn.QueryContext(ctx, `SELECT workstream_id,phase,archived,count(*) FROM items GROUP BY workstream_id,phase,archived ORDER BY workstream_id,phase,archived`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byStream := map[string]map[string]any{}
	global := map[string]any{"total": int64(0), "active": int64(0), "done": int64(0), "canceled": int64(0), "archived": int64(0), "unassigned": int64(0)}
	newEntry := func(stream string) map[string]any {
		return map[string]any{"workstreamId": stream, "total": int64(0), "active": int64(0), "done": int64(0), "canceled": int64(0), "archived": int64(0)}
	}
	for rows.Next() {
		var stream, phase string
		var archived, count int
		if err := rows.Scan(&stream, &phase, &archived, &count); err != nil {
			return nil, err
		}
		amount := int64(count)
		global["total"] = global["total"].(int64) + amount
		if archived != 0 {
			global["archived"] = global["archived"].(int64) + amount
		}
		if stream == "" {
			global["unassigned"] = global["unassigned"].(int64) + amount
		}
		if phase == "done" {
			global["done"] = global["done"].(int64) + amount
		} else if phase == "canceled" {
			global["canceled"] = global["canceled"].(int64) + amount
		} else {
			global["active"] = global["active"].(int64) + amount
		}
		if !pageIDs[stream] {
			continue
		}
		entry := byStream[stream]
		if entry == nil {
			entry = newEntry(stream)
			byStream[stream] = entry
		}
		entry["total"] = entry["total"].(int64) + amount
		if archived != 0 {
			entry["archived"] = entry["archived"].(int64) + amount
		}
		if phase == "done" {
			entry["done"] = entry["done"].(int64) + amount
		} else if phase == "canceled" {
			entry["canceled"] = entry["canceled"].(int64) + amount
		} else {
			entry["active"] = entry["active"].(int64) + amount
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for stream := range pageIDs {
		if _, ok := byStream[stream]; !ok {
			byStream[stream] = newEntry(stream)
		}
	}
	keys := make([]string, 0, len(byStream))
	for key := range byStream {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	page := make([]any, 0, len(keys))
	for _, key := range keys {
		page = append(page, byStream[key])
	}
	return map[string]any{"workstreams": page, "global": global}, nil
}

func collaborationTreeTargetChanged(patch map[string]any) bool {
	for _, key := range []string{"goal", "objective", "non_goals", "mode"} {
		if _, ok := patch[key]; ok {
			return true
		}
	}
	return false
}

func validateCollaborationTreeText(value any, name string, limit int) error {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" || len(text) > limit {
		return fmt.Errorf("%s must be a bounded non-empty string", name)
	}
	return nil
}

func (c *Client) collaborationTreeUpdate(ctx context.Context, input collaborationTreeUpdateParams) (map[string]any, error) {
	if len(input.Workstreams) > 50 || len(input.Items) > 50 {
		return nil, errors.New("at most 50 workstream or item patches")
	}
	dbPath, err := validateCollaborationBaseIdentity(input.DBPath, input.MissionID, input.ActorSessionID)
	if err != nil {
		return nil, err
	}
	if err := ensureCollaborationTreeSchema(ctx, dbPath); err != nil {
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
	missionPatch := input.Mission
	if missionPatch == nil {
		missionPatch = map[string]any{}
	}
	targetChanged := collaborationTreeTargetChanged(missionPatch)
	for _, patch := range input.Workstreams {
		if _, ok := patch["objective"]; ok {
			targetChanged = true
			break
		}
	}
	if targetChanged {
		if err := validateCollaborationTreeText(input.AuthorityRef, "authorityRef", 1024); err != nil {
			return nil, err
		}
	}
	for key, value := range missionPatch {
		if key != "goal" && key != "objective" && key != "non_goals" && key != "mode" && key != "next_action" {
			return nil, fmt.Errorf("unknown tree mission field %q", key)
		}
		if key == "non_goals" {
			if _, ok := value.([]any); !ok {
				return nil, errors.New("non_goals must be a list")
			}
		} else if err := validateCollaborationTreeText(value, key, 2048); err != nil {
			return nil, err
		}
		ledger.mission[key] = value
	}
	if targetChanged {
		ledger.mission["tree_update_authority_ref"] = input.AuthorityRef
	}
	if len(missionPatch) > 0 {
		if err := ledger.saveMissionEvent(ctx, "tree_updated"); err != nil {
			return nil, err
		}
	}
	for _, patch := range input.Workstreams {
		if err := collaborationTreeUpsertWorkstream(ctx, ledger, patch); err != nil {
			return nil, err
		}
		if err := ledger.saveMissionEvent(ctx, "workstream_updated"); err != nil {
			return nil, err
		}
	}
	for _, patch := range input.Items {
		if err := collaborationTreeUpdateItem(ctx, ledger, patch); err != nil {
			return nil, err
		}
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"revision": ledger.revision}, nil
}

func collaborationTreeUpsertWorkstream(ctx context.Context, ledger *collaborationLedger, patch map[string]any) error {
	id, ok := patch["id"].(string)
	if !ok || strings.TrimSpace(id) == "" {
		return errors.New("workstream id is required")
	}
	if len(id) > 256 {
		return errors.New("workstream id is too long")
	}
	var oldRaw string
	err := ledger.conn.QueryRowContext(ctx, "SELECT data FROM workstreams WHERE id=?", id).Scan(&oldRaw)
	if errors.Is(err, sql.ErrNoRows) {
		oldRaw = "{}"
	} else if err != nil {
		return err
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(oldRaw), &value); err != nil {
		return errors.New("collaboration workstream is invalid")
	}
	for key, raw := range patch {
		switch key {
		case "id":
		case "title", "objective", "owner":
			if err := validateCollaborationTreeText(raw, "workstream."+key, 2048); err != nil {
				return err
			}
			value[key] = raw
		case "archived":
			if _, ok := raw.(bool); !ok {
				return errors.New("workstream.archived must be boolean")
			}
			value[key] = raw
		default:
			return fmt.Errorf("unknown workstream field %q", key)
		}
	}
	if value["title"] == nil {
		return errors.New("workstream title is required")
	}
	title, _ := value["title"].(string)
	objective, _ := value["objective"].(string)
	owner, _ := value["owner"].(string)
	archived := collaborationBoolDefault(value, "archived", false)
	raw, err := encodeCollaborationJSON(value)
	if err != nil {
		return err
	}
	_, err = ledger.conn.ExecContext(ctx, `INSERT INTO workstreams(id,title,objective,owner,archived,revision,data) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET title=excluded.title,objective=excluded.objective,owner=excluded.owner,archived=excluded.archived,revision=excluded.revision,data=excluded.data`,
		id, title, objective, owner, boolInt(archived), ledger.revision, raw)
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func collaborationTreeUpdateItem(ctx context.Context, ledger *collaborationLedger, patch map[string]any) error {
	id, ok := patch["id"].(string)
	if !ok || strings.TrimSpace(id) == "" {
		return errors.New("item id is required")
	}
	old, err := ledger.item(ctx, id)
	if err != nil {
		return err
	}
	item := cloneParams(old)
	for key, value := range patch {
		switch key {
		case "id":
		case "title":
			if value != nil {
				if err := validateCollaborationTreeText(value, "title", 2048); err != nil {
					return err
				}
			}
			item[key] = value
		case "workstream_id":
			if value != nil {
				stream, ok := value.(string)
				if !ok {
					return errors.New("workstream_id must be a string")
				}
				if stream != "" {
					if err := validateCollaborationOpaqueID(stream, "workstream_id"); err != nil {
						return err
					}
					var found int
					err := ledger.conn.QueryRowContext(ctx, "SELECT 1 FROM workstreams WHERE id=?", stream).Scan(&found)
					if errors.Is(err, sql.ErrNoRows) {
						return errors.New("workstream_id does not exist")
					}
					if err != nil {
						return err
					}
				}
			}
			item[key] = value
		case "owner":
			if err := validateCollaborationTreeText(value, "owner", 500); err != nil {
				return err
			}
			item[key] = value
		case "archived":
			return errors.New("use archive action for archived state")
		default:
			return fmt.Errorf("unknown tree item field %q", key)
		}
	}
	if mapStringValue(old, "phase") != mapStringValue(item, "phase") {
		return errors.New("tree_update cannot change task phase")
	}
	if collaborationFinalPhases[mapStringValue(old, "phase")] {
		if !collaborationValueEqual(old["owner"], item["owner"]) || !collaborationValueEqual(old["workstream_id"], item["workstream_id"]) {
			return errors.New("closed task ownership is immutable")
		}
	}
	if collaborationHoldsExecution(old) && (!collaborationValueEqual(old["owner"], item["owner"]) || !collaborationValueEqual(old["workstream_id"], item["workstream_id"])) {
		return errors.New("running task ownership is immutable")
	}
	validationItem := cloneParams(item)
	for _, key := range []string{"current_attempt", "title", "workstream_id", "archived"} {
		delete(validationItem, key)
	}
	if err := validateCollaborationItem(validationItem, ledger.mission); err != nil {
		return err
	}
	if err := ledger.checkUnique(ctx, id, validationItem); err != nil {
		return err
	}
	if err := ledger.saveItem(ctx, item); err != nil {
		return err
	}
	_, err = ledger.conn.ExecContext(ctx, "UPDATE items SET title=?,workstream_id=?,archived=? WHERE id=?", mapStringValue(item, "title"), mapStringValue(item, "workstream_id"), boolInt(collaborationBoolDefault(item, "archived", false)), id)
	return err
}

func (c *Client) collaborationTreeArchive(ctx context.Context, input collaborationTreeArchiveParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(input.DBPath, input.MissionID, input.ActorSessionID)
	if err != nil {
		return nil, err
	}
	if err := ensureCollaborationTreeSchema(ctx, dbPath); err != nil {
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
	if err := validateCollaborationOpaqueID(input.ItemID, "itemId"); err != nil {
		return nil, err
	}
	item, err := ledger.item(ctx, input.ItemID)
	if err != nil {
		return nil, err
	}
	phase := mapStringValue(item, "phase")
	if !collaborationFinalPhases[phase] {
		return nil, errors.New("only done or canceled tasks can be archived")
	}
	binding, _ := item["binding"].(map[string]any)
	callback := collaborationStringDefault(item, "callback", "none")
	if callback != "none" && callback != "acked" || binding != nil && callback != "acked" {
		return nil, errors.New("task callback is not acknowledged")
	}
	current := collaborationBoolDefault(item, "archived", false)
	if current == input.Archived {
		if err := ledger.commit(ctx); err != nil {
			return nil, err
		}
		return map[string]any{"revision": ledger.revision, "itemId": input.ItemID, "archived": current, "replayed": true}, nil
	}
	item["archived"] = input.Archived
	if err := ledger.saveItem(ctx, item); err != nil {
		return nil, err
	}
	if _, err := ledger.conn.ExecContext(ctx, "UPDATE items SET archived=? WHERE id=?", boolInt(input.Archived), input.ItemID); err != nil {
		return nil, err
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"revision": ledger.revision, "itemId": input.ItemID, "archived": input.Archived}, nil
}
