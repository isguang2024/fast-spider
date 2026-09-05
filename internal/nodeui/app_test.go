package nodeui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/componentmgr"
	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/server"
	"github.com/isguang2024/fast-spider/internal/hub/store"
	"github.com/isguang2024/fast-spider/internal/node"
)

type appLifecycleTestAgent struct {
	closeCalls atomic.Int32
}

func (a *appLifecycleTestAgent) Control(context.Context, string, map[string]any) (map[string]any, error) {
	return map[string]any{}, nil
}

func (a *appLifecycleTestAgent) Close(context.Context) error {
	a.closeCalls.Add(1)
	return nil
}

func TestLocalConfigIsPrivateAndRoundTrips(t *testing.T) {
	dataDir := t.TempDir()
	cfg := LocalConfig{
		Version:                         localConfigVersion,
		HubURL:                          "https://hub.example/fast-spider",
		MachineName:                     "Office Windows",
		BrowserSidecarDir:               `C:\FastSpider\browser`,
		LocalBridgeEnabled:              true,
		AllowInsecureLocalHub:           false,
		ChatGPTDefaultConfigurationMode: "advanced",
		ChatGPTDefaultCreateMode:        "quick_chat",
		ChatGPTDefaultModel:             "gpt-5-6-thinking",
		ChatGPTDefaultThinking:          "max",
	}
	if err := saveLocalConfig(dataDir, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadLocalConfig(dataDir, "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if loaded != cfg {
		t.Fatalf("local config round trip mismatch: got=%+v want=%+v", loaded, cfg)
	}
	info, err := os.Stat(filepath.Join(dataDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("local config permissions=%o, want private", info.Mode().Perm())
	}
}

func TestLocalConfigV1LoadsIntoCurrentVersionWithoutLosingExistingSettings(t *testing.T) {
	dataDir := t.TempDir()
	legacy := `{"version":1,"hubUrl":"https://hub.example","machineName":"Legacy Node","browserSidecarDir":"C:/browser","localBridgeEnabled":true,"allowInsecureLocalHub":false}`
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadLocalConfig(dataDir, "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != localConfigVersion || cfg.MachineName != "Legacy Node" || cfg.HubURL != "https://hub.example" || !cfg.LocalBridgeEnabled || cfg.AutoStartEnabled || cfg.AutoUpdateEnabled || cfg.ChatGPTDefaultConfigurationMode != "auto" || cfg.ChatGPTDefaultCreateMode != "complete" || cfg.ChatGPTDefaultModel != "" || cfg.ChatGPTDefaultThinking != "" {
		t.Fatalf("legacy config migration mismatch: %+v", cfg)
	}
}

func TestDefaultLocalConfigPreservesChatGPTCreateDefaults(t *testing.T) {
	cfg := defaultLocalConfig("Test Node")
	if cfg.ChatGPTDefaultConfigurationMode != "auto" || cfg.ChatGPTDefaultCreateMode != "complete" || cfg.ChatGPTDefaultModel != "" || cfg.ChatGPTDefaultThinking != "" {
		t.Fatalf("new local config must preserve existing ChatGPT create defaults: %+v", cfg)
	}
}

type chatGPTDefaultsTestAgent struct {
	configurationMode, mode, model, thinking string
}

func (a *chatGPTDefaultsTestAgent) Control(context.Context, string, map[string]any) (map[string]any, error) {
	return map[string]any{}, nil
}
func (a *chatGPTDefaultsTestAgent) Close(context.Context) error { return nil }
func (a *chatGPTDefaultsTestAgent) SetChatGPTCloudCreateDefaults(configurationMode, mode, model, thinking string) {
	a.configurationMode, a.mode, a.model, a.thinking = configurationMode, mode, model, thinking
}

func TestLocalUIConfigUpdatesChatGPTCreateDefaultsImmediately(t *testing.T) {
	app, err := New(Options{DataDir: t.TempDir(), Version: "ui-test", MachineName: "Test Node", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	fake := &chatGPTDefaultsTestAgent{}
	app.agentController = fake
	configurationMode, mode, model, thinking := "advanced", "quick_chat", "gpt-5.6-terra-wm", "max"
	body, err := json.Marshal(configRequest{MachineName: "Test Node", LocalBridgeEnabled: true, ChatGPTDefaultConfigurationMode: &configurationMode, ChatGPTDefaultCreateMode: &mode, ChatGPTDefaultModel: &model, ChatGPTDefaultThinking: &thinking})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
	response := httptest.NewRecorder()
	app.handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("config status=%d body=%s", response.Code, response.Body.String())
	}
	if app.config.ChatGPTDefaultConfigurationMode != configurationMode || app.config.ChatGPTDefaultCreateMode != mode || app.config.ChatGPTDefaultModel != model || app.config.ChatGPTDefaultThinking != thinking {
		t.Fatalf("saved defaults=%+v", app.config)
	}
	if fake.configurationMode != configurationMode || fake.mode != mode || fake.model != model || fake.thinking != thinking {
		t.Fatalf("runtime defaults=%q %q %q %q", fake.configurationMode, fake.mode, fake.model, fake.thinking)
	}
}

func TestBackgroundDuplicateInstanceExitsWithoutOpeningUI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := handleExistingInstance(ctx, "http://127.0.0.1:1/", true); err != nil {
		t.Fatalf("background duplicate should exit quietly: %v", err)
	}
}

func TestLocalUIAPIRequiresUISecretAndReportsConnectionModel(t *testing.T) {
	app, err := New(Options{DataDir: t.TempDir(), Version: "ui-test", MachineName: "Test Node", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	handler := app.handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var status statusResponse
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.RegistrationMode != "connection_token" || status.ConfigurationScope != "local_node" || status.RuntimeCredential != "device_key" || status.ConnectionTokenSaved {
		t.Fatalf("unexpected connection model: %+v", status)
	}
	if status.TraySupported != (runtime.GOOS == "windows") || status.TrayActive {
		t.Fatalf("unexpected tray status before App.Run: %+v", status)
	}
}

func TestLocalUIRegisteredViewDoesNotPromptForConnectionTokenAgain(t *testing.T) {
	if !strings.Contains(localUIHTML, `id="registration-panel"`) || !strings.Contains(localUIHTML, `id="registered-panel"`) {
		t.Fatal("local UI is missing separate registration/registered states")
	}
	if strings.Contains(localUIHTML, `id="config-hub"`) {
		t.Fatal("Hub URL is duplicated in local configuration")
	}
	if strings.Contains(localUIHTML, "Token 是否保存") {
		t.Fatal("local UI still exposes the confusing connection-token saved card")
	}
	if !strings.Contains(localUIHTML, "以后打开 Fast Spider 会自动连接 Hub，不需要再次输入连接密钥") {
		t.Fatal("registered state does not explain automatic device-key reconnect")
	}
	if !strings.Contains(localUIHTML, `id="browser-install"`) || !strings.Contains(localUIHTML, "/api/components/ensure") {
		t.Fatal("local UI is missing the managed Browser component install flow")
	}
}

func TestLocalUIConfigPreservesRegistrationHubWhenHubFieldIsOmitted(t *testing.T) {
	dataDir := t.TempDir()
	app, err := New(Options{DataDir: dataDir, Version: "ui-test", MachineName: "Test Node", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	app.config.HubURL = "https://custom.example/fast-spider"
	app.config.ChatGPTDefaultConfigurationMode = "preset"
	app.config.ChatGPTDefaultCreateMode = "quick_chat"
	app.config.ChatGPTDefaultModel = "gpt-existing"
	app.config.ChatGPTDefaultThinking = "max"
	body, err := json.Marshal(configRequest{MachineName: "Renamed Node", LocalBridgeEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
	response := httptest.NewRecorder()
	app.handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("config status=%d body=%s", response.Code, response.Body.String())
	}
	if app.config.HubURL != "https://custom.example/fast-spider" || app.config.MachineName != "Renamed Node" {
		t.Fatalf("config lost registration state: %+v", app.config)
	}
	if app.config.ChatGPTDefaultConfigurationMode != "preset" || app.config.ChatGPTDefaultCreateMode != "quick_chat" || app.config.ChatGPTDefaultModel != "gpt-existing" || app.config.ChatGPTDefaultThinking != "max" {
		t.Fatalf("older config request reset ChatGPT defaults: %+v", app.config)
	}
}

func TestLegacyCodexDesktopBridgeFieldsAreIgnoredAndRemovedOnSave(t *testing.T) {
	dataDir := t.TempDir()
	legacy := `{"version":6,"machineName":"Test Node","localBridgeEnabled":true,"codexDesktopBridgeEnabled":true,"codexDesktopBridgeConfigured":true,"chatgptDefaultConfigurationMode":"auto","chatgptDefaultCreateMode":"complete"}`
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadLocalConfig(dataDir, "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if err := saveLocalConfig(dataDir, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "codexDesktopBridge") {
		t.Fatalf("legacy Codex Desktop bridge fields survived save: %s", raw)
	}
}

func TestConfigureInstalledBrowserComponentUpdatesSidecarPath(t *testing.T) {
	cfg := defaultLocalConfig("Test Node")
	installed := componentmgr.Installed{ID: "browser", Platform: "windows-amd64", Version: "1.62.0", Path: `C:\\FastSpider\\browser-component`}
	next, changed := configureInstalledComponent(cfg, installed)
	if !changed {
		t.Fatal("browser component did not update local config")
	}
	if next.BrowserSidecarDir != installed.Path {
		t.Fatalf("browser sidecar dir=%q want=%q", next.BrowserSidecarDir, installed.Path)
	}
	again, changed := configureInstalledComponent(next, installed)
	if changed || again.BrowserSidecarDir != installed.Path {
		t.Fatalf("same browser component should be idempotent: changed=%v cfg=%+v", changed, again)
	}
}

func TestLocalUIConnectRejectsWhenCLIInstanceOwnsRuntime(t *testing.T) {
	app, err := New(Options{DataDir: t.TempDir(), Version: "ui-test", MachineName: "Test Node", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(connectRequest{HubURL: "https://hub.example", Token: "ctk_example", MachineName: "Test Node"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/connect", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
	response := httptest.NewRecorder()
	app.handler().ServeHTTP(response, req)
	if response.Code != http.StatusConflict {
		t.Fatalf("connect while runtime not owned status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestLocalUIConnectAndSwitchAccountPreserveRegistrationOnFailure(t *testing.T) {
	ctx := context.Background()
	hubData := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(hubData, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := core.New(st, registry.New(), core.Config{DataDir: hubData, Version: "ui-hub-test"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	account, err := service.BootstrapAccount(ctx, bootstrap, "ui-owner", "UI Owner", "correct horse battery staple", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	connectionToken, err := service.CreateConnectionToken(ctx, account.OwnerID, "Reusable", time.Hour, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	hub := httptest.NewServer(server.New(service, server.Config{}).Handler())
	defer hub.Close()

	nodeData := t.TempDir()
	app, err := New(Options{DataDir: nodeData, Version: "ui-node-test", MachineName: "UI Node", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	app.config.AllowInsecureLocalHub = true
	app.runtimeOwned = true
	body, err := json.Marshal(connectRequest{HubURL: hub.URL, Token: connectionToken.Token, MachineName: "UI Node"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/connect", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
	response := httptest.NewRecorder()
	app.handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("connect status=%d body=%s", response.Code, response.Body.String())
	}
	if !app.snapshot().Registered {
		t.Fatal("UI connect did not register the Node")
	}
	configRaw, err := os.ReadFile(filepath.Join(nodeData, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	stateRaw, err := os.ReadFile(filepath.Join(nodeData, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configRaw), connectionToken.Token) || strings.Contains(string(stateRaw), connectionToken.Token) || strings.Contains(string(configRaw), "ctk_") || strings.Contains(string(stateRaw), "ctk_") {
		t.Fatal("connection token was persisted in Node config/state")
	}

	connectAgain := func(token string, switching bool) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(connectRequest{HubURL: hub.URL, Token: token, MachineName: "Switched Node", SwitchAccount: switching})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/connect", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Fast-Spider-UI-Token", app.uiToken)
		response := httptest.NewRecorder()
		app.handler().ServeHTTP(response, req)
		return response
	}
	if response := connectAgain(connectionToken.Token, false); response.Code != http.StatusBadRequest {
		t.Fatalf("ordinary connect replaced an active registration: %d", response.Code)
	}
	if response := connectAgain("ctk_invalid", true); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid switch token accepted: %d", response.Code)
	}
	stateAfterFailure, err := os.ReadFile(filepath.Join(nodeData, "state.json"))
	if err != nil || !bytes.Equal(stateAfterFailure, stateRaw) {
		t.Fatalf("failed switch changed registration: %v", err)
	}
	configAfterFailure, err := os.ReadFile(filepath.Join(nodeData, "config.json"))
	if err != nil || !bytes.Equal(configAfterFailure, configRaw) || app.config.MachineName != "UI Node" {
		t.Fatalf("failed switch changed configuration: %v", err)
	}
	if response := connectAgain(connectionToken.Token, true); response.Code != http.StatusOK {
		t.Fatalf("same-account re-registration failed: %d %s", response.Code, response.Body.String())
	}
	otherAccount, err := service.CreateUser(ctx, account.OwnerID, "other-owner", "Other Owner", "correct horse battery staple", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	otherToken, err := service.CreateConnectionToken(ctx, otherAccount.OwnerID, "Other account", time.Hour, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if response := connectAgain(otherToken.Token, true); response.Code != http.StatusOK {
		t.Fatalf("switch to another account failed: %d %s", response.Code, response.Body.String())
	}
	switched, err := node.LoadState(filepath.Join(nodeData, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	machines, err := service.ListMachines(ctx, otherAccount.OwnerID)
	if err != nil || len(machines) != 1 || machines[0].MachineID != switched.MachineID {
		t.Fatalf("switch did not select the target owner's device: %+v %v", machines, err)
	}
	if err := service.RevokeMachine(ctx, otherAccount.OwnerID, switched.MachineID, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if response := connectAgain(otherToken.Token, true); response.Code != http.StatusOK {
		t.Fatalf("re-register after revoked credentials failed: %d %s", response.Code, response.Body.String())
	}
	repaired, err := node.LoadState(filepath.Join(nodeData, "state.json"))
	if err != nil || repaired.MachineID == switched.MachineID {
		t.Fatalf("revoked identity was reused: %+v %v", repaired, err)
	}
	for _, name := range []string{"config.json", "state.json"} {
		raw, err := os.ReadFile(filepath.Join(nodeData, name))
		if err != nil || bytes.Contains(raw, []byte("ctk_")) {
			t.Fatalf("switch persisted a connection token in %s: %v", name, err)
		}
	}
}

func TestRuntimeRestartKeepsAppOwnedAgentOpen(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := New(Options{DataDir: dataDir, Version: "runtime-lifecycle-test", MachineName: "Test Node", Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.agentController.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	agent := &appLifecycleTestAgent{}
	app.agentController = agent
	app.ctx = context.Background()
	app.runtimeOwned = true
	if err := node.SaveState(filepath.Join(dataDir, "state.json"), node.State{
		HubURL:         "http://127.0.0.1:8787",
		MachineID:      "mach_runtime_restart",
		CredentialID:   "cred_runtime_restart",
		HubPublicKey:   "unused",
		HubFingerprint: "unused",
	}); err != nil {
		t.Fatal(err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		app.startRuntime()
		waitForRuntimeClear(t, app)
		if got := agent.closeCalls.Load(); got != 0 {
			t.Fatalf("restart attempt %d closed App-owned agent %d times", attempt, got)
		}
	}
	if err := app.agentController.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := agent.closeCalls.Load(); got != 1 {
		t.Fatalf("App-owned agent Close() calls=%d, want 1 at App shutdown", got)
	}
}

func TestStopRuntimeTimeoutKeepsHandleAndBlocksOverlappingStart(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	app := &App{
		opts:          Options{DataDir: t.TempDir(), Logger: logger},
		ctx:           context.Background(),
		runtimeOwned:  true,
		runtimeCancel: cancel,
		runtimeDone:   done,
	}

	if app.stopRuntimeWithin(10 * time.Millisecond) {
		t.Fatal("stopRuntimeWithin() reported a hung runtime as stopped")
	}
	select {
	case <-runCtx.Done():
	default:
		t.Fatal("stopRuntimeWithin() did not cancel the runtime context")
	}
	app.mu.Lock()
	keptCancel := app.runtimeCancel != nil
	keptDone := app.runtimeDone == done
	app.mu.Unlock()
	if !keptCancel || !keptDone {
		t.Fatalf("timed-out runtime handle was cleared: cancel=%v done=%v", keptCancel, keptDone)
	}

	app.startRuntime()
	app.mu.Lock()
	stillSameRuntime := app.runtimeDone == done
	app.mu.Unlock()
	if !stillSameRuntime {
		t.Fatal("startRuntime() replaced a runtime that had not stopped")
	}
}

func TestRestartRuntimeStartsOnceAfterTimedOutRuntimeEventuallyStops(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	requests := make(chan struct{}, 2)
	releaseRequest := make(chan struct{})
	var requestCount atomic.Int32
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		requests <- struct{}{}
		<-releaseRequest
		http.Error(w, "stop test request", http.StatusServiceUnavailable)
	}))
	defer hub.Close()

	app, err := New(Options{DataDir: dataDir, Version: "delayed-restart-test", MachineName: "Test Node", Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.agentController.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	agent := &appLifecycleTestAgent{}
	app.agentController = agent
	appCtx, stopApp := context.WithCancel(context.Background())
	defer stopApp()
	app.ctx = appCtx
	app.runtimeOwned = true
	app.config.AllowInsecureLocalHub = true
	if err := node.SaveState(filepath.Join(dataDir, "state.json"), node.State{
		HubURL:         hub.URL,
		MachineID:      "mach_delayed_restart",
		CredentialID:   "cred_delayed_restart",
		HubPublicKey:   "unused",
		HubFingerprint: "unused",
	}); err != nil {
		t.Fatal(err)
	}

	oldCtx, cancelOld := context.WithCancel(appCtx)
	oldDone := make(chan struct{})
	app.runtimeCancel = cancelOld
	app.runtimeDone = oldDone
	app.restartRuntimeWithin(10 * time.Millisecond)
	app.restartRuntimeWithin(10 * time.Millisecond)
	select {
	case <-oldCtx.Done():
	default:
		t.Fatal("restart did not cancel the old runtime")
	}
	select {
	case <-requests:
		t.Fatal("new runtime started before the old runtime completed")
	default:
	}

	app.clearRuntime(oldDone)
	close(oldDone)
	select {
	case <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("new runtime did not start after the old runtime completed")
	}
	time.Sleep(50 * time.Millisecond)
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("new runtime starts=%d, want exactly 1", got)
	}
	app.mu.Lock()
	newDone := app.runtimeDone
	restartAfter := app.runtimeRestartAfter
	app.mu.Unlock()
	if newDone == nil || newDone == oldDone {
		t.Fatal("delayed restart did not install a new runtime handle")
	}
	if restartAfter != nil {
		t.Fatal("delayed restart marker was not cleared")
	}

	close(releaseRequest)
	if !app.stopRuntimeWithin(2 * time.Second) {
		t.Fatal("new runtime did not stop during test cleanup")
	}
	if err := agent.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUnexpectedRuntimeReturnCancelsSharedBridgeContext(t *testing.T) {
	runCtx, cancel := context.WithCancel(context.Background())
	unexpected := errors.New("runtime returned unexpectedly")
	err := runRuntimeClient(runCtx, cancel, nil, func(got context.Context) error {
		if got != runCtx {
			t.Fatal("runRuntimeClient() passed a different context")
		}
		return unexpected
	})
	if !errors.Is(err, unexpected) {
		t.Fatalf("runRuntimeClient() error=%v, want %v", err, unexpected)
	}
	select {
	case <-runCtx.Done():
	default:
		t.Fatal("runtime return did not cancel the context shared with local bridge")
	}
}

func TestRuntimeReturnWaitsForLocalBridgeCleanup(t *testing.T) {
	runCtx, cancel := context.WithCancel(context.Background())
	bridgeDone := make(chan struct{})
	bridgeCanceled := make(chan struct{})
	releaseBridge := make(chan struct{})
	go func() {
		<-runCtx.Done()
		close(bridgeCanceled)
		<-releaseBridge
		close(bridgeDone)
	}()
	runtimeDone := make(chan error, 1)
	go func() {
		runtimeDone <- runRuntimeClient(runCtx, cancel, bridgeDone, func(context.Context) error { return nil })
	}()
	select {
	case <-bridgeCanceled:
	case <-time.After(time.Second):
		t.Fatal("runtime return did not cancel the local bridge")
	}
	select {
	case <-runtimeDone:
		t.Fatal("runtime completed before local bridge cleanup")
	default:
	}
	close(releaseBridge)
	select {
	case err := <-runtimeDone:
		if err != nil {
			t.Fatalf("runtime error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not finish after local bridge cleanup")
	}
}

func waitForRuntimeClear(t *testing.T, app *App) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		app.mu.Lock()
		cleared := app.runtimeCancel == nil && app.runtimeDone == nil
		app.mu.Unlock()
		if cleared {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("runtime did not clear after Client.Run returned")
}
