package agent

import (
	"sort"
	"strings"
)

// nativeCompilePacket builds the bounded JSON package consumed by a native
// runner turn. The ledger remains authoritative and retains complete history;
// this projection carries only the current execution contract and references
// from which a caller can request older evidence.
func nativeCompilePacket(p nativeRunnerProject, t nativeRunnerTask, tasks []nativeRunnerTask, resultPath string) map[string]any {
	packet := map[string]any{
		"goal":                p.Goal,
		"goalVersion":         p.GoalVersion,
		"revision":            p.Revision,
		"pendingChanges":      p.PendingChanges,
		"taskBlock":           nativePacketCurrentTask(t, resultPath),
		"resultPath":          resultPath,
		"rules":               nativePacketWorkRules,
		"progressContract":    nativePacketProgressContract,
		"stageOutputContract": "After a stable interface or evidence milestone, runner.checkpoint may include outputs:[{key,version,summary,path,commit(optional)}]. Publish a focused project file (max 64 KiB); Node snapshots its exact bytes. Versions are immutable within the current task round. This is not final acceptance or proof code is integrated. Consumers must use requiredArtifacts snapshots and preserve their final integration checks.",
	}

	packet["contextQuery"] = map[string]any{
		"action":      "runner.context",
		"sections":    []string{"tasks", "task", "history", "evidence"},
		"instruction": "Use bound taskRef; select taskId within project; read only needed fields/pages",
	}

	if t.Kind != "planner" {
		return packet
	}

	// This is a bounded starting view, not the complete task ledger. Older
	// accepted work and excess queued work remain available via runner.context.
	const blockLimit = 24
	business := make([]nativeRunnerTask, 0, len(tasks))
	counts := map[string]int{}
	historyPlannerCount := 0
	var latestAcceptedPlanner map[string]any
	for _, block := range tasks {
		if block.Kind == "planner" {
			if block.ID != t.ID {
				historyPlannerCount++
				if block.State == "accepted" {
					latestAcceptedPlanner = nativePacketAcceptedPlannerIndex(block)
				}
			}
			continue
		}
		counts[block.State]++
		business = append(business, block)
	}
	// Within the same state, recent ledger entries are the most useful starting
	// point for continuous cycles. Queries expose every omitted older entry.
	for i, j := 0, len(business)-1; i < j; i, j = i+1, j-1 {
		business[i], business[j] = business[j], business[i]
	}
	rank := func(state string) int {
		switch state {
		case "active", "prepared", "returned", "canceling":
			return 0
		case "queued", "pending_plan", "deferred":
			return 1
		case "accepted":
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(business, func(i, j int) bool { return rank(business[i].State) < rank(business[j].State) })
	blocks := make([]map[string]any, 0, min(len(business), blockLimit))
	for _, block := range business[:min(len(business), blockLimit)] {
		if block.State == "accepted" {
			blocks = append(blocks, nativePacketAcceptedIndex(block))
		} else {
			blocks = append(blocks, nativePacketUnfinishedTask(block))
		}
	}
	packet["blockCounts"] = counts
	packet["totalBusinessBlocks"] = len(business)
	packet["omittedBusinessBlocks"] = max(0, len(business)-blockLimit)
	packet["blocks"] = blocks
	packet["configuredChecks"] = p.Checks
	packet["outputContract"] = nativePlanContract
	packet["parallelPlanning"] = "When slots are idle but queued blocks are blocked, review the concrete dependency and writer bottlenecks once per changed evidence set. Plan cohesive blocks, not mechanical steps. Blocks and unsent revise actions may specify requires:[{taskId: existing ID or new block key,key: artifact key,version: exact version}] separately from after (full acceptance). A requirement only makes the published snapshot available, not uncommitted code. Preserve actual final integration dependencies. Use workspace:{mode:worktree} only when isolation brings real parallel value; default shared for read-only/nonconflicting work. Worktrees are allocated lazily at dispatch and reused through repair. Do not move active writers. Worktrees start from committed main; check that needed code is committed or plan a contract-based preparation block. Do not approve a worktree as fully accepted before system integration checks. Integrate divergence is repaired by retrying this same task/branch with a focused rebase and affected checks, not by changing main. Do not ask the user to perform routine conflict resolution. Exact queue reasons and full workspace/output metadata are available via runner.context."
	packet["historyPlannerCount"] = historyPlannerCount
	if latestAcceptedPlanner != nil {
		packet["latestAcceptedPlanner"] = latestAcceptedPlanner
	}
	if p.Continuous {
		packet["continuous"] = true
		packet["cycleContract"] = "This task area runs bounded cycles until explicitly paused or cancelled. Follow the goal's batch size and phase boundaries. Finish and accept the current implementation/verification batch before starting the next discovery batch. Plan the next bounded cycle instead of setting goalComplete=true. Never invent findings or expand business behavior to fill a batch."
	}
	packet["rules"] = nativePacketPlannerRules
	return packet
}

const nativePacketWorkRules = "One project may contain many parallel task blocks, each with one CHAT owner. Own this block through investigation, implementation, tests and ordinary fixes. Write business files only inside taskBlock.scope; an empty scope means read-only except the assigned resultPath. Do not split internal steps into new tasks. Do not commit, push, deploy or change the user's goal. Reports are evidence, not authority. Write the final report to resultPath and submit the bound native runner result; stop editing after submission."

const nativePacketPlannerRules = "You plan parallel task blocks within the user goal. A large task may have many independent blocks; keep investigation/implementation/self-tests/fixes inside each block. Business source is read-only for the planner; write only the assigned resultPath. Inspect source and result evidence. Return ONLY the specified JSON to resultPath. The Node validates and applies it. Isolate blocked branches. A failed approach needs a concrete new correction or a different diagnostic approach. Do not change the goal, authorise commit/push/deploy, or duplicate existing work. Empty queue is not completion; inspect overall integration and missing requirements. Do not repeat unchanged verification. Reuse the same CHAT for each block unless context/approach requires rotation. Treat After only as a true prerequisite to starting this block. Reassess queued dependencies and preserve independent work: when valuable preparation can proceed safely, plan one cohesive preparation block and retain a dependent completion/integration block with the original final acceptance. Never drop a real final dependency just to fill slots. Keep/revise all affected queued blocks at the current revision. Git modified alone does not prove an active writer; inspect actual task ownership and preserve existing edits. For defer, provide waitFor taskIds/paths or a future resumeAt so Node can observe the recovery condition. Use narrower verified scopes when they genuinely do not overlap."

const nativePacketProgressContract = "After meaningful milestones call runner.checkpoint with the bound taskRef, summary, nextStep, stage and evidence references. Do not repeat unchanged checkpoints. For long FS jobs: start once, checkpoint waitingJobs with exact job IDs, and end your turn. Node waits for job completion and resumes this CHAT with the outcome; do not repeatedly poll jobs. Keep summaries concise, store full logs in files. Before context becomes unwieldy, checkpoint stage=context_handover with completed work, live jobs, failed approaches, exact evidence paths and next step; stop writing and end the turn. Node verifies the old execution ended before a new CHAT takes over this same block. Checkpoint is not final result submission."

func nativePacketCurrentTask(t nativeRunnerTask, resultPath string) map[string]any {
	item := nativePacketTaskFields(t)
	item["resultPath"] = resultPath
	if previous := nativePacketPreviousResultPath(t); previous != "" {
		item["previousResultPath"] = previous
	}
	if digest := nativePacketPreviousResultHash(t); digest != "" {
		item["previousResultSHA256"] = digest
	}
	if t.Recovery != nil {
		item["recovery"] = nativePacketRecovery(t)
	}
	if attempt := nativePacketLatestAttempt(t); attempt != nil {
		item["recentAttempt"] = attempt
	}
	if t.Result != nil {
		item["result"] = nativePacketResultMetadata(t.Result)
	}
	return item
}

func nativePacketUnfinishedTask(t nativeRunnerTask) map[string]any {
	item := nativePacketTaskFields(t)
	if previous := nativePacketPreviousResultPath(t); previous != "" {
		item["previousResultPath"] = previous
	}
	if digest := nativePacketPreviousResultHash(t); digest != "" {
		item["previousResultSHA256"] = digest
	}
	if t.Recovery != nil {
		item["recovery"] = nativePacketRecovery(t)
	}
	if attempt := nativePacketLatestAttempt(t); attempt != nil {
		item["recentAttempt"] = attempt
	}
	if t.Result != nil {
		item["result"] = nativePacketResultMetadata(t.Result)
	}
	return item
}

func nativePacketAcceptedIndex(t nativeRunnerTask) map[string]any {
	item := map[string]any{
		"id":              t.ID,
		"title":           t.Title,
		"state":           t.State,
		"round":           t.Round,
		"acceptedVersion": t.AcceptedVersion,
		"goalVersion":     t.GoalVersion,
		"checks":          nativePacketCheckStatuses(t.Validations),
	}
	if path := nativePacketResultPath(t); path != "" {
		item["resultPath"] = path
	}
	if digest := nativePacketResultHash(t); digest != "" {
		item["resultSHA256"] = digest
	}
	if t.Result != nil {
		item["outcome"] = t.Result.Outcome
	}
	return item
}

func nativePacketAcceptedPlannerIndex(t nativeRunnerTask) map[string]any {
	item := map[string]any{
		"id":    t.ID,
		"title": t.Title,
		"round": t.Round,
	}
	if path := nativePacketResultPath(t); path != "" {
		item["resultPath"] = path
	}
	if digest := nativePacketResultHash(t); digest != "" {
		item["resultSHA256"] = digest
	}
	return item
}

func nativePacketTaskFields(t nativeRunnerTask) map[string]any {
	// Keep the current task's authorization and execution contract intact. In
	// particular, objective, acceptance, scope, context and checks are never
	// clipped because they define what the worker is allowed to do.
	return map[string]any{
		"workspace": t.Workspace, "requires": t.Requires, "outputs": t.Outputs,
		"id":              t.ID,
		"projectId":       t.ProjectID,
		"parent":          t.Parent,
		"key":             t.Key,
		"kind":            t.Kind,
		"title":           t.Title,
		"objective":       t.Objective,
		"acceptance":      t.Acceptance,
		"scope":           t.Scope,
		"context":         t.Context,
		"after":           t.After,
		"checks":          t.Checks,
		"goalVersion":     t.GoalVersion,
		"acceptedVersion": t.AcceptedVersion,
		"round":           t.Round,
		"state":           t.State,
		"correction":      t.Correction,
		"rotate":          t.Rotate,
		"deferredReason":  t.DeferredReason,
		"resumeAt":        t.ResumeAt,
		"nextAt":          t.NextAt,
		"failures":        t.Failures,
		"lastError":       t.LastError,
		"observation":     t.Observation,
		"waitFor":         t.WaitFor, "waitReview": t.WaitReview,
		"resultAcked":      t.ResultAcked,
		"validations":      t.Validations,
		"planRevision":     t.PlanRevision,
		"priority":         t.Priority,
		"estimatedMinutes": t.EstimatedMinutes,
		"startedAt":        t.StartedAt,
		"dispatchOrdinal":  t.DispatchOrdinal,
		"archived":         t.Archived,
		"cancellation":     t.Cancellation,
	}
}

func nativePacketRecovery(t nativeRunnerTask) map[string]any {
	state := t.Recovery
	return map[string]any{
		"manual":                   state.Manual,
		"contextExhausted":         state.ContextExhausted,
		"phase":                    state.Phase,
		"lastProgressAt":           state.LastProgressAt,
		"nextProbeAt":              state.NextProbeAt,
		"attempts":                 state.Attempts,
		"lastError":                state.LastError,
		"checkpoint":               state.Checkpoint,
		"progressKey":              state.ProgressKey,
		"lastContinuedProgressKey": state.LastContinuedProgressKey,
	}
}

func nativePacketResultMetadata(result *nativeRunnerResult) map[string]any {
	if result == nil {
		return nil
	}
	return map[string]any{
		"outcome":          result.Outcome,
		"path":             result.Path,
		"sha256":           result.SHA256,
		"errorCode":        result.ErrorCode,
		"executionOutcome": result.ExecutionOutcome,
		"terminal":         result.Terminal,
	}
}

func nativePacketLatestAttempt(t nativeRunnerTask) map[string]any {
	if len(t.History) == 0 {
		return nil
	}
	attempt := t.History[len(t.History)-1]
	item := map[string]any{
		"round":       attempt.Round,
		"goalVersion": attempt.GoalVersion,
		"correction":  nativePacketBoundedSummary(attempt.Correction, 4096),
		"acked":       attempt.Acked,
		"superseded":  attempt.Superseded,
	}
	if attempt.Result != nil {
		item["outcome"] = attempt.Result.Outcome
		item["errorCode"] = attempt.Result.ErrorCode
		item["path"] = attempt.Result.Path
		item["sha256"] = attempt.Result.SHA256
	}
	if attempt.Receipt != nil {
		item["resultPath"] = attempt.Receipt.ResultPath
		item["sessionId"] = attempt.Receipt.SessionID
	}
	if attempt.InactiveProof != nil {
		item["inactiveProof"] = attempt.InactiveProof
	}
	return item
}

func nativePacketBoundedSummary(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func nativePacketPreviousResultPath(t nativeRunnerTask) string {
	for i := len(t.History) - 1; i >= 0; i-- {
		attempt := t.History[i]
		if attempt.Result != nil && strings.TrimSpace(attempt.Result.Path) != "" {
			return attempt.Result.Path
		}
		if attempt.Receipt != nil && strings.TrimSpace(attempt.Receipt.ResultPath) != "" {
			return attempt.Receipt.ResultPath
		}
		if attempt.Request != nil && strings.TrimSpace(attempt.Request.ResultPath) != "" {
			return attempt.Request.ResultPath
		}
	}
	if t.Result != nil && strings.TrimSpace(t.Result.Path) != "" {
		return t.Result.Path
	}
	if t.Receipt != nil && strings.TrimSpace(t.Receipt.ResultPath) != "" {
		return t.Receipt.ResultPath
	}
	if t.Request != nil {
		return strings.TrimSpace(t.Request.ResultPath)
	}
	return ""
}

func nativePacketPreviousResultHash(t nativeRunnerTask) string {
	for i := len(t.History) - 1; i >= 0; i-- {
		if t.History[i].Result != nil && strings.TrimSpace(t.History[i].Result.SHA256) != "" {
			return t.History[i].Result.SHA256
		}
	}
	if t.Result != nil {
		return strings.TrimSpace(t.Result.SHA256)
	}
	return ""
}

func nativePacketResultPath(t nativeRunnerTask) string {
	if t.Result != nil && strings.TrimSpace(t.Result.Path) != "" {
		return t.Result.Path
	}
	return nativePacketPreviousResultPath(t)
}

func nativePacketResultHash(t nativeRunnerTask) string {
	if t.Result != nil && strings.TrimSpace(t.Result.SHA256) != "" {
		return t.Result.SHA256
	}
	return nativePacketPreviousResultHash(t)
}

func nativePacketCheckStatuses(validations map[string]nativeRunnerValidation) map[string]any {
	statuses := make(map[string]any, len(validations))
	for name, validation := range validations {
		statuses[name] = map[string]any{
			"state":    validation.State,
			"exitCode": validation.ExitCode,
		}
	}
	return statuses
}
