package nodeui

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	_, err = db.Exec(`CREATE TABLE runner_projects(id TEXT PRIMARY KEY,value TEXT); CREATE TABLE runner_tasks(project_id TEXT,id TEXT,value TEXT); CREATE TABLE runner_events(id INTEGER PRIMARY KEY,project_id TEXT,kind TEXT,value TEXT,created INTEGER);
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
