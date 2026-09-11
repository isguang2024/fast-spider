package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

type nativeWorkspaceCompletion struct {
	Task      nativeRunnerTask
	Operation string
	Workspace *nativeRunnerWorkspace
	Err       error
	RootKey   string
}

func nativeWorkspaceRepository(p nativeRunnerProject, t nativeRunnerTask) string {
	if t.Workspace != nil && t.Workspace.Root != "" {
		return t.Workspace.Root
	}
	return p.Root
}

func nativeValidateWorkspaceRequest(t *nativeRunnerTask) error {
	if t.Workspace == nil {
		return nil
	}
	mode := t.Workspace.Mode
	if mode == "" || mode == "shared" {
		t.Workspace = nil
		return nil
	}
	if mode != "worktree" || strings.TrimSpace(t.Scope) == "" {
		return errors.New("worktree requires a nonempty authorized write scope")
	}
	// Plans select policy only. Paths, branches and integration proof belong to Go.
	t.Workspace = &nativeRunnerWorkspace{Mode: "worktree"}
	return nil
}
func nativeWorkspaceIsolated(t nativeRunnerTask) bool {
	return t.Workspace != nil && t.Workspace.Mode == "worktree" && t.Workspace.State != "integrated" && t.Workspace.State != "cleaned" && t.State != "integrating"
}
func nativeWorkspaceNeedsPrepare(t nativeRunnerTask) bool {
	return nativeWorkspaceIsolated(t) && (t.Workspace.Path == "" || t.Workspace.State == "error" || t.Workspace.State == "preparing")
}
func nativeTaskWorkingDirectory(p nativeRunnerProject, t nativeRunnerTask) string {
	if nativeWorkspaceIsolated(t) && t.Workspace.Path != "" {
		if t.Workspace.Root != "" {
			projectRoot, err := filepath.EvalSymlinks(p.Root)
			if err == nil {
				if rel, err := filepath.Rel(t.Workspace.Root, projectRoot); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return filepath.Join(t.Workspace.Path, rel)
				}
			}
		}
		return t.Workspace.Path
	}
	return p.Root
}
func nativeTaskContextPath(p nativeRunnerProject, t nativeRunnerTask, path string) (string, error) {
	canonicalRoot, err := filepath.EvalSymlinks(p.Root)
	if err != nil {
		return "", err
	}
	full, err := nativePath(p.Root, path)
	if err != nil && canonicalRoot != p.Root {
		full, err = nativePath(canonicalRoot, path)
	}
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(canonicalRoot, full)
	if err != nil {
		return "", err
	}
	return nativePath(nativeTaskWorkingDirectory(p, t), rel)
}
func nativeTaskWriteScope(p nativeRunnerProject, t nativeRunnerTask) string {
	if t.Scope == "" {
		return ""
	}
	full, err := nativeTaskContextPath(p, t, t.Scope)
	if err != nil {
		return t.Scope
	}
	return full
}
func nativeTaskCheck(p nativeRunnerProject, t nativeRunnerTask, check nativeRunnerCheck) nativeRunnerCheck {
	if filepath.IsAbs(check.Cwd) {
		if path, err := nativeTaskContextPath(p, t, check.Cwd); err == nil {
			check.Cwd = path
		}
	}
	return check
}

// Filesystem/Git work is asynchronous. Ledger intent survives process restart;
// exact task/round matching prevents late work from reviving cancelled tasks.
func (r *nativeRunner) startWorkspace(ctx context.Context, p nativeRunnerProject, t nativeRunnerTask, operation string) bool {
	if r.workspaceInFlight == nil {
		r.workspaceInFlight = map[string]bool{}
		r.workspaceDone = make(chan nativeWorkspaceCompletion, 32)
	}
	if r.workspaceInFlight[t.ID] {
		return false
	}
	rootKey := "integrate:" + strings.ToLower(filepath.Clean(nativeWorkspaceRepository(p, t)))
	if operation == "integrate" && r.workspaceInFlight[rootKey] {
		return false
	}
	if t.Workspace != nil {
		copy := *t.Workspace
		t.Workspace = &copy
		t.Workspace.ProjectID = p.ID
		t.Workspace.TaskID = t.ID
		t.Workspace.Title = t.Title
	}
	r.workspaceInFlight[t.ID] = true
	if operation == "integrate" {
		r.workspaceInFlight[rootKey] = true
	}
	r.lifecycleWG.Add(1)
	go func() {
		defer r.lifecycleWG.Done()
		callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		out := nativeWorkspaceCompletion{Task: t, Operation: operation, RootKey: rootKey}
		switch operation {
		case "inspect":
			out.Workspace = t.Workspace
			out.Err = out.Workspace.Inspect(callCtx)
			if out.Err == nil {
				out.Workspace.SealedCommit = out.Workspace.HeadCommit
			}
		case "prepare":
			out.Workspace, out.Err = nativePrepareWorkspace(callCtx, p.Root, p.ID, t.ID, t.Title, t.Scope, t.Workspace)
		case "integrate":
			out.Workspace, out.Err = nativeIntegrateWorkspace(callCtx, p.Root, t.Scope, t.Workspace)
		case "cleanup":
			out.Workspace, out.Err = nativeCleanupWorkspace(callCtx, p.Root, t.Workspace)
		}
		select {
		case r.workspaceDone <- out:
			r.Wake()
		case <-ctx.Done():
		}
	}()
	return true
}

