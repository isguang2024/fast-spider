package agent

import (
	"context"
	"errors"
	"strings"
)

const nativeStalePlanMessage = "block evidence changed since planning; refresh the plan"

var errNativePlanEvidenceChanged = errors.New(nativeStalePlanMessage)

func nativePlannerReviewTargets(tasks []nativeRunnerTask) []string {
	targets := []string{}
	for _, task := range tasks {
		if task.Kind != "planner" && task.State == "returned" && task.Result != nil && !nativeChecking(task) {
			targets = append(targets, task.ID)
			if len(targets) == 16 {
				break
			}
		}
	}
	return targets
}

func nativeAcceptanceBatch(plan nativeRunnerPlan) bool {
	if len(plan.Blocks) > 0 || plan.GoalComplete {
		return false
	}
	for _, action := range plan.Actions {
		switch action.Action {
		case "accept", "keep", "prioritize", "revalidate", "defer":
		default:
			return false
		}
	}
	return true
}

// Snapshot movement is normal during parallel work, not a failed model attempt.
// Refresh queued legacy repairs once and retain the same task/round identity.
func (r *nativeRunner) refreshStalePlannerQueue(ctx context.Context, p nativeRunnerProject, tasks []nativeRunnerTask) error {
	for _, task := range tasks {
		if task.Kind != "planner" || task.State != "queued" || task.Request != nil || task.LastError != nativeStalePlanMessage || task.GoalVersion != p.GoalVersion || task.PlanRevision != p.Revision {
			continue
		}
		task.Basis = map[string]string{}
		for _, block := range tasks {
			if block.Kind != "planner" {
				task.Basis[block.ID] = nativeBasis(block)
			}
		}
		task.ReviewTargets = nativePlannerReviewTargets(tasks)
		task.NextAt = 0
		task.LastError = ""
		task.Correction = "Parallel task evidence changed. Read current runner.context task records and include each record's reviewToken on its action. Keep completed work and review only unresolved results; do not redo business implementation or repeat already passed tests."
		if len(task.ReviewTargets) > 0 {
			task.Title = "结果验收与后续规划"
		}
		if err := r.saveTask(ctx, task, "planner_snapshot_refreshed"); err != nil {
			return err
		}
	}
	return nil
}

type nativeRunnerPresentation struct {
	Code           string `json:"code"`
	Label          string `json:"label"`
	Summary        string `json:"summary"`
	OwnerTaskID    string `json:"ownerTaskId,omitempty"`
	OwnerSessionID string `json:"ownerSessionId,omitempty"`
	Since          int64  `json:"since,omitempty"`
	NextAt         int64  `json:"nextAt,omitempty"`
}

