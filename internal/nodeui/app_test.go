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
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/server"
	"github.com/isguang2024/fast-spider/internal/hub/store"
)

func TestLocalConfigIsPrivateAndRoundTrips(t *testing.T) {
	dataDir := t.TempDir()
	cfg := LocalConfig{
		Version:               localConfigVersion,
		HubURL:                "https://hub.example/fast-spider",
		MachineName:           "Office Windows",
		BrowserSidecarDir:     `C:\FastSpider\browser`,
		LocalBridgeEnabled:    true,
		AllowInsecureLocalHub: false,
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

func TestLocalConfigV1LoadsIntoV2WithoutLosingExistingSettings(t *testing.T) {
	dataDir := t.TempDir()
	legacy := `{"version":1,"hubUrl":"https://hub.example","machineName":"Legacy Node","browserSidecarDir":"C:/browser","localBridgeEnabled":true,"allowInsecureLocalHub":false}`
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadLocalConfig(dataDir, "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != localConfigVersion || cfg.MachineName != "Legacy Node" || cfg.HubURL != "https://hub.example" || !cfg.LocalBridgeEnabled || cfg.AutoStartEnabled || cfg.AutoUpdateEnabled {
		t.Fatalf("legacy config migration mismatch: %+v", cfg)
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

func TestLocalUIConnectDoesNotPersistReusableConnectionToken(t *testing.T) {
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
}
