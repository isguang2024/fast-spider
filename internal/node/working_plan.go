package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxWorkingPlanTasks       = 500
	maxWorkingTaskEvidences   = 32
	maxWorkingPlanIDBytes     = 128
	maxWorkingTaskIDBytes     = 128
	maxWorkingTaskTitleBytes  = 512
	maxWorkingEvidenceBytes   = 2048
	maxWorkingPlanListEntries = 512
)

type workingPlanEvidenceInput struct {
	Summary   string `json:"summary"`
	Kind      string `json:"kind,omitempty"`
	Reference string `json:"reference,omitempty"`
}

type workingPlanEvidence struct {
	Summary    string    `json:"summary"`
	Kind       string    `json:"kind,omitempty"`
	Reference  string    `json:"reference,omitempty"`
	AcceptedAt time.Time `json:"acceptedAt"`
}

type workingPlanTaskInput struct {
	ID            string                     `json:"id"`
	Title         string                     `json:"title"`
	Status        string                     `json:"status,omitempty"`
	Completion    int                        `json:"completion,omitempty"`
	BlockedReason string                     `json:"blockedReason,omitempty"`
	Evidences     []workingPlanEvidenceInput `json:"evidences,omitempty"`
}

type workingPlanTask struct {
	ID            string                `json:"id"`
	Title         string                `json:"title"`
	Status        string                `json:"status"`
	Completion    int                   `json:"completion"`
	BlockedReason string                `json:"blockedReason,omitempty"`
	Evidences     []workingPlanEvidence `json:"evidences,omitempty"`
	UpdatedAt     time.Time             `json:"updatedAt"`
}

type workingPlanSummary struct {
	PlanID        string    `json:"planId"`
	Title         string    `json:"title,omitempty"`
	TargetVersion string    `json:"targetVersion,omitempty"`
	Goal          string    `json:"goal,omitempty"`
	TaskCount     int       `json:"taskCount"`
	Completion    int       `json:"completion"`
	UpdatedAt     time.Time `json:"updatedAt"`
	Revision      string    `json:"revision"`
}

func normalizeWorkingPlanID(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		value = defaultWorkingPlanID
	}
	if !utf8.ValidString(value) || len(value) > maxWorkingPlanIDBytes || value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00") {
		return "", fmt.Errorf("planId is invalid or exceeds %d bytes", maxWorkingPlanIDBytes)
	}
	return value, nil
}

func (c *Client) workingPlanControl(ctx context.Context, action string, input workingContextParams, projectPath, planID string, git workingContextGitFacts) (workingContextResult, error) {
	switch action {
	case "plan.init":
		return c.workingPlanInit(ctx, input, projectPath, planID, git)
	case "plan.get":
		return c.workingPlanGet(action, projectPath, planID, git)
	case "plan.list":
		return c.workingPlanList(projectPath, git)
	case "task.update":
		return c.workingTaskUpdate(input, projectPath, planID, git)
	case "plan.sync", "markdown.list", "markdown.read", "markdown.append":
		return c.workingMarkdownControl(ctx, action, input, projectPath, planID, git)
	case "progress.watch":
		return c.workingProgressWatch(ctx, input, projectPath, planID, git)
	default:
		return workingContextResult{}, fmt.Errorf("unsupported working context action %q", action)
	}
}

