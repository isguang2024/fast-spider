package node

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const collaborationRoleWakeSchema = `
CREATE TABLE IF NOT EXISTS role_wakeup_outbox(
 wake_key TEXT PRIMARY KEY,
 target_role TEXT NOT NULL,
 target_session_id TEXT NOT NULL,
 revision INTEGER NOT NULL,
 reason TEXT NOT NULL,
 item_id TEXT,
 created_at INTEGER NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at INTEGER NOT NULL DEFAULT 0,
 delivered_at INTEGER,
 delivery_turn_id TEXT,
 last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS pending_role_wakeup ON role_wakeup_outbox(delivered_at,next_attempt_at,created_at,wake_key);
`

const (
	collaborationRoleWakeRetryMin = 30 * time.Second
	collaborationRoleWakeRetryMax = 10 * time.Minute
	collaborationRoleWakeBatch    = 16
)

type collaborationRoleWakeRoute struct {
	DBPath    string `json:"dbPath"`
	MissionID string `json:"missionId"`
}

type collaborationRoleWakeRecord struct {
	WakeKey         string
	TargetRole      string
	TargetSessionID string
	Revision        int64
	Reason          string
	ItemID          string
	Attempts        int64
}

// Durable notifications use the local owner's confirmed-turn delivery.
type collaborationLocalTurnDeliverer interface {
	DeliverLocalCodexTurn(context.Context, string, string) (map[string]any, error)
}

func (c *Client) collaborationRoleWakeChannel() chan struct{} {
	c.collaborationWakeMu.Lock()
	defer c.collaborationWakeMu.Unlock()
	if c.collaborationWakeNotify == nil {
		c.collaborationWakeNotify = make(chan struct{}, 1)
	}
	return c.collaborationWakeNotify
}

func (c *Client) signalCollaborationRoleWake() {
	if c == nil {
		return
	}
	ch := c.collaborationRoleWakeChannel()
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (c *Client) enqueueCollaborationRoleWake(ctx context.Context, l *collaborationLedger, targetRole, reason, itemID string, version any) error {
	return c.enqueueCollaborationRoleWakeAt(ctx, l, targetRole, reason, itemID, version, 0)
}

func (c *Client) enqueueCollaborationRoleWakeAt(ctx context.Context, l *collaborationLedger, targetRole, reason, itemID string, version any, notBefore int64) error {
	if c == nil || l == nil {
		return errors.New("collaboration role wake requires client and ledger")
	}
	if notBefore < 0 {
		return errors.New("collaboration role wake notBefore must be nonnegative")
	}
	if targetRole == "coordinator" && (reason == "validation_required" || reason == "validation_capacity_released" || reason == "result_resolved_verify" || reason == "result_resolved_integrate") {
		targetRole = collaborationDeliveryRole(l.mission)
	}
	if targetRole == "coordinator" && strings.HasPrefix(reason, "result_resolved_") && mapStringValue(l.mission, "delivery_coordinator") != "" {
		if err := c.enqueueCollaborationRoleWakeAt(ctx, l, "delivery_coordinator", reason, itemID, version, notBefore); err != nil {
			return err
		}
	}
	if targetRole != "controller" && targetRole != "coordinator" && targetRole != "delivery_coordinator" {
		return errors.New("invalid collaboration role wake target")
	}
	targetSessionID := mapStringValue(l.mission, targetRole)
	if err := validateCollaborationOpaqueID(targetSessionID, targetRole+" session"); err != nil {
		return err
	}
	if err := validateCollaborationText(reason, "role wake reason", 256); err != nil {
		return err
	}
	if itemID != "" {
		if err := validateCollaborationOpaqueID(itemID, "role wake itemId"); err != nil {
			return err
		}
	}
	// Register the route before the ledger transaction can commit. A stale route
	// is harmless; a committed outbox row can therefore always be rediscovered
	// after process restart.
	if err := c.ensureCollaborationRoleWakeRoute(mapStringValue(l.mission, "db_path"), mapStringValue(l.mission, "id")); err != nil {
		return err
	}
	if _, err := l.conn.ExecContext(ctx, collaborationRoleWakeSchema); err != nil {
		return err
	}
	key, err := collaborationActionHash([]any{mapStringValue(l.mission, "id"), targetRole, reason, itemID, version})
	if err != nil {
		return err
	}
	wakeKey := "wake-v1-" + key
	_, err = l.conn.ExecContext(ctx, `INSERT INTO role_wakeup_outbox(wake_key,target_role,target_session_id,revision,reason,item_id,created_at,next_attempt_at)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(wake_key) DO NOTHING`, wakeKey, targetRole, targetSessionID, l.revision, reason, nullableCollaborationString(itemID), time.Now().Unix(), notBefore)
	return err
}

func (c *Client) collaborationRoleWakeRegistryPath() (string, error) {
	if c == nil || strings.TrimSpace(c.cfg.DataDir) == "" {
		return "", errors.New("node data directory is required for durable collaboration wake routes")
	}
	return filepath.Join(c.cfg.DataDir, "collaboration-role-wake-routes.json"), nil
}

func (c *Client) ensureCollaborationRoleWakeRoute(dbPath, missionID string) error {
	path, err := c.collaborationRoleWakeRegistryPath()
	if err != nil {
		return err
	}
	if !filepath.IsAbs(dbPath) || missionID == "" {
		return errors.New("invalid collaboration role wake route")
	}
	c.collaborationWakeMu.Lock()
	defer c.collaborationWakeMu.Unlock()
	routes, err := readCollaborationRoleWakeRoutes(path)
	if err != nil {
		return err
	}
	for _, route := range routes {
		if samePath(route.DBPath, dbPath) && route.MissionID == missionID {
			return nil
		}
	}
	routes = append(routes, collaborationRoleWakeRoute{DBPath: dbPath, MissionID: missionID})
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].DBPath != routes[j].DBPath {
			return routes[i].DBPath < routes[j].DBPath
		}
		return routes[i].MissionID < routes[j].MissionID
	})
	return writeCollaborationRoleWakeRoutes(path, routes)
}

