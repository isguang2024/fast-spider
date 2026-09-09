package node

import (
	"context"
	"sort"
	"strings"
)

const collaborationRefillWorklistLimit = 16

type collaborationRefillPlan struct {
	CloudFree       int64
	CloudLimited    bool
	EligibleCount   int
	DispatchItemIDs []string
	BlockedReady    []any
	HasMore         bool
}

func (l *collaborationLedger) boundedCollaborationRefillPlan(ctx context.Context, limit int) (collaborationRefillPlan, error) {
	if limit <= 0 || limit > collaborationRefillWorklistLimit {
		limit = collaborationRefillWorklistLimit
	}
	plan := collaborationRefillPlan{}
	if mapStringValue(l.mission, "status") != "active" || !mapBoolValue(l.mission, "dispatch_enabled") {
		return plan, nil
	}
	items, err := l.currentItems(ctx)
	if err != nil {
		return plan, err
	}
	capacity := collaborationOptionalMap(l.mission["capacity"])
	cloudLimit, limited := collaborationInt64(capacity["cloud"])
	plan.CloudLimited = limited
	var held int64
	for _, item := range items {
		if mapStringValue(item, "executor") == "cloud" && collaborationHoldsExecution(item) {
			held++
		}
	}
	available := int64(limit)
	if limited {
		available = cloudLimit - held
		if available < 0 {
			available = 0
		}
	}
	plan.CloudFree = available
	type candidate struct {
		id       string
		priority int64
		item     map[string]any
	}
	var candidates []candidate
	addBlocked := func(item map[string]any, kind, detail string) {
		if len(plan.BlockedReady) >= 20 {
			return
		}
		entry := map[string]any{"itemId": mapStringValue(item, "id"), "kind": kind}
		if detail != "" {
			entry["detail"] = detail
		}
		plan.BlockedReady = append(plan.BlockedReady, entry)
	}
	for _, item := range items {
		if mapStringValue(item, "phase") != "ready" || mapStringValue(item, "executor") != "cloud" {
			continue
		}
		if err := l.checkDependencies(ctx, item); err != nil {
			addBlocked(item, "dependency", err.Error())
			continue
		}
		if err := l.checkUnique(ctx, mapStringValue(item, "id"), item); err != nil {
			kind := "writer_fence"
			text := err.Error()
			if strings.Contains(text, "CHAT has") {
				kind = "same_chat"
			} else if strings.Contains(text, "write scope") {
				kind = "write_scope"
			} else if strings.Contains(text, "idempotency") || strings.Contains(text, "taskRef") {
				kind = "identity"
			}
			addBlocked(item, kind, text)
			continue
		}
		candidates = append(candidates, candidate{id: mapStringValue(item, "id"), priority: collaborationIntDefault(item, "priority", 100), item: item})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		return candidates[i].id < candidates[j].id
	})
	plan.EligibleCount = len(candidates)
	selected := make([]candidate, 0, limit)
	for _, value := range candidates {
		conflictKind, conflictDetail := "", ""
		for _, prior := range selected {
			if kind, detail := collaborationReadyPairConflict(value.item, prior.item); kind != "" {
				conflictKind, conflictDetail = kind, detail
				break
			}
		}
		if conflictKind != "" {
			addBlocked(value.item, conflictKind, conflictDetail)
			continue
		}
		if int64(len(selected)) >= available || len(selected) >= limit {
			plan.HasMore = true
			continue
		}
		selected = append(selected, value)
	}
	for _, value := range selected {
		plan.DispatchItemIDs = append(plan.DispatchItemIDs, value.id)
	}
	if len(selected) < len(candidates) {
		plan.HasMore = true
	}
	return plan, nil
}

func collaborationReadyPairConflict(a, b map[string]any) (string, string) {
	aPacket, bPacket := collaborationScopePacket(a), collaborationScopePacket(b)
	aChat, bChat := collaborationChatBinding(a, aPacket), collaborationChatBinding(b, bPacket)
	if aChat != "" && aChat == bChat {
		return "same_chat", "preselected READY work shares one CHAT; dispatch only one writer at a time"
	}
	if mapStringValue(aPacket, "machineId") == "" || mapStringValue(aPacket, "machineId") != mapStringValue(bPacket, "machineId") {
		return "", ""
	}
	for _, left := range collaborationScopeRoots(aPacket) {
		for _, right := range collaborationScopeRoots(bPacket) {
			if lexicalPathWithin(left, right) || lexicalPathWithin(right, left) {
				return "write_scope", "preselected READY write scopes overlap"
			}
		}
	}
	return "", ""
}
