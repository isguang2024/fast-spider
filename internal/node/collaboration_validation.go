package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	pathpkg "path"
	"strings"
	"time"
)

const (
	collaborationDefaultValidationCapacity = int64(5)
	collaborationMaxValidationBurst        = int64(10)
)

// Existing launches stay with their creator across the single-to-dual cutover.
func collaborationDeliveryRole(mission map[string]any) string {
	if mapStringValue(mission, "delivery_coordinator") != "" {
		return "delivery_coordinator"
	}
	return "coordinator"
}

func collaborationValidationRole(mission, item map[string]any) string {
	if collaborationValidationLaunchParent(mapStringValue(item, "validation_launch_ref")) == mapStringValue(mission, "coordinator") {
		return "coordinator"
	}
	return collaborationDeliveryRole(mission)
}

type collaborationValidationClaimParams struct {
	collaborationIdentityParams
	ExpectedRevision int64  `json:"expectedRevision"`
	ItemID           string `json:"itemId"`
	LaunchRef        string `json:"launchRef"`
}

type collaborationValidationReceiptParams struct {
	collaborationIdentityParams
	ExpectedRevision int64  `json:"expectedRevision"`
	ItemID           string `json:"itemId"`
	ValidationClaim  string `json:"validationClaim"`
	ExecutionRef     string `json:"executionRef"`
}

func collaborationValidationCapacity(mission map[string]any) (normal, burst, total int64) {
	normal = collaborationDefaultValidationCapacity
	capacity := collaborationOptionalMap(mission["capacity"])
	if value, ok := collaborationInt64(capacity["validation"]); ok {
		normal = value
	}
	if value, ok := collaborationInt64(capacity["validationBurst"]); ok {
		burst = value
	}
	return normal, burst, normal + burst
}

func (l *collaborationLedger) collaborationValidationHeld(ctx context.Context) (int64, error) {
	rows, err := l.conn.QueryContext(ctx, "SELECT data FROM items WHERE phase='verifying'")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var held int64
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return 0, err
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return 0, errors.New("collaboration item is invalid")
		}
		if mapStringValue(item, "validation_claim") != "" {
			held++
		}
	}
	return held, rows.Err()
}

func collaborationValidationCapacityView(mission map[string]any, held int64) map[string]any {
	normal, burst, total := collaborationValidationCapacity(mission)
	available := total - held
	if available < 0 {
		available = 0
	}
	return map[string]any{
		"normal": normal, "burst": burst, "total": total,
		"held": held, "available": available, "burstAuthorized": burst > 0,
	}
}

func validateCollaborationValidationLaunchRef(ref string) error {
	value := strings.TrimPrefix(strings.TrimSpace(ref), "codex-agent:")
	if value == ref || value == "" {
		return errors.New("validation launchRef must be codex-agent:<coordinatorThreadId>#<canonicalPath>")
	}
	parent, canonicalPath, ok := strings.Cut(value, "#")
	if !ok || parent == "" || canonicalPath == "" || !strings.HasPrefix(canonicalPath, "/") {
		return errors.New("validation launchRef must include coordinator thread and canonical path")
	}
	if err := validateCollaborationOpaqueID(parent, "validation launch parent"); err != nil {
		return err
	}
	if strings.ContainsAny(canonicalPath, "\\\t\r\n") || pathpkg.Clean(canonicalPath) != canonicalPath || canonicalPath == "/" {
		return errors.New("validation launch path must be canonical absolute slash path")
	}
	for _, part := range strings.Split(strings.TrimPrefix(canonicalPath, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("validation launch path must be canonical")
		}
	}
	return nil
}

func collaborationValidationLaunchParent(ref string) string {
	value := strings.TrimPrefix(strings.TrimSpace(ref), "codex-agent:")
	parent, _, _ := strings.Cut(value, "#")
	return parent
}

func validateCollaborationValidationExecutionRef(ref string) error {
	if ref != strings.TrimSpace(ref) {
		return errors.New("validation executionRef must not contain surrounding whitespace")
	}
	if strings.HasPrefix(ref, "codex-agent:") {
		return validateCollaborationValidationLaunchRef(ref)
	}
	value := strings.TrimPrefix(ref, "codex-thread:")
	if value == ref || value == "" || strings.ContainsAny(value, "# /\\\t\r\n") {
		return errors.New("validation executionRef must be a native codex-agent:<parent>#<canonicalPath> or codex-thread:<threadId>")
	}
	return validateCollaborationOpaqueID(value, "validation execution thread")
}