func readCollaborationRoleWakeRoutes(path string) ([]collaborationRoleWakeRoute, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var routes []collaborationRoleWakeRoute
	if err := json.Unmarshal(raw, &routes); err != nil {
		return nil, fmt.Errorf("read collaboration role wake registry: %w", err)
	}
	if len(routes) > 512 {
		return nil, errors.New("collaboration role wake registry exceeds bounded route limit")
	}
	return routes, nil
}

func writeCollaborationRoleWakeRoutes(path string, routes []collaborationRoleWakeRoute) error {
	if len(routes) > 512 {
		return errors.New("collaboration role wake registry exceeds bounded route limit")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(routes)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".collaboration-role-wakes-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceFile(tmpPath, path); err != nil {
		return err
	}
	keep = true
	return nil
}

func (c *Client) runCollaborationRoleWakeDispatcher(ctx context.Context) {
	if c == nil {
		return
	}
	ch := c.collaborationRoleWakeChannel()
	ticker := time.NewTicker(collaborationRoleWakeRetryMin)
	defer ticker.Stop()
	c.drainCollaborationRoleWakes(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			c.drainCollaborationRoleWakes(ctx)
		case <-ticker.C:
			c.drainCollaborationRoleWakes(ctx)
		}
	}
}

func (c *Client) drainCollaborationRoleWakes(ctx context.Context) {
	path, err := c.collaborationRoleWakeRegistryPath()
	if err != nil {
		return
	}
	c.collaborationWakeMu.Lock()
	routes, err := readCollaborationRoleWakeRoutes(path)
	c.collaborationWakeMu.Unlock()
	if err != nil {
		c.cfg.Logger.Warn("read collaboration role wake routes", "error", err)
		return
	}
	for _, route := range routes {
		if ctx.Err() != nil {
			return
		}
		if err := c.drainCollaborationRoleWakeRoute(ctx, route); err != nil {
			c.cfg.Logger.Warn("deliver collaboration role wakes", "missionId", route.MissionID, "error", err)
		}
	}
}

