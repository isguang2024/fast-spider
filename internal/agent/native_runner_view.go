package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ReadNativeRunnerView reads a committed WAL snapshot without taking the runner's
// scheduling mutex or starting an agent. Viewing progress must not wait for CHAT.
func ReadNativeRunnerView(ctx context.Context, dataDir, projectID, taskID string, after int64, eventsOnly bool) (map[string]any, error) {
	path, err := filepath.Abs(filepath.Join(dataDir, "native-runner", "projects.sqlite3"))
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if projectID != "" {
			return nil, sql.ErrNoRows
		}
		return map[string]any{"projects": []any{}, "refreshedAt": time.Now().UTC().Format(time.RFC3339)}, nil
	} else if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(1500)"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if projectID == "" {
		rows, err := tx.QueryContext(ctx, "SELECT value FROM runner_projects ORDER BY rowid")
		if err != nil {
			return nil, err
		}
		projects := []nativeRunnerProject{}
		for rows.Next() {
			var raw string
			var p nativeRunnerProject
			if err = rows.Scan(&raw); err == nil {
				err = json.Unmarshal([]byte(raw), &p)
			}
			if err != nil {
				rows.Close()
				return nil, err
			}
			projects = append(projects, p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		out := []any{}
		for _, p := range projects {
			view, err := nativeProjectView(ctx, tx, p, false)
			if err != nil {
				return nil, err
			}
			out = append(out, view)
		}
		return map[string]any{"projects": out, "refreshedAt": time.Now().UTC().Format(time.RFC3339)}, nil
	}
	var raw string
	var p nativeRunnerProject
	if err = tx.QueryRowContext(ctx, "SELECT value FROM runner_projects WHERE id=?", projectID).Scan(&raw); err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, err
	}
	if eventsOnly {
		return nativeViewEvents(ctx, tx, projectID, "", after)
	}
	if taskID == "" {
		return nativeProjectView(ctx, tx, p, true)
	}
	var task nativeRunnerTask
	if err = tx.QueryRowContext(ctx, "SELECT json_remove(value,'$.history','$.request','$.basis') FROM runner_tasks WHERE project_id=? AND id=?", projectID, taskID).Scan(&raw); err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(raw), &task); err != nil {
		return nil, err
	}
	events, err := nativeViewEvents(ctx, tx, projectID, taskID, 0)
	if err != nil {
		return nil, err
	}
	// Do not serialize request prompts, frozen dispatch credentials or old rounds.
	return map[string]any{"project": nativeViewProject(p, false), "events": events["events"], "task": map[string]any{
		"id": task.ID, "title": task.Title, "parent": task.Parent, "kind": task.Kind, "state": task.State,
		"objective": task.Objective, "acceptance": task.Acceptance, "after": task.After, "scope": task.Scope,
		"context": task.Context, "round": task.Round, "checks": task.Checks, "validations": task.Validations,
		"receipt": task.Receipt, "result": task.Result, "lastError": task.LastError, "reason": task.DeferredReason,
		"resumeAt": task.ResumeAt, "nextAt": task.NextAt,
		"recovery": nativeRecoveryView(task),
		"archived": task.Archived, "priority": task.Priority, "planRevision": task.PlanRevision, "cancellation": task.Cancellation, "estimatedMinutes": task.EstimatedMinutes,
	}}, nil
}

func nativeViewProject(p nativeRunnerProject, detail bool) map[string]any {
	goal := strings.TrimSpace(p.Goal)
	title := strings.TrimSpace(strings.TrimLeft(strings.SplitN(goal, "\n", 2)[0], "#"))
	if title == "" {
		title = p.ID
	}
	clip := func(s string, n int) string {
		r := []rune(s)
		if len(r) > n {
			return string(r[:n]) + "…"
		}
		return s
	}
	view := map[string]any{"id": p.ID, "title": clip(title, 80), "goalSummary": clip(goal, 240),
		"root": p.Root, "controllerSessionId": p.ControllerSessionID, "concurrency": p.Concurrency, "maxConcurrency": p.MaxConcurrency,
		"paused": p.Paused, "questions": p.Questions, "nextPlanAt": p.NextPlanAt,
		"state": p.State, "archived": p.Archived, "revision": p.Revision, "plannedRevision": p.PlannedRevision, "pendingChanges": p.PendingChanges}
	if detail {
		view["goal"] = p.Goal
	}
	if p.Notice != nil {
		view["notification"] = map[string]any{"pending": p.NotifiedKey != p.Notice.Key, "summary": p.Notice.Summary, "error": p.Notice.Error, "nextAt": p.Notice.NextAt}
	}
	return view
}

