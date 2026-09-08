package node

import (
	"context"
	"fmt"
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
	}
	entry["recordCheck"] = map[string]any{
		"action": "record_check",
		"params": map[string]any{
			"dbPath": input.DBPath, "missionId": input.MissionID, "actorSessionId": input.ActorSessionID,
			"expectedRevision": l.revision, "expectedObservationRevision": collaborationIntDefault(observation, "revision", 0), "actionId": action.ActionID,
		},
		"requiredInput": map[string]any{"outcome": outcomes, "evidenceRef": "Reference to the actual check result, not the ledger phase"},
	}
	if action.Kind != "check_execution" {
		return nil
	}
	item, err := l.item(ctx, fmt.Sprint(action.ItemID))
	if err != nil {
		return err
	}
	entry["evidenceRule"] = "get/brief/tree only read the ledger. Do not report unchanged from phase=active. Use one exact executor observation; missing, unknown or inaccessible evidence is unavailable. Terminal facts go to the controller, not unchanged."
	if mapStringValue(item, "executor") != "cloud" {
		entry["executionRef"] = item["execution_ref"]
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