func (c *Client) drainCollaborationRoleWakeRoute(ctx context.Context, route collaborationRoleWakeRoute) error {
	deliverer, ok := c.agent.(collaborationLocalTurnDeliverer)
	if !ok {
		return ErrAgentProviderUnavailable
	}
	db, err := sql.Open("sqlite", route.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		return err
	}
	var missionRaw string
	if err := db.QueryRowContext(ctx, "SELECT data FROM mission WHERE singleton=1").Scan(&missionRaw); err != nil {
		return err
	}
	var mission map[string]any
	if json.Unmarshal([]byte(missionRaw), &mission) != nil || mapStringValue(mission, "id") != route.MissionID || !samePath(mapStringValue(mission, "db_path"), route.DBPath) {
		return errors.New("collaboration role wake route identity mismatch")
	}
	if mapStringValue(mission, "status") != "active" {
		return nil
	}
	if err := c.ensureCollaborationWorkWakes(ctx, route, mapStringValue(mission, "controller"), time.Now().Unix()); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, collaborationRoleWakeSchema); err != nil {
		return err
	}
	now := time.Now().Unix()
	rows, err := db.QueryContext(ctx, `SELECT wake_key,target_role,target_session_id,revision,reason,coalesce(item_id,''),attempts
		FROM role_wakeup_outbox WHERE delivered_at IS NULL AND next_attempt_at<=? ORDER BY created_at,wake_key LIMIT ?`, now, collaborationRoleWakeBatch)
	if err != nil {
		return err
	}
	var pending []collaborationRoleWakeRecord
	for rows.Next() {
		var wake collaborationRoleWakeRecord
		if err := rows.Scan(&wake.WakeKey, &wake.TargetRole, &wake.TargetSessionID, &wake.Revision, &wake.Reason, &wake.ItemID, &wake.Attempts); err != nil {
			_ = rows.Close()
			return err
		}
		pending = append(pending, wake)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	processed := map[string]bool{}
	for _, wake := range pending {
		if mapStringValue(mission, wake.TargetRole) != wake.TargetSessionID {
			if _, err := db.ExecContext(ctx, "DELETE FROM role_wakeup_outbox WHERE wake_key=? AND delivered_at IS NULL", wake.WakeKey); err != nil {
				return err
			}
			continue
		}
		if processed[wake.TargetSessionID] {
			continue
		}
		processed[wake.TargetSessionID] = true
		group := []collaborationRoleWakeRecord{}
		for _, candidate := range pending {
			if candidate.TargetSessionID == wake.TargetSessionID && mapStringValue(mission, candidate.TargetRole) == candidate.TargetSessionID {
				group = append(group, candidate)
				if candidate.Revision > wake.Revision {
					wake = candidate
				}
			}
		}
		prompt := collaborationRoleWakePrompt(route, wake)
		turnCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		result, sendErr := deliverer.DeliverLocalCodexTurn(turnCtx, wake.TargetSessionID, prompt)
		cancel()
		if sendErr == nil {
			sendErr = validateCollaborationRoleWakeDelivery(result)
		}
		if sendErr != nil {
			for _, wake := range group {
				attempts := wake.Attempts + 1
				delay := collaborationRoleWakeRetryMin
				for n := int64(1); n < attempts && delay < collaborationRoleWakeRetryMax; n++ {
					delay *= 2
					if delay > collaborationRoleWakeRetryMax {
						delay = collaborationRoleWakeRetryMax
					}
				}
				_, updateErr := db.ExecContext(ctx, `UPDATE role_wakeup_outbox SET attempts=?,next_attempt_at=?,last_error=? WHERE wake_key=? AND delivered_at IS NULL`, attempts, time.Now().Add(delay).Unix(), boundedCollaborationWakeError(sendErr), wake.WakeKey)
				if updateErr != nil {
					return updateErr
				}
			}
			continue
		}
		turnID := mapStringValue(result, "turnId")
		for _, wake := range group {
			if _, err := db.ExecContext(ctx, `UPDATE role_wakeup_outbox SET delivered_at=?,delivery_turn_id=?,last_error='' WHERE wake_key=? AND delivered_at IS NULL`, time.Now().Unix(), turnID, wake.WakeKey); err != nil {
				return err
			}
		}
	}
	return nil
}

func collaborationRoleWakePrompt(route collaborationRoleWakeRoute, wake collaborationRoleWakeRecord) string {
	request, _ := json.Marshal(map[string]any{"action": "next_actions", "params": map[string]any{"dbPath": route.DBPath, "missionId": route.MissionID, "actorSessionId": wake.TargetSessionID}})
	return fmt.Sprintf("FAST_SPIDER_ROLE_WAKE_V1\nWAKE_KEY: %s\nMISSION_ID: %s\nDB_PATH: %s\nROLE: %s\nACTOR_SESSION_ID: %s\nREVISION: %d\nREASON: %s\nITEM_ID: %s\nYou are the bound task identified by ACTOR_SESSION_ID. Call FastSpider_Local.collaboration_control with these exact identity parameters: %s\nConsume the returned bounded worklist using worklistContract/controllerWorklist and the current cloud-collaboration skill. Controller: decide complete packets then freeze at least one safe independent READY or record exact blockers. Delivery: fill complete controller params for results, integration and preparation; unchanged evidence never cancels an unsettled action. Execution: dispatch safe READY; when empty with capacity report precise preparationHandoff IDs. Do not end after a status summary while actionable authorized work remains. The ledger determines current role and work; do not resume superseded tasks from old conversation instructions. Treat WAKE_KEY as the stable retry identity. Callback/terminal handoff stays first. Do not poll already-dispatched Cloud CHATs and do not infer terminal state from this wake.", wake.WakeKey, route.MissionID, route.DBPath, wake.TargetRole, wake.TargetSessionID, wake.Revision, wake.Reason, wake.ItemID, request)
}

func validateCollaborationRoleWakeDelivery(result map[string]any) error {
	if strings.TrimSpace(mapStringValue(result, "turnId")) == "" {
		return errors.New("role wake delivery did not return a local Codex turnId")
	}
	mode, owner := mapStringValue(result, "executionMode"), mapStringValue(result, "owner")
	if mode == "codex_app_server" && owner == "fast_spider_node" || mode == "codex_desktop_ipc" && owner == "codex_desktop" {
		return nil
	}
	return errors.New("role wake delivery was not confirmed by a local Codex turn")
}

func boundedCollaborationWakeError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.ReplaceAll(strings.ReplaceAll(err.Error(), "\r", " "), "\n", " ")
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}