func (c *Client) workingPlanInit(ctx context.Context, input workingContextParams, projectPath, planID string, git workingContextGitFacts) (workingContextResult, error) {
	workingContextWriteMu.Lock()
	defer workingContextWriteMu.Unlock()
	path := c.workingContextPathForPlan(projectPath, planID)
	existing, raw, exists, err := loadWorkingContext(path, projectPath)
	if err != nil {
		return workingContextResult{}, err
	}
	if exists && existing.PlanID != planID {
		return workingContextResult{}, fmt.Errorf("stored working plan does not match planId")
	}
	if exists && input.ExpectedRevision == "" {
		if input.InitializeMarkdown {
			if _, err := initializeWorkingMarkdownWorkspace(projectPath, existing.MarkdownRoot); err != nil {
				return workingContextResult{}, err
			}
		}
		return workingContextResult{Action: "plan.init", Exists: true, State: &existing, CurrentGit: git, Revision: workingContextRevision(raw)}, nil
	}
	goal, err := normalizeWorkingContextText(input.Goal, maxWorkingContextGoalBytes, true)
	if err != nil {
		return workingContextResult{}, fmt.Errorf("goal: %w", err)
	}
	title, err := normalizeWorkingContextText(input.Title, 512, false)
	if err != nil {
		return workingContextResult{}, fmt.Errorf("title: %w", err)
	}
	targetVersion, err := normalizeWorkingContextText(input.TargetVersion, 128, false)
	if err != nil {
		return workingContextResult{}, fmt.Errorf("targetVersion: %w", err)
	}
	markdownRoot, err := normalizeWorkingMarkdownRoot(projectPath, input.MarkdownRoot)
	if err != nil {
		return workingContextResult{}, err
	}
	tasks, err := normalizeWorkingPlanTasks(input.Tasks)
	if err != nil {
		return workingContextResult{}, err
	}
	state := workingContextState{
		SchemaVersion: workingContextSchemaVersion, ProjectPath: projectPath, PlanID: planID,
		Title: title, TargetVersion: targetVersion, MarkdownRoot: markdownRoot, Goal: goal,
		Baseline: workingContextBaseline{Branch: strings.TrimSpace(input.BaselineBranch), Commit: strings.TrimSpace(input.BaselineCommit)},
		Tasks:    tasks, UpdatedAt: time.Now().UTC(),
	}
	if state.Baseline.Branch == "" && state.Baseline.Commit == "" && git.IsRepository {
		state.Baseline = workingContextBaseline{Branch: git.Branch, Commit: git.Head}
	}
	if input.InitializeMarkdown {
		if _, err := initializeWorkingMarkdownWorkspace(projectPath, markdownRoot); err != nil {
			return workingContextResult{}, err
		}
	}
	encoded, err := marshalWorkingPlan(state)
	if err != nil {
		return workingContextResult{}, err
	}
	if err := atomicWriteWorkingContextCAS(path, encoded, input.ExpectedRevision); err != nil {
		return workingContextResult{}, err
	}
	return workingContextResult{Action: "plan.init", Exists: true, State: &state, CurrentGit: git, Revision: workingContextRevision(encoded)}, nil
}

func (c *Client) workingPlanGet(action, projectPath, planID string, git workingContextGitFacts) (workingContextResult, error) {
	state, raw, exists, err := loadWorkingContext(c.workingContextPathForPlan(projectPath, planID), projectPath)
	if err != nil {
		return workingContextResult{}, err
	}
	result := workingContextResult{Action: action, Exists: exists, CurrentGit: git}
	if exists {
		if state.PlanID != planID {
			return workingContextResult{}, fmt.Errorf("stored working plan does not match planId")
		}
		result.State, result.Revision = &state, workingContextRevision(raw)
	}
	return result, nil
}

func (c *Client) workingPlanList(projectPath string, git workingContextGitFacts) (workingContextResult, error) {
	root := filepath.Join(c.cfg.DataDir, "working-contexts")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return workingContextResult{Action: "plan.list", CurrentGit: git, Plans: []workingPlanSummary{}}, nil
	}
	if err != nil {
		return workingContextResult{}, err
	}
	plans := make([]workingPlanSummary, 0)
	for _, entry := range entries {
		if len(plans) >= maxWorkingPlanListEntries || entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(root, entry.Name()))
		if readErr != nil || len(raw) == 0 || len(raw) > maxWorkingContextBytes {
			continue
		}
		var state workingContextState
		if json.Unmarshal(raw, &state) != nil || !samePath(state.ProjectPath, projectPath) {
			continue
		}
		if state.PlanID == "" {
			state.PlanID = defaultWorkingPlanID
		}
		plans = append(plans, workingPlanSummary{PlanID: state.PlanID, Title: state.Title, TargetVersion: state.TargetVersion, Goal: state.Goal, TaskCount: len(state.Tasks), Completion: workingPlanCompletion(state.Tasks), UpdatedAt: state.UpdatedAt, Revision: workingContextRevision(raw)})
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].PlanID < plans[j].PlanID })
	return workingContextResult{Action: "plan.list", CurrentGit: git, Plans: plans}, nil
}

