package nodeui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/isguang2024/fast-spider/internal/componentmgr"
	"github.com/isguang2024/fast-spider/internal/node"
	"github.com/isguang2024/fast-spider/internal/releaseinfo"
)

const nodeUIFakeRGEnv = "FAST_SPIDER_NODEUI_FAKE_RG"

func TestMain(m *testing.M) {
	if os.Getenv(nodeUIFakeRGEnv) == "1" {
		root := os.Args[len(os.Args)-1]
		path := filepath.Join(root, "fixture.txt")
		events := []map[string]any{
			{"type": "begin", "data": map[string]any{"path": map[string]any{"text": path}}},
			{"type": "match", "data": map[string]any{"path": map[string]any{"text": path}, "lines": map[string]any{"text": "needle diagnostic\n"}, "line_number": 2, "submatches": []map[string]any{{"start": 0, "end": 6}}}},
			{"type": "end", "data": map[string]any{"path": map[string]any{"text": path}}},
			{"type": "summary", "data": map[string]any{}},
		}
		for _, event := range events {
			raw, _ := json.Marshal(event)
			fmt.Println(string(raw))
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func newComponentTestApp(t *testing.T, dataDir string) *App {
	t.Helper()
	app, err := New(Options{DataDir: dataDir, Version: "ui-test", MachineName: "Test Node", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func installComponentFixture(t *testing.T, dataDir, id, version string, executable bool) string {
	t.Helper()
	dir := filepath.Join(dataDir, "components", id, version)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := releaseinfo.NewManifest("component", id, runtime.GOOS+"-"+runtime.GOARCH, version, strings.Repeat("a", 64), 1, "/private?token=secret")
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, ".fast-spider-component.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if executable {
		name := "rg"
		if runtime.GOOS == "windows" {
			name = "rg.exe"
		}
		source, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		input, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), input, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func authorizedRequest(app *App, method, path string, body []byte) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func TestComponentsStatusIsAllowlistedAndContainsOnlyKnownComponents(t *testing.T) {
	dataDir := t.TempDir()
	browserDir := installComponentFixture(t, dataDir, "browser", "1.2.3", false)
	installComponentFixture(t, dataDir, "search-ripgrep", "14.1.0", true)
	app := newComponentTestApp(t, dataDir)
	app.config.BrowserSidecarDir = browserDir

	response := httptest.NewRecorder()
	app.handler().ServeHTTP(response, authorizedRequest(app, http.MethodGet, "/api/components", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var view componentsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Components) != 2 || view.Components[0].ID != "browser" || view.Components[1].ID != "search-ripgrep" || !view.Components[0].EngineReady || !view.Components[1].ExecutableReady {
		t.Fatalf("components=%+v", view.Components)
	}
	for _, forbidden := range []string{dataDir, browserDir, "componentRoot", "path", "hubUrl", "token", "secret", "key"} {
		if strings.Contains(strings.ToLower(response.Body.String()), strings.ToLower(forbidden)) {
			t.Fatalf("component status leaked %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestComponentEnsureRejectsUnknownAndAllowsKnownIDs(t *testing.T) {
	dataDir := t.TempDir()
	app := newComponentTestApp(t, dataDir)
	called := []string{}
	app.componentEnsure = func(_ context.Context, _, _, _, id string) (componentmgr.Installed, error) {
		called = append(called, id)
		return componentmgr.Installed{ID: id, Platform: runtime.GOOS + "-" + runtime.GOARCH, Version: "1.0.0"}, nil
	}
	unknown := httptest.NewRecorder()
	app.handler().ServeHTTP(unknown, authorizedRequest(app, http.MethodPost, "/api/components/ensure", []byte(`{"componentId":"arbitrary"}`)))
	if unknown.Code != http.StatusBadRequest || len(called) != 0 {
		t.Fatalf("unknown status=%d called=%v body=%s", unknown.Code, called, unknown.Body.String())
	}
	if err := node.SaveState(filepath.Join(dataDir, "state.json"), node.State{HubURL: "https://hub.example", MachineID: "machine", HubPublicKey: "public", HubFingerprint: "fingerprint"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"browser", "search-ripgrep"} {
		response := httptest.NewRecorder()
		body, _ := json.Marshal(componentEnsureRequest{ComponentID: id})
		app.handler().ServeHTTP(response, authorizedRequest(app, http.MethodPost, "/api/components/ensure", body))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", id, response.Code, response.Body.String())
		}
	}
	if strings.Join(called, ",") != "browser,search-ripgrep" {
		t.Fatalf("allowed calls=%v", called)
	}
}

func TestComponentPageHasManualControlsAndNoAutomaticActions(t *testing.T) {
	for _, required := range []string{`data-tab="components"`, `id="tab-components"`, `id="component-browser-data"`, `id="component-ripgrep-data"`, `id="browser-install"`, `id="ripgrep-install"`, `id="components-refresh"`, `id="search-file-self-test"`, "/api/components", "/api/search-file/self-test"} {
		if !strings.Contains(localUIHTML, required) {
			t.Fatalf("component UI missing %q", required)
		}
	}
	for _, forbidden := range []string{"setInterval(refreshComponents", "setInterval(runSearchFileSelfTest", "refreshComponents();\n  refreshStatus", "component-root"} {
		if strings.Contains(localUIHTML, forbidden) {
			t.Fatalf("component UI contains automatic/path marker %q", forbidden)
		}
	}
}

func TestSearchFileSelfTestNativeAndCleanup(t *testing.T) {
	dataDir := t.TempDir()
	app := newComponentTestApp(t, dataDir)
	response := httptest.NewRecorder()
	app.handler().ServeHTTP(response, authorizedRequest(app, http.MethodPost, "/api/search-file/self-test", []byte(`{}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result searchFileSelfTestResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "PASS" || result.Engine != "native" || result.FileRead != "PASS" || result.FileEditPreview != "PASS" {
		t.Fatalf("native self-test=%+v", result)
	}
	matches, err := filepath.Glob(filepath.Join(dataDir, diagnosticTempPrefix+"*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("diagnostic temp remains=%v err=%v", matches, err)
	}
	for _, forbidden := range []string{dataDir, "raw stderr", "token", "secret", "endpoint"} {
		if strings.Contains(strings.ToLower(response.Body.String()), strings.ToLower(forbidden)) {
			t.Fatalf("self-test leaked %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestSearchFileSelfTestUsesVerifiedManagedRipgrep(t *testing.T) {
	dataDir := t.TempDir()
	installComponentFixture(t, dataDir, "search-ripgrep", "14.1.0", true)
	t.Setenv(nodeUIFakeRGEnv, "1")
	app := newComponentTestApp(t, dataDir)
	result := app.runSearchFileSelfTest(context.Background())
	if result.Status != "PASS" || result.Engine != "ripgrep" || result.FallbackReason != "" || result.FileRead != "PASS" || result.FileEditPreview != "PASS" {
		t.Fatalf("managed self-test=%+v", result)
	}
}

func TestComponentAndSearchFileGuards(t *testing.T) {
	app := newComponentTestApp(t, t.TempDir())
	handler := app.handler()
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/components", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d", unauthorized.Code)
	}
	badOriginReq := authorizedRequest(app, http.MethodGet, "/api/search-file/status", nil)
	badOriginReq.Header.Set("Origin", "https://attacker.example")
	badOrigin := httptest.NewRecorder()
	handler.ServeHTTP(badOrigin, badOriginReq)
	if badOrigin.Code != http.StatusForbidden {
		t.Fatalf("bad origin=%d", badOrigin.Code)
	}
	wrongMethod := httptest.NewRecorder()
	handler.ServeHTTP(wrongMethod, authorizedRequest(app, http.MethodGet, "/api/search-file/self-test", nil))
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method=%d", wrongMethod.Code)
	}
}
