package node

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

const collaborationTokenVersion = 1

var collaborationTokenStoreMu sync.Mutex

type collaborationDispatchLock struct {
	mu   sync.Mutex
	refs int
}

var collaborationDispatchLocks = struct {
	sync.Mutex
	locks map[string]*collaborationDispatchLock
}{
	locks: make(map[string]*collaborationDispatchLock),
}

// collaborationDirectReadyHook is a test seam used to make the two-readers
// race deterministic. It is nil in normal operation and carries no runtime
// coordination responsibility.
var collaborationDirectReadyHook func()

type collaborationClaimParams struct {
	DBPath           string `json:"dbPath"`
	MissionID        string `json:"missionId"`
	ActorSessionID   string `json:"actorSessionId"`
	ExpectedRevision int64  `json:"expectedRevision"`
	ItemID           string `json:"itemId"`
}

type collaborationRecoverParams struct {
	DBPath         string `json:"dbPath"`
	MissionID      string `json:"missionId"`
	ActorSessionID string `json:"actorSessionId"`
	ItemID         string `json:"itemId"`
}

type collaborationTokenParams struct {
	DispatchToken  string         `json:"dispatchToken"`
	DispatchResult map[string]any `json:"dispatchResult,omitempty"`
	EvidenceRef    string         `json:"evidenceRef,omitempty"`
	NoTaskCreated  bool           `json:"noTaskCreated,omitempty"`
}

type collaborationDispatchParams struct {
	DispatchToken    string `json:"dispatchToken"`
	DBPath           string `json:"dbPath"`
	MissionID        string `json:"missionId"`
	ActorSessionID   string `json:"actorSessionId"`
	ExpectedRevision *int64 `json:"expectedRevision,omitempty"`
	ItemID           string `json:"itemId"`
}

type collaborationToken struct {
	Version         int            `json:"version"`
	DBPath          string         `json:"dbPath,omitempty"`
	MissionID       string         `json:"missionId,omitempty"`
	ActorSessionID  string         `json:"actorSessionId,omitempty"`
	ItemID          string         `json:"itemId,omitempty"`
	Claim           string         `json:"claim,omitempty"`
	PacketSHA256    string         `json:"packetSHA256,omitempty"`
	DispatchRequest map[string]any `json:"dispatchRequest,omitempty"`
	DispatchState   map[string]any `json:"dispatchState,omitempty"`
	Completed       map[string]any `json:"completed,omitempty"`
	ExpiresAt       int64          `json:"expiresAt"`
}

type collaborationLedger struct {
	db             *sql.DB
	conn           *sql.Conn
	mission        map[string]any
	revision       int64
	actorSessionID string
	closed         bool
}

func (c *Client) collaborationControl(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	action = strings.TrimSpace(action)
	if params == nil {
		params = map[string]any{}
	}
	switch action {
	case "upgrade":
		if err := requireCollaborationParams(params, "dbPath", "missionId", "actorSessionId", "expectedRevision", "backupPath", "evidenceRef"); err != nil {
			return nil, err
		}
		var input collaborationUpgradeParams
		if err := decodeCollaborationIdentityParams(params, &input); err != nil {
			return nil, err
		}
		return c.collaborationUpgrade(ctx, input)
	case "tree", "tree_update", "archive":
		return c.collaborationTreeControl(ctx, action, params)
	case "retry":
		var input collaborationRetryParams
		if err := decodeCollaborationIdentityParams(params, &input); err != nil {
			return nil, err
		}
		return c.collaborationRetry(ctx, input)
	case "init", "brief", "get", "next_actions", "record_action", "record_check", "apply", "transfer_control", "observe", "observation", "close", "compact", "cleanup":
		return c.collaborationStateControl(ctx, action, params)
	case "inbox":
		var input collaborationInboxParams
		if err := decodeCollaborationIdentityParams(params, &input); err != nil {
			return nil, err
		}
		return c.collaborationInbox(ctx, input)
	case "resolve":
		var input collaborationResolveParams
		if err := decodeCollaborationIdentityParams(params, &input); err != nil {
			return nil, err
		}
		return c.collaborationResolve(ctx, input)
	case "decision_batch":
		var input collaborationDecisionBatchParams
		if err := decodeCollaborationIdentityParams(params, &input); err != nil {
			return nil, err
		}
		return c.collaborationDecisionBatch(ctx, input)
	case "analysis_prepare":
		var input collaborationAnalysisPrepareParams
		if err := decodeCollaborationIdentityParams(params, &input); err != nil {
			return nil, err
		}
		return c.collaborationAnalysisPrepare(ctx, input)
	case "result_recover":
		var input collaborationResultRecoverParams
		if err := decodeCollaborationIdentityParams(params, &input); err != nil {
			return nil, err
		}
		return c.collaborationResultRecover(ctx, input)
	case "validation_claim":
		if err := requireCollaborationParams(params, "dbPath", "missionId", "actorSessionId", "expectedRevision", "itemId", "launchRef"); err != nil {
			return nil, err
		}
		var input collaborationValidationClaimParams
		if err := decodeCollaborationIdentityParams(params, &input); err != nil {
			return nil, err
		}
		return c.collaborationValidationClaim(ctx, input)
	case "validation_receipt":
		if err := requireCollaborationParams(params, "dbPath", "missionId", "actorSessionId", "expectedRevision", "itemId", "validationClaim", "executionRef"); err != nil {
			return nil, err
		}
		var input collaborationValidationReceiptParams
		if err := decodeCollaborationIdentityParams(params, &input); err != nil {
			return nil, err
		}
		return c.collaborationValidationReceipt(ctx, input)
	case "claim":
		if err := requireCollaborationParams(params, "dbPath", "missionId", "actorSessionId", "expectedRevision", "itemId"); err != nil {
			return nil, err
		}
		var input collaborationClaimParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid collaboration control params: %w", err)
		}
		return c.collaborationClaim(ctx, input)
	case "recover":
		if err := requireCollaborationParams(params, "dbPath", "missionId", "actorSessionId", "itemId"); err != nil {
			return nil, err
		}
		var input collaborationRecoverParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid collaboration control params: %w", err)
		}
		return c.collaborationRecover(ctx, input, false)
	case "receipt", "uncertain", "not_created", "verify":
		required := []string{"dispatchToken"}
		if action == "receipt" {
			required = append(required, "dispatchResult")
		}
		if action == "uncertain" || action == "not_created" {
			required = append(required, "evidenceRef")
		}
		if action == "not_created" {
			required = append(required, "noTaskCreated")
		}
		if err := requireCollaborationParams(params, required...); err != nil {
			return nil, err
		}
		var input collaborationTokenParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid collaboration control params: %w", err)
		}
		return c.collaborationUseToken(ctx, action, input)
	case "dispatch", "dispatch_recover":
		var input collaborationDispatchParams
		if err := decodeParams(params, &input); err != nil {
			return nil, fmt.Errorf("invalid collaboration control params: %w", err)
		}
		return c.collaborationDispatch(ctx, action, input)
	case "callback_claim", "callback_ack":
		return c.collaborationCallback(ctx, action, params)
	default:
		return nil, fmt.Errorf("unsupported collaboration control action %q", action)
	}
}

// collaborationDispatch performs the local equivalent of the Hub dispatch
// sequence. The ledger token is the authority for idempotency; provider calls
// are made with the frozen packet and are never retried with a new key.
func (c *Client) collaborationDispatch(ctx context.Context, action string, input collaborationDispatchParams) (map[string]any, error) {
	dispatchToken := strings.TrimSpace(input.DispatchToken)
	identityCount := 0
	for _, value := range []string{input.DBPath, input.MissionID, input.ActorSessionID, input.ItemID} {
		if strings.TrimSpace(value) != "" {
			identityCount++
		}
	}
	if dispatchToken != "" && identityCount != 0 {
		return nil, errors.New("dispatchToken cannot be combined with ledger identity")
	}
	if action == "dispatch_recover" && dispatchToken != "" {
		return nil, errors.New("dispatch_recover requires dbPath, missionId, actorSessionId, and itemId")
	}
	if dispatchToken == "" {
		if identityCount != 4 {
			return nil, errors.New("dbPath, missionId, actorSessionId, and itemId are required when dispatchToken is omitted")
		}
		if action == "dispatch" {
			resolvedDBPath, err := validateCollaborationIdentity(input.DBPath, input.MissionID, input.ActorSessionID, input.ItemID)
			if err != nil {
				return nil, err
			}
			ledger, err := openCollaborationLedger(ctx, resolvedDBPath, input.MissionID, input.ActorSessionID, false)
			if err != nil {
				return nil, err
			}
			item, itemErr := ledger.item(ctx, input.ItemID)
			if itemErr != nil {
				ledger.rollback()
				return nil, itemErr
			}
			phase := mapStringValue(item, "phase")
			switch phase {
			case "ready":
				revision := ledger.revision
				if input.ExpectedRevision != nil {
					revision = *input.ExpectedRevision
				}
				if collaborationDirectReadyHook != nil {
					collaborationDirectReadyHook()
				}
				if err := ledger.commit(ctx); err != nil {
					return nil, err
				}
				claimed, claimErr := c.collaborationClaim(ctx, collaborationClaimParams{DBPath: input.DBPath, MissionID: input.MissionID, ActorSessionID: input.ActorSessionID, ExpectedRevision: revision, ItemID: input.ItemID})
				if claimErr != nil {
					recoveredToken, reused, recoverErr := c.collaborationReuseIdentityToken(ctx, input, resolvedDBPath)
					if recoverErr != nil {
						return nil, recoverErr
					}
					if reused {
						dispatchToken = recoveredToken
					} else {
						return nil, claimErr
					}
				}
				if dispatchToken == "" {
					dispatchToken = mapStringValue(claimed, "dispatchToken")
				}
			case "dispatching", "in_doubt", "active":
				if err := ledger.commit(ctx); err != nil {
					return nil, err
				}
				dispatchToken, _, err = c.collaborationReuseIdentityToken(ctx, input, resolvedDBPath)
				if err != nil {
					return nil, err
				}
			default:
				ledger.rollback()
				return nil, fmt.Errorf("item phase %q cannot be retried by identity", phase)
			}
		} else {
			recovered, err := c.collaborationRecover(ctx, collaborationRecoverParams{DBPath: input.DBPath, MissionID: input.MissionID, ActorSessionID: input.ActorSessionID, ItemID: input.ItemID}, true)
			if err != nil {
				return nil, err
			}
			dispatchToken = mapStringValue(recovered, "dispatchToken")
		}
		if dispatchToken == "" {
			return nil, errors.New("local dispatch did not produce a dispatchToken")
		}
	}
	return c.collaborationDispatchToken(ctx, dispatchToken)
}

