package nodeui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isguang2024/fast-spider/internal/node"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestWorkingProgressLoopbackUsesNodePlanAndReportsCASConflict(t *testing.T) {
	dataDir := t.TempDir()
	project := t.TempDir()
	client, err := node.New(node.Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	seed := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "seed", Capability: "working.context", Action: "plan.init",
		Params: map[string]any{
			"projectPath": project, "planId": "fs-041", "goal": "verify local UI", "targetVersion": "0.4.1", "initializeMarkdown": true,
			"tasks": []map[string]any{{"id": "FS-041-005", "title": "任务与进度页面", "status": "in_progress", "completion": 50}},
		},
	})
	if seed.Error != nil {
		t.Fatalf("seed plan: %+v", seed.Error)
	}
	app, err := New(Options{DataDir: dataDir, Version: "ui-test", MachineName: "Test Node", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}

	get := callWorkingAPI(t, app, map[string]any{"action": "plan.get", "projectPath": project, "planId": "fs-041"})
	if get.Code != http.StatusOK {
		t.Fatalf("plan.get status=%d body=%s", get.Code, get.Body.String())
	}
	var plan struct {
		Revision string `json:"revision"`
		State    struct {
			PlanID        string `json:"planId"`
			TargetVersion string `json:"targetVersion"`
		} `json:"state"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Revision == "" || plan.State.PlanID != "fs-041" || plan.State.TargetVersion != "0.4.1" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if app.config.WorkingPlanID != "fs-041" {
		t.Fatalf("working binding not saved: %+v", app.config)
	}
	assertSameExistingPath(t, app.config.WorkingProjectPath, project)

	listed := callWorkingAPI(t, app, map[string]any{"action": "markdown.list", "projectPath": project, "planId": "fs-041"})
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "00-current-state.md") {
		t.Fatalf("markdown.list status=%d body=%s", listed.Code, listed.Body.String())
	}
	updated := callWorkingAPI(t, app, map[string]any{
		"action": "task.update", "projectPath": project, "planId": "fs-041", "expectedRevision": plan.Revision,
		"taskId": "FS-041-005", "taskStatus": "done", "completion": 100,
		"evidence": map[string]any{"summary": "loopback HTTP test passed", "kind": "test", "reference": "go test ./internal/nodeui"},
	})
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), "loopback HTTP test passed") {
		t.Fatalf("task.update status=%d body=%s", updated.Code, updated.Body.String())
	}
	conflict := callWorkingAPI(t, app, map[string]any{
		"action": "task.update", "projectPath": project, "planId": "fs-041", "expectedRevision": plan.Revision,
		"taskId": "FS-041-005", "taskStatus": "in_progress", "completion": 75,
	})
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "内容已变化，请刷新后重试") {
		t.Fatalf("CAS conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}

	var latest struct {
		Revision string `json:"revision"`
	}
	latestGet := callWorkingAPI(t, app, map[string]any{"action": "plan.get", "projectPath": project, "planId": "fs-041"})
	if err := json.Unmarshal(latestGet.Body.Bytes(), &latest); err != nil {
		t.Fatal(err)
	}
	synced := callWorkingAPI(t, app, map[string]any{"action": "plan.sync", "projectPath": project, "planId": "fs-041", "expectedRevision": latest.Revision})
	if synced.Code != http.StatusOK || !strings.Contains(synced.Body.String(), "00-current-state.md") {
		t.Fatalf("plan.sync status=%d body=%s", synced.Code, synced.Body.String())
	}
	read := callWorkingAPI(t, app, map[string]any{"action": "markdown.read", "projectPath": project, "planId": "fs-041", "markdownPath": "docs/progress/03-acceptance-log.md"})
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), "loopback HTTP test passed") {
		t.Fatalf("markdown.read status=%d body=%s", read.Code, read.Body.String())
	}
}

func TestWorkingProgressInitAndFolderOpenUseBoundMarkdownRoot(t *testing.T) {
	dataDir := t.TempDir()
	project := t.TempDir()
	app, err := New(Options{DataDir: dataDir, Version: "ui-test", MachineName: "Test Node", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	opened := ""
	app.openFolder = func(path string) error { opened = path; return nil }
	initialized := callWorkingAPI(t, app, map[string]any{"action": "plan.init", "projectPath": project, "planId": "default", "targetVersion": "0.4.1"})
	if initialized.Code != http.StatusOK {
		t.Fatalf("plan.init status=%d body=%s", initialized.Code, initialized.Body.String())
	}
	for _, name := range []string{"00-current-state.md", "01-roadmap-0.4.md", "02-decisions.md", "03-acceptance-log.md", "04-open-issues.md", "05-change-log.md"} {
		if _, err := os.Stat(filepath.Join(project, "docs", "progress", name)); err != nil {
			t.Fatalf("default Markdown %s: %v", name, err)
		}
	}
	external := t.TempDir()
	openedResponse := callWorkingAPI(t, app, map[string]any{"action": "folder.open", "projectPath": external, "planId": "other"})
	if openedResponse.Code != http.StatusOK {
		t.Fatalf("folder.open status=%d body=%s", openedResponse.Code, openedResponse.Body.String())
	}
	want := filepath.Join(project, "docs", "progress")
	assertSameExistingPath(t, opened, want)
}

func TestWorkingProgressUIHasTopLevelPageAndNoOpaqueMachineID(t *testing.T) {
	for _, required := range []string{
		`data-tab="working"`, `id="tab-working"`, `id="working-init"`, `id="working-task-save"`,
		`id="working-evidence-add"`, `id="working-sync"`, `id="working-open"`, `id="working-markdown"`,
		"setInterval(refresh, 10000)",
	} {
		if !strings.Contains(localUIHTML, required) {
			t.Fatalf("local UI missing %q", required)
		}
	}
	if strings.Contains(localUIHTML, "Machine ID") || strings.Contains(localUIHTML, `id="machine-id"`) || strings.Contains(localUIHTML, "setInterval(refresh, 2000)") {
		t.Fatal("local UI exposes an opaque machine ID or uses the old high-frequency refresh")
	}
}

func callWorkingAPI(t *testing.T, app *App, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/working", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
	response := httptest.NewRecorder()
	app.handler().ServeHTTP(response, req)
	return response
}

func assertSameExistingPath(t *testing.T, got, want string) {
	t.Helper()
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat actual path %q: %v", got, err)
	}
	wantInfo, err := os.Stat(want)
	if err != nil {
		t.Fatalf("stat expected path %q: %v", want, err)
	}
	if !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("paths identify different files: got=%q want=%q", got, want)
	}
}