func nativeTaskBrief(t nativeRunnerTask) map[string]any {
	item := map[string]any{"id": t.ID, "parent": t.Parent, "kind": t.Kind, "title": t.Title,
		"state": t.State, "round": t.Round, "after": t.After, "scope": t.Scope, "nextAt": t.NextAt,
		"reason": t.DeferredReason, "resumeAt": t.ResumeAt, "error": t.LastError, "checks": t.Validations}
	if t.Result != nil {
		item["result"] = t.Result
	}
	if t.Receipt != nil {
		item["sessionId"] = t.Receipt.SessionID
	}
	if t.Recovery != nil {
		item["recovery"] = nativeRecoveryView(t)
	}
	item["archived"] = t.Archived
	item["priority"] = t.Priority
	item["planRevision"] = t.PlanRevision
	item["estimatedMinutes"] = t.EstimatedMinutes
	if t.Cancellation != nil {
		item["cancellation"] = t.Cancellation
	}
	return item
}

func nativeRecoveryView(t nativeRunnerTask) any {
	if t.Recovery == nil {
		return nil
	}
	s := t.Recovery
	return map[string]any{"phase": s.Phase, "lastProgressAt": s.LastProgressAt, "nextProbeAt": s.NextProbeAt, "attempts": s.Attempts, "lastError": s.LastError, "checkpoint": s.Checkpoint, "sessionGeneration": t.Round}
}

