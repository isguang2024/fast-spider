package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

// toolExecutor is the single transport-neutral execution path for Fast Spider tools.
// MCP and Direct API keep their own authentication/authorization and response adapters,
// but all tool-to-capability routing and argument mapping lives here.
type toolExecutor struct {
	service *core.Service
}

type toolRequestError struct {
	message string
}

func (e *toolRequestError) Error() string { return e.message }

func newToolExecutor(service *core.Service) *toolExecutor {
	return &toolExecutor{service: service}
}

func toolInput[T any](tool string, input any) (T, error) {
	value, ok := input.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("tool executor input mismatch for %s: %T", tool, input)
	}
	return value, nil
}

func executeTypedTool[T any](executor *toolExecutor, ctx context.Context, ownerID, tool string, input any) (T, error) {
	var zero T
	result, err := executor.Execute(ctx, ownerID, tool, input)
	if err != nil {
		return zero, err
	}
	value, ok := result.(T)
	if !ok {
		return zero, fmt.Errorf("tool executor output mismatch for %s: %T", tool, result)
	}
	return value, nil
}

func (e *toolExecutor) Execute(ctx context.Context, ownerID, tool string, rawInput any) (any, error) {
	switch tool {
	case "machine_list":
		input, err := toolInput[machineListInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		if input.Limit != 0 || input.Cursor != 0 || input.IncludeCapabilities != nil {
			if input.Limit == 0 {
				input.Limit = 20
			}
			if input.Limit < 1 || input.Limit > 50 || input.Cursor < 0 {
				return nil, &toolRequestError{message: "limit must be between 1 and 50 and cursor must be non-negative"}
			}
			includeCapabilities := input.IncludeCapabilities != nil && *input.IncludeCapabilities
			machines, hasMore, err := e.service.ListMachinesPage(ctx, ownerID, input.Cursor, input.Limit, includeCapabilities)
			if err != nil {
				return nil, err
			}
			out := machineListOutput{Machines: make([]mcpMachine, 0, len(machines)), HasMore: &hasMore}
			for _, machine := range machines {
				out.Machines = append(out.Machines, toMCPMachine(machine))
			}
			if hasMore {
				out.NextCursor = input.Cursor + len(machines)
			}
			return out, nil
		}
		machines, err := e.service.ListMachines(ctx, ownerID)
		if err != nil {
			return nil, err
		}
		out := machineListOutput{Machines: make([]mcpMachine, 0, len(machines))}
		for _, machine := range machines {
			out.Machines = append(out.Machines, toMCPMachine(machine))
		}
		return out, nil

	case "machine_get":
		input, err := toolInput[machineGetInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		machine, err := e.service.GetMachine(ctx, ownerID, input.MachineID)
		if err != nil {
			return nil, err
		}
		return machineGetOutput{Machine: toMCPMachine(machine)}, nil

	case "audit_log":
		input, err := toolInput[auditLogInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		var before time.Time
		if strings.TrimSpace(input.Before) != "" {
			before, err = time.Parse(time.RFC3339, input.Before)
			if err != nil {
				return nil, &toolRequestError{message: "before must be an RFC3339 timestamp"}
			}
		}
		entries, err := e.service.ListAuditLog(ctx, ownerID, core.AuditLogQuery{
			MachineID: input.MachineID, ActionPrefix: input.ActionPrefix, Result: input.Result, Before: before, Limit: input.Limit,
		})
		if err != nil {
			return nil, err
		}
		out := auditLogOutput{Entries: make([]auditLogEntry, 0, len(entries))}
		for _, entry := range entries {
			out.Entries = append(out.Entries, auditLogEntry{
				ID: entry.ID, MachineID: entry.MachineID, ActorType: entry.ActorType, ActorID: entry.ActorID,
				Action: entry.Action, Result: entry.Result, RemoteAddr: entry.RemoteAddr, Detail: entry.Detail,
				CreatedAt: entry.CreatedAt.UTC().Format(time.RFC3339),
			})
		}
		return out, nil

	case "operation_log":
		input, err := toolInput[operationLogInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(input.MachineID) == "" {
			return nil, &toolRequestError{message: "machineId is required"}
		}
		if input.Limit < 0 || input.Limit > 200 {
			return nil, &toolRequestError{message: "limit must be between 0 and 200"}
		}
		if len(input.Before) > 256 {
			return nil, &toolRequestError{message: "before cursor is too long"}
		}
		result, err := e.service.CallCapability(ctx, ownerID, input.MachineID, "operation.log", "query", map[string]any{
			"level": input.Level, "category": input.Category, "limit": input.Limit, "before": input.Before,
		})
		if err != nil {
			return nil, err
		}
		var out operationLogOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, err
		}
		return out, nil

	case "capability_list":
		input, err := toolInput[capabilityListInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		view := strings.TrimSpace(input.View)
		capabilities := make([]protocolv1.CapabilityDescriptor, 0)
		if input.MachineID != "" {
			machine, getErr := e.service.GetMachine(ctx, ownerID, input.MachineID)
			if getErr != nil {
				return nil, getErr
			}
			capabilities = machine.Capabilities
		} else if view == "" || view == "overview" || view == "catalog" || view == "capability" {
			capabilities = e.service.CapabilityCatalog()
		}
		if view == "" && input.MachineID != "" {
			return capabilityListOutput{Capabilities: capabilities, CapabilitySummaries: mcpCapabilitySummaries(capabilities)}, nil
		}
		guideView := view
		if guideView == "" {
			guideView = "overview"
		}
		var guide *mcpGuide
		if guideView == "capability" {
			guide, err = newMCPCapabilityGuide(e.service.Version(), capabilities, input.Name)
		} else {
			guide, err = newMCPGuide(e.service.Version(), guideView, input.Name)
		}
		if err != nil {
			return nil, err
		}
		outputCapabilities := capabilities
		if view == "overview" {
			outputCapabilities = []protocolv1.CapabilityDescriptor{}
		}
		return capabilityListOutput{Capabilities: outputCapabilities, CapabilitySummaries: mcpCapabilitySummaries(capabilities), Guide: guide}, nil

	case "codex_cloud_collaboration":
		input, err := toolInput[cloudCollaborationInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		rawParams, err := json.Marshal(input.Params)
		if err != nil {
			return nil, &toolRequestError{message: "params must be a JSON object"}
		}
		var params cloudCollaborationParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return nil, &toolRequestError{message: "invalid cloud collaboration params"}
		}
		if strings.HasPrefix(input.Action, "completion.") {
			completionInput := cloudCompletionInput{
				Action: strings.TrimPrefix(input.Action, "completion."), CollaborationID: params.CollaborationID, TaskID: params.TaskID,
				ActorSessionID: params.ActorSessionID, SourceSessionID: params.SourceSessionID, Outcome: params.Outcome,
				ClaimID: params.ClaimID, Limit: params.Limit, Acknowledgements: params.Acknowledgements,
			}
			if err := validateCloudCompletionToolInput(completionInput); err != nil {
				return nil, err
			}
			acknowledgements := make([]core.CloudCompletionAckItem, 0, len(completionInput.Acknowledgements))
			for _, item := range completionInput.Acknowledgements {
				acknowledgements = append(acknowledgements, core.CloudCompletionAckItem{
					NotificationID: item.NotificationID, ResultID: item.ResultID, ResultStatus: item.ResultStatus,
					ResultBytes: item.ResultBytes, ResultSHA256: item.ResultSHA256, DeliverableStatus: item.DeliverableStatus,
				})
			}
			result, err := e.service.CloudCompletion(ctx, ownerID, core.CloudCompletionRequest{
				Action: completionInput.Action, CollaborationID: completionInput.CollaborationID, TaskID: completionInput.TaskID,
				ActorSessionID: completionInput.ActorSessionID, SourceSessionID: completionInput.SourceSessionID, Outcome: completionInput.Outcome,
				ClaimID: completionInput.ClaimID, Limit: completionInput.Limit, Acknowledgements: acknowledgements,
			})
			if err != nil {
				return nil, err
			}
			return genericCapabilityOutput{Result: result}, nil
		}
		var deadline time.Time
		if strings.TrimSpace(params.Deadline) != "" {
			deadline, err = time.Parse(time.RFC3339, params.Deadline)
			if err != nil {
				return nil, &toolRequestError{message: "deadline must be RFC3339"}
			}
		}
		result, err := e.service.CloudCollaboration(ctx, ownerID, core.CloudCollaborationRequest{
			Action: input.Action, CollaborationID: params.CollaborationID, ExpectedRevision: params.ExpectedRevision, ActorSessionID: params.ActorSessionID, ActorRole: params.ActorRole,
			MachineID: params.MachineID, IdempotencyKey: params.IdempotencyKey, RequestHash: params.RequestHash, ControllerSessionID: params.ControllerSessionID, DispatcherSessionID: params.DispatcherSessionID,
			Title: params.Title, Goal: params.Goal, Scope: params.Scope, DoneWhen: params.DoneWhen, WorkingDirectory: params.WorkingDirectory, AllowedActions: params.AllowedActions,
			MaxDepth: params.MaxDepth, MaxActiveChats: params.MaxActiveChats, MaxCreates: params.MaxCreates, HeartbeatMinutes: params.HeartbeatMinutes, StallMinutes: params.StallMinutes, Deadline: deadline,
			GoalID: params.GoalID, GoalStatus: params.GoalStatus, TaskID: params.TaskID, TaskStatus: params.TaskStatus, ParentSessionID: params.ParentSessionID, Prompt: params.Prompt, AccessMode: params.AccessMode, WriteScope: params.WriteScope,
			DeliverablePath: params.DeliverablePath, EventID: params.EventID, EventSequence: params.EventSequence, EventType: params.EventType, EventGeneration: params.EventGeneration, ResultID: params.ResultID, ResultStatus: params.ResultStatus, ResultBytes: params.ResultBytes, ResultSHA256: params.ResultSHA256, DeliverableStatus: params.DeliverableStatus,
			DecisionID: params.DecisionID, DecisionStatus: params.DecisionStatus, Question: params.Question, Options: params.Options, Recommendation: params.Recommendation, Checkpoint: params.Checkpoint, InactiveVerified: params.InactiveVerified, Limit: params.Limit,
		})
		if err != nil {
			return nil, err
		}
		return genericCapabilityOutput{Result: result}, nil

	case "codex_cloud_completion":
		input, err := toolInput[cloudCompletionInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		if err := validateCloudCompletionToolInput(input); err != nil {
			return nil, err
		}
		acknowledgements := make([]core.CloudCompletionAckItem, 0, len(input.Acknowledgements))
		for _, item := range input.Acknowledgements {
			acknowledgements = append(acknowledgements, core.CloudCompletionAckItem{
				NotificationID: item.NotificationID, ResultID: item.ResultID, ResultStatus: item.ResultStatus,
				ResultBytes: item.ResultBytes, ResultSHA256: item.ResultSHA256, DeliverableStatus: item.DeliverableStatus,
			})
		}
		result, err := e.service.CloudCompletion(ctx, ownerID, core.CloudCompletionRequest{
			Action: input.Action, CollaborationID: input.CollaborationID, TaskID: input.TaskID,
			ActorSessionID: input.ActorSessionID, SourceSessionID: input.SourceSessionID, Outcome: input.Outcome,
			ClaimID: input.ClaimID, Limit: input.Limit, Acknowledgements: acknowledgements,
		})
		if err != nil {
			return nil, err
		}
		return genericCapabilityOutput{Result: result}, nil

	case "file_read":
		input, err := toolInput[fileReadInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		params := map[string]any{"path": input.Path}
		addOptionalFileReadParam(params, "offset", input.Offset)
		addOptionalFileReadParam(params, "limit", input.Limit)
		addOptionalFileReadParam(params, "lineStart", input.LineStart)
		addOptionalFileReadParam(params, "lineCount", input.LineCount)
		addOptionalFileReadParam(params, "headLines", input.HeadLines)
		addOptionalFileReadParam(params, "tailLines", input.TailLines)
		addOptionalFileReadParam(params, "aroundLine", input.AroundLine)
		addOptionalFileReadParam(params, "contextLines", input.ContextLines)
		addOptionalFileReadParam(params, "statOnly", input.StatOnly)
		addOptionalFileReadParam(params, "includeLineNumbers", input.IncludeLineNumbers)
		result, err := e.service.CallCapability(ctx, ownerID, input.MachineID, "file.read", "read", params)
		if err != nil {
			return nil, err
		}
		var out fileReadOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, err
		}
		return out, nil

	case "code_search":
		input, err := toolInput[codeSearchInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		result, err := e.service.CallCapability(ctx, ownerID, input.MachineID, "code.search", "search", map[string]any{
			"query": input.Query, "path": input.Path, "mode": input.Mode, "regex": input.Regex, "ignoreCase": input.IgnoreCase,
			"include": input.Include, "exclude": input.Exclude, "context": input.Context, "beforeContext": input.BeforeContext,
			"afterContext": input.AfterContext, "limit": input.Limit,
		})
		if err != nil {
			return nil, err
		}
		adaptRollingCodeSearchResult(result)
		var out codeSearchOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, err
		}
		return out, nil

	case "file_edit":
		input, err := toolInput[fileEditInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		action := input.Action
		if action == "" {
			action = "edit"
		}
		result, err := e.service.CallCapability(ctx, ownerID, input.MachineID, "file.write", action, map[string]any{
			"path": input.Path, "previewOf": input.PreviewOf, "content": input.Content,
			"oldText": input.OldText, "newText": input.NewText, "edits": input.Edits,
			"expectedFileSha256": input.ExpectedFileSHA256, "expectedAbsent": input.ExpectedAbsent,
		})
		if err != nil {
			return nil, err
		}
		adaptRollingFileEditResult(result, action)
		var out fileEditOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, err
		}
		if action != "preview" {
			out.Diff = ""
			out.DiffTruncated = false
		}
		return out, nil

	case "shell_run":
		input, err := toolInput[shellRunInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		result, err := e.service.CallCapability(ctx, ownerID, input.MachineID, "shell.exec", "run", map[string]any{
			"argv": input.Argv, "cwd": input.Cwd, "runtime": input.Runtime,
			"timeoutSeconds": input.TimeoutSeconds, "idempotencyKey": input.IdempotencyKey,
		})
		if err != nil {
			return nil, err
		}
		var out jobOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, err
		}
		return out, nil

	case "job_watch":
		input, err := toolInput[jobWatchInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		result, err := e.service.CallCapability(ctx, ownerID, input.MachineID, "job.control", "watch", map[string]any{
			"jobId": input.JobID, "cursor": input.Cursor, "waitSeconds": input.WaitSeconds,
		})
		if err != nil {
			return nil, err
		}
		var out jobOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, err
		}
		return out, nil

	case "job_cancel":
		input, err := toolInput[jobCancelInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		result, err := e.service.CallCapability(ctx, ownerID, input.MachineID, "job.control", "cancel", map[string]any{"jobId": input.JobID})
		if err != nil {
			return nil, err
		}
		var out jobOutput
		if err := decodeCapabilityResult(result, &out); err != nil {
			return nil, err
		}
		return out, nil

	case "git_control":
		input, err := toolInput[gitControlInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		result, err := e.service.CallCapability(ctx, ownerID, input.MachineID, "git.repository", input.Action, map[string]any{
			"repositoryPath": input.RepositoryPath, "revision": input.Revision, "paths": input.Paths, "message": input.Message,
			"remote": input.Remote, "branch": input.Branch, "worktreePath": input.WorktreePath, "idempotencyKey": input.IdempotencyKey,
		})
		if err != nil {
			return nil, err
		}
		return genericCapabilityOutput{Result: result}, nil

	case "build_control":
		input, err := toolInput[buildControlInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		result, err := e.service.CallCapability(ctx, ownerID, input.MachineID, "build.exec", input.Action, map[string]any{
			"argv": input.Argv, "cwd": input.Cwd, "runtime": input.Runtime,
			"timeoutSeconds": input.TimeoutSeconds, "idempotencyKey": input.IdempotencyKey,
		})
		if err != nil {
			return nil, err
		}
		return genericCapabilityOutput{Result: result}, nil

	case "browser_control":
		input, err := toolInput[browserControlInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		result, err := e.service.CallCapability(ctx, ownerID, input.MachineID, "browser.automation", input.Action, browserControlParams(input))
		if err != nil {
			return nil, err
		}
		return genericCapabilityOutput{Result: result}, nil

	case "screenshot_take":
		input, err := toolInput[screenshotTakeInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		params := map[string]any{"displayIndex": input.DisplayIndex, "windowId": input.WindowID, "format": input.Format, "quality": input.Quality}
		result, err := e.service.CallCapability(ctx, ownerID, input.MachineID, "screenshot.capture", input.Action, params)
		if err != nil {
			return nil, err
		}
		return genericCapabilityOutput{Result: result}, nil

	case "thinking_team":
		input, err := toolInput[thinkingTeamInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		result, err := thinkingTeamResult(input)
		if err != nil {
			return nil, err
		}
		return genericCapabilityOutput{Result: result}, nil

	case "ai_control":
		input, err := toolInput[aiControlInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		if input.Action == "session.create" && (len(input.IdempotencyKey) < 12 || len(input.IdempotencyKey) > 128) {
			return nil, &toolRequestError{message: "idempotencyKey is required for session.create and must be 12 to 128 characters"}
		}
		params := map[string]any{
			"providerId": input.ProviderID, "appType": input.AppType, "sessionId": input.SessionID, "turnId": input.TurnID, "requestId": input.RequestID,
			"idempotencyKey": input.IdempotencyKey, "mode": input.Mode,
			"visibility": input.Visibility, "backend": input.Backend, "visibilityTarget": input.VisibilityTarget, "ephemeral": input.Ephemeral,
			"prompt": input.Prompt, "workingDirectory": input.WorkingDirectory, "model": input.Model,
			"thinking": input.Thinking, "cursor": input.Cursor, "waitSeconds": input.WaitSeconds,
			"limit": input.Limit, "pageCursor": input.PageCursor, "mcpDetail": input.MCPDetail, "name": input.Name, "forceReload": input.ForceReload,
			"marketplaceKinds": input.MarketplaceKinds, "pluginName": input.PluginName, "marketplacePath": input.MarketplacePath,
			"remoteMarketplaceName": input.RemoteMarketplaceName, "remotePluginId": input.RemotePluginID, "skillName": input.SkillName,
			"numTurns": input.NumTurns, "objective": input.Objective, "goalStatus": input.GoalStatus, "tokenBudget": input.TokenBudget,
			"skills": input.Skills, "images": input.Images, "localImages": input.LocalImages, "mentions": input.Mentions, "imageDetail": input.ImageDetail,
			"outputSchema": input.OutputSchema, "decision": input.Decision, "answers": input.Answers, "responseContent": input.ResponseContent,
			"effort": input.Effort, "permissions": input.Permissions, "personality": input.Personality, "serviceTier": input.ServiceTier, "summary": input.Summary,
			"reviewType": input.ReviewType, "reviewDelivery": input.ReviewDelivery, "reviewBranch": input.ReviewBranch,
			"reviewSha": input.ReviewSHA, "reviewTitle": input.ReviewTitle, "reviewInstructions": input.ReviewInstructions,
			"callbackTargetSessionId": input.CallbackTargetSessionID, "callbackMissionId": input.CallbackMissionID,
			"callbackTaskId": input.CallbackTaskID, "callbackGeneration": input.CallbackGeneration, "callbackDeliverablePath": input.CallbackDeliverablePath,
			"callbackClaimId": input.CallbackClaimID, "callbackClaimLimit": input.CallbackClaimLimit,
		}
		if len(input.Skills) > 0 {
			converted := make([]map[string]any, len(input.Skills))
			for i, item := range input.Skills {
				converted[i] = map[string]any{"name": item["name"], "path": item["path"]}
			}
			params["skills"] = converted
		}
		if len(input.Mentions) > 0 {
			converted := make([]map[string]any, len(input.Mentions))
			for i, item := range input.Mentions {
				converted[i] = map[string]any{"name": item["name"], "path": item["path"]}
			}
			params["mentions"] = converted
		}
		result, err := e.service.CallCapability(ctx, ownerID, input.MachineID, "agent.control", input.Action, params)
		if err != nil {
			return nil, err
		}
		return genericCapabilityOutput{Result: result}, nil

	case "working_context":
		input, err := toolInput[workingContextInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		params := map[string]any{
			"projectPath": input.ProjectPath, "goal": input.Goal,
			"planId": input.PlanID, "expectedRevision": input.ExpectedRevision, "title": input.Title,
			"targetVersion": input.TargetVersion, "markdownRoot": input.MarkdownRoot, "initializeMarkdown": input.InitializeMarkdown,
			"baselineBranch": input.BaselineBranch, "baselineCommit": input.BaselineCommit,
			"completed": input.Completed, "constraints": input.Constraints, "pending": input.Pending,
			"keyFiles": input.KeyFiles, "facts": input.Facts,
			"tasks": input.Tasks, "taskId": input.TaskID, "taskTitle": input.TaskTitle, "taskStatus": input.TaskStatus,
			"blockedReason": input.BlockedReason, "completion": input.Completion, "evidence": input.Evidence,
			"markdownPath": input.MarkdownPath, "content": input.Content, "managedBlock": input.ManagedBlock,
			"expectedFileRevision": input.ExpectedFileRevision, "sinceRevision": input.SinceRevision, "waitSeconds": input.WaitSeconds,
		}
		result, err := e.service.CallCapability(ctx, ownerID, input.MachineID, "working.context", input.Action, params)
		if err != nil {
			return nil, err
		}
		return genericCapabilityOutput{Result: result}, nil

	case "artifact_get":
		input, err := toolInput[artifactGetInput](tool, rawInput)
		if err != nil {
			return nil, err
		}
		switch input.Action {
		case "uploadFile", "uploadJobLog":
			params := map[string]any{"path": input.Path, "jobId": input.JobID, "logicalName": input.LogicalName, "contentType": input.ContentType}
			result, err := e.service.CallCapability(ctx, ownerID, input.MachineID, "artifact.store", input.Action, params)
			if err != nil {
				return nil, err
			}
			return genericCapabilityOutput{Result: result}, nil
		case "publishFile":
			params := map[string]any{"path": input.Path, "logicalName": input.LogicalName, "contentType": input.ContentType}
			result, err := e.service.CallCapability(ctx, ownerID, input.MachineID, "artifact.store", input.Action, params)
			if err != nil {
				return nil, err
			}
			return genericCapabilityOutput{Result: result}, nil
		case "get":
			artifact, err := e.service.GetArtifact(ctx, ownerID, input.ArtifactID)
			if err != nil {
				return nil, err
			}
			raw, err := json.Marshal(artifact)
			if err != nil {
				return nil, err
			}
			var result map[string]any
			if err := json.Unmarshal(raw, &result); err != nil {
				return nil, err
			}
			result["downloadPath"] = "/api/v1/artifacts/" + artifact.ID + "/content"
			if content, ok, err := readArtifactInline(ctx, e.service, artifact); err != nil {
				return nil, err
			} else if ok {
				result["content"] = content
				result["encoding"] = "utf-8"
			}
			return genericCapabilityOutput{Result: result}, nil
		default:
			return nil, &toolRequestError{message: fmt.Sprintf("unsupported artifact action %q", input.Action)}
		}
	default:
		return nil, fmt.Errorf("unknown tool %q", tool)
	}
}

func validateCloudCompletionToolInput(input cloudCompletionInput) error {
	action := strings.TrimSpace(input.Action)
	if !validCloudCompletionToolID(input.ActorSessionID) {
		return &toolRequestError{message: "actorSessionId is required and must be a bounded opaque ID"}
	}
	switch action {
	case "notify":
		if strings.TrimSpace(input.CollaborationID) == "" || strings.TrimSpace(input.TaskID) == "" {
			return &toolRequestError{message: "collaborationId and taskId are required for notify"}
		}
		if input.Outcome != "completed" && input.Outcome != "blocked" && input.Outcome != "failed" {
			return &toolRequestError{message: "outcome must be completed, blocked, or failed for notify"}
		}
		if input.ActorSessionID == "$self" {
			if strings.TrimSpace(input.SourceSessionID) != "" {
				return &toolRequestError{message: "sourceSessionId must be omitted when actorSessionId is $self"}
			}
		} else if !validCloudCompletionToolID(input.SourceSessionID) {
			return &toolRequestError{message: "sourceSessionId is required for dispatcher fallback notify"}
		}
	case "claim":
		if input.ActorSessionID == "$self" {
			return &toolRequestError{message: "claim requires the dispatcher actorSessionId"}
		}
		if strings.TrimSpace(input.ClaimID) != "" && !validCloudCompletionToolID(input.ClaimID) {
			return &toolRequestError{message: "claimId must be a bounded opaque ID"}
		}
		if input.Limit < 0 || input.Limit > 64 {
			return &toolRequestError{message: "limit must be between 1 and 64 when provided"}
		}
	case "ack":
		if input.ActorSessionID == "$self" {
			return &toolRequestError{message: "ack requires the dispatcher actorSessionId"}
		}
		if !validCloudCompletionToolID(input.ClaimID) {
			return &toolRequestError{message: "claimId is required and must be a bounded opaque ID for ack"}
		}
		if len(input.Acknowledgements) > 64 {
			return &toolRequestError{message: "acknowledgements may contain at most 64 items"}
		}
	default:
		return &toolRequestError{message: "action must be notify, claim, or ack"}
	}
	return nil
}

func validCloudCompletionToolID(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n\t ")
}
