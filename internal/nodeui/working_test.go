package nodeui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/isguang2024/fast-spider/internal/node"
)

func TestWorkingLoopbackReadsAndWritesPlainText(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	app := newWorkingTestApp(t)

	saved := callWorkingAPI(t, app, map[string]any{
		"action": "set", "projectPath": project, "text": "# 目标\n完成简化\n\n## 下一步\n运行测试",
	})
	if saved.Code != http.StatusOK {
		t.Fatalf("set status=%d body=%s", saved.Code, saved.Body.String())
	}
	var state struct {
		Revision string `json:"revision"`
		State    struct {
			Text string `json:"text"`
		} `json:"state"`
	}
	if err := json.Unmarshal(saved.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Revision == "" || state.State.Text == "" {
		t.Fatalf("saved=%+v", state)
	}
	if app.config.WorkingProjectPath != project {
		t.Fatalf("bound project=%q want=%q", app.config.WorkingProjectPath, project)
	}

	get := callWorkingAPI(t, app, map[string]any{"action": "get", "projectPath": project})
	if get.Code != http.StatusOK || !bytes.Contains(get.Body.Bytes(), []byte("完成简化")) {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.String())
	}
	conflict := callWorkingAPI(t, app, map[string]any{
		"action": "set", "projectPath": project, "text": "stale", "expectedRevision": "sha256:stale",
	})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	legacy := callWorkingAPI(t, app, map[string]any{"action": "plan.init", "projectPath": project})
	if legacy.Code != http.StatusBadRequest {
		t.Fatalf("legacy status=%d body=%s", legacy.Code, legacy.Body.String())
	}
}

func newWorkingTestApp(t *testing.T) *App {
	t.Helper()
	dataDir := t.TempDir()
	app := &App{opts: Options{DataDir: dataDir, Version: "test"}}
	client, err := node.New(node.Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	_ = client
	return app
}

func callWorkingAPI(t *testing.T, app *App, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/working", bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.handleWorking(response, request)
	return response
}
