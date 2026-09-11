package agent

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"time"
)

const nativeRunnerWaitReviewDelay = 5 * time.Minute

type nativeRunnerWaitFor struct {
	TaskIDs []string `json:"taskIds"`
	Paths   []string `json:"paths"`
}

type nativeRunnerWaitReview struct {
	Fingerprint string `json:"fingerprint"`
	NextAt      int64  `json:"nextAt"`
	Reviewed    bool   `json:"reviewed"`
	Evidence    string `json:"evidence"`
}

// nativeValidateWaitFor validates and normalizes a structured wait condition.
// keys contains the current project's semantic task keys mapped to task IDs.
func nativeValidateWaitFor(p nativeRunnerProject, condition *nativeRunnerWaitFor, keys map[string]string) error {
	if condition == nil {
		return nil
	}
	if len(condition.TaskIDs) > 16 || len(condition.Paths) > 16 {
		return errors.New("waitFor accepts at most 16 task IDs and 16 paths")
	}
	for i, value := range condition.TaskIDs {
		value = strings.TrimSpace(value)
		if value == "" {
			return errors.New("waitFor task ID cannot be empty")
		}
		if id, ok := keys[value]; ok {
			condition.TaskIDs[i] = id
			continue
		}
		found := false
		for _, id := range keys {
			if id == value {
				found = true
				break
			}
		}
		if !found {
			return errors.New("waitFor task does not belong to the project: " + value)
		}
		condition.TaskIDs[i] = value
	}
	for i, value := range condition.Paths {
		if strings.TrimSpace(value) == "" {
			return errors.New("waitFor path cannot be empty")
		}
		path, err := nativePath(p.Root, value)
		if err != nil {
			return err
		}
		condition.Paths[i] = path
	}
	return nil
}

// reviewDeferredWaits performs only cheap local observation. It records a
// baseline first, then clears planning state once when the structured wait
// facts change or the first review deadline expires. It never changes task
// After/State and never calls the Cloud backend.
func (r *nativeRunner) reviewDeferredWaits(ctx context.Context, p *nativeRunnerProject, tasks, global []nativeRunnerTask) error {
	if p == nil {
		return errors.New("native runner wait review is unavailable")
	}
	if p.Paused || p.Archived || p.State == "cancelled" || p.State == "canceling" {
		return nil
	}
	if r == nil || r.db == nil {
		return errors.New("native runner wait review is unavailable")
	}
	now := r.now().Unix()
	changed := make([]nativeRunnerTask, 0)
	type waitEvent struct {
		taskID      string
		kind        string
		fingerprint string
		evidence    string
	}
	events := make([]waitEvent, 0)
	replan := false
	for i := range tasks {
		task := &tasks[i]
		if task.State != "deferred" || nativeHolds(*task) {
			continue
		}
		fingerprint := nativeDeferredWaitFingerprint(*p, *task, tasks, global)
		if task.WaitReview == nil || strings.TrimSpace(task.WaitReview.Fingerprint) == "" {
			task.WaitReview = &nativeRunnerWaitReview{
				Fingerprint: fingerprint,
				NextAt:      now + int64(nativeRunnerWaitReviewDelay/time.Second),
				Evidence:    "已记录结构化等待条件基线；到期后复核。",
			}
			changed = append(changed, *task)
			events = append(events, waitEvent{taskID: task.ID, kind: "wait_review_baseline", fingerprint: fingerprint, evidence: task.WaitReview.Evidence})
			continue
		}
		previous := task.WaitReview.Fingerprint
		if previous != fingerprint {
			task.WaitReview = &nativeRunnerWaitReview{Fingerprint: fingerprint, Reviewed: true, Evidence: "等待条件发生新变化；已请求规划复核。"}
			changed = append(changed, *task)
			events = append(events, waitEvent{taskID: task.ID, kind: "wait_review_triggered", fingerprint: fingerprint, evidence: task.WaitReview.Evidence})
			replan = true
			continue
		}
		if !task.WaitReview.Reviewed && (task.WaitReview.NextAt == 0 || task.WaitReview.NextAt <= now) {
			task.WaitReview = &nativeRunnerWaitReview{Fingerprint: fingerprint, Reviewed: true, Evidence: "等待条件在复核窗口内保持静默；已请求规划复核。"}
			changed = append(changed, *task)
			events = append(events, waitEvent{taskID: task.ID, kind: "wait_review_triggered", fingerprint: fingerprint, evidence: task.WaitReview.Evidence})
			replan = true
		}
	}
	if len(changed) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, task := range changed {
		if err = nativeSave(tx, "runner_tasks", task.ID, p.ID, task); err != nil {
			return err
		}
	}
	if replan {
		p.PlanBasis = ""
		p.Questions = nil
		p.Notice = nil
		p.NextPlanAt = 0
	}
	for _, event := range events {
		if err = r.event(tx, p.ID, event.kind, map[string]any{"taskId": event.taskID, "fingerprint": event.fingerprint, "evidence": event.evidence}); err != nil {
			return err
		}
	}
	if err = nativeSave(tx, "runner_projects", p.ID, "", *p); err != nil {
		return err
	}
	return tx.Commit()
}

type nativeWaitTaskFact struct {
	ID              string `json:"id"`
	State           string `json:"state"`
	Round           int    `json:"round"`
	ResultEventID   string `json:"resultEventId,omitempty"`
	ResultOutcome   string `json:"resultOutcome,omitempty"`
	ResultPath      string `json:"resultPath,omitempty"`
	ResultSHA256    string `json:"resultSHA256,omitempty"`
	ResultErrorCode string `json:"resultErrorCode,omitempty"`
	ResultTerminal  bool   `json:"resultTerminal,omitempty"`
	Missing         bool   `json:"missing,omitempty"`
}