func (c *Client) workingTaskUpdate(input workingContextParams, projectPath, planID string, git workingContextGitFacts) (workingContextResult, error) {
	if input.ExpectedRevision == "" {
		return workingContextResult{}, fmt.Errorf("expectedRevision is required for task.update")
	}
	workingContextWriteMu.Lock()
	defer workingContextWriteMu.Unlock()
	path := c.workingContextPathForPlan(projectPath, planID)
	state, _, exists, err := loadWorkingContext(path, projectPath)
	if err != nil || !exists {
		if err == nil {
			err = os.ErrNotExist
		}
		return workingContextResult{}, err
	}
	taskID, err := boundedSafeText(input.TaskID, maxWorkingTaskIDBytes, true)
	if err != nil {
		return workingContextResult{}, fmt.Errorf("taskId: %w", err)
	}
	index := -1
	for i := range state.Tasks {
		if state.Tasks[i].ID == taskID {
			index = i
			break
		}
	}
	if index < 0 {
		if len(state.Tasks) >= maxWorkingPlanTasks {
			return workingContextResult{}, fmt.Errorf("plan exceeds %d tasks", maxWorkingPlanTasks)
		}
		title, titleErr := boundedSafeText(input.TaskTitle, maxWorkingTaskTitleBytes, true)
		if titleErr != nil {
			return workingContextResult{}, fmt.Errorf("taskTitle: %w", titleErr)
		}
		state.Tasks = append(state.Tasks, workingPlanTask{ID: taskID, Title: title, Status: "pending"})
		index = len(state.Tasks) - 1
	}
	task := &state.Tasks[index]
	if strings.TrimSpace(input.TaskTitle) != "" {
		task.Title, err = boundedSafeText(input.TaskTitle, maxWorkingTaskTitleBytes, true)
		if err != nil {
			return workingContextResult{}, err
		}
	}
	if input.TaskStatus != "" {
		status := strings.TrimSpace(input.TaskStatus)
		if !workingTaskStatusValid(status) {
			return workingContextResult{}, fmt.Errorf("taskStatus must be pending, in_progress, blocked, or done")
		}
		task.Status = status
	}
	if input.Completion != nil {
		if *input.Completion < 0 || *input.Completion > 100 {
			return workingContextResult{}, fmt.Errorf("completion must be between 0 and 100")
		}
		task.Completion = *input.Completion
	} else if task.Status == "done" {
		task.Completion = 100
	}
	if input.BlockedReason != "" {
		task.BlockedReason, err = boundedSafeText(input.BlockedReason, maxWorkingEvidenceBytes, false)
		if err != nil {
			return workingContextResult{}, err
		}
	} else if task.Status != "blocked" {
		task.BlockedReason = ""
	}
	if input.Evidence != nil {
		if len(task.Evidences) >= maxWorkingTaskEvidences {
			return workingContextResult{}, fmt.Errorf("task exceeds %d evidences", maxWorkingTaskEvidences)
		}
		evidence, evidenceErr := normalizeWorkingEvidence(*input.Evidence)
		if evidenceErr != nil {
			return workingContextResult{}, evidenceErr
		}
		task.Evidences = append(task.Evidences, evidence)
	}
	now := time.Now().UTC()
	task.UpdatedAt, state.UpdatedAt = now, now
	encoded, err := marshalWorkingPlan(state)
	if err != nil {
		return workingContextResult{}, err
	}
	if err := atomicWriteWorkingContextCAS(path, encoded, input.ExpectedRevision); err != nil {
		return workingContextResult{}, err
	}
	return workingContextResult{Action: "task.update", Exists: true, State: &state, CurrentGit: git, Revision: workingContextRevision(encoded), Changed: true}, nil
}