func nativeProjectView(ctx context.Context, tx *sql.Tx, p nativeRunnerProject, detail bool) (map[string]any, error) {
	rows, err := tx.QueryContext(ctx, `SELECT json_remove(value,'$.history','$.request.prompt'),
	 (SELECT count(*) FROM json_each(json_extract(runner_tasks.value,'$.history')) h
	 WHERE json_extract(h.value,'$.result') IS NOT NULL AND coalesce(json_extract(h.value,'$.acked'),0)=0)
	 FROM runner_tasks WHERE project_id=? ORDER BY rowid`, p.ID)
	if err != nil {
		return nil, err
	}
	tasks := []nativeRunnerTask{}
	pendingACK := 0
	for rows.Next() {
		var raw string
		var t nativeRunnerTask
		var historyACK int
		if err = rows.Scan(&raw, &historyACK); err == nil {
			err = json.Unmarshal([]byte(raw), &t)
		}
		if err != nil {
			rows.Close()
			return nil, err
		}
		pendingACK += historyACK
		if t.Result != nil && !t.ResultAcked {
			pendingACK++
		}
		tasks = append(tasks, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var updated, cooldown int64
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(created),0) FROM runner_events WHERE project_id=?", p.ID).Scan(&updated); err != nil {
		return nil, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(json_extract(value,'$.until')),0) FROM runner_events WHERE kind='account_cooldown'").Scan(&cooldown); err != nil {
		return nil, err
	}
	times := map[string]string{}
	rows, err = tx.QueryContext(ctx, "SELECT json_extract(value,'$.taskId'),max(created) FROM runner_events WHERE project_id=? AND json_extract(value,'$.taskId') IS NOT NULL GROUP BY json_extract(value,'$.taskId')", p.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var at int64
		if err = rows.Scan(&id, &at); err != nil {
			rows.Close()
			return nil, err
		}
		times[id] = time.Unix(at, 0).UTC().Format(time.RFC3339)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	byID := map[string]nativeRunnerTask{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	brief := []map[string]any{}
	otherTasks := []nativeRunnerTask{}
	rows, err = tx.QueryContext(ctx, "SELECT json_remove(value,'$.history','$.request.prompt') FROM runner_tasks WHERE project_id<>?", p.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw string
		var other nativeRunnerTask
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal([]byte(raw), &other)
		}
		if err != nil {
			rows.Close()
			return nil, err
		}
		otherTasks = append(otherTasks, other)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	holds := append(append([]nativeRunnerTask{}, tasks...), otherTasks...)
	limit, err := nativeLoadGlobalConcurrency(tx)
	if err != nil {
		return nil, err
	}
	projects := []nativeRunnerProject{}
	rows, err = tx.QueryContext(ctx, "SELECT value FROM runner_projects ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw string
		var project nativeRunnerProject
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal([]byte(raw), &project)
		}
		if err != nil {
			rows.Close()
			return nil, err
		}
		projects = append(projects, project)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	scheduling := nativeBuildSchedulingSnapshot(holds, projects, limit)
	allocation := scheduling.Projects[p.ID]
	groups := map[string]map[string]int{}
	for _, t := range tasks {
		item := nativeTaskBrief(t)
		if at := times[t.ID]; at != "" {
			item["updatedAt"] = at
		}
		if t.State == "queued" || t.State == "deferred" {
			blocked := []string{}
			for _, id := range t.After {
				if prior, ok := byID[id]; !ok || prior.State != "accepted" {
					blocked = append(blocked, id)
				}
			}
			reason := "等待调度"
			switch {
			case p.Paused:
				reason = "任务区已暂停"
			case len(blocked) > 0:
				reason = "等待前置任务验收"
			case t.State == "deferred":
				reason = t.DeferredReason
				if reason == "" {
					reason = "等待恢复条件"
				}
			case cooldown > time.Now().Unix():
				reason = "等待账号冷却结束"
			case t.NextAt > time.Now().Unix():
				reason = "等待已安排的重试时间"
			default:
				for _, other := range holds {
					if other.ID != t.ID && (nativeHolds(other) || nativeChecking(other)) && nativeScopeOverlap(t.Scope, other.Scope) {
						blocked = append(blocked, other.ID)
					}
				}
				if len(blocked) > 0 {
					reason = "等待重叠写域释放"
				}
			}
			item["waitingReason"] = reason
			item["blockedBy"] = blocked
		}
		if t.State == "pending_plan" {
			item["waitingReason"] = "等待云端分析最新需求与当前任务的影响"
		}
		if t.State == "canceling" {
			item["waitingReason"] = "正在确认云端会话和关联作业停止"
		}
		brief = append(brief, item)
		if t.Kind != "planner" {
			group := t.Parent
			if group == "" {
				group = p.ID
			}
			if groups[group] == nil {
				groups[group] = map[string]int{}
			}
			groups[group][t.State]++
		}
	}
	view := map[string]any{"project": nativeViewProject(p, detail), "tasks": brief, "groups": groups,
		"pendingAcknowledgements": pendingACK, "cooldownUntil": cooldown, "complete": p.CompleteVersion == p.GoalVersion,
		"refreshedAt": time.Now().UTC().Format(time.RFC3339)}
	view["scheduling"] = map[string]any{"globalLimit": scheduling.GlobalLimit, "globalActive": scheduling.GlobalActive, "projectLimit": allocation.ProjectLimit, "projectActive": allocation.ProjectActive, "allocation": allocation.Allocation}
	if updated != 0 {
		view["updatedAt"] = time.Unix(updated, 0).UTC().Format(time.RFC3339)
	}
	return view, nil
}

func nativeViewEvents(ctx context.Context, tx *sql.Tx, projectID, taskID string, after int64) (map[string]any, error) {
	query := "SELECT id,kind,value,created FROM runner_events WHERE project_id=?"
	args := []any{projectID}
	if taskID != "" {
		query += " AND json_extract(value,'$.taskId')=?"
		args = append(args, taskID)
	}
	if after > 0 {
		query += " AND id>? ORDER BY id ASC LIMIT 50"
		args = append(args, after)
	} else {
		query += " ORDER BY id DESC LIMIT 50"
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []map[string]any{}
	cursor := after
	labels := map[string]string{"dispatched": "任务已派发", "result_saved": "结果已保存", "check_progress": "检查状态更新", "callback_ack_progress": "回调确认已更新", "plan_applied": "规划已应用", "planner_created": "已安排后续规划", "pause": "任务区已暂停", "resume": "任务区已恢复", "goal": "目标已更新", "signal": "收到恢复信息", "report_recovery_needed": "结果报告需要恢复", "plan_repair_scheduled": "已安排规划修正", "dispatch_rejected": "派发未被接受", "preparation_error": "任务准备失败", "account_cooldown": "账号进入冷却", "prepared": "任务包已准备", "recovery_observation": "恢复状态已更新", "add": "任务已加入"}
	for kind, label := range map[string]string{
		"change": "已提交需求变更", "cancel": "已提交撤销请求", "archive": "已归档", "unarchive": "已取消归档",
		"task_cancelled": "任务已撤销", "project_cancelled": "任务区已撤销", "cancellation_pending": "等待云端及关联作业停止",
		"task_redirected": "任务已按新规划转向", "cancelled_result_received": "已保留撤销任务的迟到结果",
	} {
		labels[kind] = label
	}
	for rows.Next() {
		var id, at int64
		var kind, raw string
		if err = rows.Scan(&id, &kind, &raw, &at); err != nil {
			return nil, err
		}
		var meta struct {
			TaskID string `json:"taskId"`
			Error  string `json:"error"`
		}
		if err = json.Unmarshal([]byte(raw), &meta); err != nil {
			return nil, err
		}
		label := labels[kind]
		if label == "" {
			label = "任务状态已更新"
		}
		if meta.Error != "" {
			label += "：" + meta.Error
		}
		events = append(events, map[string]any{"id": id, "kind": kind, "at": time.Unix(at, 0).UTC().Format(time.RFC3339), "summary": label, "taskId": meta.TaskID})
		if id > cursor {
			cursor = id
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if after == 0 {
		for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
			events[left], events[right] = events[right], events[left]
		}
	}
	return map[string]any{"events": events, "cursor": cursor, "hasMore": len(events) == 50}, nil
}
