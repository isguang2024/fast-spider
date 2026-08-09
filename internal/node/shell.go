package node

import (
	"context"
	"fmt"
	"os"
	"time"
)

type shellRunParams struct {
	Argv           []string `json:"argv"`
	Cwd            string   `json:"cwd,omitempty"`
	TimeoutSeconds int64    `json:"timeoutSeconds,omitempty"`
	IdempotencyKey string   `json:"idempotencyKey"`
}

type jobWatchParams struct {
	JobID       string `json:"jobId"`
	Cursor      int64  `json:"cursor,omitempty"`
	WaitSeconds int64  `json:"waitSeconds,omitempty"`
}

type jobCancelParams struct {
	JobID string `json:"jobId"`
}

func (c *Client) shellRun(ctx context.Context, params map[string]any) (JobSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return JobSnapshot{}, err
	}
	var input shellRunParams
	if err := decodeParams(params, &input); err != nil {
		return JobSnapshot{}, fmt.Errorf("invalid params: %w", err)
	}
	if input.Cwd == "" {
		return JobSnapshot{}, fmt.Errorf("absolute cwd is required")
	}
	resolvedCwd, err := ResolveMachinePath(input.Cwd)
	if err != nil {
		return JobSnapshot{}, err
	}
	info, err := os.Stat(resolvedCwd)
	if err != nil {
		return JobSnapshot{}, err
	}
	if !info.IsDir() {
		return JobSnapshot{}, fmt.Errorf("cwd must be a directory")
	}
	if input.TimeoutSeconds < 0 {
		return JobSnapshot{}, fmt.Errorf("timeoutSeconds must be non-negative")
	}
	timeout := time.Duration(input.TimeoutSeconds) * time.Second
	if err := ctx.Err(); err != nil {
		return JobSnapshot{}, err
	}
	return c.jobs.StartShell(resolvedCwd, input.Argv, timeout, input.IdempotencyKey)
}

func (c *Client) jobWatch(ctx context.Context, params map[string]any) (JobSnapshot, error) {
	var input jobWatchParams
	if err := decodeParams(params, &input); err != nil {
		return JobSnapshot{}, fmt.Errorf("invalid params: %w", err)
	}
	if input.JobID == "" || input.WaitSeconds < 0 {
		return JobSnapshot{}, fmt.Errorf("jobId is required and waitSeconds must be non-negative")
	}
	return c.jobs.Watch(ctx, input.JobID, input.Cursor, time.Duration(input.WaitSeconds)*time.Second)
}

func (c *Client) jobCancel(ctx context.Context, params map[string]any) (JobSnapshot, error) {
	var input jobCancelParams
	if err := decodeParams(params, &input); err != nil {
		return JobSnapshot{}, fmt.Errorf("invalid params: %w", err)
	}
	if input.JobID == "" {
		return JobSnapshot{}, fmt.Errorf("jobId is required")
	}
	return c.jobs.Cancel(ctx, input.JobID)
}
