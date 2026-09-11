package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Requirements name immutable published artifacts; After continues to mean
// final acceptance. Publishing a contract never accepts the producing task.
type nativeRunnerRequirement struct {
	TaskID  string `json:"taskId"`
	Key     string `json:"key"`
	Version string `json:"version"`
}
type nativeRunnerOutput struct {
	Key     string `json:"key"`
	Version string `json:"version"`
	Summary string `json:"summary"`
	Path    string `json:"path"`
	Commit  string `json:"commit,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Round   int    `json:"round"`
}
type nativeRunnerQueueReason struct {
	Code    string   `json:"code"`
	Summary string   `json:"summary"`
	TaskIDs []string `json:"taskIds,omitempty"`
	Paths   []string `json:"paths,omitempty"`
}

func nativeValidateRequirements(requirements []nativeRunnerRequirement) error {
	if len(requirements) > 16 {
		return errors.New("too many stage requirements")
	}
	seen := map[string]bool{}
	for _, requirement := range requirements {
		if strings.TrimSpace(requirement.TaskID) == "" || strings.TrimSpace(requirement.Key) == "" || strings.TrimSpace(requirement.Version) == "" || len(requirement.Key) > 128 || len(requirement.Version) > 128 {
			return errors.New("stage requirement needs taskId, key and exact version")
		}
		key := requirement.TaskID + "/" + requirement.Key
		if seen[key] {
			return errors.New("duplicate stage requirement")
		}
		seen[key] = true
	}
	return nil
}

func (r *nativeRunner) contextQueueReasons(ctx context.Context, projectID string) (map[string]*nativeRunnerQueueReason, error) {
	p, _, err := r.read(ctx, projectID)
	if err != nil {
		return nil, err
	}
	all, err := r.readAllTasks(ctx)
	if err != nil {
		return nil, err
	}
	return nativeBuildSchedulingSnapshot(all, []nativeRunnerProject{p}, r.globalConcurrency, r.now()).QueueReasons, nil
}

func nativeRequirementsMissing(t nativeRunnerTask, tasks []nativeRunnerTask) []string {
	missing := []string{}
	for _, requirement := range t.Requires {
		found := false
		for _, producer := range tasks {
			if producer.ID != requirement.TaskID || producer.ProjectID != t.ProjectID || producer.GoalVersion != t.GoalVersion || producer.State == "cancelled" || producer.State == "canceling" {
				continue
			}
			for _, output := range producer.Outputs {
				if output.Key == requirement.Key && output.Version == requirement.Version && output.Round == producer.Round && output.SHA256 != "" {
					found = true
					break
				}
			}
		}
		if !found {
			missing = append(missing, requirement.TaskID)
		}
	}
	return missing
}

func nativeTaskScopeConflict(a, b nativeRunnerTask) bool {
	if a.ID == b.ID {
		return false
	}
	barrier := func(task nativeRunnerTask) bool {
		return task.Workspace != nil && (task.State == "awaiting_integration" || task.State == "integrating")
	}
	if barrier(a) && !nativeWorkspaceIsolated(b) {
		return nativeScopeOverlap(a.Workspace.Root, b.Scope)
	}
	if barrier(b) && !nativeWorkspaceIsolated(a) {
		return nativeScopeOverlap(b.Workspace.Root, a.Scope)
	}
	if nativeWorkspaceIsolated(a) || nativeWorkspaceIsolated(b) {
		// Separate worktrees isolate physical writes even before their lazy creation.
		// Integrated work returns to the main scope for checks and final acceptance.
		return false
	}
	return nativeScopeOverlap(a.Scope, b.Scope)
}

func nativeQueueReason(t nativeRunnerTask, p nativeRunnerProject, tasks []nativeRunnerTask, s nativeRunnerSchedulingSnapshot, now int64) *nativeRunnerQueueReason {
	reason := func(code, summary string, ids []string) *nativeRunnerQueueReason {
		return &nativeRunnerQueueReason{Code: code, Summary: summary, TaskIDs: ids}
	}
	if t.State == "awaiting_integration" || t.State == "integrating" {
		owners := []string{}
		for _, other := range tasks {
			if other.ID != t.ID && !nativeWorkspaceIsolated(other) && (nativeHolds(other) || nativeChecking(other) || other.State == "integrating") && nativeScopeOverlap(nativeWorkspaceRepository(p, t), other.Scope) {
				owners = append(owners, other.ID)
			}
		}
		if len(owners) > 0 {
			return reason("integration", "等待同仓库共享工作区释放后集成", owners)
		}
		return reason("integration", "开发已完成，等待集成与集成检查", nil)
	}
	if t.State == "workspace_preparing" {
		return reason("workspace", "正在按需准备独立工作目录", nil)
	}
	if t.Result != nil || t.Request != nil || t.State == "accepted" || t.State == "cancelled" {
		return nil
	}
	if p.Paused {
		return reason("paused", "任务区已暂停新派发", nil)
	}
	if t.State == "pending_plan" || t.GoalVersion != p.GoalVersion || (t.PlanRevision > 0 && t.PlanRevision < p.Revision) {
		return reason("planning", "等待云端规划按最新要求调整", nil)
	}
	if t.NextAt > now {
		return reason("backoff", "等待已记录的重试时间", nil)
	}
	if t.State == "deferred" {
		return reason("dependency", t.DeferredReason, nil)
	}
	missing := []string{}
	for _, dep := range t.After {
		accepted := false
		for _, other := range tasks {
			if other.ID == dep && other.State == "accepted" {
				accepted = true
				break
			}
		}
		if !accepted {
			missing = append(missing, dep)
		}
	}
	if len(missing) > 0 {
		return reason("dependency", fmt.Sprintf("等待 %d 个前置任务验收", len(missing)), missing)
	}
	if missing = nativeRequirementsMissing(t, tasks); len(missing) > 0 {
		return reason("artifact", fmt.Sprintf("等待 %d 项指定版本的阶段产出", len(missing)), missing)
	}
	owners := []string{}
	paths := []string{}
	for _, other := range tasks {
		if (nativeHolds(other) || nativeChecking(other) || other.State == "integrating" || other.State == "awaiting_integration") && nativeTaskScopeConflict(t, other) {
			owners = append(owners, other.ID)
			paths = append(paths, other.Scope)
		}
	}
	if len(owners) > 0 {
		out := reason("write_scope", "等待同一写入范围的任务释放", owners)
		out.Paths = paths
		return out
	}
	if s.GlobalActive >= s.GlobalLimit {
		return reason("global_capacity", "等待全局共享并发槽", nil)
	}
	if v := s.Projects[p.ID]; v.ProjectLimit > 0 && v.ProjectActive >= v.ProjectLimit {
		return reason("area_capacity", "达到本任务区并发上限", nil)
	}
	if t.LastError != "" {
		return reason("preparation", t.LastError, nil)
	}
	return reason("ready", "条件已满足，等待公平调度派发", nil)
}

func (r *nativeRunner) publishOutputs(p nativeRunnerProject, t *nativeRunnerTask, outputs []nativeRunnerOutput) error {
	if len(outputs) > 8 {
		return errors.New("stage output index exceeds bounded limit")
	}
	for _, output := range outputs {
		if output.Key == "" || output.Version == "" || output.Summary == "" || len(output.Key) > 128 || len(output.Version) > 128 || len(output.Summary) > 2048 {
			return errors.New("stage output requires bounded key, version and summary")
		}
		root := nativeTaskWorkingDirectory(p, *t)
		path, err := nativePath(root, output.Path)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			f.Close()
			return errors.New("stage output must be a regular project file")
		}
		raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
		f.Close()
		if err != nil {
			return err
		}
		if len(raw) > 64<<10 {
			return errors.New("stage contract exceeds 64 KiB; publish a focused interface or evidence index")
		}
		sum := sha256.Sum256(raw)
		output.SHA256 = hex.EncodeToString(sum[:])
		output.Round = t.Round
		duplicate := false
		for _, old := range t.Outputs {
			if old.Round == t.Round && old.Key == output.Key && old.Version == output.Version {
				if old.SHA256 != output.SHA256 {
					return errors.New("published stage version is immutable; publish a new version")
				}
				duplicate = true
			}
		}
		if duplicate {
			continue
		}
		if len(t.Outputs) >= 32 {
			return errors.New("stage output index exceeds bounded limit")
		}
		output.Path = filepath.Join(r.dir, "stage-"+nativeHash([]any{t.ID, t.Round, output.Key, output.Version, output.SHA256})+".artifact")
		if err = os.WriteFile(output.Path, raw, 0600); err != nil {
			return err
		}
		// Caller-supplied commit is only a reference, never integration proof.
		if len(output.Commit) > 128 {
			return errors.New("stage commit reference too long")
		}
		t.Outputs = append(t.Outputs, output)
	}
	return nil
}

func (r *nativeRunner) nativeIdlePlanningNeeded(ctx context.Context, p nativeRunnerProject, tasks []nativeRunnerTask) bool {
	all, err := r.readAllTasks(ctx)
	if err != nil {
		return false
	}
	active := 0
	for _, task := range all {
		if nativeHolds(task) {
			active++
		}
	}
	if active >= r.globalConcurrency {
		return false
	}
	s := nativeRunnerSchedulingSnapshot{GlobalActive: active, GlobalLimit: r.globalConcurrency, Projects: map[string]nativeRunnerProjectSchedule{}}
	for _, task := range tasks {
		if task.Kind == "work" && task.State == "queued" && task.Request == nil && task.NextAt <= r.now().Unix() {
			q := nativeQueueReason(task, p, all, s, r.now().Unix())
			if q != nil && (q.Code == "dependency" || q.Code == "artifact" || q.Code == "write_scope") {
				return true
			}
		}
	}
	return false
}
