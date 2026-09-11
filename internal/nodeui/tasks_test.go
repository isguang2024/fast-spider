package nodeui

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type taskActionTestAgent struct {
	actions []string
	params  []map[string]any
}

func (a *taskActionTestAgent) Control(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	a.actions = append(a.actions, action)
	a.params = append(a.params, params)
	return map[string]any{"saved": true}, nil
}

func (*taskActionTestAgent) Close(context.Context) error { return nil }

func TestTaskCenterRoutesKeepEventsIDsAndAreaIsolation(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "native-runner"), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "native-runner", "projects.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE runner_settings(id TEXT PRIMARY KEY,value TEXT); CREATE TABLE runner_projects(id TEXT PRIMARY KEY,value TEXT); CREATE TABLE runner_tasks(project_id TEXT,id TEXT,value TEXT); CREATE TABLE runner_events(id INTEGER PRIMARY KEY,project_id TEXT,kind TEXT,value TEXT,created INTEGER);
	INSERT INTO runner_projects VALUES('events','{"id":"events","goalVersion":"v1"}'),('other','{"id":"other","goalVersion":"v1"}');
	INSERT INTO runner_tasks VALUES('events','events','{"id":"events","title":"Route task","state":"queued"}');`)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{opts: Options{DataDir: dir}, uiToken: "task-token"}
	for _, tc := range []struct {
		path, contains string
		status         int
	}{
		{"/api/tasks/events/tasks/events", `"title":"Route task"`, 200},
		{"/api/tasks/events/events", `"events":[]`, 200},
		{"/api/tasks/other/tasks/events", "不存在", 404},
	} {
		r := httptest.NewRequest("GET", tc.path, nil)
		r.Header.Set("X-Fast-Spider-UI-Token", "task-token")
		w := httptest.NewRecorder()
		a.handler().ServeHTTP(w, r)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
}

func TestTaskCenterUsesExistingLocalAuthorization(t *testing.T) {
	a := &App{opts: Options{DataDir: t.TempDir(), Version: "test"}, uiToken: "task-token"}
	for _, tc := range []struct {
		path, token, origin string
		want                int
	}{
		{"/api/tasks", "", "", http.StatusUnauthorized},
		{"/api/tasks", "task-token", "https://untrusted.example", http.StatusForbidden},
		{"/api/tasks", "task-token", "", http.StatusOK},
		{"/api/tasks?after=-1", "task-token", "", http.StatusBadRequest},
	} {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		r.Header.Set("X-Fast-Spider-UI-Token", tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		a.apiOnly(a.handleTaskView)(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s status=%d body=%s", tc.path, w.Code, w.Body.String())
		}
	}
}

func TestTaskCenterPageSecurityAndToken(t *testing.T) {
	a := &App{opts: Options{Version: "test"}, uiToken: "task-token"}
	w := httptest.NewRecorder()
	a.handleTaskCenter(w, httptest.NewRequest(http.MethodGet, "/tasks", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "task-token") || strings.Contains(w.Body.String(), "{{UI_TOKEN}}") {
		t.Fatal("task page token not rendered")
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("task page lost local CSP")
	}
}

func TestTaskCenterPageRendersSchedulingAndOptionalEstimate(t *testing.T) {
	a := &App{opts: Options{Version: "test"}, uiToken: "task-token"}
	w := httptest.NewRecorder()
	a.handleTaskCenter(w, httptest.NewRequest(http.MethodGet, "/tasks", nil))
	body := w.Body.String()
	for _, want := range []string{"调度状态", "全局占用", "本区运行", "本区可新增", "任务区上限", "动态建议", "estimatedMinutes", "持续循环", "暂停任务区", "恢复任务区", "取消任务区", "归档任务区", "取消归档任务区", "只停止新派发", "activityState", "businessComplete", "business_complete_pending", "planning", "p.questions", "needs_recovery", "awaiting_review", "待恢复", "待验收", "业务已完成 · 待处理事项", "回调待确认", "待处理"} {
		if !strings.Contains(body, want) {
			t.Fatalf("task page missing %q", want)
		}
	}
}

func TestTaskCenterPageRendersQueueWorkspaceAndArtifactFields(t *testing.T) {
	a := &App{opts: Options{Version: "test"}, uiToken: "task-token"}
	w := httptest.NewRecorder()
	a.handleTaskCenter(w, httptest.NewRequest(http.MethodGet, "/tasks", nil))
	body := w.Body.String()
	for _, want := range []string{
		"queueReason",
		"排队：",
		"阻塞 / 排队原因",
		"依赖任务 ",
		"awaiting_integration",
		"integrating",
		"集成状态",
		"pending_integration",
		"共享工作区",
		"阶段产出",
		"所需产出",
		"outputs",
		"requires",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("task page missing %q", want)
		}
	}
}

func TestTaskCenterPageRendersBackendPresentationStages(t *testing.T) {
	a := &App{opts: Options{Version: "test"}, uiToken: "task-token"}
	w := httptest.NewRecorder()
	a.handleTaskCenter(w, httptest.NewRequest(http.MethodGet, "/tasks", nil))
	body := w.Body.String()
	for _, want := range []string{
		"presentation",
		"presentationLabel",
		"presentationSummary",
		"当前阶段",
		"阶段开始",
		"下一处理时间",
		"负责验收任务",
		"负责会话",
		"waiting_dependency",
		"waiting_artifact",
		"waiting_scope",
		"waiting_capacity",
		"retry_wait",
		"pending_plan",
		"workspace_preparing",
		"dispatching",
		"executing",
		"waiting_job",
		"recovering",
		"report_missing",
		"checks_queued",
		"checks_running",
		"checks_failed",
		"validating_commit",
		"review_queued",
		"reviewing",
		"review_applying",
		"review_retry",
		"needs_fix",
		"awaiting_integration",
		"integrating",
		"accepted",
		"deferred",
		"canceling",
		"cancelled",
		"returned:'已返回'",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("task page missing %q", want)
		}
	}
}

func TestTaskCenterActionsAllowlistAuthAndForwarding(t *testing.T) {
	agent := &taskActionTestAgent{}
	a := &App{opts: Options{DataDir: t.TempDir()}, uiToken: "task-token", agentController: agent}
	call := func(path, token, origin, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Fast-Spider-UI-Token", token)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		a.handler().ServeHTTP(w, r)
		return w
	}
	if got := call("/api/tasks/project/actions", "", "", `{"action":"cancel"}`).Code; got != http.StatusUnauthorized {
		t.Fatalf("missing token status=%d", got)
	}
	if got := call("/api/tasks/project/actions", "task-token", "https://untrusted.example", `{"action":"cancel"}`).Code; got != http.StatusForbidden {
		t.Fatalf("invalid origin status=%d", got)
	}
	if got := call("/api/tasks/project/actions", "task-token", "", `{"action":"restart"}`).Code; got != http.StatusBadRequest {
		t.Fatalf("unsupported action status=%d", got)
	}
	if got := call("/api/tasks/project/actions", "task-token", "", `{"action":"pause"}`).Code; got != http.StatusAccepted {
		t.Fatalf("project pause status=%d", got)
	}
	if got := call("/api/tasks/project/actions", "task-token", "", `{"action":"resume"}`).Code; got != http.StatusAccepted {
		t.Fatalf("project resume status=%d", got)
	}
	if got := call("/api/tasks/project/actions", "task-token", "", `{"action":"pause","taskId":"task-1"}`).Code; got != http.StatusBadRequest {
		t.Fatalf("task pause unexpectedly accepted status=%d", got)
	}
	if got := call("/api/tasks/project/actions", "task-token", "", `{"action":"cancel","taskId":"task-1","evidence":"user requested"}`).Code; got != http.StatusAccepted {
		t.Fatalf("task action status=%d", got)
	}
	if got := call("/api/tasks/project/actions", "task-token", "", `{"action":"archive"}`).Code; got != http.StatusAccepted {
		t.Fatalf("project action status=%d", got)
	}
	if len(agent.actions) != 4 || agent.actions[0] != "runner.pause" || agent.actions[1] != "runner.resume" || agent.actions[2] != "runner.cancel" || agent.actions[3] != "runner.archive" {
		t.Fatalf("forwarded actions=%v", agent.actions)
	}
	if got := agent.params[2]; got["projectId"] != "project" || got["taskId"] != "task-1" || got["evidence"] != "user requested" {
		t.Fatalf("task params=%#v", got)
	}
	for _, index := range []int{0, 1, 3} {
		if got := agent.params[index]; got["projectId"] != "project" {
			t.Fatalf("project params[%d]=%#v", index, got)
		}
	}
	for _, index := range []int{0, 1, 3} {
		if _, ok := agent.params[index]["taskId"]; ok {
			t.Fatalf("project action unexpectedly included taskId: %#v", agent.params[index])
		}
	}
}
