package node

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// collaborationUpgradeParams is the explicit, controller-authorized migration
// boundary. BackupPath is a new destination; the live mission database is never
// replaced or deleted by this operation.
type collaborationUpgradeParams struct {
	collaborationIdentityParams
	ExpectedRevision int64  `json:"expectedRevision"`
	BackupPath       string `json:"backupPath"`
	EvidenceRef      string `json:"evidenceRef"`
}

type collaborationUpgradeMissionMeta struct {
	Mission  map[string]any
	Revision int64
}

type collaborationInboxBinder interface {
	BindCollaborationInbox(context.Context, []map[string]any) error
}

func (c *Client) collaborationUpgrade(ctx context.Context, input collaborationUpgradeParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(input.DBPath, input.MissionID, input.ActorSessionID)
	if err != nil {
		return nil, err
	}
	if input.ExpectedRevision < 0 {
		return nil, errors.New("expectedRevision must be nonnegative")
	}
	if err := validateCollaborationText(input.EvidenceRef, "upgrade evidence", 1024); err != nil {
		return nil, err
	}
	backupPath, err := collaborationUpgradeBackupPath(dbPath, input.BackupPath)
	if err != nil {
		return nil, err
	}
	before, err := readCollaborationUpgradeMission(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	if before.Revision != input.ExpectedRevision {
		return nil, fmt.Errorf("revision conflict; current=%d", before.Revision)
	}
	if mapStringValue(before.Mission, "id") != input.MissionID {
		return nil, errors.New("wrong mission/database identity")
	}
	if mapStringValue(before.Mission, "controller") != input.ActorSessionID {
		return nil, errors.New("controller-only upgrade")
	}
	if mapStringValue(before.Mission, "status") != "paused" || mapBoolValue(before.Mission, "dispatch_enabled") {
		return nil, errors.New("upgrade requires a paused mission with dispatch disabled")
	}
	if err := createAndVerifyCollaborationUpgradeBackup(ctx, dbPath, backupPath, input.MissionID, input.ExpectedRevision); err != nil {
		return nil, err
	}

	ledger, err := openCollaborationLedger(ctx, dbPath, input.MissionID, input.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	if mapStringValue(ledger.mission, "controller") != input.ActorSessionID {
		return nil, errors.New("controller-only upgrade")
	}
	if ledger.revision != input.ExpectedRevision {
		return nil, fmt.Errorf("revision conflict after backup; current=%d", ledger.revision)
	}
	if mapStringValue(ledger.mission, "status") != "paused" || mapBoolValue(ledger.mission, "dispatch_enabled") {
		return nil, errors.New("upgrade requires a paused mission with dispatch disabled")
	}
	if err := ensureCollaborationUpgradeSchema(ctx, ledger); err != nil {
		return nil, err
	}
	routes, err := collaborationUpgradeProjectionAndRoutes(ctx, ledger, dbPath, input.MissionID)
	if err != nil {
		return nil, err
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}

	routesBound := len(routes) == 0
	if len(routes) > 0 {
		binder, ok := c.agent.(collaborationInboxBinder)
		if !ok {
			return nil, fmt.Errorf("upgrade committed at revision %d but callback route binder is unavailable; routes=%d", input.ExpectedRevision, len(routes))
		}
		if err := binder.BindCollaborationInbox(ctx, routes); err != nil {
			return nil, fmt.Errorf("upgrade committed at revision %d but callback route binding failed: %w", input.ExpectedRevision, err)
		}
		routesBound = true
	}
	return map[string]any{
		"version":     c.cfg.Version,
		"revision":    input.ExpectedRevision,
		"backupPath":  backupPath,
		"routeCount":  len(routes),
		"routesBound": routesBound,
	}, nil
}

func collaborationUpgradeBackupPath(dbPath, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || !filepath.IsAbs(requested) || !strings.EqualFold(filepath.Ext(requested), ".sqlite3") {
		return "", errors.New("backupPath must be an absolute .sqlite3 path")
	}
	requested = filepath.Clean(requested)
	if samePath(dbPath, requested) {
		return "", errors.New("backupPath must be independent from the live database")
	}
	if info, err := os.Lstat(requested); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", errors.New("backupPath must be a new regular file")
		}
		return "", errors.New("backupPath already exists; refusing to overwrite a backup")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(requested)
	info, err := os.Stat(parent)
	if err != nil {
		return "", fmt.Errorf("backup parent is unavailable: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("backup parent must be a directory")
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	resolved := filepath.Join(resolvedParent, filepath.Base(requested))
	if samePath(dbPath, resolved) {
		return "", errors.New("backupPath must be independent from the live database")
	}
	return resolved, nil
}

func readCollaborationUpgradeMission(ctx context.Context, dbPath string) (collaborationUpgradeMissionMeta, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return collaborationUpgradeMissionMeta{}, err
	}
	defer db.Close()
	var raw string
	var revision int64
	if err := db.QueryRowContext(ctx, "SELECT data,revision FROM mission WHERE singleton=1").Scan(&raw, &revision); err != nil {
		return collaborationUpgradeMissionMeta{}, err
	}
	var mission map[string]any
	if err := json.Unmarshal([]byte(raw), &mission); err != nil {
		return collaborationUpgradeMissionMeta{}, errors.New("collaboration mission is invalid")
	}
	return collaborationUpgradeMissionMeta{Mission: mission, Revision: revision}, nil
}

func createAndVerifyCollaborationUpgradeBackup(ctx context.Context, dbPath, backupPath, missionID string, revision int64) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return err
	}
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", backupPath); err != nil {
		_ = db.Close()
		return fmt.Errorf("create mission backup: %w", err)
	}
	if err := db.Close(); err != nil {
		return err
	}
	backupInfo, err := os.Stat(backupPath)
	if err != nil || !backupInfo.Mode().IsRegular() {
		return errors.New("mission backup was not created as a regular file")
	}
	verified, err := readCollaborationUpgradeMission(ctx, backupPath)
	if err != nil {
		return fmt.Errorf("verify mission backup: %w", err)
	}
	if mapStringValue(verified.Mission, "id") != missionID || verified.Revision != revision {
		return errors.New("backup mission or revision does not match the live mission")
	}
	return nil
}

