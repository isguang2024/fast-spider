package node

import (
	"context"
	"fmt"
	"strings"
)

func collaborationRecordCheckParamError(err error) error {
	return fmt.Errorf("%w; record_check requires dbPath, missionId, actorSessionId, expectedRevision, expectedObservationRevision, actionId, outcome, evidenceRef; optional progressToken, retryAt, notified, now. Do not pass itemId or evidence. Copy next_actions.actions[].recordCheck.params, then add outcome and evidenceRef", err)
}

// Return concrete call arguments rather than requiring an AI to reconstruct
// identities and CAS fields from prose. This function never calls an executor.
func (l *collaborationLedger) addCollaborationCheckCalls(ctx context.Context, input collaborationNextActionsParams, action collaborationActionCandidate, observation, entry map[string]any) error {
	if action.Kind == "launch_validation" || action.Kind == "recover_validation_binding" {
		if action.ItemID == nil {
			return nil
		}
		item, err := l.item(ctx, fmt.Sprint(action.ItemID))
		if err != nil {
			return err
		}
		if action.Kind == "launch_validation" {
			entry["validationClaim"] = map[string]any{
				"action": "validation_claim",
				"params": map[string]any{
					"dbPath": input.DBPath, "missionId": input.MissionID, "actorSessionId": input.ActorSessionID,
					"expectedRevision": l.revision, "itemId": action.ItemID,
				},
				"requiredInput": map[string]any{"launchRef": "codex-agent:<this coordinator thread id>#/<canonical validator path>"},
			}
			entry["completionRule"] = "Claim one validation slot before launch. Launch exactly once, then validation_receipt binds the real codex-thread executionRef. If launch receipt is lost, recover the claimed binding; never spawn a substitute validator."
			return nil
		}
		l.addCollaborationNativeCheck(entry, mapStringValue(item, "validation_launch_ref"))
		entry["validationClaim"] = item["validation_claim"]
		entry["validationReceipt"] = map[string]any{
			"action": "validation_receipt",
			"params": map[string]any{
				"dbPath": input.DBPath, "missionId": input.MissionID, "actorSessionId": input.ActorSessionID,
				"expectedRevision": l.revision, "itemId": action.ItemID, "validationClaim": item["validation_claim"],
			},
			"requiredInput": map[string]any{"executionRef": "real codex-thread:<agentThreadId> recovered from nativeBindingLookup"},
		}
		entry["completionRule"] = "Recover only the child created for validation_launch_ref and bind it with validation_receipt. Do not record unavailable checks or relaunch while the claim is unbound."
		return nil
	}
	switch action.Kind {
	case "consistency_audit", "check_execution", "reconcile_dispatch", "check_validation", "notify_validation_due", "recheck_blocker", "decide_stalled_check":
	default:
		return nil
	}
	outcomes := []string{"unchanged", "unavailable"}
	if action.Kind == "consistency_audit" {
		outcomes = []string{"completed"}
		entry["completionRule"] = "record_check(completed) records the full audit atomically; do not call observe first. If observe(full=true) already succeeded, refresh next_actions and continue remaining actions, not the obsolete audit action."
	}
	if action.Kind != "decide_stalled_check" {
		entry["recordCheck"] = map[string]any{
			"action": "record_check",
			"params": map[string]any{
				"dbPath": input.DBPath, "missionId": input.MissionID, "actorSessionId": input.ActorSessionID,
				"expectedRevision": l.revision, "expectedObservationRevision": collaborationIntDefault(observation, "revision", 0), "actionId": action.ActionID,
			},
			"requiredInput": map[string]any{"outcome": outcomes, "evidenceRef": "Reference to the actual check result, not the ledger phase"},
		}
	}
	if action.Kind != "check_execution" && action.Kind != "check_validation" && action.Kind != "notify_validation_due" && action.Kind != "decide_stalled_check" {
		return nil
	}
	if action.ItemID == nil {
		return nil
	}
	item, err := l.item(ctx, fmt.Sprint(action.ItemID))
	if err != nil {
		return err
	}
	entry["evidenceRule"] = "get/brief/tree only read the ledger. Do not report unchanged from phase=active. Use one exact executor observation; missing, unknown or inaccessible evidence is unavailable. Terminal facts go to the controller, not unchanged."
	if action.Kind == "check_validation" || action.Kind == "notify_validation_due" {
		l.addCollaborationNativeCheck(entry, mapStringValue(item, "validation_execution_ref"))
		entry["logicalValidationOwner"] = item["validation_owner"]
		entry["completionRule"] = "Inspect only the bound local validation execution. Terminal validation evidence is applied by the controller; validation stalls never recover, continue, cancel, or otherwise touch the original Cloud writer."
		return nil
	}
	if action.Kind == "decide_stalled_check" && (action.SourceKind == "check_validation" || action.SourceKind == "notify_validation_due") {
		l.addCollaborationNativeCheck(entry, mapStringValue(item, "validation_execution_ref"))
		entry["logicalValidationOwner"] = item["validation_owner"]
		entry["recoveryRule"] = "Recover or replace only the validation execution under its validation claim. Never use the business item's Cloud binding/executor to recover a stalled validator. Controller remains the only authority that applies PASS/FAIL."
		return nil
	}
	if mapStringValue(item, "executor") != "cloud" {
		if action.Kind == "decide_stalled_check" {
			entry["recoveryRule"] = "Controller assigns one bounded recovery to the actual blocked owner or records an actionable external/user blocker. Do not turn exhausted checks into indefinite silent waiting."
			return nil
		}
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
	entry["minimumIntervalSeconds"] = int64(600)
	entry["firstCheckAfterSeconds"] = int64(1800)
	entry["evidenceRule"] = "Status=running is not progress. Use activity.fingerprint (optionally combined with verified exact tool/job progress) as progressToken with outcome=observed; Node compares it with the prior checkpoint. Unknown status may still have observable activity. No fingerprint or inaccessible evidence is unavailable. pendingRequestsKnown=false is not evidence that no tools/jobs are running. Terminal facts require callback recovery."
	if call, ok := entry["recordCheck"].(map[string]any); ok {
		call["requiredInput"] = map[string]any{"outcome": []string{"observed", "unchanged", "unavailable"}, "progressToken": "Required for observed: actual activity.fingerprint or a bounded combined executor/job progress fingerprint", "evidenceRef": "Exact observation reference, not ledger state"}
	}
	key, err := encodeCollaborationJSON([]any{"coordinator", "check_execution", action.ItemID})
	if err != nil {
		return err
	}
	checks, _ := observation["action_checks"].(map[string]any)
	if prior, ok := checks[key].(map[string]any); ok {
		entry["progressCheckpoint"] = selectCollaborationFields(prior, "progress_token", "last_progress_at", "attempts", "progress_state")
		if action.Kind == "decide_stalled_check" {
			entry["progressRecoveryRecord"] = map[string]any{
				"action": "record_check",
				"params": map[string]any{
					"dbPath": input.DBPath, "missionId": input.MissionID, "actorSessionId": mapStringValue(l.mission, "coordinator"),
					"expectedRevision": l.revision, "expectedObservationRevision": collaborationIntDefault(observation, "revision", 0),
					"actionId": prior["action_id"], "outcome": "observed",
				},
				"requiredInput": "Coordinator uses a genuinely changed progressToken and evidenceRef from the exact executor/job; unchanged evidence cannot reset the exhausted check. Refresh CAS after intervening ledger writes.",
			}
		}
	}
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
		if action.Kind == "decide_stalled_check" {
			params := cloneParams(entry["terminalRecovery"].(map[string]any)["params"].(map[string]any))
			params["idempotencyKey"] = "continue-" + action.ActionID
			params["prompt"] = "Continue the current task without restarting. Finish missing validation, save the UTF-8 report at the original DELIVERABLE_PATH, verify it is readable, then finish for callback."
			entry["continuation"] = map[string]any{"capability": "agent.control", "action": "session.callback.continue", "params": params}
			entry["automaticContinuationUsed"] = strings.HasPrefix(mapStringValue(item, "execution_ref"), "cloud-continuation:")
			entry["recoveryRule"] = "Controller must arrange one bounded diagnosis of this exact Cloud/tool execution. A frozen fingerprint is suspicion, not proof of a stopped writer. If genuinely progressing, record fresh evidence; otherwise confirm a quiescent/explicitly canceled turn and its relevant jobs before continuing in the original CHAT. Use at most one automatic continuation per business attempt; if it stalls again, assign a concrete repair/external action. Do not use nativeBindingLookup for Cloud, repeatedly cancel/continue, or release unknown writers. After continueSent=true, apply returned executionRef and nextCheckAt to this item once; preserve binding/claim/scope."
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
