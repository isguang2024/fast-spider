package node

import (
	"context"
	"fmt"
	"os"
	"time"
)

type buildControlParams struct {
	Action         string `json:"action"`
	ProfileID      string `json:"profileId,omitempty"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

type buildProfileSummary struct {
	ProfileID      string `json:"profileId"`
	DisplayName    string `json:"displayName"`
	Cwd            string `json:"cwd"`
	TimeoutSeconds int64  `json:"timeoutSeconds"`
}

type buildControlResult struct {
	Profiles []buildProfileSummary `json:"profiles,omitempty"`
	Job      *JobSnapshot          `json:"job,omitempty"`
}

func (c *Client) buildControl(ctx context.Context, workspaceID string, params map[string]any) (buildControlResult, error) {
	var input buildControlParams
	if err := decodeParams(params, &input); err != nil {
		return buildControlResult{}, fmt.Errorf("invalid params: %w", err)
	}
	store := NewWorkspaceStore(c.cfg.DataDir)
	workspace, err := store.Resolve(workspaceID)
	if err != nil {
		return buildControlResult{}, err
	}
	switch input.Action {
	case "list":
		profiles, err := store.BuildProfiles(workspaceID)
		if err != nil {
			return buildControlResult{}, err
		}
		out := buildControlResult{Profiles: make([]buildProfileSummary, 0, len(profiles))}
		for _, profile := range profiles {
			out.Profiles = append(out.Profiles, buildProfileSummary{ProfileID: profile.ProfileID, DisplayName: profile.DisplayName, Cwd: profile.Cwd, TimeoutSeconds: profile.TimeoutSeconds})
		}
		return out, nil
	case "run":
		if !workspace.Allows(WorkspacePermissionBuild) {
			return buildControlResult{}, ErrPermissionDenied
		}
		if input.ProfileID == "" || input.IdempotencyKey == "" {
			return buildControlResult{}, fmt.Errorf("profileId and idempotencyKey are required")
		}
		profile, err := store.BuildProfile(workspaceID, input.ProfileID)
		if err != nil {
			return buildControlResult{}, err
		}
		cwd, err := resolveWorkspacePath(workspace.Root, profile.Cwd)
		if err != nil {
			return buildControlResult{}, err
		}
		info, err := os.Stat(cwd)
		if err != nil {
			return buildControlResult{}, err
		}
		if !info.IsDir() {
			return buildControlResult{}, fmt.Errorf("profile cwd must be a directory")
		}
		timeout := time.Duration(profile.TimeoutSeconds) * time.Second
		job, err := c.jobs.StartShell(workspaceID, cwd, profile.Argv, timeout, input.IdempotencyKey, func() bool {
			current, err := store.Resolve(workspaceID)
			return err == nil && current.Allows(WorkspacePermissionBuild)
		})
		if err != nil {
			return buildControlResult{}, err
		}
		return buildControlResult{Job: &job}, nil
	default:
		return buildControlResult{}, fmt.Errorf("unsupported build action %q", input.Action)
	}
}
