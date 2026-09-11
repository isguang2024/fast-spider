package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// Context exposes only the project assigned by the current immutable taskRef.
// A requested taskId selects evidence within that project, never a new owner.
func (t *nativeRunnerTransport) Context(ctx context.Context, params map[string]any) (map[string]any, error) {
	if t == nil || t.manager == nil || t.manager.nativeRunner == nil || t.manager.callbackStore == nil {
		return nil, nodeRunnerUnavailableError()
	}
	var input struct {
		nativeContextParams
		TaskRef string `json:"taskRef"`
	}
	if err := decodeParams(params, &input); err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.TaskRef) == "" {
		return nil, errors.New("runner.context requires the assigned taskRef")
	}
	registrations, _, err := t.manager.callbackStore.registrationsSnapshot("", "")
	if err != nil {
		return nil, err
	}
	var owner *sessionCallbackRegistration
	for i := range registrations {
		candidate := &registrations[i]
		if candidate.CallbackClaimTransport != callbackClaimTransportLocal || nativeRunnerTaskRef(nativeRunnerDispatch{ProjectID: candidate.MissionID, TaskID: candidate.TaskID, Round: int(candidate.Generation)}) != input.TaskRef {
			continue
		}
		if owner != nil {
			return nil, errors.New("runner.context taskRef has multiple callback bindings")
		}
		owner = candidate
	}
	if owner == nil || !callbackRegistrationProviderActive(*owner) {
		return nil, errors.New("runner.context taskRef is not an active assignment")
	}
	if input.ProjectID != "" && input.ProjectID != owner.MissionID {
		return nil, errors.New("runner.context cannot read another project")
	}
	var raw string
	if err := t.manager.nativeRunner.db.QueryRowContext(ctx, "SELECT value FROM runner_tasks WHERE id=? AND project_id=?", owner.TaskID, owner.MissionID).Scan(&raw); err != nil {
		return nil, err
	}
	var task nativeRunnerTask
	if err := json.Unmarshal([]byte(raw), &task); err != nil {
		return nil, err
	}
	if task.Round != int(owner.Generation) || task.Request == nil || nativeRunnerTaskRef(*task.Request) != input.TaskRef || (task.Receipt != nil && (task.Receipt.Generation != owner.Generation || task.Receipt.SessionID != owner.SourceSessionID)) || task.State == "cancelled" || task.State == "canceling" {
		return nil, errors.New("runner.context assignment generation is no longer current")
	}
	query := make(map[string]any, len(params)+1)
	for key, value := range params {
		query[key] = value
	}
	query["projectId"] = owner.MissionID
	return t.manager.nativeRunner.QueryContext(ctx, query)
}
