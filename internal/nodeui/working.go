package nodeui

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

type workingRequest struct {
	Action           string                  `json:"action"`
	ProjectPath      string                  `json:"projectPath,omitempty"`
	PlanID           string                  `json:"planId,omitempty"`
	TargetVersion    string                  `json:"targetVersion,omitempty"`
	ExpectedRevision string                  `json:"expectedRevision,omitempty"`
	TaskID           string                  `json:"taskId,omitempty"`
	TaskStatus       string                  `json:"taskStatus,omitempty"`
	Completion       *int                    `json:"completion,omitempty"`
	Evidence         *workingEvidenceRequest `json:"evidence,omitempty"`
	MarkdownPath     string                  `json:"markdownPath,omitempty"`
}

type workingEvidenceRequest struct {
	Summary   string `json:"summary"`
	Kind      string `json:"kind,omitempty"`
	Reference string `json:"reference,omitempty"`
}

var loopbackWorkingActions = map[string]bool{
	"plan.init": true, "plan.get": true, "plan.sync": true,
	"task.update": true, "markdown.list": true, "markdown.read": true,
}

func (a *App) handleWorking(w http.ResponseWriter, r *http.Request) {
	var input workingRequest
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	input.Action = strings.TrimSpace(input.Action)
	if input.Action == "folder.open" {
		a.handleWorkingFolderOpen(w, r)
		return
	}
	if !loopbackWorkingActions[input.Action] {
		writeAPIError(w, http.StatusBadRequest, errors.New("不支持的任务与进度操作"))
		return
	}
	input.ProjectPath = strings.TrimSpace(input.ProjectPath)
	input.PlanID = strings.TrimSpace(input.PlanID)
	if input.ProjectPath == "" || input.PlanID == "" {
		writeAPIError(w, http.StatusBadRequest, errors.New("项目路径和 planId 不能为空"))
		return
	}
	client, err := node.New(node.Config{DataDir: a.opts.DataDir, Version: a.opts.Version, Logger: a.opts.Logger})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, errors.New("无法读取本地任务状态"))
		return
	}
	params := map[string]any{"projectPath": input.ProjectPath, "planId": input.PlanID}
	addWorkingParams(params, input)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	response := client.HandleLocalCapability(ctx, protocolv1.CapabilityRequest{
		RequestId: "nodeui-working", Capability: "working.context", Action: input.Action, Params: params,
	})
	if response.Error != nil {
		writeWorkingError(w, response.Error)
		return
	}
	if input.Action == "plan.init" || input.Action == "plan.get" {
		if exists, _ := response.Result["exists"].(bool); exists {
			projectPath, planID := workingBinding(response.Result, input.ProjectPath, input.PlanID)
			if err := a.bindWorkingPlan(projectPath, planID); err != nil {
				writeAPIError(w, http.StatusInternalServerError, errors.New("任务状态已读取，但无法保存本地绑定"))
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, response.Result)
}

func workingBinding(result map[string]any, fallbackProjectPath, fallbackPlanID string) (string, string) {
	state, _ := result["state"].(map[string]any)
	projectPath, _ := state["projectPath"].(string)
	planID, _ := state["planId"].(string)
	if strings.TrimSpace(projectPath) == "" {
		projectPath = fallbackProjectPath
	}
	if strings.TrimSpace(planID) == "" {
		planID = fallbackPlanID
	}
	return projectPath, planID
}

func addWorkingParams(params map[string]any, input workingRequest) {
	if input.Action == "plan.init" {
		params["goal"] = "维护项目任务与进度"
		params["title"] = "任务与进度"
		params["initializeMarkdown"] = true
		if value := strings.TrimSpace(input.TargetVersion); value != "" {
			params["targetVersion"] = value
		}
	}
	if input.ExpectedRevision != "" {
		params["expectedRevision"] = input.ExpectedRevision
	}
	if input.TaskID != "" {
		params["taskId"] = input.TaskID
	}
	if input.TaskStatus != "" {
		params["taskStatus"] = input.TaskStatus
	}
	if input.Completion != nil {
		params["completion"] = *input.Completion
	}
	if input.Evidence != nil {
		params["evidence"] = input.Evidence
	}
	if input.MarkdownPath != "" {
		params["markdownPath"] = input.MarkdownPath
	}
}

func (a *App) bindWorkingPlan(projectPath, planID string) error {
	a.mu.Lock()
	next := a.config
	next.WorkingProjectPath = projectPath
	next.WorkingPlanID = planID
	a.mu.Unlock()
	if err := saveLocalConfig(a.opts.DataDir, next); err != nil {
		return err
	}
	a.mu.Lock()
	a.config = next
	a.mu.Unlock()
	return nil
}

func (a *App) handleWorkingFolderOpen(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	projectPath, planID := a.config.WorkingProjectPath, a.config.WorkingPlanID
	a.mu.Unlock()
	if projectPath == "" || planID == "" {
		writeAPIError(w, http.StatusBadRequest, errors.New("请先初始化或绑定项目计划"))
		return
	}
	client, err := node.New(node.Config{DataDir: a.opts.DataDir, Version: a.opts.Version, Logger: a.opts.Logger})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, errors.New("无法读取本地任务状态"))
		return
	}
	folder, err := client.WorkingMarkdownFolder(r.Context(), projectPath, planID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, errors.New("Markdown Workspace 不可用，请先刷新或初始化"))
		return
	}
	if err := a.openFolder(folder); err != nil {
		writeAPIError(w, http.StatusInternalServerError, errors.New("无法打开 Markdown 文件夹"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"opened": true})
}

func writeWorkingError(w http.ResponseWriter, problem *protocolv1.ProtocolError) {
	if problem.Code == "REVISION_CONFLICT" {
		writeJSON(w, http.StatusConflict, map[string]any{"code": problem.Code, "error": "内容已变化，请刷新后重试"})
		return
	}
	status := http.StatusBadRequest
	message := "任务与进度操作失败，请检查项目路径和输入内容"
	if problem.Code == "NOT_FOUND" {
		status, message = http.StatusNotFound, "尚未找到该计划，请先初始化或绑定"
	}
	writeJSON(w, status, map[string]any{"code": problem.Code, "error": message})
}