type nativeWaitPathFact struct {
	Path      string `json:"path"`
	Available bool   `json:"available"`
	Missing   bool   `json:"missing"`
	Size      int64  `json:"size,omitempty"`
	ModTime   int64  `json:"modTime,omitempty"`
}

type nativeWaitHolderFact struct {
	ProjectID string `json:"projectId"`
	TaskID    string `json:"taskId"`
	Round     int    `json:"round"`
	Scope     string `json:"scope,omitempty"`
	State     string `json:"state"`
	SessionID string `json:"sessionId,omitempty"`
}

type nativeDeferredWaitFacts struct {
	TaskID       string                 `json:"taskId"`
	WaitFor      *nativeRunnerWaitFor   `json:"waitFor,omitempty"`
	WatchedTasks []nativeWaitTaskFact   `json:"watchedTasks,omitempty"`
	WatchedPaths []nativeWaitPathFact   `json:"watchedPaths,omitempty"`
	AreaTasks    []nativeWaitTaskFact   `json:"areaTasks,omitempty"`
	Holders      []nativeWaitHolderFact `json:"holders,omitempty"`
}

func nativeDeferredWaitFingerprint(p nativeRunnerProject, task nativeRunnerTask, tasks, global []nativeRunnerTask) string {
	facts := nativeDeferredWaitFacts{TaskID: task.ID}
	if task.WaitFor != nil {
		condition := &nativeRunnerWaitFor{TaskIDs: append([]string(nil), task.WaitFor.TaskIDs...), Paths: append([]string(nil), task.WaitFor.Paths...)}
		sort.Strings(condition.TaskIDs)
		sort.Strings(condition.Paths)
		facts.WaitFor = condition
		for _, id := range condition.TaskIDs {
			facts.WatchedTasks = append(facts.WatchedTasks, nativeWaitTaskFactForID(id, task.ProjectID, tasks))
		}
		for _, path := range condition.Paths {
			facts.WatchedPaths = append(facts.WatchedPaths, nativeWaitPathFactForPath(p, path))
		}
	} else {
		for _, candidate := range tasks {
			if candidate.ProjectID == task.ProjectID && candidate.Kind != "planner" && candidate.ID != task.ID {
				facts.AreaTasks = append(facts.AreaTasks, nativeWaitTaskFactFor(candidate))
			}
		}
		sort.Slice(facts.AreaTasks, func(i, j int) bool { return facts.AreaTasks[i].ID < facts.AreaTasks[j].ID })
	}
	for _, candidate := range global {
		overlap := nativeScopeOverlap(task.Scope, candidate.Scope)
		if task.WaitFor != nil {
			for _, path := range task.WaitFor.Paths {
				overlap = overlap || nativeScopeOverlap(path, candidate.Scope)
			}
		}
		if candidate.ID == task.ID || !nativeHolds(candidate) || !overlap {
			continue
		}
		sessionID := ""
		if candidate.Receipt != nil {
			sessionID = candidate.Receipt.SessionID
		} else if candidate.Request != nil {
			sessionID = candidate.Request.TargetSessionID
		}
		facts.Holders = append(facts.Holders, nativeWaitHolderFact{ProjectID: candidate.ProjectID, TaskID: candidate.ID, Round: candidate.Round, Scope: candidate.Scope, State: candidate.State, SessionID: sessionID})
	}
	sort.Slice(facts.WatchedTasks, func(i, j int) bool { return facts.WatchedTasks[i].ID < facts.WatchedTasks[j].ID })
	sort.Slice(facts.WatchedPaths, func(i, j int) bool { return facts.WatchedPaths[i].Path < facts.WatchedPaths[j].Path })
	sort.Slice(facts.Holders, func(i, j int) bool {
		if facts.Holders[i].ProjectID != facts.Holders[j].ProjectID {
			return facts.Holders[i].ProjectID < facts.Holders[j].ProjectID
		}
		return facts.Holders[i].TaskID < facts.Holders[j].TaskID
	})
	// Writes by a known current owner are progress, not repeated unblock
	// events. Re-evaluate the files once that owner releases its scope.
	if len(facts.Holders) > 0 {
		facts.WatchedPaths = nil
	}
	return nativeHash(facts)
}

func nativeWaitTaskFactForID(id, projectID string, tasks []nativeRunnerTask) nativeWaitTaskFact {
	for _, task := range tasks {
		if task.ID == id && task.ProjectID == projectID {
			return nativeWaitTaskFactFor(task)
		}
	}
	return nativeWaitTaskFact{ID: id, Missing: true}
}

func nativeWaitTaskFactFor(task nativeRunnerTask) nativeWaitTaskFact {
	fact := nativeWaitTaskFact{ID: task.ID, State: task.State, Round: task.Round}
	if task.Result != nil {
		fact.ResultEventID = task.Result.EventID
		fact.ResultOutcome = task.Result.Outcome
		fact.ResultPath = task.Result.Path
		fact.ResultSHA256 = task.Result.SHA256
		fact.ResultErrorCode = task.Result.ErrorCode
		fact.ResultTerminal = task.Result.Terminal
	}
	return fact
}

func nativeWaitPathFactForPath(p nativeRunnerProject, path string) nativeWaitPathFact {
	resolved, err := nativePath(p.Root, path)
	if err != nil {
		return nativeWaitPathFact{Path: "<invalid>", Missing: true}
	}
	fact := nativeWaitPathFact{Path: resolved}
	info, err := os.Stat(resolved)
	if err != nil {
		fact.Missing = os.IsNotExist(err)
		return fact
	}
	fact.Available = true
	fact.Size = info.Size()
	fact.ModTime = info.ModTime().UnixNano()
	return fact
}