func ensureCollaborationUpgradeSchema(ctx context.Context, ledger *collaborationLedger) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS callback_inbox(result_id TEXT PRIMARY KEY, item_id TEXT NOT NULL, data TEXT NOT NULL, received_at INTEGER NOT NULL, resolution TEXT, resolved_at INTEGER)`,
		`CREATE TABLE IF NOT EXISTS execution_attempts(item_id TEXT NOT NULL, attempt INTEGER NOT NULL, claim TEXT NOT NULL, dispatch_key TEXT UNIQUE, task_ref TEXT UNIQUE, data TEXT NOT NULL, PRIMARY KEY(item_id,attempt), UNIQUE(item_id,claim))`,
		`CREATE TABLE IF NOT EXISTS workstreams(id TEXT PRIMARY KEY, title TEXT NOT NULL, objective TEXT NOT NULL DEFAULT '', owner TEXT NOT NULL DEFAULT '', archived INTEGER NOT NULL DEFAULT 0, revision INTEGER NOT NULL DEFAULT 0, data TEXT NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := ledger.conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if err := collaborationUpgradeAddColumns(ctx, ledger, "callback_inbox", map[string]string{
		"result_id":   "ALTER TABLE callback_inbox ADD COLUMN result_id TEXT NOT NULL DEFAULT ''",
		"item_id":     "ALTER TABLE callback_inbox ADD COLUMN item_id TEXT NOT NULL DEFAULT ''",
		"data":        "ALTER TABLE callback_inbox ADD COLUMN data TEXT NOT NULL DEFAULT '{}'",
		"received_at": "ALTER TABLE callback_inbox ADD COLUMN received_at INTEGER NOT NULL DEFAULT 0",
		"resolution":  "ALTER TABLE callback_inbox ADD COLUMN resolution TEXT",
		"resolved_at": "ALTER TABLE callback_inbox ADD COLUMN resolved_at INTEGER",
	}); err != nil {
		return err
	}
	if err := collaborationUpgradeAddColumns(ctx, ledger, "execution_attempts", map[string]string{
		"item_id":      "ALTER TABLE execution_attempts ADD COLUMN item_id TEXT NOT NULL DEFAULT ''",
		"attempt":      "ALTER TABLE execution_attempts ADD COLUMN attempt INTEGER NOT NULL DEFAULT 1",
		"claim":        "ALTER TABLE execution_attempts ADD COLUMN claim TEXT NOT NULL DEFAULT ''",
		"dispatch_key": "ALTER TABLE execution_attempts ADD COLUMN dispatch_key TEXT",
		"task_ref":     "ALTER TABLE execution_attempts ADD COLUMN task_ref TEXT",
		"data":         "ALTER TABLE execution_attempts ADD COLUMN data TEXT NOT NULL DEFAULT '{}'",
	}); err != nil {
		return err
	}
	if err := collaborationUpgradeAddColumns(ctx, ledger, "items", map[string]string{
		"title":         "ALTER TABLE items ADD COLUMN title TEXT NOT NULL DEFAULT ''",
		"workstream_id": "ALTER TABLE items ADD COLUMN workstream_id TEXT NOT NULL DEFAULT ''",
		"archived":      "ALTER TABLE items ADD COLUMN archived INTEGER NOT NULL DEFAULT 0",
	}); err != nil {
		return err
	}
	if err := collaborationUpgradeAddColumns(ctx, ledger, "workstreams", map[string]string{
		"title":     "ALTER TABLE workstreams ADD COLUMN title TEXT NOT NULL DEFAULT ''",
		"objective": "ALTER TABLE workstreams ADD COLUMN objective TEXT NOT NULL DEFAULT ''",
		"owner":     "ALTER TABLE workstreams ADD COLUMN owner TEXT NOT NULL DEFAULT ''",
		"archived":  "ALTER TABLE workstreams ADD COLUMN archived INTEGER NOT NULL DEFAULT 0",
		"revision":  "ALTER TABLE workstreams ADD COLUMN revision INTEGER NOT NULL DEFAULT 0",
		"data":      "ALTER TABLE workstreams ADD COLUMN data TEXT NOT NULL DEFAULT '{}'",
	}); err != nil {
		return err
	}
	if _, err := ledger.conn.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS pending_callback_inbox ON callback_inbox(resolved_at,result_id)"); err != nil {
		return err
	}
	_, err := ledger.conn.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS workstreams_archived_id ON workstreams(archived,id)")
	return err
}