func (c *Client) collaborationReuseIdentityToken(ctx context.Context, input collaborationDispatchParams, resolvedDBPath string) (string, bool, error) {
	ledger, err := openCollaborationLedger(ctx, resolvedDBPath, input.MissionID, input.ActorSessionID, false)
	if err != nil {
		return "", false, err
	}
	item, err := ledger.item(ctx, input.ItemID)
	if err != nil {
		ledger.rollback()
		return "", false, err
	}
	phase := mapStringValue(item, "phase")
	if phase == "ready" {
		ledger.rollback()
		return "", false, nil
	}
	if phase != "dispatching" && phase != "in_doubt" && phase != "active" {
		ledger.rollback()
		return "", true, fmt.Errorf("item phase %q cannot be retried by identity", phase)
	}
	claim := mapStringValue(item, "claim")
	packet, packetOK := item["packet"].(map[string]any)
	if claim == "" || !packetOK {
		ledger.rollback()
		return "", true, errors.New("existing dispatch phase has no recoverable claim or packet")
	}
	if err := validateCollaborationPacket(packet, ledger.mission, false); err != nil {
		ledger.rollback()
		return "", true, err
	}
	revision := ledger.revision
	if err := ledger.commit(ctx); err != nil {
		return "", true, err
	}
	token, err := c.reuseOrRebuildCollaborationToken(resolvedDBPath, input.MissionID, input.ActorSessionID, input.ItemID, claim, packet, revision)
	return token, true, err
}

func acquireCollaborationDispatchLock(tokenID string) func() {
	collaborationDispatchLocks.Lock()
	lock := collaborationDispatchLocks.locks[tokenID]
	if lock == nil {
		lock = &collaborationDispatchLock{}
		collaborationDispatchLocks.locks[tokenID] = lock
	}
	lock.refs++
	collaborationDispatchLocks.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		collaborationDispatchLocks.Lock()
		lock.refs--
		if lock.refs == 0 && collaborationDispatchLocks.locks[tokenID] == lock {
			delete(collaborationDispatchLocks.locks, tokenID)
		}
		collaborationDispatchLocks.Unlock()
	}
}

func validateCollaborationTokenRecord(tokenID string, token collaborationToken) (map[string]any, error) {
	if token.DBPath == "" || token.MissionID == "" || token.ActorSessionID == "" || token.ItemID == "" || token.Claim == "" || token.PacketSHA256 == "" {
		return nil, errors.New("dispatchToken record is incomplete")
	}
	if collaborationTokenID(token.DBPath, token.MissionID, token.ItemID, token.Claim) != tokenID {
		return nil, errors.New("dispatchToken identity does not match its token id")
	}
	if token.DispatchRequest == nil || mapStringValue(token.DispatchRequest, "action") != "dispatch" {
		return nil, errors.New("dispatchToken record has an invalid dispatchRequest")
	}
	packet, ok := token.DispatchRequest["params"].(map[string]any)
	if !ok || packet == nil {
		return nil, errors.New("dispatchToken record has an invalid dispatchRequest")
	}
	packetBytes, err := json.Marshal(packet)
	if err != nil {
		return nil, fmt.Errorf("dispatchToken packet is invalid: %w", err)
	}
	digest := "sha256:" + hex.EncodeToString(collaborationHash(packetBytes))
	if token.PacketSHA256 != digest {
		return nil, errors.New("dispatchToken packet digest does not match dispatchRequest")
	}
	return packet, nil
}

func (c *Client) collaborationDispatchToken(ctx context.Context, dispatchToken string) (map[string]any, error) {
	release := acquireCollaborationDispatchLock(dispatchToken)
	defer release()

	token, err := c.readCollaborationToken(dispatchToken)
	if err != nil {
		return nil, err
	}
	if c.projectPolicy != nil && c.projectPolicy.root != "" {
		if err := c.projectPolicy.validate("collaboration.control", "dispatch", map[string]any{"dbPath": token.DBPath}); err != nil {
			return nil, err
		}
	}
	packet, err := validateCollaborationTokenRecord(dispatchToken, token)
	if err != nil {
		return nil, err
	}
	if token.Completed != nil {
		return token.Completed, nil
	}
	runtimePacket, err := c.localCollaborationRuntimePacket(packet)
	if err != nil {
		return nil, err
	}
	if completed, active, err := c.recoverActiveCollaborationReceipt(ctx, token); err != nil {
		return nil, err
	} else if active {
		return c.finishCollaborationToken(dispatchToken, token, completed)
	}
	if token.ExpiresAt <= time.Now().Unix() {
		return nil, errors.New("dispatch token expired; use recover with the exact ledger item")
	}
	if err := validateCollaborationDispatchAuthority(ctx, token); err != nil {
		return nil, err
	}
	if c.agent == nil {
		return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:agent-provider-unavailable")
	}
	callbackTarget := mapStringValue(runtimePacket, "callbackSessionId")
	if callbackTarget == "" {
		return nil, errors.New("callbackSessionId is required for local dispatch")
	}
	missionID, taskID, generation := localCallbackIdentity(token)
	state := token.DispatchState
	phase := mapStringValue(state, "phase")
	if phase != "" && phase != "prepared" && phase != "created" && phase != "registered" && phase != "sent" && phase != "send_in_doubt" && phase != "armed" {
		return nil, fmt.Errorf("unsupported local dispatch recovery phase %q", phase)
	}
	sourceSessionID := mapStringValue(state, "chatSessionId")
	if sourceSessionID == "" {
		sourceSessionID = mapStringValue(runtimePacket, "targetSessionId")
	}
	stateFromToken := mapStringValue(state, "chatSessionId") != ""
	sessionMode := mapStringValue(state, "sessionMode")
	reuseSession := sessionMode == "reuse" || sessionMode == "" && mapStringValue(runtimePacket, "targetSessionId") != ""
	if state != nil && sessionMode == "new" && sourceSessionID == "" {
		return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:session-create-missing-session-id")
	}
	baselineMode := ""
	if reuseSession {
		baselineMode = "reuse"
	}
	// Validate the local Codex callback owner before any Cloud create/send.
	// This is a local target check and cannot create a task by itself.
	if _, err := c.agent.Control(ctx, "session.callback.prepare", map[string]any{
		"providerId": "codex", "sessionId": callbackTarget, "mode": "target",
	}); err != nil {
		if stateFromToken {
			return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:callback-target:"+stableCollaborationDigest(err.Error()))
		}
		return c.recordCollaborationNotCreated(ctx, dispatchToken, token, "local:callback-target:"+stableCollaborationDigest(err.Error()))
	}
	if reuseSession && sourceSessionID != "" && phase == "" {
		if err := c.ensureLocalCloudReadiness(ctx); err != nil {
			if stateFromToken {
				return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:provider-readiness:"+stableCollaborationDigest(err.Error()))
			}
			return c.recordCollaborationNotCreated(ctx, dispatchToken, token, "local:provider-readiness:"+stableCollaborationDigest(err.Error()))
		}
	}
	if reuseSession && sourceSessionID != "" && phase == "" {
		if _, err := c.agent.Control(ctx, "session.callback.prepare", map[string]any{
			"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": sourceSessionID,
		}); err != nil {
			if stateFromToken {
				return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:callback-prepare:"+stableCollaborationDigest(err.Error()))
			}
			return c.finishLocalDispatchError(ctx, dispatchToken, token, err, "local:callback-prepare")
		}
		if token.DispatchState == nil {
			token.DispatchState = map[string]any{"providerAction": "session.send", "sessionMode": "reuse", "chatSessionId": sourceSessionID, "callbackTargetSessionId": callbackTarget, "callbackMissionId": missionID, "callbackTaskId": taskID, "callbackGeneration": generation, "deliverablePath": localCallbackDeliverablePath(runtimePacket, token), "phase": "prepared"}
			if err := c.persistLocalDispatchState(dispatchToken, token, token.DispatchState); err != nil {
				return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:dispatch-state:"+stableCollaborationDigest(err.Error()))
			}
			phase = "prepared"
		}
	} else if !stateFromToken {
		if err := c.ensureLocalCloudReadiness(ctx); err != nil {
			return c.recordCollaborationNotCreated(ctx, dispatchToken, token, "local:provider-readiness:"+stableCollaborationDigest(err.Error()))
		}
		createParams := map[string]any{
			"providerId": "codex", "backend": "chatgpt_cloud", "visibility": "visible", "mode": "quick_chat",
			"workingDirectory": mapStringValue(runtimePacket, "workingDirectory"), "prompt": localCollaborationBootstrap(runtimePacket, token),
			"idempotencyKey": mapStringValue(runtimePacket, "idempotencyKey"),
		}
		applyCollaborationModelParams(runtimePacket, createParams)
		result, callErr := c.agent.Control(ctx, "session.create", createParams)
		if callErr != nil {
			return c.finishLocalDispatchError(ctx, dispatchToken, token, callErr, "local:session-create")
		}
		sourceSessionID = mapStringValue(result, "sessionId")
		if sourceSessionID == "" {
			return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:session-create-missing-session-id")
		}
		if err := c.persistLocalDispatchState(dispatchToken, token, map[string]any{"providerAction": "session.create", "sessionMode": "new", "chatSessionId": sourceSessionID, "callbackTargetSessionId": callbackTarget, "callbackMissionId": missionID, "callbackTaskId": taskID, "callbackGeneration": generation, "deliverablePath": localCallbackDeliverablePath(runtimePacket, token), "phase": "created"}); err != nil {
			return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:dispatch-state:"+stableCollaborationDigest(err.Error()))
		}
		token.DispatchState = map[string]any{"providerAction": "session.create", "sessionMode": "new", "chatSessionId": sourceSessionID, "callbackTargetSessionId": callbackTarget, "callbackMissionId": missionID, "callbackTaskId": taskID, "callbackGeneration": generation, "deliverablePath": localCallbackDeliverablePath(runtimePacket, token), "phase": "created"}
		phase = "created"
	}

	registerParams := map[string]any{
		"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": sourceSessionID,
		"callbackTargetSessionId": callbackTarget, "callbackMissionId": missionID, "callbackTaskId": taskID,
		"callbackGeneration": generation, "callbackType": mapStringValue(runtimePacket, "callbackType"),
		"callbackDeliverablePath": localCallbackDeliverablePath(runtimePacket, token), "callbackImmediateWake": true,
		"callbackArmRequired": true, "callbackClaimTransport": "local", "mode": baselineMode,
	}
	registerNeeded := phase == "" || phase == "prepared" || phase == "created"
	if registerNeeded {
		ledger, err := openCollaborationLedger(ctx, token.DBPath, token.MissionID, token.ActorSessionID, false)
		if err != nil {
			return nil, err
		}
		if ledger.hasInbox(ctx) {
			registerParams["callbackInboxRoute"] = map[string]any{"dbPath": token.DBPath, "missionId": token.MissionID, "itemId": token.ItemID, "claim": token.Claim}
		}
		ledger.rollback()
		if _, err := c.agent.Control(ctx, "session.callback.register", registerParams); err != nil {
			// Register may have committed before a transport or persistence error.
			// Keep the prepared/created identity and recover the exact route rather
			// than declaring the round absent or sending a replacement.
			return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:callback-register:"+stableCollaborationDigest(err.Error()))
		}
		if token.DispatchState != nil {
			token.DispatchState["phase"] = "registered"
			if err := c.persistLocalDispatchState(dispatchToken, token, token.DispatchState); err != nil {
				return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:dispatch-state:"+stableCollaborationDigest(err.Error()))
			}
			phase = "registered"
		}
	}
	if reuseSession && phase == "registered" && mapStringValue(token.DispatchState, "sendOutcome") == "rejected" {
		evidence := mapStringValue(token.DispatchState, "sendEvidence")
		if evidence == "" {
			evidence = "local:session-send-rejected"
		}
		if _, err := c.agent.Control(ctx, "session.callback.unregister", map[string]any{
			"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": sourceSessionID,
			"callbackTargetSessionId": callbackTarget, "callbackMissionId": missionID, "callbackTaskId": taskID,
			"callbackGeneration": generation,
		}); err != nil {
			return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:session-send-unregister:"+stableCollaborationDigest(err.Error()))
		}
		return c.recordCollaborationNotCreated(ctx, dispatchToken, token, evidence)
	}
	previousSendUncertain := phase == "send_in_doubt" || phase == "armed" && mapStringValue(token.DispatchState, "sendOutcome") == "uncertain"
	sendNeeded := reuseSession && (phase == "registered" || previousSendUncertain)
	sendUncertain := false
	if sendNeeded {
		if err := c.ensureLocalCloudReadiness(ctx); err != nil {
			return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:provider-readiness:"+stableCollaborationDigest(err.Error()))
		}
		sendParams := map[string]any{
			"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": sourceSessionID,
			"mode": "quick_chat", "prompt": localCollaborationBootstrap(runtimePacket, token), "idempotencyKey": mapStringValue(runtimePacket, "idempotencyKey"),
		}
		applyCollaborationModelParams(runtimePacket, sendParams)
		if _, err := c.agent.Control(ctx, "session.send", sendParams); err != nil {
			if !dispatchErrorRetryable(err) && !previousSendUncertain {
				evidence := "local:session-send:" + stableCollaborationDigest(err.Error())
				if unregisterErr := func() error {
					_, unregisterErr := c.agent.Control(ctx, "session.callback.unregister", map[string]any{
						"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": sourceSessionID,
						"callbackTargetSessionId": callbackTarget, "callbackMissionId": missionID, "callbackTaskId": taskID,
						"callbackGeneration": generation,
					})
					return unregisterErr
				}(); unregisterErr == nil {
					return c.recordCollaborationNotCreated(ctx, dispatchToken, token, evidence)
				}
				if token.DispatchState != nil {
					token.DispatchState["sendOutcome"] = "rejected"
					token.DispatchState["sendEvidence"] = evidence
					if persistErr := c.persistLocalDispatchState(dispatchToken, token, token.DispatchState); persistErr != nil {
						return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:dispatch-state:"+stableCollaborationDigest(persistErr.Error()))
					}
				}
				return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:session-send-unregister:"+stableCollaborationDigest(err.Error()))
			}
			sendUncertain = true
			if token.DispatchState != nil {
				token.DispatchState["sendOutcome"] = "uncertain"
				if phase != "armed" {
					token.DispatchState["phase"] = "send_in_doubt"
					phase = "send_in_doubt"
				}
				if persistErr := c.persistLocalDispatchState(dispatchToken, token, token.DispatchState); persistErr != nil {
					return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:dispatch-state:"+stableCollaborationDigest(persistErr.Error()))
				}
			}
		} else if token.DispatchState != nil {
			if phase != "armed" {
				token.DispatchState["phase"] = "sent"
				phase = "sent"
			}
			token.DispatchState["sendOutcome"] = "sent"
			if err := c.persistLocalDispatchState(dispatchToken, token, token.DispatchState); err != nil {
				return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:dispatch-state:"+stableCollaborationDigest(err.Error()))
			}
		}
	}
	armNeeded := phase != "armed"
	if armNeeded {
		if _, err := c.agent.Control(ctx, "session.callback.arm", map[string]any{
			"providerId": "codex", "backend": "chatgpt_cloud", "sessionId": sourceSessionID,
			"callbackTargetSessionId": callbackTarget, "callbackMissionId": missionID, "callbackTaskId": taskID,
			"callbackGeneration": generation, "callbackClaimTransport": "local",
		}); err != nil {
			return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:callback-arm:"+stableCollaborationDigest(err.Error()))
		}
		if token.DispatchState != nil {
			token.DispatchState["phase"] = "armed"
			phase = "armed"
			if sendUncertain {
				token.DispatchState["sendOutcome"] = "uncertain"
			}
			if err := c.persistLocalDispatchState(dispatchToken, token, token.DispatchState); err != nil {
				return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:dispatch-state:"+stableCollaborationDigest(err.Error()))
			}
		}
	}
	if mapStringValue(token.DispatchState, "sendOutcome") == "uncertain" {
		return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, "local:session-send-uncertain")
	}
	binding := map[string]any{"chatSessionId": sourceSessionID, "collaborationId": missionID, "taskRef": taskID, "callbackSessionId": callbackTarget, "idempotencyKey": mapStringValue(runtimePacket, "idempotencyKey")}
	active, err := c.recordCollaborationActive(ctx, token, binding)
	if err != nil {
		return nil, err
	}
	continueRefill := c.collaborationShouldContinueRefill(ctx, token)
	completed := localCollaborationActiveReceipt(token, binding, active["revision"], false, continueRefill)
	return c.finishCollaborationToken(dispatchToken, token, completed)
}

