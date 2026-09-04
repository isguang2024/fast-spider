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
	Action           string `json:"action"`
	ProjectPath      string `json:"projectPath,omitempty"`
	Text             string `json:"text,omitempty"`
	ExpectedRevision string `json:"expectedRevision,omitempty"`
}

var loopbackWorkingActions = map[string]bool{"get": true, "set": true, "clear": true}

func (a *App) handleWorking(w http.ResponseWriter, r *http.Request) {
	var input workingRequest
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	input.Action = strings.TrimSpace(input.Action)
	if !loopbackWorkingActions[input.Action] {
		writeAPIError(w, http.StatusBadRequest, errors.New("不支持的项目上下文操作"))
		return
	}
	input.ProjectPath = strings.TrimSpace(input.ProjectPath)
	if input.ProjectPath == "" {
		writeAPIError(w, http.StatusBadRequest, errors.New("项目路径不能为空"))
		return
	}
	client, err := node.New(node.Config{DataDir: a.opts.DataDir, Version: a.opts.Version, Logger: a.opts.Logger})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, errors.New("无法读取本地项目上下文"))
		return
	}
	params := map[string]any{"projectPath": input.ProjectPath}
	if input.Action == "set" {
		params["text"] = input.Text
	}
	if input.ExpectedRevision != "" {
		params["expectedRevision"] = input.ExpectedRevision
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	response := client.HandleLocalCapability(ctx, protocolv1.CapabilityRequest{
		RequestId: "nodeui-working", Capability: "working.context", Action: input.Action, Params: params,
	})
	if response.Error != nil {
		writeWorkingError(w, response.Error)
		return
	}
	if input.Action == "get" || input.Action == "set" {
		if err := a.bindWorkingProject(input.ProjectPath); err != nil {
			writeAPIError(w, http.StatusInternalServerError, errors.New("项目上下文已读取，但无法保存本地绑定"))
			return
		}
	}
	writeJSON(w, http.StatusOK, response.Result)
}

func (a *App) bindWorkingProject(projectPath string) error {
	a.mu.Lock()
	next := a.config
	next.WorkingProjectPath = projectPath
	a.mu.Unlock()
	if err := saveLocalConfig(a.opts.DataDir, next); err != nil {
		return err
	}
	a.mu.Lock()
	a.config = next
	a.mu.Unlock()
	return nil
}

func writeWorkingError(w http.ResponseWriter, problem *protocolv1.ProtocolError) {
	if problem.Code == "REVISION_CONFLICT" {
		writeJSON(w, http.StatusConflict, map[string]any{"code": problem.Code, "error": "内容已变化，请刷新后再保存"})
		return
	}
	status := http.StatusBadRequest
	message := "项目上下文操作失败，请检查项目路径和文本内容"
	if problem.Code == "NOT_FOUND" {
		status, message = http.StatusNotFound, "尚未保存该项目的上下文"
	}
	writeJSON(w, status, map[string]any{"code": problem.Code, "error": message})
}
