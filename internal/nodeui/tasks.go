package nodeui

import (
	"context"
	"database/sql"
	"errors"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/agent"
)

func (a *App) handleTaskCenter(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	page := strings.ReplaceAll(taskCenterHTML, "{{UI_TOKEN}}", html.EscapeString(a.uiToken))
	page = strings.ReplaceAll(page, "{{VERSION}}", html.EscapeString(a.opts.Version))
	_, _ = io.WriteString(w, page)
}

func (a *App) handleTaskView(w http.ResponseWriter, r *http.Request) {
	var after int64
	if raw := r.URL.Query().Get("after"); raw != "" {
		var err error
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || after < 0 {
			writeAPIError(w, http.StatusBadRequest, errors.New("无效的活动游标"))
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	view, err := agent.ReadNativeRunnerView(ctx, a.opts.DataDir, r.PathValue("projectID"), r.PathValue("taskID"), after, r.Pattern == "GET /api/tasks/{projectID}/events")
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, errors.New("任务区或任务不存在"))
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, errors.New("暂时无法读取任务状态，请稍后重试"))
		return
	}
	writeJSON(w, http.StatusOK, view)
}

type taskActionRequest struct {
	Action   string `json:"action"`
	TaskID   string `json:"taskId,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

func (a *App) handleTaskAction(w http.ResponseWriter, r *http.Request) {
	var req taskActionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action != "cancel" && action != "archive" && action != "unarchive" && action != "pause" && action != "resume" {
		writeAPIError(w, http.StatusBadRequest, errors.New("unsupported task action"))
		return
	}
	if (action == "pause" || action == "resume") && strings.TrimSpace(req.TaskID) != "" {
		writeAPIError(w, http.StatusBadRequest, errors.New("task area action cannot target a task"))
		return
	}
	projectID := strings.TrimSpace(r.PathValue("projectID"))
	if projectID == "" {
		writeAPIError(w, http.StatusBadRequest, errors.New("project id is required"))
		return
	}
	if a.agentController == nil {
		writeAPIError(w, http.StatusServiceUnavailable, errors.New("agent controller unavailable"))
		return
	}
	params := map[string]any{"projectId": projectID}
	if taskID := strings.TrimSpace(req.TaskID); taskID != "" {
		params["taskId"] = taskID
	}
	if evidence := strings.TrimSpace(req.Evidence); evidence != "" {
		params["evidence"] = evidence
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, err := a.agentController.Control(ctx, "runner."+action, params)
	if err != nil {
		writeAPIError(w, http.StatusConflict, err)
		return
	}
	if result == nil {
		result = map[string]any{}
	}
	result["accepted"] = true
	writeJSON(w, http.StatusAccepted, result)
}
