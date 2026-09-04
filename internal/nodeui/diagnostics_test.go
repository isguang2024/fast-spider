package nodeui

import (
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

func TestDiagnosticsLoopbackAllowlistWorkspaceAndReadOnlyDiscovery(t *testing.T) {
	dataDir := t.TempDir()
	projectPath := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	client, err := node.New(node.Config{DataDir: dataDir, Version: "node-test", Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	initialized := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "diagnostics-test-init", Capability: "working.context", Action: "set",
		Params: map[string]any{"projectPath": projectPath, "text": "diagnostics test"},
	})
	if initialized.Error != nil {
		t.Fatalf("initialize working plan: %+v", initialized.Error)
	}
	if err := node.SaveState(filepath.Join(dataDir, "state.json"), node.State{
		HubURL:    "https://private@example.test@hub.example.test/private?token=" + aiSensitiveMarker,
		MachineID: "machine-private", CredentialID: "device-key-private", HubPublicKey: "public-key",
		HubFingerprint: "sha256:" + strings.Repeat("b", 64),
	}); err != nil {
		t.Fatal(err)
	}
	browserPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "components"), 0o700); err != nil {
		t.Fatal(err)
	}

	app, err := New(Options{DataDir: dataDir, Version: "ui-test", MachineName: "private@example.test", Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAIController{}
	app.agentController = fake
	app.config.WorkingProjectPath = projectPath
	app.config.HubURL = "https://private.example/private?token=" + aiSensitiveMarker
	app.config.BrowserSidecarDir = browserPath
	app.config.LocalBridgeEnabled = true
	app.runtimeStatus = "error"
	app.runtimeError = "unauthorized upstream token=" + aiSensitiveMarker + " endpoint=https://private.example/api"

	handler := app.handler()
	index := httptest.NewRecorder()
	handler.ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/", nil))
	if index.Code != http.StatusOK {
		t.Fatalf("index status=%d", index.Code)
	}
	if len(fake.actions) != 0 {
		t.Fatalf("initial page load triggered Agent actions: %#v", fake.actions)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics", nil)
	req.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("diagnostics status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, forbidden := range []string{
		aiSensitiveMarker, "private@example.test", "machine-private", "device-key-private",
		projectPath, dataDir, browserPath, "settings_config", `"meta"`, `"endpoint"`, "raw upstream",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("diagnostics allowlist leaked %q: %s", forbidden, body)
		}
	}

	var view diagnosticsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Node.Version != "ui-test" || !view.Node.Registered || view.Node.RuntimeStatus != "error" {
		t.Fatalf("unexpected node diagnostics: %+v", view.Node)
	}
	if !view.Hub.Configured || view.Hub.Host != "hub.example.test" {
		t.Fatalf("unexpected Hub diagnostics: %+v", view.Hub)
	}
	if !view.Agent.Codex.Available || view.Agent.Codex.Version != "codex-cli 1.2.3" || view.Agent.Codex.Route != "cc_switch" || view.Agent.Codex.ReadyForCreate == nil || !*view.Agent.Codex.ReadyForCreate || view.Agent.Codex.ReadinessCode != "READY" || view.Agent.Codex.ReadinessMs != 12 {
		t.Fatalf("unexpected Codex diagnostics: %+v", view.Agent.Codex)
	}
	if view.Agent.ClaudeCode.AuthConfigured == nil || !*view.Agent.ClaudeCode.AuthConfigured {
		t.Fatalf("Claude auth status missing: %+v", view.Agent.ClaudeCode)
	}
	if !view.Agent.CCSwitch.DBDetected || view.Agent.CCSwitch.SchemaSupported || view.Agent.CCSwitch.SchemaFingerprint == "" || view.Agent.CCSwitch.SelectionConsistent == nil || !*view.Agent.CCSwitch.SelectionConsistent {
		t.Fatalf("unexpected CC Switch diagnostics: %+v", view.Agent.CCSwitch)
	}
	if view.Agent.CCSwitch.CurrentRoute != "codex: cc_switch" {
		t.Fatalf("unexpected current route: %q", view.Agent.CCSwitch.CurrentRoute)
	}
	if !view.Workspace.Bound || !view.Workspace.Readable || !view.Workspace.Exists || view.Workspace.Revision == "" {
		t.Fatalf("unexpected workspace diagnostics: %+v", view.Workspace)
	}
	if !view.Local.LocalBridgeConfigured || !view.Local.BrowserConfigured || !view.Local.BrowserPresent || !view.Local.ComponentRootPresent {
		t.Fatalf("unexpected local diagnostics: %+v", view.Local)
	}
	if len(view.Errors) == 0 || view.Errors[len(view.Errors)-1].ErrorClass != "auth_failed" || strings.Contains(view.Errors[len(view.Errors)-1].PublicMessage, aiSensitiveMarker) {
		t.Fatalf("unexpected public diagnostics errors: %+v", view.Errors)
	}
	if view.Summary.Node == "" || view.Summary.Hub == "" || view.Summary.Agent == "" || view.Summary.Workspace == "" || view.Summary.Local == "" {
		t.Fatalf("diagnostics summary incomplete: %+v", view.Summary)
	}

	fake.mu.Lock()
	actions := append([]string(nil), fake.actions...)
	fake.mu.Unlock()
	if len(actions) != 3 || actions[0] != "providers.list" || actions[1] != "routing.status" || actions[2] != "provider.readiness" {
		t.Fatalf("diagnostics discovery actions=%v, want read-only providers/routing/readiness", actions)
	}
	for _, forbiddenAction := range []string{"session.create", "session.send"} {
		if fake.called(forbiddenAction) {
			t.Fatalf("diagnostics triggered %s", forbiddenAction)
		}
	}
}

func TestDiagnosticsNavigationAndNoAutomaticModelExecution(t *testing.T) {
	for _, required := range []string{
		`data-tab="diagnostics"`, `id="tab-diagnostics"`, `id="diagnostics-refresh"`,
		`id="diagnostics-node"`, `id="diagnostics-hub"`, `id="diagnostics-codex"`,
		`id="diagnostics-claude"`, `id="diagnostics-ccswitch"`,
		`id="diagnostics-workspace"`, `id="diagnostics-local"`, `id="diagnostics-errors"`,
		`id="diagnostics-summary"`, "/api/diagnostics",
	} {
		if !strings.Contains(localUIHTML, required) {
			t.Fatalf("diagnostics UI missing %q", required)
		}
	}
	for _, forbidden := range []string{"session.create", "session.send", "/api/ai-health", "setInterval(refreshDiagnostics"} {
		if strings.Contains(localUIHTML, forbidden) {
			t.Fatalf("diagnostics UI contains automatic model execution marker %q", forbidden)
		}
	}
}

func TestDiagnosticsLoopbackGuardsTokenOriginAndMethod(t *testing.T) {
	app, err := New(Options{DataDir: t.TempDir(), Version: "ui-test", MachineName: "Test Node", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	app.agentController = &fakeAIController{}
	handler := app.handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/diagnostics", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}

	badOriginReq := httptest.NewRequest(http.MethodGet, "/api/diagnostics", nil)
	badOriginReq.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
	badOriginReq.Header.Set("Origin", "https://attacker.example")
	badOrigin := httptest.NewRecorder()
	handler.ServeHTTP(badOrigin, badOriginReq)
	if badOrigin.Code != http.StatusForbidden {
		t.Fatalf("bad origin status=%d", badOrigin.Code)
	}

	wrongMethodReq := httptest.NewRequest(http.MethodPost, "/api/diagnostics", nil)
	wrongMethodReq.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
	wrongMethod := httptest.NewRecorder()
	handler.ServeHTTP(wrongMethod, wrongMethodReq)
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status=%d", wrongMethod.Code)
	}
}

func TestDiagnosticsWorkspaceUsesOnlyReadOnlyLocalWorkingActions(t *testing.T) {
	raw, err := os.ReadFile("diagnostics.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{`Action: "get"`} {
		if !strings.Contains(source, required) {
			t.Fatalf("diagnostics workspace missing %s", required)
		}
	}
	for _, forbidden := range []string{`Action: "plan.init"`, `Action: "plan.get"`, `Action: "plan.sync"`, `Action: "task.update"`, `Action: "markdown.list"`, `Action: "markdown.append"`, `Action: "progress.watch"`} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("diagnostics workspace contains mutating action %s", forbidden)
		}
	}
}