func (r *nativeRunner) drainWorkspace(ctx context.Context) error {
	for {
		select {
		case out := <-r.workspaceDone:
			delete(r.workspaceInFlight, out.Task.ID)
			if out.Operation == "integrate" {
				delete(r.workspaceInFlight, out.RootKey)
			}
			_, tasks, err := r.read(ctx, out.Task.ProjectID)
			if err != nil {
				return err
			}
			for _, t := range tasks {
				if t.ID != out.Task.ID || t.Round != out.Task.Round {
					continue
				}
				if out.Workspace != nil {
					t.Workspace = out.Workspace
				}
				cancelled := t.Cancellation != nil || t.State == "cancelled" || t.State == "canceling"
				if out.Err != nil {
					if t.Workspace != nil {
						t.Workspace.Error = out.Err.Error()
						if out.Operation == "cleanup" {
							t.Workspace.State = "retained"
						}
					}
					if out.Operation != "cleanup" && !cancelled {
						t.LastError = out.Err.Error()
						if out.Operation == "prepare" {
							t.State = "deferred"
							t.DeferredReason = "工作目录准备失败：" + out.Err.Error()
							t.WaitReview = nil
						} else {
							t.State = "returned"
							t.Workspace.State = "conflict"
						}
					}
				} else {
					if t.Workspace != nil {
						t.Workspace.Error = ""
					}
					switch out.Operation {
					case "inspect":
						if !cancelled {
							t.LastError = ""
						}
					case "prepare":
						if !cancelled {
							t.State = "queued"
							t.LastError = ""
							t.NextAt = 0
						}
					case "integrate":
						if !cancelled {
							t.Workspace.State = "integrated"
							t.State = "integrating"
							t.Validations = map[string]nativeRunnerValidation{}
							t.LastError = ""
							t.NextAt = 0
						}
					case "cleanup":
						t.Workspace.State = "cleaned"
					}
				}
				if err = r.saveTask(ctx, t, "workspace_"+out.Operation+"_completed"); err != nil {
					return err
				}
			}
		default:
			return nil
		}
	}
}

func (r *nativeRunner) tickWorkspaces(ctx context.Context, p nativeRunnerProject, tasks, global []nativeRunnerTask) error {
	for _, t := range tasks {
		if t.Workspace == nil || t.Workspace.Mode != "worktree" {
			continue
		}
		switch t.State {
		case "returned":
			if nativeWorkspaceIsolated(t) && t.Result != nil && t.Result.Outcome == "completed" && t.Workspace.State != "ready" && t.Workspace.State != "conflict" {
				r.startWorkspace(ctx, p, t, "inspect")
			}
		case "workspace_preparing":
			r.startWorkspace(ctx, p, t, "prepare")
		case "awaiting_integration":
			if p.Archived || p.State == "cancelled" || p.State == "canceling" {
				continue
			}
			blocked := false
			for _, other := range global {
				if other.ID != t.ID && !nativeWorkspaceIsolated(other) && (nativeHolds(other) || nativeChecking(other) || other.State == "integrating") && nativeScopeOverlap(nativeWorkspaceRepository(p, t), other.Scope) {
					blocked = true
					break
				}
			}
			if blocked || r.workspaceInFlight["integrate:"+strings.ToLower(filepath.Clean(nativeWorkspaceRepository(p, t)))] {
				continue
			}
			t.State = "integrating"
			if err := r.saveTask(ctx, t, "workspace_integration_started"); err != nil {
				return err
			}
			r.startWorkspace(ctx, p, t, "integrate")
		case "integrating":
			if t.Workspace.State != "integrated" {
				// Restart may have interrupted the ff operation after Git committed it.
				// The primitive recognizes ancestry and completes idempotently.
				r.startWorkspace(ctx, p, t, "integrate")
				continue
			}
			if nativeChecking(t) {
				continue
			}
			passed := true
			for _, name := range t.Checks {
				if t.Validations[name].State != "passed" {
					passed = false
				}
			}
			if passed {
				t.State = "accepted"
				t.AcceptedVersion = t.GoalVersion
			} else {
				t.State = "returned"
				t.LastError = "集成后的受影响检查失败；在原任务分支修复并重新提交"
			}
			if err := r.saveTask(ctx, t, "workspace_integration_checked"); err != nil {
				return err
			}
		case "accepted", "cancelled":
			if t.Workspace.State != "cleaned" && t.Workspace.State != "retained" {
				r.startWorkspace(ctx, p, t, "cleanup")
			}
		}
	}
	return nil
}