func (c *Client) workingProgressWatch(ctx context.Context, input workingContextParams, projectPath, planID string, git workingContextGitFacts) (workingContextResult, error) {
	if input.WaitSeconds < 0 || input.WaitSeconds > 15 {
		return workingContextResult{}, fmt.Errorf("waitSeconds must be between 0 and 15")
	}
	deadline := time.Now().Add(time.Duration(input.WaitSeconds) * time.Second)
	for {
		result, err := c.workingPlanGet("progress.watch", projectPath, planID, inspectWorkingContextGit(ctx, projectPath))
		if err != nil {
			return workingContextResult{}, err
		}
		result.Changed = result.Revision != input.SinceRevision
		if result.Changed || input.WaitSeconds == 0 || !time.Now().Before(deadline) {
			return result, nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return workingContextResult{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func normalizeWorkingPlanTasks(inputs []workingPlanTaskInput) ([]workingPlanTask, error) {
	if len(inputs) > maxWorkingPlanTasks {
		return nil, fmt.Errorf("plan exceeds %d tasks", maxWorkingPlanTasks)
	}
	out := make([]workingPlanTask, 0, len(inputs))
	seen := map[string]bool{}
	now := time.Now().UTC()
	for _, input := range inputs {
		id, err := boundedSafeText(input.ID, maxWorkingTaskIDBytes, true)
		if err != nil {
			return nil, fmt.Errorf("task id: %w", err)
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate task id %q", id)
		}
		seen[id] = true
		title, err := boundedSafeText(input.Title, maxWorkingTaskTitleBytes, true)
		if err != nil {
			return nil, fmt.Errorf("task title: %w", err)
		}
		status := strings.TrimSpace(input.Status)
		if status == "" {
			status = "pending"
		}
		if !workingTaskStatusValid(status) {
			return nil, fmt.Errorf("invalid task status %q", status)
		}
		if input.Completion < 0 || input.Completion > 100 {
			return nil, fmt.Errorf("task completion must be between 0 and 100")
		}
		if len(input.Evidences) > maxWorkingTaskEvidences {
			return nil, fmt.Errorf("task exceeds %d evidences", maxWorkingTaskEvidences)
		}
		task := workingPlanTask{ID: id, Title: title, Status: status, Completion: input.Completion, UpdatedAt: now}
		if status == "done" && task.Completion == 0 {
			task.Completion = 100
		}
		task.BlockedReason, err = boundedSafeText(input.BlockedReason, maxWorkingEvidenceBytes, false)
		if err != nil {
			return nil, err
		}
		for _, raw := range input.Evidences {
			evidence, err := normalizeWorkingEvidence(raw)
			if err != nil {
				return nil, err
			}
			task.Evidences = append(task.Evidences, evidence)
		}
		out = append(out, task)
	}
	return out, nil
}

func normalizeWorkingEvidence(input workingPlanEvidenceInput) (workingPlanEvidence, error) {
	summary, err := boundedSafeText(input.Summary, maxWorkingEvidenceBytes, true)
	if err != nil {
		return workingPlanEvidence{}, fmt.Errorf("evidence summary: %w", err)
	}
	kind, err := boundedSafeText(input.Kind, 64, false)
	if err != nil {
		return workingPlanEvidence{}, fmt.Errorf("evidence kind: %w", err)
	}
	reference, err := boundedSafeText(input.Reference, 1024, false)
	if err != nil {
		return workingPlanEvidence{}, fmt.Errorf("evidence reference: %w", err)
	}
	return workingPlanEvidence{Summary: summary, Kind: kind, Reference: reference, AcceptedAt: time.Now().UTC()}, nil
}

func workingTaskStatusValid(status string) bool {
	return status == "pending" || status == "in_progress" || status == "blocked" || status == "done"
}

func workingPlanCompletion(tasks []workingPlanTask) int {
	if len(tasks) == 0 {
		return 0
	}
	total := 0
	for _, task := range tasks {
		total += task.Completion
	}
	return total / len(tasks)
}

func marshalWorkingPlan(state workingContextState) ([]byte, error) {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if len(raw) > maxWorkingContextBytes {
		return nil, fmt.Errorf("working plan exceeds %d bytes", maxWorkingContextBytes)
	}
	return raw, nil
}

func boundedSafeText(value string, limit int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fmt.Errorf("value is required")
	}
	if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 || len(value) > limit {
		return "", fmt.Errorf("value is invalid or exceeds %d bytes", limit)
	}
	if containsSensitiveWorkspaceMaterial(value) {
		return "", fmt.Errorf("value appears to contain sensitive or raw conversation material")
	}
	return value, nil
}

func containsSensitiveWorkspaceMaterial(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"bearer ", "full prompt:", "chat transcript:", "raw upstream error:", "<|user|>", "<|assistant|>"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	for _, key := range []string{"api_key", "apikey", "access_token", "refresh_token", "authorization", "cookie"} {
		for _, sep := range []string{"=", ":"} {
			if strings.Contains(lower, key+sep) || strings.Contains(lower, key+" "+sep) {
				return true
			}
		}
	}
	return false
}
