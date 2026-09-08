package localmcp

import (
	"context"
	"errors"
	"strings"
	"time"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

type collaborationControlInput struct {
	Action string         `json:"action" jsonschema:"claim, recover, receipt, uncertain, not_created, or verify"`
	Params map[string]any `json:"params,omitempty" jsonschema:"action-specific task-local ledger parameters; no machineId or Cloud credentials"`
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
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	response, err := call(callCtx, dataDir, protocolv1.CapabilityRequest{
		Capability: protocolv1.CollaborationControlCapability.CapabilityId,
		Action:     action,
		Params:     params,
		Deadline:   protocolv1.Timestamp(time.Now().UTC().Add(30 * time.Second)),
	})
	if err != nil {
		return collaborationControlOutput{}, err
	}
	if response.Error != nil {
		return collaborationControlOutput{}, errors.New(response.Error.Code + ": " + response.Error.Message)
	}
	return collaborationControlOutput{Result: response.Result}, nil
}
