package node

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const collaborationControllerWorkReminderSeconds int64 = 3600

func collaborationDecisionWork(kind string) bool {
	switch kind {
	case "prepare_task", "prepare_task_packet", "prepare_followup", "review_result", "prepare_review_result_decision", "review_integration", "prepare_review_integration_decision", "prepare_rework", "decide_stalled_check":
		return true
	}
	return false
}

func collaborationWorkWakeVersion(action collaborationActionCandidate, item map[string]any) []any {
	return []any{action.Kind, item["current_attempt"], item["terminal_ref"], item["execution_ref"], item["validation_claim"], item["started_at"]}
}

func collaborationWorkWakeKey(mission map[string]any, role string, action collaborationActionCandidate, item map[string]any) (string, error) {
	hash, err := collaborationActionHash([]any{mapStringValue(mission, "id"), role, "work_due_" + action.Kind, fmt.Sprint(action.ItemID), collaborationWorkWakeVersion(action, item)})
	return "wake-v1-" + hash, err
}

// Reuses the durable role outbox. A delivered message is not a settled action.
// The first row supplies age; hourly reminder rows retain the original audit.
func (c *Client) ensureCollaborationWorkWakes(ctx context.Context, route collaborationRoleWakeRoute, controller string, now int64) error {
	l, err := openCollaborationLedgerMode(ctx, route.DBPath, route.MissionID, controller, true, true)
	if err != nil {
		return err
	}
	defer l.rollback()
	if mapStringValue(l.mission, "status") != "active" {
		return nil
	}
	if _, err := l.conn.ExecContext(ctx, collaborationRoleWakeSchema); err != nil {
		return err
	}
	observation, err := l.readObservation(ctx)
	if err != nil {
		return err
	}
	actions, err := l.actionCandidatesAt(ctx, observation, now)
	if err != nil {
		return err
	}
	live := map[string]bool{}
	for _, a := range actions {
		localCheck := false
		if a.ItemID == nil {
			continue
		}
		item, err := l.item(ctx, fmt.Sprint(a.ItemID))
		if err != nil {
			return err
		}
		if a.Kind == "check_execution" && mapStringValue(item, "executor") == "local" {
			localCheck = true
		}
		if !collaborationDecisionWork(a.Kind) && !localCheck {
			continue
		}
		live[a.Owner+"|"+a.Kind+"|"+fmt.Sprint(a.ItemID)] = true
		role := "controller"
		if a.Owner == mapStringValue(l.mission, "delivery_coordinator") {
			role = "delivery_coordinator"
		} else if a.Owner == mapStringValue(l.mission, "coordinator") {
			role = "coordinator"
		}
		key, err := collaborationWorkWakeKey(l.mission, role, a, item)
		if err != nil {
			return err
		}
		var created int64
		var delivered sql.NullInt64
		err = l.conn.QueryRowContext(ctx, "SELECT created_at,delivered_at FROM role_wakeup_outbox WHERE wake_key=?", key).Scan(&created, &delivered)
		if errors.Is(err, sql.ErrNoRows) {
			if err := c.enqueueCollaborationRoleWakeAt(ctx, l, role, "work_due_"+a.Kind, fmt.Sprint(a.ItemID), collaborationWorkWakeVersion(a, item), a.DueAt); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if delivered.Valid && now-delivered.Int64 >= collaborationControllerWorkReminderSeconds {
			// A stable hourly key coalesces retries across restarts without hiding work.
			version := []any{collaborationWorkWakeVersion(a, item), (now - delivered.Int64) / collaborationControllerWorkReminderSeconds}
			if err := c.enqueueCollaborationRoleWakeAt(ctx, l, role, "work_reminder_"+a.Kind, fmt.Sprint(a.ItemID), version, 0); err != nil {
				return err
			}
		}
	}
	rows, err := l.conn.QueryContext(ctx, "SELECT wake_key,target_session_id,reason,item_id FROM role_wakeup_outbox WHERE delivered_at IS NULL AND (reason LIKE 'work_due_%' OR reason LIKE 'work_reminder_%')")
	if err != nil {
		return err
	}
	var obsolete []string
	for rows.Next() {
		var key, owner, reason, id string
		if err := rows.Scan(&key, &owner, &reason, &id); err != nil {
			rows.Close()
			return err
		}
		kind := strings.TrimPrefix(strings.TrimPrefix(reason, "work_due_"), "work_reminder_")
		if !live[owner+"|"+kind+"|"+id] {
			obsolete = append(obsolete, key)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, key := range obsolete {
		if _, err := l.conn.ExecContext(ctx, "DELETE FROM role_wakeup_outbox WHERE wake_key=? AND delivered_at IS NULL", key); err != nil {
			return err
		}
	}
	return l.commit(ctx)
}

func (l *collaborationLedger) addCollaborationWorkCall(ctx context.Context, input collaborationNextActionsParams, a collaborationActionCandidate, entry map[string]any) error {
	if !collaborationDecisionWork(a.Kind) || a.ItemID == nil {
		return nil
	}
	item, err := l.item(ctx, fmt.Sprint(a.ItemID))
	if err != nil {
		return err
	}
	params := map[string]any{"dbPath": input.DBPath, "missionId": input.MissionID, "actorSessionId": l.mission["controller"], "expectedRevision": l.revision}
	call := map[string]any{"action": "apply", "params": params, "controllerOnly": true}
	params["items"] = []any{map[string]any{"id": a.ItemID}}
	switch a.Kind {
	case "prepare_task", "prepare_task_packet", "prepare_followup", "prepare_rework":
		call["requiredInput"] = []string{"items[0]: complete authorized goal/acceptance, dependencies, narrow scope and packet; phase=ready only after controller approval"}
		entry["completionRule"] = "Delivery prepares complete controller apply params from current evidence; controller freezes safe READY now or records an exact dependency/scope/policy blocker. Existing authorization covers ordinary reversible technical work. Missing packet fields require preparation, not indefinite waiting. Do not copy a sibling's blocker."
	case "review_result", "prepare_review_result_decision":
		call["action"] = "resolve"
		delete(params, "items")
		params["resultId"] = item["terminal_ref"]
		call["requiredInput"] = []string{"decision", "evidenceRef", "decision-specific fields: accept validation/integration, verify validationOwner, block blocker"}
		entry["completionRule"] = "Delivery fills the result decision and all required fields; controller resolves the exact inbox. Execution completion is not business acceptance."
	case "review_integration", "prepare_review_integration_decision":
		call["requiredInput"] = []string{"items[0]: evidence, validation/integration disposition and phase; for a real wait include exact blocker owner/resume_when/next_check_at"}
		entry["completionRule"] = "Produce an actionable controller apply package. If integration waits on a known dependency, propose a concrete blocked disposition and its resume condition. A repeated message may be suppressed; the unsettled action must still be decided. Do not keep resending or silently waiting on integration=pending."
	case "decide_stalled_check":
		call["requiredInput"] = []string{"controller disposition: verified continuation with native evidence, confirmed stopped replacement binding, or explicit blocker owner/resume_when/next_check_at; preserve unknown writer scope"}
		call["blockerFields"] = []string{"kind", "owner", "reason", "resume_when", "next_check_at"}
		entry["completionRule"] = "Controller must choose a concrete continue/replace/block disposition from actual native evidence. Merely postponing next_check_at does not settle this action. Continue other independent work."
	}
	entry["controllerCall"] = call
	entry["sourceFacts"] = selectCollaborationFields(item, "id", "phase", "owner", "depends_on", "next_action", "terminal_ref", "execution_ref", "local_scope", "validation", "integration")
	entry["handoff"] = map[string]any{"tool": "send_message_to_thread", "params": map[string]any{"threadId": l.mission["controller"]}, "requiredInput": "Complete proposed action/params, current revision, decisive evidence and exact remaining constraints; controller alone executes the decision"}
	return nil
}

func (l *collaborationLedger) addCollaborationWorkDiagnostics(ctx context.Context, input collaborationNextActionsParams, now int64, actions []collaborationActionCandidate, refill collaborationRefillPlan, result map[string]any) error {
	items, err := l.currentItems(ctx)
	if err != nil {
		return err
	}
	eligible := []any{}
	blocked := []any{}
	unverified := []any{}
	work := []any{}
	maxAge := int64(0)
	observation, err := l.readObservation(ctx)
	if err != nil {
		return err
	}
	checks := collaborationOptionalMap(observation["action_checks"])
	checkIDs := map[string]string{}
	for _, a := range actions {
		if a.Kind == "check_execution" {
			checkIDs[fmt.Sprint(a.ItemID)] = a.ActionID
		}
	}
	for _, item := range items {
		id := mapStringValue(item, "id")
		if mapStringValue(item, "phase") == "planned" {
			reason, detail := "", ""
			if mapStringValue(l.mission, "status") != "active" || !mapBoolValue(l.mission, "dispatch_enabled") {
				reason, detail = "policy", "mission is paused or dispatch disabled"
			} else if err := l.checkDependencies(ctx, item); err != nil {
				reason, detail = "dependency", err.Error()
			}
			scope := collaborationScopePacket(item)
			if reason == "" && len(scope) > 0 {
				candidate := cloneParams(item)
				candidate["phase"] = "ready"
				if err := l.checkUnique(ctx, id, candidate); err != nil {
					reason, detail = "write_scope_or_policy", err.Error()
				}
			}
			entry := map[string]any{"itemId": id}
			if reason != "" {
				entry["reason"], entry["detail"] = reason, detail
				blocked = append(blocked, entry)
			} else {
				entry["scopeStatus"] = "requires_controller_frozen_packet"
				if len(scope) > 0 {
					entry["scopeStatus"] = "checked_against_current_writers"
				}
				entry["nextAction"] = "controller prepare_task; delivery fills exact packet, scope and acceptance before approval"
				eligible = append(eligible, entry)
			}
		}
		if mapStringValue(item, "executor") == "local" && mapStringValue(item, "phase") == "active" {
			key, _ := encodeCollaborationJSON([]any{"coordinator", "check_execution", id})
			record := collaborationOptionalMap(checks[key])
			if mapStringValue(record, "action_id") != checkIDs[id] || collaborationIntDefault(record, "last_progress_at", 0) == 0 || collaborationBoolDefault(record, "exhausted", false) || now-collaborationIntDefault(record, "last_progress_at", 0) > 1800 {
				unverified = append(unverified, map[string]any{"itemId": id, "executionRef": item["execution_ref"], "firstCheckAt": collaborationIntDefault(item, "started_at", 0) + 900})
			}
		}
	}
	var hasOutbox int
	if err := l.conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='role_wakeup_outbox'").Scan(&hasOutbox); err != nil {
		return err
	}
	for _, a := range actions {
		if a.Owner != mapStringValue(l.mission, "controller") || !collaborationDecisionWork(a.Kind) || a.DueAt > now {
			continue
		}
		entry := map[string]any{"actionId": a.ActionID, "itemId": a.ItemID, "kind": a.Kind, "owner": a.Owner}
		if a.SourceKind != "" {
			entry["sourceKind"] = a.SourceKind
		}
		if err := l.addCollaborationCheckCalls(ctx, input, a, observation, entry); err != nil {
			return err
		}
		if err := l.addCollaborationWorkCall(ctx, input, a, entry); err != nil {
			return err
		}
		item, err := l.item(ctx, fmt.Sprint(a.ItemID))
		if err != nil {
			return err
		}
		age := int64(0)
		if hasOutbox != 0 {
			key, err := collaborationWorkWakeKey(l.mission, "controller", a, item)
			if err != nil {
				return err
			}
			var created int64
			err = l.conn.QueryRowContext(ctx, "SELECT created_at FROM role_wakeup_outbox WHERE wake_key=?", key).Scan(&created)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil && now > created {
				age = now - created
			}
		}
		entry["dueActionAgeSeconds"] = age
		entry["overdue"] = age >= collaborationControllerWorkReminderSeconds
		if age > maxAge {
			maxAge = age
		}
		work = append(work, entry)
	}
	idle := refill.CloudFree > 0 && len(refill.DispatchItemIDs) == 0 && len(eligible) > 0
	bounded := func(values []any) []any {
		if len(values) > 20 {
			return values[:20]
		}
		return values
	}
	result["diagnostics"] = map[string]any{"controller_due_action_age": maxAge, "planned_eligible_not_ready": len(eligible), "coordinator_idle_with_capacity": idle, "active_without_verified_progress": len(unverified), "activeWithoutVerifiedProgress": bounded(unverified)}
	result["plannedPreparation"] = map[string]any{"eligible": bounded(eligible), "blocked": bounded(blocked), "hasMore": len(eligible) > 20 || len(blocked) > 20, "scopeRule": "eligible means dependencies permit preparation; missing scope is never permission to dispatch"}
	sort.SliceStable(work, func(i, j int) bool {
		rank := func(v any) int {
			k := mapStringValue(v.(map[string]any), "kind")
			if k == "decide_stalled_check" {
				return 0
			}
			if k == "review_result" || k == "review_integration" {
				return 1
			}
			return 2
		}
		return rank(work[i]) < rank(work[j])
	})
	if len(work) > 8 {
		result["controllerWorklistHasMore"] = true
		work = work[:8]
	}
	result["controllerWorklist"] = work
	result["boundedRefillInvariant"] = map[string]any{"required": refill.CloudFree > 0 && (len(eligible) > 0 || len(refill.DispatchItemIDs) > 0), "rule": "Complete at least one safe dispatch/READY preparation in this wake, or provide each candidate's actual dependency/write-scope/policy reason. Packet preparation is required before dispatch; one blocked chain does not freeze independent work. No invented business authorization."}
	if idle && input.ActorSessionID == mapStringValue(l.mission, "coordinator") {
		result["preparationHandoff"] = map[string]any{"tool": "send_message_to_thread", "params": map[string]any{"threadId": l.mission["controller"]}, "requiredInput": "Report exact controllerWorklist prepare_task IDs and missing packet/scope facts; ready=0 is not no work. Do not make controller decisions.", "coalescingRule": "Unchanged requests share persistent work wakes; report a newly observed gap once, then process other current actions"}
	}
	role := "controller"
	if input.ActorSessionID == mapStringValue(l.mission, "delivery_coordinator") {
		role = "delivery"
	} else if input.ActorSessionID == mapStringValue(l.mission, "coordinator") {
		role = "execution"
	}
	result["worklistContract"] = map[string]any{"role": role, "limit": 8, "order": []string{"callback_or_native_terminal", "complete_controller_decision_packages", "independent_ready_preparation_or_dispatch", "persist_dispositions_then_exit"}, "mustConsume": true, "unchangedRule": "Deduplicate notifications, never settle unresolved work by reading it. Existing overall authority does not expire while waiting on a different chain."}
	return nil
}