func collaborationUpgradeAddColumns(ctx context.Context, ledger *collaborationLedger, table string, definitions map[string]string) error {
	columns, err := collaborationUpgradeColumns(ctx, ledger, table)
	if err != nil {
		return err
	}
	for name, statement := range definitions {
		if columns[name] {
			continue
		}
		if _, err := ledger.conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func collaborationUpgradeColumns(ctx context.Context, ledger *collaborationLedger, table string) (map[string]bool, error) {
	rows, err := ledger.conn.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		result[name] = true
	}
	return result, rows.Err()
}

func collaborationUpgradeNeedsRoute(item map[string]any) bool {
	if item["binding"] != nil || mapStringValue(item, "claim") != "" || item["started_at"] != nil {
		return true
	}
	switch mapStringValue(item, "phase") {
	case "dispatching", "in_doubt", "active", "returned", "verifying", "integrating", "accepted", "done":
		return true
	default:
		return false
	}
}

func collaborationUpgradeProjectionAndRoutes(ctx context.Context, ledger *collaborationLedger, dbPath, missionID string) ([]map[string]any, error) {
	rows, err := ledger.conn.QueryContext(ctx, "SELECT id,data FROM items ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	callbackSessions, err := collaborationCallbackSessions(ledger.mission)
	if err != nil {
		return nil, err
	}
	var routes []map[string]any
	for rows.Next() {
		var itemID, raw string
		if err := rows.Scan(&itemID, &raw); err != nil {
			return nil, err
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return nil, fmt.Errorf("item %s is invalid: %w", itemID, err)
		}
		title := mapStringValue(item, "title")
		stream := mapStringValue(item, "workstream_id")
		archived := boolInt(collaborationBoolDefault(item, "archived", false))
		if _, err := ledger.conn.ExecContext(ctx, "UPDATE items SET title=?,workstream_id=?,archived=? WHERE id=?", title, stream, archived, itemID); err != nil {
			return nil, err
		}
		if mapStringValue(item, "executor") != "cloud" || collaborationStringDefault(item, "callback", "none") == "acked" {
			continue
		}
		binding, _ := item["binding"].(map[string]any)
		if binding == nil {
			if !collaborationUpgradeNeedsRoute(item) {
				continue
			}
			return nil, fmt.Errorf("item %s has an unacknowledged Cloud execution without a binding", itemID)
		}
		claim := mapStringValue(item, "claim")
		if claim == "" {
			return nil, fmt.Errorf("item %s has an unacknowledged Cloud binding without a claim", itemID)
		}
		source := mapStringValue(binding, "chatSessionId")
		target := mapStringValue(binding, "callbackSessionId")
		if source == "" || target == "" || mapStringValue(binding, "collaborationId") == "" || mapStringValue(binding, "taskRef") == "" {
			return nil, fmt.Errorf("item %s has an incomplete Cloud execution binding", itemID)
		}
		if !callbackSessions[target] {
			return nil, fmt.Errorf("item %s binding targets an unknown callback session", itemID)
		}
		expectedMission, expectedTask, generation := localCallbackIdentity(collaborationToken{DBPath: dbPath, MissionID: missionID, ItemID: itemID, Claim: claim})
		if mapStringValue(binding, "collaborationId") != expectedMission || mapStringValue(binding, "taskRef") != expectedTask {
			return nil, fmt.Errorf("item %s binding does not match local callback identity", itemID)
		}
		routes = append(routes, map[string]any{
			"sourceSessionId": source,
			"targetSessionId": target,
			"missionId":       expectedMission,
			"taskId":          expectedTask,
			"generation":      generation,
			"callbackInboxRoute": map[string]any{
				"dbPath": dbPath, "missionId": missionID, "itemId": itemID, "claim": claim,
			},
		})
	}
	return routes, rows.Err()
}