func (c *Client) recoverActiveCollaborationReceipt(ctx context.Context, token collaborationToken) (map[string]any, bool, error) {
	ledger, err := openCollaborationLedger(ctx, token.DBPath, token.MissionID, token.ActorSessionID, false)
	if err != nil {
		return nil, false, err
	}
	defer ledger.rollback()
	if mapStringValue(ledger.mission, "coordinator") != token.ActorSessionID {
		return nil, false, errors.New("only bound coordinator recovers an active dispatch receipt")
	}
	item, err := ledger.item(ctx, token.ItemID)
	if err != nil {
		return nil, false, err
	}
	if mapStringValue(item, "claim") != token.Claim {
		return nil, false, errors.New("wrong dispatch claim")
	}
	if mapStringValue(item, "phase") != "active" {
		if err := ledger.commit(ctx); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	packet, ok := item["packet"].(map[string]any)
	if !ok {
		return nil, false, errors.New("active ledger item has no frozen packet")
	}
	packetBytes, err := json.Marshal(packet)
	if err != nil {
		return nil, false, err
	}
	digest := "sha256:" + hex.EncodeToString(collaborationHash(packetBytes))
	if digest != token.PacketSHA256 {
		return nil, false, errors.New("active ledger packet differs from dispatch token")
	}
	binding, ok := item["binding"].(map[string]any)
	if !ok || binding == nil {
		return nil, false, errors.New("active ledger item has no dispatch binding")
	}
	if err := validateCollaborationBinding(binding, item, ledger.mission); err != nil {
		return nil, false, err
	}
	plan, err := ledger.boundedCollaborationRefillPlan(ctx, collaborationRefillWorklistLimit)
	if err != nil {
		return nil, false, err
	}
	continueRefill := len(plan.DispatchItemIDs) > 0
	if err := ledger.commit(ctx); err != nil {
		return nil, false, err
	}
	return localCollaborationActiveReceipt(token, binding, ledger.revision, true, continueRefill), true, nil
}

func (c *Client) collaborationShouldContinueRefill(ctx context.Context, token collaborationToken) bool {
	ledger, err := openCollaborationLedger(ctx, token.DBPath, token.MissionID, token.ActorSessionID, false)
	if err != nil {
		return false
	}
	defer ledger.rollback()
	if mapStringValue(ledger.mission, "coordinator") != token.ActorSessionID {
		return false
	}
	plan, err := ledger.boundedCollaborationRefillPlan(ctx, collaborationRefillWorklistLimit)
	if err != nil {
		return false
	}
	_ = ledger.commit(ctx)
	return len(plan.DispatchItemIDs) > 0
}

func localCollaborationActiveReceipt(token collaborationToken, binding map[string]any, revision any, replayed, continueRefill bool) map[string]any {
	completed := map[string]any{
		"itemId": token.ItemID, "binding": binding, "packetSHA256": token.PacketSHA256,
		"ledgerRevision": revision, "phase": "active", "activePollingAllowed": false,
		"callerShouldYield": !continueRefill, "refillRecommended": continueRefill,
		"nextAction": "End the current turn and await the formal local callback.",
	}
	if continueRefill {
		completed["nextAction"] = "Continue this coordinator wake with the bounded pagination-safe refillInputs from collaboration.control next_actions; dispatch only preselected independent READY work. Do not poll or wait on the dispatched CHAT."
	}
	if replayed {
		completed["replayed"] = true
	}
	return completed
}

func validateCollaborationDispatchAuthority(ctx context.Context, token collaborationToken) error {
	ledger, err := openCollaborationLedger(ctx, token.DBPath, token.MissionID, token.ActorSessionID, false)
	if err != nil {
		return err
	}
	defer ledger.rollback()
	if mapStringValue(ledger.mission, "coordinator") != token.ActorSessionID {
		return errors.New("only bound coordinator dispatches a claimed round")
	}
	if mapStringValue(ledger.mission, "status") != "active" || !mapBoolValue(ledger.mission, "dispatch_enabled") {
		return errors.New("dispatch is paused/disabled")
	}
	item, err := ledger.item(ctx, token.ItemID)
	if err != nil {
		return err
	}
	if mapStringValue(item, "claim") != token.Claim {
		return errors.New("wrong dispatch claim")
	}
	phase := mapStringValue(item, "phase")
	if phase != "dispatching" && phase != "in_doubt" {
		return fmt.Errorf("item phase %q cannot dispatch or recover", phase)
	}
	if err := ledger.checkAnalysisAuthority(ctx, item); err != nil {
		return err
	}
	return ledger.commit(ctx)
}

func (c *Client) collaborationCallback(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	if c.agent == nil {
		return nil, ErrAgentProviderUnavailable
	}
	params = cloneParams(params)
	params["callbackClaimTransport"] = "local"
	if action == "callback_claim" {
		return c.agent.Control(ctx, "session.callback.claim", params)
	}
	return c.agent.Control(ctx, "session.callback.ack", params)
}

func (c *Client) persistLocalDispatchState(dispatchToken string, token collaborationToken, state map[string]any) error {
	token.DispatchState = state
	return c.writeCollaborationToken(dispatchToken, token)
}

func (c *Client) finishLocalDispatchError(ctx context.Context, dispatchToken string, token collaborationToken, err error, evidencePrefix string) (map[string]any, error) {
	if dispatchErrorRetryable(err) {
		return c.finishLocalDispatchUncertain(ctx, dispatchToken, token, evidencePrefix+":"+stableCollaborationDigest(err.Error()))
	}
	return c.recordCollaborationNotCreated(ctx, dispatchToken, token, evidencePrefix+":"+stableCollaborationDigest(err.Error()))
}

func (c *Client) finishLocalDispatchUncertain(ctx context.Context, dispatchToken string, token collaborationToken, evidence string) (map[string]any, error) {
	return c.recordCollaborationUncertain(ctx, dispatchToken, token, evidence)
}

func dispatchErrorRetryable(err error) bool {
	if err == nil {
		return false
	}
	var providerErr AgentCapabilityError
	if errors.As(err, &providerErr) {
		code, _, retryable := providerErr.CapabilityError()
		return retryable || code == "AGENT_CREATE_IN_DOUBT"
	}
	// Untyped provider/transport failures do not prove that an external create
	// or send was rejected. Keep the original key in doubt until reconciliation.
	return true
}

func localCallbackIdentity(token collaborationToken) (string, string, int64) {
	mission := "local-collaboration-" + stableCollaborationDigest(token.DBPath + "|" + token.MissionID)[:24]
	task := "local-task-" + stableCollaborationDigest(token.ItemID + "|" + token.Claim)[:24]
	return mission, task, 1
}

func localCallbackDeliverablePath(packet map[string]any, token collaborationToken) string {
	if path := mapStringValue(packet, "deliverablePath"); path != "" {
		return path
	}
	if mapStringValue(packet, "callbackType") != "local_file" {
		return ""
	}
	return filepath.Join(mapStringValue(packet, "workingDirectory"), ".fast-spider-result-"+stableCollaborationDigest(token.ItemID + "|" + token.Claim)[:24]+".md")
}

func (c *Client) localCollaborationRuntimePacket(packet map[string]any) (map[string]any, error) {
	runtimePacket := cloneParams(packet)
	if c.statePath != "" {
		state, err := c.State()
		if err == nil {
			if mapStringValue(packet, "machineId") != state.MachineID {
				return nil, errors.New("frozen machineId does not match this Node")
			}
		} else if !errors.Is(err, ErrNotRegistered) {
			return nil, fmt.Errorf("load local Node identity: %w", err)
		}
	}
	workingDirectory, err := resolveLocalCollaborationPath("", mapStringValue(packet, "workingDirectory"), false)
	if err != nil {
		return nil, fmt.Errorf("invalid collaboration workingDirectory: %w", err)
	}
	info, err := os.Stat(workingDirectory)
	if err != nil || !info.IsDir() {
		return nil, errors.New("collaboration workingDirectory must be an existing directory")
	}
	if c.projectPolicy != nil && c.projectPolicy.root != "" && !pathWithin(c.projectPolicy.root, workingDirectory) {
		return nil, fmt.Errorf("%w: workingDirectory", ErrProjectPathForbidden)
	}
	runtimePacket["workingDirectory"] = workingDirectory

	var writeBoundary string
	if mapStringValue(packet, "accessMode") == "write" {
		writeScope := mapStringValue(packet, "writeScope")
		writeBoundary, err = resolveLocalCollaborationScopeBoundary(workingDirectory, writeScope)
		if err != nil {
			return nil, err
		}
		if !lexicalPathWithin(workingDirectory, writeBoundary) {
			return nil, errors.New("writeScope must stay inside workingDirectory")
		}
		if c.projectPolicy != nil && c.projectPolicy.root != "" && !lexicalPathWithin(c.projectPolicy.root, writeBoundary) {
			return nil, fmt.Errorf("%w: writeScope", ErrProjectPathForbidden)
		}
	}

	deliverable := mapStringValue(packet, "deliverablePath")
	if deliverable == "" {
		if mapStringValue(packet, "callbackType") == "local_file" {
			base := workingDirectory
			if writeBoundary != "" {
				base = writeBoundary
			}
			runtimePacket["deliverablePath"] = filepath.Join(base, ".fast-spider-result-"+stableCollaborationDigest(mapStringValue(packet, "idempotencyKey"))+".md")
		}
		return runtimePacket, nil
	}
	if !filepath.IsAbs(deliverable) {
		return nil, errors.New("deliverablePath must be absolute")
	}
	if mapStringValue(packet, "callbackType") != "local_file" {
		return nil, errors.New("deliverablePath requires callbackType=local_file")
	}
	if mapStringValue(packet, "accessMode") != "write" {
		return nil, errors.New("read_only collaboration must use the Node-assigned result path")
	}
	resolvedDeliverable, err := resolveLocalCollaborationPath(workingDirectory, deliverable, true)
	if err != nil {
		return nil, fmt.Errorf("invalid deliverablePath: %w", err)
	}
	if !lexicalPathWithin(workingDirectory, resolvedDeliverable) || !lexicalPathWithin(writeBoundary, resolvedDeliverable) {
		return nil, errors.New("deliverablePath must stay inside workingDirectory and writeScope")
	}
	if c.projectPolicy != nil && c.projectPolicy.root != "" && !lexicalPathWithin(c.projectPolicy.root, resolvedDeliverable) {
		return nil, fmt.Errorf("%w: deliverablePath", ErrProjectPathForbidden)
	}
	runtimePacket["deliverablePath"] = resolvedDeliverable
	return runtimePacket, nil
}

func resolveLocalCollaborationScopeBoundary(workingDirectory, writeScope string) (string, error) {
	writeScope = strings.TrimSpace(writeScope)
	if writeScope == "" {
		return "", errors.New("writeScope is required for write collaboration")
	}
	prefix := writeScope
	if index := strings.IndexAny(prefix, "*?["); index >= 0 {
		prefix = prefix[:index]
		if prefix != "" && !strings.HasSuffix(prefix, "/") && !strings.HasSuffix(prefix, "\\") {
			prefix = filepath.Dir(prefix)
		}
	}
	if strings.TrimSpace(prefix) == "" || prefix == "." {
		prefix = workingDirectory
	}
	return resolveLocalCollaborationPath(workingDirectory, prefix, true)
}

func resolveLocalCollaborationPath(base, value string, allowCreate bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("path is empty or invalid")
	}
	if !filepath.IsAbs(value) {
		if base == "" {
			return "", errors.New("path must be absolute")
		}
		value = filepath.Join(base, value)
	}
	value = filepath.Clean(value)
	if resolved, err := ResolveMachinePath(value); err == nil {
		return resolved, nil
	} else if !allowCreate {
		return "", err
	}

	ancestor := filepath.Dir(value)
	for {
		if resolvedAncestor, err := ResolveMachinePath(ancestor); err == nil {
			remainder, relErr := filepath.Rel(ancestor, value)
			if relErr != nil || remainder == ".." || strings.HasPrefix(remainder, ".."+string(filepath.Separator)) || filepath.IsAbs(remainder) {
				return "", errors.New("path escapes its existing parent")
			}
			return filepath.Clean(filepath.Join(resolvedAncestor, remainder)), nil
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", errors.New("path has no resolvable parent")
		}
		ancestor = parent
	}
}

