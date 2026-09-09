package localmcp

import (
	"context"
	"errors"
	"strings"
	"time"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

type collaborationControlInput struct {
	Action string         `json:"action" jsonschema:"collaboration.control v2 action such as init, brief, next_actions, apply, dispatch, callback_claim, close, or cleanup"`
	Params map[string]any `json:"params,omitempty" jsonschema:"action-specific structured task ledger, dispatch, or callback parameters; never a JSON input file path and never Cloud credentials"`
}

type collaborationControlOutput struct {
	Result map[string]any `json:"result"`
}

func callCollaborationControl(ctx context.Context, dataDir string, call bridgeCaller, input collaborationControlInput) (collaborationControlOutput, error) {
	action := strings.TrimSpace(input.Action)
	if action == "" {
		return collaborationControlOutput{}, errors.New("action is required")
	}
	params := input.Params
	if params == nil {
		params = map[string]any{}
	}
	if action == "callback_claim" || action == "callback_ack" {
		params = cloneParams(params)
		params["callbackClaimTransport"] = "local"
	}
	timeout := 30 * time.Second
	if action == "dispatch" || action == "dispatch_recover" {
		timeout = 180 * time.Second
	} else if action == "compact" {
		timeout = 120 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response, err := call(callCtx, dataDir, protocolv1.CapabilityRequest{
		Capability: protocolv1.CollaborationControlCapability.CapabilityId,
		Action:     action,
		Params:     params,
		Deadline:   protocolv1.Timestamp(time.Now().UTC().Add(timeout)),
	})
	if err != nil {
		return collaborationControlOutput{}, err
	}
	if response.Error != nil {
		return collaborationControlOutput{}, errors.New(response.Error.Code + ": " + response.Error.Message)
	}
	return collaborationControlOutput{Result: response.Result}, nil
}

func cloneParams(input map[string]any) map[string]any {
	out := make(map[string]any, len(input)+1)
	for key, value := range input {
		out[key] = value
	}
	return out
}
