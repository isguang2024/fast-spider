package node

import (
	"context"
	"fmt"
	"os"
	"time"
)

type buildControlParams struct {
	Action         string           `json:"action"`
	Argv           []string         `json:"argv"`
	Cwd            string           `json:"cwd"`
	Runtime        executionRuntime `json:"runtime,omitempty"`
	TimeoutSeconds int64            `json:"timeoutSeconds,omitempty"`
	IdempotencyKey string           `json:"idempotencyKey"`
}

type buildControlResult struct {
	Job *JobSnapshot `json:"job,omitempty"`
}

func (c *Client) buildControl(ctx context.Context, requestID, traceID string, params map[string]any) (buildControlResult, error) {
	var input buildControlParams
	if err := decodeParams(params, &input); err != nil {
		return buildControlResult{}, fmt.Errorf("invalid params: %w", err)
	}
	if input.Action != "run" {
		return buildControlResult{}, fmt.Errorf("unsupported build action %q", input.Action)
	}
	if input.Cwd == "" || input.IdempotencyKey == "" {
		return buildControlResult{}, fmt.Errorf("absolute cwd and idempotencyKey are required")
	}
	cwd, err := ResolveMachinePath(input.Cwd)
	if err != nil {
		return buildControlResult{}, err
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return buildControlResult{}, err
	}
	if !info.IsDir() {
		return buildControlResult{}, fmt.Errorf("cwd must be a directory")
	}
	if input.TimeoutSeconds < 0 {
		return buildControlResult{}, fmt.Errorf("timeoutSeconds must be non-negative")
	}
	timeout := time.Duration(input.TimeoutSeconds) * time.Second
	job, err := c.jobs.StartExecution(ctx, cwd, input.Argv, input.Runtime, timeout, input.IdempotencyKey, requestID, traceID)
	if err != nil {
		return buildControlResult{}, err
	}
	return buildControlResult{Job: &job}, nil
}