func (c *Client) ensureLocalCloudReadiness(ctx context.Context) error {
	result, err := c.agent.Control(ctx, "provider.readiness", map[string]any{"providerId": "codex", "backend": "chatgpt_cloud", "mode": "safe"})
	if err != nil {
		return err
	}
	if ready, ok := result["readyForSessionCreate"].(bool); ok && !ready {
		return fmt.Errorf("Cloud provider is not ready: %s", mapStringValue(result, "reasonCode"))
	}
	if ready, ok := result["ready"].(bool); ok && !ready {
		return fmt.Errorf("Cloud provider is not ready: %s", mapStringValue(result, "reasonCode"))
	}
	return nil
}

func localCollaborationBootstrap(packet map[string]any, token collaborationToken) string {
	deliverable := localCallbackDeliverablePath(packet, token)
	writeScope := mapStringValue(packet, "writeScope")
	if writeScope == "" {
		writeScope = "(read-only)"
	}
	callbackType := mapStringValue(packet, "callbackType")
	deliveryRule := ""
	if callbackType == "local_file" {
		deliveryRule = " Before ending this turn, save the final report as a UTF-8 file at the exact DELIVERABLE_PATH above and verify that it exists and is readable. The Node delivers the callback but does not write this report for you. A final chat response alone is not a local_file deliverable. If saving fails, report the exact failure; do not claim successful delivery."
		if mapStringValue(packet, "accessMode") == "read_only" {
			deliveryRule += " In read_only mode, writing only this Node-assigned report file is permitted; business source and all other files remain read-only."
		}
	}
	return fmt.Sprintf("FAST_SPIDER_LOCAL_COLLABORATION_V1\nMACHINE_ID: %s\nWORKING_DIRECTORY: %s\nACCESS_MODE: %s\nWRITE_SCOPE: %s\nCALLBACK_TYPE: %s\nDELIVERABLE_PATH: %s\n\nTASK:\n%s\n\nComplete only this round within the frozen scope. The Node will register and deliver the callback automatically. Do not call remote task_result_submit or create another CHAT. In your final response report completed work, blockers, and validation evidence.%s",
		mapStringValue(packet, "machineId"), mapStringValue(packet, "workingDirectory"), mapStringValue(packet, "accessMode"), writeScope, callbackType, deliverable, mapStringValue(packet, "prompt"), deliveryRule)
}

func stableCollaborationDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (c *Client) collaborationClaim(ctx context.Context, input collaborationClaimParams) (map[string]any, error) {
	if input.ExpectedRevision < 0 {
		return nil, errors.New("expectedRevision must be nonnegative")
	}
	dbPath, err := validateCollaborationIdentity(input.DBPath, input.MissionID, input.ActorSessionID, input.ItemID)
	if err != nil {
		return nil, err
	}
	ledger, err := openCollaborationLedger(ctx, dbPath, input.MissionID, input.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	if mapStringValue(ledger.mission, "coordinator") != input.ActorSessionID {
		return nil, errors.New("only bound coordinator claims READY")
	}
	if mapStringValue(ledger.mission, "status") != "active" || !mapBoolValue(ledger.mission, "dispatch_enabled") {
		return nil, errors.New("dispatch is paused/disabled")
	}
	if ledger.revision != input.ExpectedRevision {
		return nil, fmt.Errorf("revision conflict; current=%d", ledger.revision)
	}
	item, err := ledger.item(ctx, input.ItemID)
	if err != nil {
		return nil, err
	}
	if mapStringValue(item, "phase") != "ready" {
		return nil, errors.New("not READY; reconcile existing claim, never redispatch blindly")
	}
	if mapStringValue(item, "executor") != "cloud" {
		return nil, errors.New("Cloud dispatch requires a frozen packet")
	}
	packet, ok := item["packet"].(map[string]any)
	if !ok {
		return nil, errors.New("Cloud dispatch requires a frozen packet")
	}
	if err := validateCollaborationPacket(packet, ledger.mission, true); err != nil {
		return nil, err
	}
	if _, err := c.localCollaborationRuntimePacket(packet); err != nil {
		return nil, err
	}
	if err := ledger.checkDependencies(ctx, item); err != nil {
		return nil, err
	}
	if err := ledger.checkUnique(ctx, input.ItemID, item); err != nil {
		return nil, err
	}
	if err := ledger.checkCloudCapacity(ctx); err != nil {
		return nil, err
	}
	claim, err := randomCollaborationClaim()
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	item["phase"] = "dispatching"
	item["claim"] = claim
	item["started_at"] = now
	if _, ok := item["next_check_at"]; !ok || item["next_check_at"] == nil {
		item["next_check_at"] = now + 1800
	}
	item["next_action"] = "Await dispatch receipt; uncertainty requires original-key reconciliation"
	if err := ledger.saveItem(ctx, item); err != nil {
		return nil, err
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return c.storeCollaborationClaim(dbPath, input.MissionID, input.ActorSessionID, input.ItemID, claim, packet, ledger.revision)
}

func (c *Client) collaborationRecover(ctx context.Context, input collaborationRecoverParams, allowActive bool) (map[string]any, error) {
	dbPath, err := validateCollaborationIdentity(input.DBPath, input.MissionID, input.ActorSessionID, input.ItemID)
	if err != nil {
		return nil, err
	}
	ledger, err := openCollaborationLedger(ctx, dbPath, input.MissionID, input.ActorSessionID, false)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	if mapStringValue(ledger.mission, "coordinator") != input.ActorSessionID {
		return nil, errors.New("only bound coordinator recovers a dispatch token")
	}
	item, err := ledger.item(ctx, input.ItemID)
	if err != nil {
		return nil, err
	}
	phase := mapStringValue(item, "phase")
	if phase == "active" && !allowActive {
		return nil, errors.New("active round already has a binding; use dispatch_recover only to repair its local receipt")
	}
	if phase != "active" && (mapStringValue(ledger.mission, "status") != "active" || !mapBoolValue(ledger.mission, "dispatch_enabled")) {
		return nil, errors.New("dispatch is paused/disabled")
	}
	if phase != "dispatching" && phase != "in_doubt" && phase != "active" {
		return nil, fmt.Errorf("item phase %q cannot recover a dispatch token", phase)
	}
	claim := mapStringValue(item, "claim")
	packet, ok := item["packet"].(map[string]any)
	if claim == "" || !ok {
		return nil, errors.New("ledger item has no recoverable frozen packet")
	}
	if phase == "active" {
		binding, bound := item["binding"].(map[string]any)
		if !bound || binding == nil {
			return nil, errors.New("active ledger item has no dispatch binding")
		}
		if err := validateCollaborationBinding(binding, item, ledger.mission); err != nil {
			return nil, err
		}
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return c.storeCollaborationClaim(dbPath, input.MissionID, input.ActorSessionID, input.ItemID, claim, packet, ledger.revision)
}

// reuseOrRebuildCollaborationToken returns the token bound to an existing
// ledger claim. A retry by ledger identity must never mint a new claim or
// provider idempotency key: the original token is the durable authority for
// both in-flight and completed dispatches.
func (c *Client) reuseOrRebuildCollaborationToken(dbPath, missionID, actorSessionID, itemID, claim string, packet map[string]any, revision int64) (string, error) {
	packetBytes, err := json.Marshal(packet)
	if err != nil {
		return "", err
	}
	digest := "sha256:" + hex.EncodeToString(collaborationHash(packetBytes))
	tokenID := collaborationTokenID(dbPath, missionID, itemID, claim)
	existing, readErr := c.readCollaborationToken(tokenID)
	if readErr == nil {
		if existing.DBPath != dbPath || existing.MissionID != missionID || existing.ActorSessionID != actorSessionID || existing.ItemID != itemID || existing.Claim != claim || existing.PacketSHA256 != digest {
			return "", errors.New("existing dispatch token does not match the frozen ledger claim")
		}
		return tokenID, nil
	}
	if readErr.Error() != "dispatchToken was not found; use recover with the exact ledger item" {
		return "", readErr
	}
	if _, err := c.storeCollaborationClaim(dbPath, missionID, actorSessionID, itemID, claim, packet, revision); err != nil {
		return "", err
	}
	return tokenID, nil
}

func (c *Client) storeCollaborationClaim(dbPath, missionID, actorSessionID, itemID, claim string, packet map[string]any, revision int64) (map[string]any, error) {
	packetBytes, err := json.Marshal(packet)
	if err != nil {
		return nil, err
	}
	digest := "sha256:" + hex.EncodeToString(collaborationHash(packetBytes))
	tokenID := collaborationTokenID(dbPath, missionID, itemID, claim)
	record := collaborationToken{
		Version: collaborationTokenVersion, DBPath: dbPath, MissionID: missionID, ActorSessionID: actorSessionID,
		ItemID: itemID, Claim: claim, PacketSHA256: digest,
		DispatchRequest: map[string]any{"action": "dispatch", "params": packet},
		ExpiresAt:       time.Now().Add(24 * time.Hour).Unix(),
	}
	// Recovering the same frozen claim must not erase provider identity or a
	// completed receipt already recorded by a prior local dispatch attempt.
	if existing, readErr := c.readCollaborationToken(tokenID); readErr == nil && existing.PacketSHA256 == digest && existing.DBPath == dbPath && existing.MissionID == missionID && existing.ActorSessionID == actorSessionID && existing.ItemID == itemID && existing.Claim == claim {
		record.DispatchState = existing.DispatchState
		record.Completed = existing.Completed
		if existing.Completed != nil {
			record.ExpiresAt = time.Now().Add(time.Hour).Unix()
		}
	}
	if err := c.writeCollaborationToken(tokenID, record); err != nil {
		return nil, err
	}
	return map[string]any{
		"dispatchToken": tokenID, "itemId": itemID, "claim": claim, "packetSHA256": digest,
		"ledgerRevision": revision, "dispatchRequest": record.DispatchRequest,
		"nextAction": "Pass dispatchRequest unchanged to FastSpider_FS once, then pass its untouched structured result to receipt. Do not wait or poll.",
	}, nil
}

func (c *Client) collaborationUseToken(ctx context.Context, action string, input collaborationTokenParams) (map[string]any, error) {
	var release func()
	if action != "verify" {
		release = acquireCollaborationDispatchLock(input.DispatchToken)
		defer release()
	}
	token, err := c.readCollaborationToken(input.DispatchToken)
	if err != nil {
		return nil, err
	}
	if c.projectPolicy != nil && c.projectPolicy.root != "" {
		if err := c.projectPolicy.validate("collaboration.control", "brief", map[string]any{"dbPath": token.DBPath}); err != nil {
			return nil, err
		}
	}
	if _, err := validateCollaborationTokenRecord(input.DispatchToken, token); err != nil {
		return nil, err
	}
	if token.Completed != nil {
		return token.Completed, nil
	}
	if token.ExpiresAt <= time.Now().Unix() {
		return nil, errors.New("dispatch token expired; use recover with the exact ledger item")
	}
	if action == "verify" {
		return map[string]any{"dispatchToken": input.DispatchToken, "itemId": token.ItemID, "claim": token.Claim, "packetSHA256": token.PacketSHA256, "dispatchRequest": token.DispatchRequest}, nil
	}
	if action == "uncertain" {
		if strings.TrimSpace(input.EvidenceRef) == "" {
			return nil, errors.New("evidenceRef is required for an uncertain dispatch")
		}
		return c.recordCollaborationUncertain(ctx, input.DispatchToken, token, input.EvidenceRef)
	}
	if action == "not_created" {
		if !input.NoTaskCreated || strings.TrimSpace(input.EvidenceRef) == "" {
			return nil, errors.New("not_created requires explicit noTaskCreated=true and evidenceRef")
		}
		return c.recordCollaborationNotCreated(ctx, input.DispatchToken, token, input.EvidenceRef)
	}
	dispatch, err := unwrapCollaborationDispatchResult(input.DispatchResult)
	if err != nil {
		return nil, err
	}
	if collaborationDispatchUncertain(dispatch) {
		return c.recordCollaborationUncertain(ctx, input.DispatchToken, token, "fast-spider:structured-dispatch-uncertain")
	}
	packet, ok := token.DispatchRequest["params"].(map[string]any)
	if !ok {
		return nil, errors.New("dispatchToken record is incomplete")
	}
	if value := mapStringValue(dispatch, "callbackSessionId"); value != "" && value != mapStringValue(packet, "callbackSessionId") {
		return nil, errors.New("dispatch callbackSessionId differs from the frozen packet")
	}
	if value := mapStringValue(dispatch, "idempotencyKey"); value != "" && value != mapStringValue(packet, "idempotencyKey") {
		return nil, errors.New("dispatch idempotencyKey differs from the frozen packet")
	}
	if target := mapStringValue(packet, "targetSessionId"); target != "" && mapStringValue(dispatch, "chatSessionId") != target {
		return nil, errors.New("dispatch chatSessionId differs from the frozen target")
	}
	binding := map[string]any{
		"chatSessionId": mapStringValue(dispatch, "chatSessionId"), "collaborationId": mapStringValue(dispatch, "collaborationId"),
		"taskRef": mapStringValue(dispatch, "taskRef"), "callbackSessionId": mapStringValue(packet, "callbackSessionId"),
		"idempotencyKey": mapStringValue(packet, "idempotencyKey"),
	}
	result, err := c.recordCollaborationActive(ctx, token, binding)
	if err != nil {
		return nil, err
	}
	completed := map[string]any{
		"itemId": token.ItemID, "binding": binding, "packetSHA256": token.PacketSHA256,
		"ledgerRevision": result["revision"], "phase": result["phase"],
		"nextAction": "End the current turn and await the formal callback.",
	}
	if replayed, ok := result["replayed"]; ok {
		completed["replayed"] = replayed
	}
	return c.finishCollaborationToken(input.DispatchToken, token, completed)
}

func (c *Client) recordCollaborationNotCreated(ctx context.Context, dispatchToken string, token collaborationToken, evidenceRef string) (map[string]any, error) {
	ledger, err := openCollaborationLedger(ctx, token.DBPath, token.MissionID, token.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	if mapStringValue(ledger.mission, "coordinator") != token.ActorSessionID {
		return nil, errors.New("only bound coordinator records dispatch receipt")
	}
	item, err := ledger.item(ctx, token.ItemID)
	if err != nil {
		return nil, err
	}
	if mapStringValue(item, "claim") != token.Claim {
		return nil, errors.New("wrong dispatch claim")
	}
	phase := mapStringValue(item, "phase")
	if phase == "dispatch_rejected" && mapStringValue(item, "terminal_ref") == evidenceRef {
		if err := ledger.commit(ctx); err != nil {
			return nil, err
		}
		return c.finishCollaborationToken(dispatchToken, token, map[string]any{
			"itemId": token.ItemID, "phase": phase, "ledgerRevision": ledger.revision,
			"replayed": true, "evidenceRef": evidenceRef,
			"nextAction": "Controller closes the rejected round, then may freeze a corrected new round.",
		})
	}
	if phase != "dispatching" && phase != "in_doubt" {
		return nil, errors.New("not-created receipt conflicts with current state")
	}
	item["phase"] = "dispatch_rejected"
	item["terminal_ref"] = evidenceRef
	item["evidence"] = appendUniqueCollaborationString(collaborationStringList(item["evidence"]), evidenceRef)
	item["next_action"] = "Controller closes rejected round, then may freeze a corrected new round"
	if err := ledger.saveItem(ctx, item); err != nil {
		return nil, err
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return c.finishCollaborationToken(dispatchToken, token, map[string]any{
		"itemId": token.ItemID, "phase": "dispatch_rejected", "ledgerRevision": ledger.revision,
		"evidenceRef": evidenceRef,
		"nextAction":  "Controller closes the rejected round, then may freeze a corrected new round.",
	})
}

func (c *Client) finishCollaborationToken(dispatchToken string, token collaborationToken, completed map[string]any) (map[string]any, error) {
	completed["packetSHA256"] = token.PacketSHA256
	finished := collaborationToken{
		Version: collaborationTokenVersion, DBPath: token.DBPath, MissionID: token.MissionID,
		ActorSessionID: token.ActorSessionID, ItemID: token.ItemID, Claim: token.Claim,
		PacketSHA256: token.PacketSHA256, DispatchRequest: token.DispatchRequest, DispatchState: token.DispatchState,
		Completed: completed, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	if err := c.writeCollaborationToken(dispatchToken, finished); err != nil {
		return nil, err
	}
	return completed, nil
}

func (c *Client) recordCollaborationUncertain(ctx context.Context, dispatchToken string, token collaborationToken, evidenceRef string) (map[string]any, error) {
	ledger, err := openCollaborationLedger(ctx, token.DBPath, token.MissionID, token.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	if mapStringValue(ledger.mission, "coordinator") != token.ActorSessionID {
		return nil, errors.New("only bound coordinator records dispatch receipt")
	}
	item, err := ledger.item(ctx, token.ItemID)
	if err != nil {
		return nil, err
	}
	if mapStringValue(item, "claim") != token.Claim {
		return nil, errors.New("wrong dispatch claim")
	}
	phase := mapStringValue(item, "phase")
	if phase != "dispatching" && phase != "in_doubt" {
		return nil, errors.New("late uncertain receipt conflicts with current state")
	}
	if phase == "dispatching" || mapStringValue(item, "next_action") != "Reconcile original key and frozen packet; do not create another round" {
		item["phase"] = "in_doubt"
		item["next_action"] = "Reconcile original key and frozen packet; do not create another round"
		if err := ledger.saveItem(ctx, item); err != nil {
			return nil, err
		}
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{
		"revision": ledger.revision, "phase": "in_doubt", "dispatchToken": dispatchToken,
		"packetSHA256": token.PacketSHA256, "evidenceRef": evidenceRef,
		"callerShouldYield": true, "activePollingAllowed": false,
		"nextAction": "Keep the original token, packet and key; schedule one bounded reconciliation and end the turn.",
	}, nil
}

func (c *Client) recordCollaborationActive(ctx context.Context, token collaborationToken, binding map[string]any) (map[string]any, error) {
	ledger, err := openCollaborationLedger(ctx, token.DBPath, token.MissionID, token.ActorSessionID, true)
	if err != nil {
		return nil, err
	}
	defer ledger.rollback()
	if mapStringValue(ledger.mission, "coordinator") != token.ActorSessionID {
		return nil, errors.New("only bound coordinator records dispatch receipt")
	}
	item, err := ledger.item(ctx, token.ItemID)
	if err != nil {
		return nil, err
	}
	if mapStringValue(item, "claim") != token.Claim {
		return nil, errors.New("wrong dispatch claim")
	}
	if err := validateCollaborationBinding(binding, item, ledger.mission); err != nil {
		return nil, err
	}
	phase := mapStringValue(item, "phase")
	if phase == "blocked" && item["binding"] == nil && collaborationHoldsExecution(item) {
		item["binding"] = binding
		if err := ledger.checkUnique(ctx, token.ItemID, item); err != nil {
			return nil, err
		}
		if err := ledger.saveItem(ctx, item); err != nil {
			return nil, err
		}
		if err := ledger.commit(ctx); err != nil {
			return nil, err
		}
		return map[string]any{"revision": ledger.revision, "phase": "blocked"}, nil
	}
	if phase != "dispatching" && phase != "in_doubt" {
		stored, _ := item["binding"].(map[string]any)
		if !collaborationMapsEqual(stored, binding) {
			return nil, errors.New("late receipt conflicts with current state")
		}
		if err := ledger.commit(ctx); err != nil {
			return nil, err
		}
		return map[string]any{"revision": ledger.revision, "phase": phase, "replayed": true}, nil
	}
	item["binding"] = binding
	item["phase"] = "active"
	item["next_action"] = "Await formal callback at original controller"
	if err := ledger.checkUnique(ctx, token.ItemID, item); err != nil {
		return nil, err
	}
	if err := ledger.saveItem(ctx, item); err != nil {
		return nil, err
	}
	if err := ledger.commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"revision": ledger.revision, "phase": "active"}, nil
}

func openCollaborationLedger(ctx context.Context, dbPath, missionID, actorSessionID string, writable bool) (*collaborationLedger, error) {
	return openCollaborationLedgerMode(ctx, dbPath, missionID, actorSessionID, writable, false)
}

func openCollaborationLedgerMode(ctx context.Context, dbPath, missionID, actorSessionID string, writable, allowClosed bool) (*collaborationLedger, error) {
	return openCollaborationLedgerAccess(ctx, dbPath, missionID, actorSessionID, writable, allowClosed, false)
}

func openCollaborationLedgerAccess(ctx context.Context, dbPath, missionID, actorSessionID string, writable, allowClosed, callbackOnly bool) (*collaborationLedger, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open collaboration database: %w", err)
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		conn.Close()
		db.Close()
		return nil, err
	}
	begin := "BEGIN"
	if writable {
		begin = "BEGIN IMMEDIATE"
	}
	if _, err := conn.ExecContext(ctx, begin); err != nil {
		conn.Close()
		db.Close()
		return nil, fmt.Errorf("begin collaboration transaction: %w", err)
	}
	ledger := &collaborationLedger{db: db, conn: conn, actorSessionID: actorSessionID}
	var raw string
	if err := conn.QueryRowContext(ctx, "SELECT data, revision FROM mission WHERE singleton=1").Scan(&raw, &ledger.revision); err != nil {
		ledger.rollback()
		return nil, fmt.Errorf("read collaboration mission: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &ledger.mission); err != nil {
		ledger.rollback()
		return nil, errors.New("collaboration mission is invalid")
	}
	if mapStringValue(ledger.mission, "id") != missionID || !samePath(mapStringValue(ledger.mission, "db_path"), dbPath) {
		ledger.rollback()
		return nil, errors.New("wrong mission/database identity")
	}
	if schema, ok := collaborationInt64(ledger.mission["schema"]); !ok || schema != 1 {
		ledger.rollback()
		return nil, errors.New("collaboration mission schema is unsupported")
	}
	controller := mapStringValue(ledger.mission, "controller")
	coordinator := mapStringValue(ledger.mission, "coordinator")
	legacyCallback := false
	if callbackOnly {
		for _, old := range collaborationStringList(ledger.mission["legacy_callback_sessions"]) {
			if old == actorSessionID {
				legacyCallback = true
			}
		}
	}
	if actorSessionID != controller && actorSessionID != coordinator && actorSessionID != mapStringValue(ledger.mission, "delivery_coordinator") && !legacyCallback {
		ledger.rollback()
		return nil, errors.New("actor not bound to this task")
	}
	if writable && !allowClosed && mapStringValue(ledger.mission, "status") == "closed" {
		ledger.rollback()
		return nil, errors.New("mission closed; no more writes")
	}
	return ledger, nil
}

func (l *collaborationLedger) rollback() {
	if l == nil || l.closed {
		return
	}
	_, _ = l.conn.ExecContext(context.Background(), "ROLLBACK")
	_ = l.conn.Close()
	_ = l.db.Close()
	l.closed = true
}

func (l *collaborationLedger) commit(ctx context.Context) error {
	if l.closed {
		return errors.New("collaboration transaction is closed")
	}
	if _, err := l.conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	_ = l.conn.Close()
	_ = l.db.Close()
	l.closed = true
	return nil
}

func (l *collaborationLedger) item(ctx context.Context, itemID string) (map[string]any, error) {
	var raw string
	if err := l.conn.QueryRowContext(ctx, "SELECT data FROM items WHERE id=?", itemID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("unknown item")
		}
		return nil, err
	}
	var item map[string]any
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return nil, errors.New("collaboration item is invalid")
	}
	return item, nil
}

func (l *collaborationLedger) saveItem(ctx context.Context, item map[string]any) error {
	if err := l.migrateLegacyExecutionChecks(ctx); err != nil {
		return err
	}
	item = cloneParams(item)
	if collaborationFinalPhases[mapStringValue(item, "phase")] {
		packet, _ := item["packet"].(map[string]any)
		key := mapStringValue(packet, "idempotencyKey")
		if key == "" {
			if binding, _ := item["binding"].(map[string]any); binding != nil {
				key = mapStringValue(binding, "idempotencyKey")
			}
		}
		delete(item, "packet")
		if key != "" {
			item["dispatch_key"] = key
		}
	}
	itemID := mapStringValue(item, "id")
	phase := mapStringValue(item, "phase")
	kind := mapStringValue(item, "kind")
	if itemID == "" || phase == "" || kind == "" {
		return errors.New("collaboration item identity is incomplete")
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if len(raw) > 48000 {
		return errors.New("item too large; store logs in evidence files")
	}
	dispatchKey := collaborationDispatchKey(item)
	taskRef := ""
	if binding, _ := item["binding"].(map[string]any); binding != nil {
		taskRef = mapStringValue(binding, "taskRef")
	}
	l.revision++
	if _, err := l.conn.ExecContext(ctx, `INSERT INTO items(id,phase,kind,revision,data,dispatch_key,task_ref) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET phase=excluded.phase,kind=excluded.kind,revision=excluded.revision,data=excluded.data,dispatch_key=excluded.dispatch_key,task_ref=excluded.task_ref`,
		itemID, phase, kind, l.revision, string(raw), nullableCollaborationString(dispatchKey), nullableCollaborationString(taskRef)); err != nil {
		return err
	}
	var hasTree int
	if err := l.conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workstreams'").Scan(&hasTree); err != nil {
		return err
	}
	if hasTree != 0 {
		stream := mapStringValue(item, "workstream_id")
		if stream != "" {
			var found int
			if err := l.conn.QueryRowContext(ctx, "SELECT 1 FROM workstreams WHERE id=?", stream).Scan(&found); err != nil {
				return errors.New("unknown task workstream")
			}
		}
		if _, err := l.conn.ExecContext(ctx, "UPDATE items SET title=?,workstream_id=?,archived=? WHERE id=?", mapStringValue(item, "title"), stream, boolInt(collaborationBoolDefault(item, "archived", false)), itemID); err != nil {
			return err
		}
	}
	if _, err := l.conn.ExecContext(ctx, "INSERT INTO events(revision,object_id,phase) VALUES(?,?,?)", l.revision, itemID, phase); err != nil {
		return err
	}
	if _, err := l.conn.ExecContext(ctx, "DELETE FROM events WHERE revision <= ?", l.revision-100); err != nil {
		return err
	}
	missionRaw, err := json.Marshal(l.mission)
	if err != nil {
		return err
	}
	_, err = l.conn.ExecContext(ctx, "UPDATE mission SET revision=?,data=? WHERE singleton=1", l.revision, string(missionRaw))
	return err
}

func (l *collaborationLedger) checkDependencies(ctx context.Context, item map[string]any) error {
	for _, dep := range collaborationStringList(item["depends_on"]) {
		dependency, err := l.item(ctx, dep)
		if err != nil {
			return err
		}
		phase := mapStringValue(dependency, "phase")
		if phase != "accepted" && phase != "done" {
			return fmt.Errorf("dependency not done: %s", dep)
		}
	}
	return nil
}

func (l *collaborationLedger) checkCloudCapacity(ctx context.Context) error {
	capacity, _ := l.mission["capacity"].(map[string]any)
	limit, ok := collaborationInt64(capacity["cloud"])
	if !ok {
		return nil
	}
	rows, err := l.conn.QueryContext(ctx, "SELECT data FROM items WHERE phase NOT IN ('done','canceled')")
	if err != nil {
		return err
	}
	defer rows.Close()
	var held int64
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var item map[string]any
		if json.Unmarshal([]byte(raw), &item) != nil {
			return errors.New("collaboration item is invalid")
		}
		if mapStringValue(item, "executor") == "cloud" && collaborationHoldsExecution(item) {
			held++
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if held >= limit {
		return errors.New("registered Cloud capacity exhausted")
	}
	return nil
}

func (l *collaborationLedger) checkUnique(ctx context.Context, itemID string, item map[string]any) error {
	phase := mapStringValue(item, "phase")
	if collaborationAnalysisItem(item) && (phase == "ready" || phase == "dispatching" || phase == "in_doubt") {
		if err := l.checkAnalysisAuthority(ctx, item); err != nil {
			return err
		}
		if err := l.checkAnalysisCapacity(ctx, itemID); err != nil {
			return err
		}
	}
	key := collaborationDispatchKey(item)
	if l.hasAttempts(ctx) && key != "" {
		var found int
		err := l.conn.QueryRowContext(ctx, "SELECT 1 FROM execution_attempts WHERE dispatch_key=? LIMIT 1", key).Scan(&found)
		if err == nil {
			return errors.New("idempotency key belongs to a historical execution attempt")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if key != "" {
		var found int
		err := l.conn.QueryRowContext(ctx, "SELECT 1 FROM items WHERE id<>? AND dispatch_key=? LIMIT 1", itemID, key).Scan(&found)
		if err == nil {
			return errors.New("idempotency key already belongs to another round")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	binding, _ := item["binding"].(map[string]any)
	if taskRef := mapStringValue(binding, "taskRef"); taskRef != "" {
		var found int
		err := l.conn.QueryRowContext(ctx, "SELECT 1 FROM items WHERE id<>? AND task_ref=? LIMIT 1", itemID, taskRef).Scan(&found)
		if err == nil {
			return errors.New("taskRef already registered")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if mapStringValue(item, "phase") != "ready" && !collaborationHoldsExecution(item) {
		return nil
	}
	packet := collaborationScopePacket(item)
	rows, err := l.conn.QueryContext(ctx, "SELECT data FROM items WHERE id<>? AND phase NOT IN ('done','canceled')", itemID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var other map[string]any
		if json.Unmarshal([]byte(raw), &other) != nil {
			return errors.New("collaboration item is invalid")
		}
		if !collaborationHoldsExecution(other) {
			continue
		}
		otherPacket := collaborationScopePacket(other)
		chat := collaborationChatBinding(item, packet)
		otherChat := collaborationChatBinding(other, otherPacket)
		if chat != "" && chat == otherChat {
			return errors.New("CHAT has an active or uncertain round; reconcile original")
		}
		if mapStringValue(packet, "machineId") != mapStringValue(otherPacket, "machineId") {
			continue
		}
		for _, a := range collaborationScopeRoots(packet) {
			for _, b := range collaborationScopeRoots(otherPacket) {
				if lexicalPathWithin(a, b) || lexicalPathWithin(b, a) {
					return fmt.Errorf("write scope held by active/uncertain round: item=%s scope=%s overlaps requested=%s; preserve the existing writer until terminal evidence", mapStringValue(other, "id"), b, a)
				}
			}
		}
	}
	return rows.Err()
}

func validateCollaborationIdentity(dbPath, missionID, actorSessionID, itemID string) (string, error) {
	resolved, err := validateCollaborationBaseIdentity(dbPath, missionID, actorSessionID)
	if err != nil {
		return "", err
	}
	if err := validateCollaborationOpaqueID(itemID, "itemId"); err != nil {
		return "", err
	}
	return resolved, nil
}

func validateCollaborationBaseIdentity(dbPath, missionID, actorSessionID string) (string, error) {
	if !filepath.IsAbs(dbPath) || !strings.EqualFold(filepath.Ext(dbPath), ".sqlite3") {
		return "", errors.New("dbPath must be an absolute .sqlite3 path")
	}
	info, err := os.Lstat(dbPath)
	if err != nil {
		return "", errors.New("database missing; initialize explicitly")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("database must be a regular non-symlink file")
	}
	resolved, err := ResolveMachinePath(dbPath)
	if err != nil {
		return "", err
	}
	for name, value := range map[string]string{"missionId": missionID, "actorSessionId": actorSessionID} {
		if err := validateCollaborationOpaqueID(value, name); err != nil {
			return "", err
		}
	}
	return resolved, nil
}

func validateCollaborationPacket(packet, mission map[string]any, dispatchable bool) error {
	allowed := map[string]bool{"machineId": true, "callbackSessionId": true, "workingDirectory": true, "prompt": true, "idempotencyKey": true, "accessMode": true, "writeScope": true, "callbackType": true, "targetSessionId": true, "deliverablePath": true, "model": true, "thinking": true}
	for _, key := range []string{"model", "thinking"} {
		if value, exists := packet[key]; exists {
			if err := validateCollaborationText(value, "packet "+key, 128); err != nil {
				return err
			}
		}
	}
	for key := range packet {
		if !allowed[key] {
			return errors.New("unknown packet fields")
		}
	}
	for _, key := range []string{"machineId", "callbackSessionId", "workingDirectory", "idempotencyKey"} {
		if err := validateCollaborationText(packet[key], key, 1024); err != nil {
			return err
		}
	}
	callback := mapStringValue(packet, "callbackSessionId")
	if dispatchable {
		if callback != mapStringValue(mission, "controller") {
			return errors.New("new READY must callback to current controller")
		}
	} else {
		sessions, err := collaborationCallbackSessions(mission)
		if err != nil {
			return err
		}
		if !sessions[callback] {
			return errors.New("unknown callback owner")
		}
	}
	key := mapStringValue(packet, "idempotencyKey")
	if len(key) < 12 || len(key) > 128 {
		return errors.New("invalid idempotency key")
	}
	if !filepath.IsAbs(mapStringValue(packet, "workingDirectory")) {
		return errors.New("workingDirectory must be absolute")
	}
	accessMode := mapStringValue(packet, "accessMode")
	if accessMode != "read_only" && accessMode != "write" {
		return errors.New("explicit accessMode required")
	}
	if accessMode == "write" {
		if scope, ok := packet["writeScope"].(string); ok {
			if err := validateCollaborationText(scope, "writeScope", 1024); err != nil {
				return err
			}
		} else if legacy, ok := packet["writeScope"].([]any); !dispatchable && ok && len(legacy) > 0 {
			for _, scope := range legacy {
				if err := validateCollaborationText(scope, "legacy writeScope", 1024); err != nil {
					return err
				}
			}
		} else {
			return errors.New("FS writeScope must be one string; split independent paths into separate task rounds")
		}
	}
	callbackType := mapStringValue(packet, "callbackType")
	if callbackType != "text" && callbackType != "local_file" && callbackType != "status" {
		return errors.New("callbackType required")
	}
	prompt, ok := packet["prompt"].(string)
	if !ok || prompt == "" || utf8.RuneCountInString(prompt) > 16000 {
		return errors.New("prompt too large or absent")
	}
	return nil
}

func validateCollaborationBinding(binding, item, mission map[string]any) error {
	allowed := map[string]bool{"chatSessionId": true, "collaborationId": true, "taskRef": true, "callbackSessionId": true, "idempotencyKey": true}
	if len(binding) != len(allowed) {
		return errors.New("binding must contain the exact dispatch identity")
	}
	for key := range binding {
		if !allowed[key] {
			return errors.New("binding must contain the exact dispatch identity")
		}
		if err := validateCollaborationText(binding[key], key, 1024); err != nil {
			return err
		}
	}
	callback := mapStringValue(binding, "callbackSessionId")
	validCallback := callback == mapStringValue(mission, "controller")
	for _, legacy := range collaborationStringList(mission["legacy_callback_sessions"]) {
		validCallback = validCallback || callback == legacy
	}
	if !validCallback {
		return errors.New("foreign callback owner")
	}
	packet, _ := item["packet"].(map[string]any)
	if packet != nil {
		if mapStringValue(binding, "idempotencyKey") != mapStringValue(packet, "idempotencyKey") {
			return errors.New("binding key differs from packet")
		}
		if target := mapStringValue(packet, "targetSessionId"); target != "" && mapStringValue(binding, "chatSessionId") != target {
			return errors.New("receipt differs from frozen target CHAT")
		}
	}
	return nil
}

func validateCollaborationText(value any, name string, limit int) error {
	text, ok := value.(string)
	if !ok || text == "" || len(text) > limit {
		return fmt.Errorf("invalid %s", name)
	}
	for _, char := range text {
		if char < 32 {
			return fmt.Errorf("invalid %s", name)
		}
	}
	return nil
}

func collaborationScopePacket(item map[string]any) map[string]any {
	if local, _ := item["local_scope"].(map[string]any); local != nil {
		return local
	}
	packet, _ := item["packet"].(map[string]any)
	return packet
}

func collaborationScopeRoots(packet map[string]any) []string {
	if mapStringValue(packet, "accessMode") != "write" {
		return nil
	}
	var scopes []string
	switch value := packet["writeScope"].(type) {
	case string:
		scopes = []string{value}
	case []any:
		for _, item := range value {
			if text, ok := item.(string); ok {
				scopes = append(scopes, text)
			}
		}
	}
	workDir := mapStringValue(packet, "workingDirectory")
	roots := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		prefix := scope
		if index := strings.IndexAny(prefix, "*?["); index >= 0 {
			prefix = prefix[:index]
			if prefix != scope && !strings.HasSuffix(prefix, "/") && !strings.HasSuffix(prefix, "\\") {
				prefix = filepath.Dir(prefix)
			}
		}
		root := prefix
		if !filepath.IsAbs(root) {
			root = filepath.Join(workDir, root)
		}
		if absolute, err := filepath.Abs(root); err == nil {
			root = filepath.Clean(absolute)
		}
		// New target directories still share the identity of their existing
		// ancestor (including Windows short names and directory junctions).
		if resolved, err := resolveLocalCollaborationPath("", root, true); err == nil {
			root = resolved
		}
		roots = append(roots, root)
	}
	return roots
}

func collaborationChatBinding(item, packet map[string]any) string {
	if binding, _ := item["binding"].(map[string]any); binding != nil {
		if chat := mapStringValue(binding, "chatSessionId"); chat != "" {
			return chat
		}
	}
	return mapStringValue(packet, "targetSessionId")
}

func collaborationExecutionEnded(item map[string]any) bool {
	if mapStringValue(item, "terminal_ref") != "" {
		return true
	}
	result := mapStringValue(item, "result")
	if result == "" || result == "none" || len(collaborationAnyList(item["evidence"])) == 0 {
		return false
	}
	if mapStringValue(item, "executor") != "cloud" {
		return true
	}
	callback := mapStringValue(item, "callback")
	return callback == "received" || callback == "acked"
}

func collaborationHoldsExecution(item map[string]any) bool {
	started := mapStringValue(item, "claim") != "" || item["binding"] != nil || item["started_at"] != nil || mapStringValue(item, "phase") == "active"
	return started && mapStringValue(item, "phase") != "dispatch_rejected" && !collaborationExecutionEnded(item)
}

func collaborationDispatchKey(item map[string]any) string {
	if packet, _ := item["packet"].(map[string]any); packet != nil {
		if key := mapStringValue(packet, "idempotencyKey"); key != "" {
			return key
		}
	}
	if key := mapStringValue(item, "dispatch_key"); key != "" {
		return key
	}
	if binding, _ := item["binding"].(map[string]any); binding != nil {
		return mapStringValue(binding, "idempotencyKey")
	}
	return ""
}

func (c *Client) collaborationTokenPath(token string) (string, error) {
	if len(token) != 64 {
		return "", errors.New("invalid dispatchToken")
	}
	if _, err := hex.DecodeString(token); err != nil {
		return "", errors.New("invalid dispatchToken")
	}
	dir := filepath.Join(c.cfg.DataDir, "collaboration-control", "tokens")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, token+".json"), nil
}

func (c *Client) writeCollaborationToken(token string, record collaborationToken) error {
	collaborationTokenStoreMu.Lock()
	defer collaborationTokenStoreMu.Unlock()

	path, err := c.collaborationTokenPath(token)
	if err != nil {
		return err
	}
	if c.beforeCollaborationTokenWriteOverride != nil {
		if err := c.beforeCollaborationTokenWriteOverride(record); err != nil {
			return err
		}
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "token-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if err := json.NewEncoder(temp).Encode(record); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceFile(tempPath, path); err != nil {
		return err
	}
	if err := syncParentDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	cleanupExpiredCollaborationTokens(filepath.Dir(path), time.Now().Unix())
	return nil
}

func (c *Client) readCollaborationToken(token string) (collaborationToken, error) {
	collaborationTokenStoreMu.Lock()
	defer collaborationTokenStoreMu.Unlock()

	path, err := c.collaborationTokenPath(token)
	if err != nil {
		return collaborationToken{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return collaborationToken{}, errors.New("dispatchToken was not found; use recover with the exact ledger item")
	}
	var record collaborationToken
	if json.Unmarshal(data, &record) != nil || record.Version != collaborationTokenVersion || record.ExpiresAt <= 0 {
		return collaborationToken{}, errors.New("dispatchToken record is invalid")
	}
	if record.Completed == nil && (record.DBPath == "" || record.MissionID == "" || record.ActorSessionID == "" || record.ItemID == "" || record.Claim == "" || record.PacketSHA256 == "" || record.DispatchRequest == nil) {
		return collaborationToken{}, errors.New("dispatchToken record is incomplete")
	}
	return record, nil
}

func cleanupExpiredCollaborationTokens(dir string, now int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var record collaborationToken
		if json.Unmarshal(data, &record) == nil && record.ExpiresAt > 0 && record.ExpiresAt <= now {
			_ = os.Remove(path)
		}
	}
}

func unwrapCollaborationDispatchResult(input map[string]any) (map[string]any, error) {
	current := input
	for range 8 {
		if completeCollaborationDispatch(current) || collaborationDispatchUncertain(current) {
			return current, nil
		}
		advanced := false
		for _, key := range []string{"result", "structuredContent", "data"} {
			if child, ok := current[key].(map[string]any); ok {
				current = child
				advanced = true
				break
			}
		}
		if !advanced {
			break
		}
	}
	return nil, errors.New("dispatch result has no complete binding; record uncertain instead")
}

func completeCollaborationDispatch(value map[string]any) bool {
	return mapStringValue(value, "chatSessionId") != "" && mapStringValue(value, "collaborationId") != "" && mapStringValue(value, "taskRef") != ""
}

func collaborationDispatchUncertain(value map[string]any) bool {
	for _, key := range []string{"createInDoubt", "deliveryInDoubt", "callbackPending"} {
		if mapBoolValue(value, key) {
			return true
		}
	}
	return false
}

func requireCollaborationParams(input map[string]any, names ...string) error {
	for _, name := range names {
		if _, ok := input[name]; !ok {
			return fmt.Errorf("%s is required", name)
		}
	}
	return nil
}

func randomCollaborationClaim() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func collaborationTokenID(dbPath, missionID, itemID, claim string) string {
	return hex.EncodeToString(collaborationHash([]byte(strings.Join([]string{dbPath, missionID, itemID, claim}, "\x00"))))
}

func collaborationHash(value []byte) []byte {
	digest := sha256.Sum256(value)
	return digest[:]
}

func collaborationMapsEqual(left, right map[string]any) bool {
	a, errA := json.Marshal(left)
	b, errB := json.Marshal(right)
	return errA == nil && errB == nil && string(a) == string(b)
}

func collaborationStringList(value any) []string {
	items := collaborationAnyList(value)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func appendUniqueCollaborationString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func collaborationAnyList(value any) []any {
	items, _ := value.([]any)
	return items
}

func collaborationInt64(value any) (int64, bool) {
	switch number := value.(type) {
	case float64:
		return int64(number), number == float64(int64(number))
	case int64:
		return number, true
	case int:
		return int64(number), true
	default:
		return 0, false
	}
}

func mapStringValue(value map[string]any, key string) string {
	text, _ := value[key].(string)
	return text
}

func mapBoolValue(value map[string]any, key string) bool {
	flag, _ := value[key].(bool)
	return flag
}

func nullableCollaborationString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