func clearCollaborationValidationExecution(item map[string]any) {
	for _, key := range []string{"validation_owner", "validation_claim", "validation_launch_ref", "validation_execution_ref", "validation_claimed_at", "validation_started_at"} {
		item[key] = nil
	}
}

func (c *Client) collaborationValidationClaim(ctx context.Context, p collaborationValidationClaimParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(p.DBPath, p.MissionID, p.ActorSessionID)
	if err != nil {
		return nil, err
	}
	if err := validateCollaborationOpaqueID(p.ItemID, "itemId"); err != nil {
		return nil, err
	}
	if err := validateCollaborationValidationLaunchRef(p.LaunchRef); err != nil {
		return nil, err
	}
	if collaborationValidationLaunchParent(p.LaunchRef) != p.ActorSessionID {
		return nil, errors.New("validation launchRef parent must be the bound coordinator session")
	}
	l, err := openCollaborationLedger(ctx, dbPath, p.MissionID, p.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer l.rollback()
	item, err := l.item(ctx, p.ItemID)
	if err != nil {
		return nil, err
	}
	if mapStringValue(item, "phase") != "verifying" {
		return nil, errors.New("validation claim requires phase=verifying")
	}
	if existing := mapStringValue(item, "validation_claim"); existing != "" {
		if mapStringValue(item, "validation_launch_ref") != p.LaunchRef {
			return nil, errors.New("validation already claimed with another launchRef; recover the original binding")
		}
		if err := l.commit(ctx); err != nil {
			return nil, err
		}
		held, _ := c.collaborationValidationHeldRead(ctx, dbPath, p.MissionID, p.ActorSessionID)
		result := map[string]any{"revision": l.revision, "itemId": p.ItemID, "validationClaim": existing, "launchRef": p.LaunchRef, "replayed": true, "capacity": collaborationValidationCapacityView(l.mission, held)}
		if ref := mapStringValue(item, "validation_execution_ref"); ref != "" {
			result["executionRef"] = ref
		}
		return result, nil
	}
	if p.ExpectedRevision != l.revision {
		return nil, fmt.Errorf("revision conflict; current=%d", l.revision)
	}
	if mapStringValue(l.mission, collaborationDeliveryRole(l.mission)) != p.ActorSessionID {
		return nil, errors.New("delivery coordinator owns new validation claims")
	}
	if mapStringValue(l.mission, "status") != "active" || !mapBoolValue(l.mission, "dispatch_enabled") {
		return nil, errors.New("mission dispatch paused")
	}
	held, err := l.collaborationValidationHeld(ctx)
	if err != nil {
		return nil, err
	}
	_, _, total := collaborationValidationCapacity(l.mission)
	if held >= total {
		return nil, errors.New("validation capacity exhausted; normal limit is 5 and additional slots require controller-authorized validationBurst")
	}
	claim, err := randomCollaborationClaim()
	if err != nil {
		return nil, err
	}
	item["validation_claim"] = claim
	item["validation_launch_ref"] = p.LaunchRef
	item["validation_claimed_at"] = time.Now().Unix()
	item["next_action"] = "Launch one native spawn_agent child and bind its returned canonical executionRef with validation_receipt; do not create a top-level tracking task"
	if err := validateCollaborationItem(item, l.mission); err != nil {
		return nil, err
	}
	if err := l.saveItem(ctx, item); err != nil {
		return nil, err
	}
	if err := c.enqueueCollaborationRoleWake(ctx, l, collaborationValidationRole(l.mission, item), "validation_binding_pending", p.ItemID, claim); err != nil {
		return nil, err
	}
	if err := l.commit(ctx); err != nil {
		return nil, err
	}
	c.signalCollaborationRoleWake()
	return map[string]any{
		"revision": l.revision, "itemId": p.ItemID, "validationClaim": claim, "launchRef": p.LaunchRef,
		"capacity":   collaborationValidationCapacityView(l.mission, held+1),
		"nextAction": "Use spawn_agent once under this coordinator, then bind codex-agent:<parent>#<returned canonical path> (or the real child thread ID). If receipt is lost, recover the same child; never create a tracked top-level substitute.",
	}, nil
}

func (c *Client) collaborationValidationReceipt(ctx context.Context, p collaborationValidationReceiptParams) (map[string]any, error) {
	dbPath, err := validateCollaborationBaseIdentity(p.DBPath, p.MissionID, p.ActorSessionID)
	if err != nil {
		return nil, err
	}
	if err := validateCollaborationOpaqueID(p.ItemID, "itemId"); err != nil {
		return nil, err
	}
	if err := validateCollaborationOpaqueID(p.ValidationClaim, "validationClaim"); err != nil {
		return nil, err
	}
	if err := validateCollaborationValidationExecutionRef(p.ExecutionRef); err != nil {
		return nil, err
	}
	l, err := openCollaborationLedger(ctx, dbPath, p.MissionID, p.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer l.rollback()
	item, err := l.item(ctx, p.ItemID)
	if err != nil {
		return nil, err
	}
	if collaborationValidationLaunchParent(mapStringValue(item, "validation_launch_ref")) != p.ActorSessionID {
		return nil, errors.New("validation receipt belongs to its launch owner")
	}
	if mapStringValue(item, "phase") != "verifying" || mapStringValue(item, "validation_claim") != p.ValidationClaim {
		return nil, errors.New("validation receipt does not match the active validation claim")
	}
	if strings.HasPrefix(p.ExecutionRef, "codex-agent:") && p.ExecutionRef != mapStringValue(item, "validation_launch_ref") {
		return nil, errors.New("canonical validation executionRef must match this claim's launchRef")
	}
	if existing := mapStringValue(item, "validation_execution_ref"); existing != "" {
		if existing != p.ExecutionRef {
			return nil, errors.New("validation claim already bound to another executionRef")
		}
		if err := l.commit(ctx); err != nil {
			return nil, err
		}
		return map[string]any{"revision": l.revision, "itemId": p.ItemID, "validationClaim": p.ValidationClaim, "executionRef": existing, "replayed": true}, nil
	}
	if p.ExpectedRevision != l.revision {
		return nil, fmt.Errorf("revision conflict; current=%d", l.revision)
	}
	startedAt := time.Now().Unix()
	dueAt := startedAt + int64((10*time.Minute)/time.Second)
	item["validation_execution_ref"] = p.ExecutionRef
	item["validation_started_at"] = startedAt
	item["next_check_at"] = nil
	item["next_action"] = "Controller consumes terminal validation evidence through apply; never recover the original Cloud writer for validation stalls"
	if err := validateCollaborationItem(item, l.mission); err != nil {
		return nil, err
	}
	if err := l.saveItem(ctx, item); err != nil {
		return nil, err
	}
	if mapStringValue(l.mission, "delivery_coordinator") == "" {
		if err := c.enqueueCollaborationRoleWake(ctx, l, "controller", "validation_execution_bound", p.ItemID, p.ExecutionRef); err != nil {
			return nil, err
		}
		if err := c.enqueueCollaborationRoleWakeAt(ctx, l, "controller", "validation_check_due", p.ItemID, []any{p.ExecutionRef, dueAt}, dueAt); err != nil {
			return nil, err
		}
	}
	if err := c.enqueueCollaborationRoleWakeAt(ctx, l, collaborationValidationRole(l.mission, item), "validation_notify_due", p.ItemID, []any{p.ExecutionRef, dueAt}, dueAt); err != nil {
		return nil, err
	}
	if err := l.commit(ctx); err != nil {
		return nil, err
	}
	c.signalCollaborationRoleWake()
	return map[string]any{"revision": l.revision, "itemId": p.ItemID, "validationClaim": p.ValidationClaim, "executionRef": p.ExecutionRef, "phase": "verifying"}, nil
}

func (c *Client) collaborationValidationHeldRead(ctx context.Context, dbPath, missionID, actorSessionID string) (int64, error) {
	l, err := openCollaborationLedger(ctx, dbPath, missionID, actorSessionID, false)
	if err != nil {
		return 0, err
	}
	defer l.rollback()
	held, err := l.collaborationValidationHeld(ctx)
	if err != nil {
		return 0, err
	}
	return held, l.commit(ctx)
}