func nativeTaskPresentation(p nativeRunnerProject, t nativeRunnerTask, tasks []nativeRunnerTask, q *nativeRunnerQueueReason, now int64) nativeRunnerPresentation {
	v := func(code, label, summary string) nativeRunnerPresentation {
		return nativeRunnerPresentation{Code: code, Label: label, Summary: summary}
	}
	switch t.State {
	case "accepted":
		return v("accepted", "已验收", "当前结果已通过验收")
	case "cancelled":
		return v("cancelled", "已取消", "任务已停止，已有成果按规则保留")
	case "canceling":
		return v("canceling", "取消中", "正在确认原会话和关联作业停止")
	case "workspace_preparing":
		return v("workspace_preparing", "准备工作区", "正在后台创建或复用任务工作目录")
	case "awaiting_integration":
		out := v("awaiting_integration", "待集成", "开发结果已采纳，等待安全集成")
		if q != nil {
			out.Summary = q.Summary
		}
		return out
	case "integrating":
		if t.Workspace != nil && t.Workspace.State == "integrated" {
			return v("checks_running", "集成检查中", "已合入，正在完成受影响检查")
		}
		return v("integrating", "集成中", "正在后台核对并合入准确提交")
	case "prepared":
		return v("dispatching", "派发中", "正在建立或确认本次云端任务绑定")
	case "active":
		if t.Recovery != nil {
			r := t.Recovery
			if r.Phase == "waiting_job" {
				out := v("waiting_job", "等待作业", "已登记后台作业，系统等待结果后继续")
				out.NextAt = r.NextProbeAt
				return out
			}
			if r.Phase == "uncertain" || r.Phase == "continuing" || r.Phase == "handover" {
				out := v("recovering", "恢复中", r.LastError)
				out.NextAt = r.NextProbeAt
				return out
			}
		}
		if t.Kind == "planner" {
			if len(t.ReviewTargets) > 0 {
				return v("reviewing", "验收与规划中", "正在处理本轮指定结果和后续安排")
			}
			return v("executing", "规划中", "云端正在生成或调整任务计划")
		}
		return v("executing", "执行中", "云端正在执行当前任务块")
	}
	if t.State == "returned" || t.State == "deferred" {
		if t.Result != nil {
			if t.Result.ErrorCode == "RUNNER_REPORT_UNAVAILABLE" || t.Result.ErrorCode == "RUNNER_REPORT_CHANGED" {
				return v("report_missing", "报告待恢复", "执行已结束，但报告缺失或内容变化，不能直接验收")
			}
			if t.Result.Outcome != "completed" {
				return v("needs_fix", "待处理返回结果", "已返回失败或阻塞证据，等待云端处理")
			}
			if nativeWorkspaceIsolated(t) && t.Workspace.SealedCommit == "" && t.Workspace.State != "conflict" {
				return v("validating_commit", "核对分支提交", "正在将结果绑定到准确、干净的任务提交")
			}
			running, pending, failed := false, false, false
			for _, name := range t.Checks {
				check := t.Validations[name]
				failed = failed || check.State == "failed"
				running = running || (check.JobID != "" && check.State != "passed" && check.State != "failed")
				pending = pending || (check.JobID == "" && check.State != "passed" && check.State != "failed")
			}
			if running {
				return v("checks_running", "检查中", "本地检查作业正在执行或确认状态")
			}
			if failed {
				return v("checks_failed", "检查未通过", "检查失败，等待云端依据失败证据安排修复")
			}
			if pending {
				return v("checks_queued", "等待检查", "结果已提交，检查作业尚未启动")
			}
			if t.Kind == "planner" {
				return v("review_applying", "应用计划中", "云端计划已返回，系统正在校验和应用")
			}
			for _, owner := range tasks {
				if owner.Kind != "planner" || owner.PlanRevision != p.Revision || owner.GoalVersion != p.GoalVersion {
					continue
				}
				assigned := false
				for _, id := range owner.ReviewTargets {
					if id == t.ID {
						assigned = true
						break
					}
				}
				if !assigned || owner.State == "accepted" || owner.State == "cancelled" || owner.State == "canceling" {
					continue
				}
				out := v("review_queued", "待验收", "已分配验收目标，等待云端任务开始")
				out.OwnerTaskID = owner.ID
				out.Since = owner.StartedAt
				out.NextAt = owner.NextAt
				if owner.Receipt != nil {
					out.OwnerSessionID = owner.Receipt.SessionID
				}
				if owner.State == "active" && owner.Receipt != nil && !owner.Receipt.InDoubt {
					out.Code = "reviewing"
					out.Label = "验收中"
					out.Summary = "已由正在执行的云端验收任务接手"
				}
				if owner.State == "returned" {
					out.Code = "review_applying"
					out.Label = "验收结果应用中"
					out.Summary = "验收计划已返回，等待系统应用"
				}
				if owner.NextAt > now || (owner.Recovery != nil && owner.Recovery.Phase == "uncertain") {
					out.Code = "review_retry"
					out.Label = "验收恢复等待"
					out.Summary = owner.LastError
					if out.Summary == "" {
						out.Summary = "验收会话正在等待恢复"
					}
				}
				// A changed result is a new review target, even if the old reviewer is live.
				if owner.State == "active" && strings.TrimSuffix(owner.Basis[t.ID], "-due") != nativeBasis(t) {
					out.Code = "review_queued"
					out.Label = "待重新验收"
					out.Summary = "结果已更新，旧验收快照不再适用"
				}
				return out
			}
			return v("review_queued", "待验收", "结果和检查已就绪，尚未分配正在执行的验收任务")
		}
	}
	if q != nil {
		code, label := "queued", "排队中"
		switch q.Code {
		case "dependency":
			code, label = "waiting_dependency", "等待前置验收"
		case "artifact":
			code, label = "waiting_artifact", "等待阶段产出"
		case "write_scope":
			code, label = "waiting_scope", "等待写域"
		case "global_capacity", "area_capacity":
			code, label = "waiting_capacity", "等待并发槽"
		case "backoff":
			code, label = "retry_wait", "等待重试"
		case "planning":
			code, label = "pending_plan", "等待规划"
		case "paused":
			code, label = "deferred", "暂停派发"
		}
		out := v(code, label, q.Summary)
		out.NextAt = t.NextAt
		return out
	}
	if t.State == "deferred" {
		out := v("deferred", "等待恢复条件", t.DeferredReason)
		out.NextAt = t.ResumeAt
		return out
	}
	return v("queued", "排队中", "等待系统调度")
}
