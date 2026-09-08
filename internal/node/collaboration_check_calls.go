package node

import (
	"context"
	"fmt"
	"strings"
)

func collaborationRecordCheckParamError(err error) error {
	return fmt.Errorf("%w; record_check requires dbPath, missionId, actorSessionId, expectedRevision, expectedObservationRevision, actionId, outcome, evidenceRef; optional retryAt, notified, now. Do not pass itemId or evidence. Copy next_actions.actions[].recordCheck.params, then add outcome and evidenceRef", err)
}

// Return concrete call arguments rather than requiring an AI to reconstruct
// identities and CAS fields from prose. This function never calls an executor.
func (l *collaborationLedger) addCollaborationCheckCalls(ctx context.Context, input collaborationNextActionsParams, action collaborationActionCandidate, observation, entry map[string]any) error {
	switch action.Kind {
	case "consistency_audit", "check_execution", "reconcile_dispatch", "check_validation", "notify_validation_due", "recheck_blocker":
	default:
		return nil
	}
	outcomes := []string{"unchanged", "unavailable"}
	if action.Kind == "consistency_audit" {
		outcomes = []string{"completed"}
		entry["completionRule"] = "record_check(completed) records the full audit atomically; do not call observe first. If observe(full=true) already succeeded, refresh next_actions and continue remaining actions, not the obsolete audit action."
	}
	entry["recordCheck"] = map[string]any{
		"action": "record_check",
		"params": map[string]any{
			"dbPath": input.DBPath, "missionId": input.MissionID, "actorSessionId": input.ActorSessionID,
			"expectedRevision": l.revision, "expectedObservationRevision": collaborationIntDefault(observation, "revision", 0), "actionId": action.ActionID,
		},
		"requiredInput": map[string]any{"outcome": outcomes, "evidenceRef": "Reference to the actual check result, not the ledger phase"},
	}
	if action.Kind != "check_execution" && action.Kind != "check_validation" && action.Kind != "notify_validation_due" {
		return nil
	}
	item, err := l.item(ctx, fmt.Sprint(action.ItemID))
	if err != nil {
		return err
	}
	entry["evidenceRule"] = "get/brief/tree only read the ledger. Do not report unchanged from phase=active. Use one exact executor observation; missing, unknown or inaccessible evidence is unavailable. Terminal facts go to the controller, not unchanged."
	if action.Kind == "check_validation" || action.Kind == "notify_validation_due" {
		l.addCollaborationNativeCheck(entry, mapStringValue(item, "validation_owner"))
		entry["completionRule"] = "Validation due is not an already-processed Cloud callback. Inspect the exact local validation result or notify the controller once. The controller consumes terminal evidence through apply and wakes the coordinator for newly READY work in the same turn."
		return nil
	}
	if mapStringValue(item, "executor") != "cloud" {
		l.addCollaborationNativeCheck(entry, mapStringValue(item, "execution_ref"))
		return nil
	}
	binding := collaborationOptionalMap(item["binding"])
	sessionID := mapStringValue(binding, "chatSessionId")
	if sessionID == "" {
		entry["checkUnavailable"] = "Cloud binding has no chatSessionId; report the binding conflict without inventing or scanning sessions"
		return nil
	}
	entry["executionCheck"] = map[string]any{
		"capability": "agent.control", "action": "session.get",
		"params": map[string]any{"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": sessionID, "metadataOnly": true},
	}
	entry["minimumIntervalSeconds"] = int64(1800)
	claim := mapStringValue(item, "claim")
	if claim == "" {
		return nil
	}
	dbPath, err := validateCollaborationBaseIdentity(input.DBPath, input.MissionID, input.ActorSessionID)
	if err != nil {
		return err
	}
	mission, task, generation := localCallbackIdentity(collaborationToken{DBPath: dbPath, MissionID: input.MissionID, ItemID: fmt.Sprint(action.ItemID), Claim: claim})
	if mapStringValue(binding, "collaborationId") == mission && mapStringValue(binding, "taskRef") == task {
		entry["terminalRecovery"] = map[string]any{
			"capability": "agent.control", "action": "session.callback.recover",
			"params": map[string]any{
				"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": sessionID,
				"callbackTargetSessionId": binding["callbackSessionId"], "callbackMissionId": mission,
				"callbackTaskId": task, "callbackGeneration": generation,
			},
		}
	}
	return nil
}

// Native Codex task inspection stays with the caller, never the FS provider.
// A canonical agent path is not a thread ID; only native runtime evidence can
// resolve it to the real child ID, which the owner can persist for later checks.
func (l *collaborationLedger) addCollaborationNativeCheck(entry map[string]any, ref string) {
	entry["executionRef"] = ref
	entry["terminalHandoff"] = map[string]any{
		"tool":          "send_message_to_thread",
		"params":        map[string]any{"threadId": mapStringValue(l.mission, "controller")},
		"requiredInput": "prompt containing itemId, exact execution reference, observed terminal status and evidence reference; do not infer PASS from completed",
	}
	read := func(threadID string) map[string]any {
		return map[string]any{"tool": "read_thread", "params": map[string]any{
			"threadId": threadID, "turnLimit": 1, "includeOutputs": false, "maxOutputCharsPerItem": 1800,
		}}
	}
	if id := strings.TrimPrefix(ref, "codex-thread:"); id != ref && strings.TrimSpace(id) != "" && !strings.ContainsAny(id, "# /\\\t\r\n") {
		entry["nativeExecutionCheck"] = read(id)
		return
	}
	if parentPath := strings.TrimPrefix(ref, "codex-agent:"); parentPath != ref {
		parent, path, ok := strings.Cut(parentPath, "#")
		if ok && parent != "" && strings.HasPrefix(path, "/") {
			entry["nativeBindingLookup"] = read(parent)
			entry["bindingRule"] = "Match the registered canonical path " + path + " to subAgentActivity.agentThreadId in the native parent result, then read that exact child once. Persist a verified codex-thread:<agentThreadId> through controller apply. Missing mapping is unavailable; never pass the canonical reference to read_thread or scan unrelated tasks."
			return
		}
	}
	entry["checkUnavailable"] = "No readable native execution binding; notify the controller to supply the real task ID, without scanning or treating ledger active as running"
}
