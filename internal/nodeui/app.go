package nodeui

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/isguang2024/fast-spider/internal/agent"
	"github.com/isguang2024/fast-spider/internal/componentmgr"
	"github.com/isguang2024/fast-spider/internal/localbridge"
	"github.com/isguang2024/fast-spider/internal/node"
	"github.com/isguang2024/fast-spider/internal/nodeinstance"
	"github.com/isguang2024/fast-spider/internal/nodeupdate"
	"github.com/isguang2024/fast-spider/internal/operationlog"
	"github.com/isguang2024/fast-spider/internal/security"
)

const (
	defaultListenAddress = "127.0.0.1:17891"
	maxUIRequestBytes    = 32 << 10
)

type Options struct {
	DataDir      string
	Version      string
	MachineName  string
	NoOpenWindow bool
	Logger       *slog.Logger
}

type App struct {
	opts Options

	mu                  sync.Mutex
	ctx                 context.Context
	cancel              context.CancelFunc
	config              LocalConfig
	uiToken             string
	runtimeCancel       context.CancelFunc
	runtimeDone         chan struct{}
	runtimeRestartAfter chan struct{}
	runtimeOwned        bool
	runtimeStatus       string
	runtimeError        string
	updateStatus        updateStatusResponse
	updateArtifact      string
	updateRunning       bool
	releasePushID       string
	releasePushRunning  bool
	trayActive          bool
	openFolder          func(string) error
	agentController     node.AgentController
	componentEnsure     componentEnsureFunc
	operationLog        *operationlog.Store
}

type statusResponse struct {
	Version              string               `json:"version"`
	DataDir              string               `json:"dataDir"`
	Registered           bool                 `json:"registered"`
	HubURL               string               `json:"hubUrl,omitempty"`
	HubFingerprint       string               `json:"hubFingerprint,omitempty"`
	RegistrationMode     string               `json:"registrationMode"`
	ConfigurationScope   string               `json:"configurationScope"`
	RuntimeCredential    string               `json:"runtimeCredential"`
	ConnectionTokenSaved bool                 `json:"connectionTokenSaved"`
	RuntimeOwned         bool                 `json:"runtimeOwned"`
	RuntimeStatus        string               `json:"runtimeStatus"`
	RuntimeError         string               `json:"runtimeError,omitempty"`
	AutoStartSupported   bool                 `json:"autoStartSupported"`
	AutoStartEnabled     bool                 `json:"autoStartEnabled"`
	TraySupported        bool                 `json:"traySupported"`
	TrayActive           bool                 `json:"trayActive"`
	ComponentRoot        string               `json:"componentRoot"`
	Update               updateStatusResponse `json:"update"`
	Config               LocalConfig          `json:"config"`
}

type connectRequest struct {
	HubURL      string `json:"hubUrl"`
	Token       string `json:"token"`
	MachineName string `json:"machineName"`
}

type configRequest struct {
	HubURL                          string  `json:"hubUrl"`
	MachineName                     string  `json:"machineName"`
	BrowserSidecarDir               string  `json:"browserSidecarDir"`
	LocalBridgeEnabled              bool    `json:"localBridgeEnabled"`
	AutoStartEnabled                bool    `json:"autoStartEnabled"`
	AutoUpdateEnabled               bool    `json:"autoUpdateEnabled"`
	AllowInsecureLocalHub           bool    `json:"allowInsecureLocalHub"`
	CodexDesktopBridgeEnabled       bool    `json:"codexDesktopBridgeEnabled"`
	CodexDesktopBridgeConfigured    bool    `json:"codexDesktopBridgeConfigured"`
	ChatGPTDefaultConfigurationMode *string `json:"chatgptDefaultConfigurationMode"`
	ChatGPTDefaultCreateMode        *string `json:"chatgptDefaultCreateMode"`
	ChatGPTDefaultModel             *string `json:"chatgptDefaultModel"`
	ChatGPTDefaultThinking          *string `json:"chatgptDefaultThinking"`
}

type componentEnsureRequest struct {
	ComponentID string `json:"componentId"`
}

func configureInstalledComponent(cfg LocalConfig, installed componentmgr.Installed) (LocalConfig, bool) {
	switch installed.ID {
	case "browser":
		path := strings.TrimSpace(installed.Path)
		if path != "" && filepath.Clean(cfg.BrowserSidecarDir) != filepath.Clean(path) {
			cfg.BrowserSidecarDir = path
			return cfg, true
		}
	}
	return cfg, false
}

func New(opts Options) (*App, error) {
	if strings.TrimSpace(opts.DataDir) == "" {
		return nil, errors.New("node UI data directory is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	cfg, err := loadLocalConfig(opts.DataDir, opts.MachineName)
	if err != nil {
		return nil, err
	}
	uiToken, err := security.RandomOpaque("ui_")
	if err != nil {
		return nil, err
	}
	opLog, err := operationlog.NewStore(opts.DataDir, opts.Logger)
	if err != nil {
		opts.Logger.Warn("operation log store unavailable", "error", err)
	}
	agentController := agent.New(opts.DataDir, opts.Logger)
	agentController.SetCodexDesktopBridgeEnabled(cfg.CodexDesktopBridgeEnabled)
	agentController.SetChatGPTCloudCreateDefaults(cfg.ChatGPTDefaultConfigurationMode, cfg.ChatGPTDefaultCreateMode, cfg.ChatGPTDefaultModel, cfg.ChatGPTDefaultThinking)
	return &App{
		opts:            opts,
		config:          cfg,
		uiToken:         uiToken,
		runtimeStatus:   "stopped",
		openFolder:      openLocalFolder,
		agentController: agentController,
		componentEnsure: componentmgr.Ensure,
		operationLog:    opLog,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if a.agentController != nil {
			_ = a.agentController.Close(closeCtx)
		}
	}()
	a.mu.Lock()
	autoStart := a.config.AutoStartEnabled
	a.mu.Unlock()
	if autostartSupported() {
		if err := setAutostart(autoStart, a.opts.DataDir); err != nil {
			a.opts.Logger.Warn("synchronize Windows autostart failed", "error", err)
		}
	}
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	uiURL := "http://" + defaultListenAddress + "/"
	lease, leaseErr := nodeinstance.Acquire()
	if leaseErr != nil {
		if errors.Is(leaseErr, nodeinstance.ErrAlreadyRunning) {
			return handleExistingInstance(ctx, uiURL, a.opts.NoOpenWindow)
		}
		return leaseErr
	}
	defer lease.Close()
	if applied, err := runStartupUpdateMaintenance(a.applyReadyUpdateOnStartup, func() error {
		return nodeupdate.CleanupConsumedCurrent(a.opts.DataDir, a.opts.Version)
	}); err != nil {
		a.opts.Logger.Warn("startup Node update maintenance failed", "error", err)
	} else if applied {
		return nil
	}
	if err := nodeupdate.CleanupStale(a.opts.DataDir, a.opts.Version); err != nil {
		a.opts.Logger.Warn("cleanup stale Node updates failed", "error", err)
	}
	if err := cleanupLegacyInstallArtifactsOnStartup(os.Executable, nodeupdate.CleanupLegacyInstallArtifacts); err != nil {
		a.opts.Logger.Warn("cleanup legacy Node install artifacts failed", "error", err)
	}
	a.mu.Lock()
	browserSidecarDir := a.config.BrowserSidecarDir
	a.mu.Unlock()
	if err := componentmgr.CleanupConfigured(a.opts.DataDir, "browser", browserSidecarDir); err != nil {
		a.opts.Logger.Warn("cleanup configured Browser component failed", "error", err)
	}

	listener, err := net.Listen("tcp", defaultListenAddress)
	if err != nil {
		return fmt.Errorf("listen on local UI %s: %w", defaultListenAddress, err)
	}
	defer listener.Close()

	a.mu.Lock()
	a.ctx = runCtx
	a.cancel = runCancel
	a.runtimeOwned = true
	a.mu.Unlock()

	trayStop, trayErr := startTray(runCtx, func() {
		if err := openLocalUI(uiURL); err != nil {
			a.opts.Logger.Warn("open local Node UI from tray failed", "url", uiURL, "error", err)
		}
	}, runCancel, a.opts.Logger)
	if trayErr != nil {
		a.opts.Logger.Warn("start Windows tray failed", "error", trayErr)
	} else {
		a.mu.Lock()
		a.trayActive = traySupported()
		a.mu.Unlock()
		defer func() {
			trayStop()
			a.mu.Lock()
			a.trayActive = false
			a.mu.Unlock()
		}()
	}

	if lease != nil {
		a.startRuntime()
	}

	server := &http.Server{
		Handler:           a.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()
	go func() {
		if err := ensureDesktopShortcut(runCtx); err != nil && runCtx.Err() == nil {
			a.opts.Logger.Warn("ensure Fast Spider Desktop shortcut failed", "error", err)
		}
	}()

	if !a.opts.NoOpenWindow {
		if err := openLocalUI(uiURL); err != nil {
			a.opts.Logger.Warn("open local Node UI failed", "url", uiURL, "error", err)
		}
	}
	go a.autoUpdateLoop(runCtx)
	go a.operationLogCleanupLoop(runCtx)

	select {
	case <-runCtx.Done():
	case err := <-serveErr:
		if err != nil {
			a.stopRuntime()
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	a.stopRuntime()
	return nil
}

func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Fast-Spider-UI", "1")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "fast-spider-node-ui\n")
	})
	mux.HandleFunc("GET /", a.handleIndex)
	mux.HandleFunc("GET /api/status", a.apiOnly(a.handleStatus))
	mux.HandleFunc("POST /api/connect", a.apiOnly(a.handleConnect))
	mux.HandleFunc("POST /api/config", a.apiOnly(a.handleConfig))
	mux.HandleFunc("GET /api/chatgpt-advanced-models", a.apiOnly(a.handleChatGPTAdvancedModels))
	mux.HandleFunc("POST /api/chatgpt-advanced-models", a.apiOnly(a.handleChatGPTAdvancedModels))
	mux.HandleFunc("POST /api/update/check", a.apiOnly(a.handleUpdateCheck))
	mux.HandleFunc("POST /api/update/install", a.apiOnly(a.handleUpdateInstall))
	mux.HandleFunc("GET /api/components", a.apiOnly(a.handleComponents))
	mux.HandleFunc("POST /api/components", a.apiOnly(methodNotAllowed))
	mux.HandleFunc("POST /api/components/ensure", a.apiOnly(a.handleComponentEnsure))
	mux.HandleFunc("GET /api/components/ensure", a.apiOnly(methodNotAllowed))
	mux.HandleFunc("GET /api/search-file/status", a.apiOnly(a.handleSearchFileStatus))
	mux.HandleFunc("POST /api/search-file/status", a.apiOnly(methodNotAllowed))
	mux.HandleFunc("POST /api/search-file/self-test", a.apiOnly(a.handleSearchFileSelfTest))
	mux.HandleFunc("GET /api/search-file/self-test", a.apiOnly(methodNotAllowed))
	mux.HandleFunc("POST /api/working", a.apiOnly(a.handleWorking))
	mux.HandleFunc("GET /api/ai-routing", a.apiOnly(a.handleAIRouting))
	mux.HandleFunc("GET /api/diagnostics", a.apiOnly(a.handleDiagnostics))
	mux.HandleFunc("POST /api/exit", a.apiOnly(a.handleExit))
	mux.HandleFunc("GET /api/operation-logs", a.apiOnly(a.handleOperationLogs))
	mux.HandleFunc("POST /api/operation-logs", a.apiOnly(methodNotAllowed))
	mux.HandleFunc("POST /api/operation-logs/cleanup", a.apiOnly(a.handleOperationLogsCleanup))
	mux.HandleFunc("GET /api/operation-logs/cleanup", a.apiOnly(methodNotAllowed))
	mux.HandleFunc("GET /api/operation-logs/stats", a.apiOnly(a.handleOperationLogsStats))
	mux.HandleFunc("POST /api/operation-logs/stats", a.apiOnly(methodNotAllowed))
	var handler http.Handler = mux
	handler = a.logOperation(handler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		handler.ServeHTTP(w, r)
	})
}

func methodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (a *App) apiOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Fast-Spider-UI-Token")), []byte(a.uiToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && origin != "http://"+defaultListenAddress {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		next(w, r)
	}
}

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	page := strings.ReplaceAll(localUIHTML, "{{UI_TOKEN}}", html.EscapeString(a.uiToken))
	page = strings.ReplaceAll(page, "{{VERSION}}", html.EscapeString(a.opts.Version))
	_, _ = io.WriteString(w, page)
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.snapshot())
}

func (a *App) handleConnect(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	runtimeOwned := a.runtimeOwned
	a.mu.Unlock()
	if !runtimeOwned {
		writeAPIError(w, http.StatusConflict, errors.New("已有无界面 Node 实例运行，请先停止旧 run/connect 进程再重新登记"))
		return
	}
	var req connectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	req.HubURL = strings.TrimSpace(req.HubURL)
	req.Token = strings.TrimSpace(req.Token)
	req.MachineName = strings.TrimSpace(req.MachineName)
	if req.HubURL == "" || req.Token == "" || req.MachineName == "" {
		writeAPIError(w, http.StatusBadRequest, errors.New("Hub 地址、连接密钥和设备名称不能为空"))
		return
	}
	if len(req.HubURL) > 2048 || len(req.Token) > 256 || len(req.MachineName) > 128 {
		writeAPIError(w, http.StatusBadRequest, errors.New("连接参数过长"))
		return
	}

	a.mu.Lock()
	cfg := a.config
	a.mu.Unlock()
	client, err := node.New(node.Config{
		DataDir:       a.opts.DataDir,
		Version:       a.opts.Version,
		AllowInsecure: cfg.AllowInsecureLocalHub,
		Logger:        a.opts.Logger,
	})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if _, err := client.Connect(ctx, req.HubURL, req.Token, req.MachineName); err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}

	a.mu.Lock()
	a.config.HubURL = req.HubURL
	a.config.MachineName = req.MachineName
	cfg = a.config
	a.mu.Unlock()
	if err := saveLocalConfig(a.opts.DataDir, cfg); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	a.restartRuntime()
	writeJSON(w, http.StatusOK, a.snapshot())
}

func (a *App) handleExit(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"stopping": true})
	if cancel != nil {
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()
	}
}

func (a *App) handleConfig(w http.ResponseWriter, r *http.Request) {
	var req configRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	a.mu.Lock()
	old := a.config
	a.mu.Unlock()
	hubURL := strings.TrimSpace(req.HubURL)
	if hubURL == "" {
		hubURL = old.HubURL
	}
	next := LocalConfig{
		Version:                         localConfigVersion,
		HubURL:                          hubURL,
		MachineName:                     strings.TrimSpace(req.MachineName),
		BrowserSidecarDir:               strings.TrimSpace(req.BrowserSidecarDir),
		LocalBridgeEnabled:              req.LocalBridgeEnabled,
		AutoStartEnabled:                req.AutoStartEnabled,
		AutoUpdateEnabled:               req.AutoUpdateEnabled,
		AllowInsecureLocalHub:           req.AllowInsecureLocalHub,
		CodexDesktopBridgeEnabled:       req.CodexDesktopBridgeEnabled,
		CodexDesktopBridgeConfigured:    req.CodexDesktopBridgeConfigured,
		ChatGPTDefaultConfigurationMode: old.ChatGPTDefaultConfigurationMode,
		ChatGPTDefaultCreateMode:        old.ChatGPTDefaultCreateMode,
		ChatGPTDefaultModel:             old.ChatGPTDefaultModel,
		ChatGPTDefaultThinking:          old.ChatGPTDefaultThinking,
		WorkingProjectPath:              old.WorkingProjectPath,
	}
	if req.ChatGPTDefaultConfigurationMode != nil {
		next.ChatGPTDefaultConfigurationMode = *req.ChatGPTDefaultConfigurationMode
	}
	if req.ChatGPTDefaultCreateMode != nil {
		next.ChatGPTDefaultCreateMode = *req.ChatGPTDefaultCreateMode
	}
	if req.ChatGPTDefaultModel != nil {
		next.ChatGPTDefaultModel = *req.ChatGPTDefaultModel
	}
	if req.ChatGPTDefaultThinking != nil {
		next.ChatGPTDefaultThinking = *req.ChatGPTDefaultThinking
	}
	if err := normalizeChatGPTDefaults(&next); err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	if next.MachineName == "" {
		writeAPIError(w, http.StatusBadRequest, errors.New("设备名称不能为空"))
		return
	}
	if !autostartSupported() && next.AutoStartEnabled {
		writeAPIError(w, http.StatusBadRequest, errors.New("当前系统暂不支持开机自动启动"))
		return
	}
	if next.CodexDesktopBridgeEnabled && runtime.GOOS != "windows" {
		writeAPIError(w, http.StatusBadRequest, errors.New("Codex Desktop 会话接管仅支持 Windows"))
		return
	}
	if err := setAutostart(next.AutoStartEnabled, a.opts.DataDir); err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	if err := saveLocalConfig(a.opts.DataDir, next); err != nil {
		_ = setAutostart(old.AutoStartEnabled, a.opts.DataDir)
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}

	a.mu.Lock()
	a.config = next
	a.mu.Unlock()
	if old.CodexDesktopBridgeEnabled != next.CodexDesktopBridgeEnabled {
		if controller, ok := a.agentController.(interface{ SetCodexDesktopBridgeEnabled(bool) }); ok {
			controller.SetCodexDesktopBridgeEnabled(next.CodexDesktopBridgeEnabled)
		}
	}
	if controller, ok := a.agentController.(interface {
		SetChatGPTCloudCreateDefaults(string, string, string, string)
	}); ok {
		controller.SetChatGPTCloudCreateDefaults(next.ChatGPTDefaultConfigurationMode, next.ChatGPTDefaultCreateMode, next.ChatGPTDefaultModel, next.ChatGPTDefaultThinking)
	}
	if old.BrowserSidecarDir != next.BrowserSidecarDir || old.LocalBridgeEnabled != next.LocalBridgeEnabled || old.AllowInsecureLocalHub != next.AllowInsecureLocalHub || old.MachineName != next.MachineName {
		a.restartRuntime()
	}
	if !old.AutoUpdateEnabled && next.AutoUpdateEnabled {
		if _, err := node.LoadState(filepath.Join(a.opts.DataDir, "state.json")); err == nil {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				_ = a.refreshUpdate(ctx, true)
			}()
		}
	}
	writeJSON(w, http.StatusOK, a.snapshot())
}

func (a *App) snapshot() statusResponse {
	a.mu.Lock()
	cfg := a.config
	runtimeOwned := a.runtimeOwned
	runtimeStatus := a.runtimeStatus
	runtimeError := a.runtimeError
	a.mu.Unlock()

	autoStartEnabled, _ := autostartEnabled(a.opts.DataDir)
	response := statusResponse{
		Version:              a.opts.Version,
		DataDir:              a.opts.DataDir,
		RegistrationMode:     "connection_token",
		ConfigurationScope:   "local_node",
		RuntimeCredential:    "device_key",
		ConnectionTokenSaved: false,
		RuntimeOwned:         runtimeOwned,
		RuntimeStatus:        runtimeStatus,
		RuntimeError:         runtimeError,
		AutoStartSupported:   autostartSupported(),
		AutoStartEnabled:     autoStartEnabled,
		TraySupported:        traySupported(),
		TrayActive:           a.trayActive,
		ComponentRoot:        filepath.Join(a.opts.DataDir, "components"),
		Update:               a.updateSnapshot(),
		Config:               cfg,
	}
	state, err := node.LoadState(filepath.Join(a.opts.DataDir, "state.json"))
	if err == nil {
		response.Registered = true
		response.HubURL = state.HubURL
		response.HubFingerprint = state.HubFingerprint
	}
	return response
}

func (a *App) startRuntime() {
	a.mu.Lock()
	if !a.runtimeOwned {
		a.runtimeStatus = "external_running"
		a.mu.Unlock()
		return
	}
	if a.runtimeCancel != nil || a.ctx == nil || a.ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	if _, err := node.LoadState(filepath.Join(a.opts.DataDir, "state.json")); err != nil {
		if errors.Is(err, node.ErrNotRegistered) {
			a.runtimeStatus = "not_registered"
			a.runtimeError = ""
		} else {
			a.runtimeStatus = "error"
			a.runtimeError = err.Error()
		}
		a.mu.Unlock()
		return
	}
	cfg := a.config
	runCtx, cancel := context.WithCancel(a.ctx)
	done := make(chan struct{})
	a.runtimeCancel = cancel
	a.runtimeDone = done
	a.runtimeStatus = "starting"
	a.runtimeError = ""
	a.mu.Unlock()

	go func() {
		defer close(done)
		client, err := node.New(node.Config{
			DataDir:           a.opts.DataDir,
			Version:           a.opts.Version,
			DisplayName:       cfg.MachineName,
			AllowInsecure:     cfg.AllowInsecureLocalHub,
			BrowserSidecarDir: cfg.BrowserSidecarDir,
			Agent:             a.agentController,
			AgentCallerOwned:  true,
			Logger:            a.opts.Logger,
			OperationLog:      a.operationLog,
			ConnectionStatus:  a.setConnectionStatus,
			ReleaseNotice:     a.handleReleaseNotice,
		})
		if err != nil {
			a.setConnectionStatus(node.ConnectionStatus{State: "error", Error: err.Error()})
			a.clearRuntime(done)
			return
		}
		var bridgeDone chan struct{}
		if cfg.LocalBridgeEnabled {
			bridgeDone = make(chan struct{})
			go func() {
				defer close(bridgeDone)
				if bridgeErr := localbridge.Run(runCtx, a.opts.DataDir, client.HandleLocalCapability); bridgeErr != nil && runCtx.Err() == nil {
					a.opts.Logger.Error("local bridge stopped", "endpoint", localbridge.Endpoint(a.opts.DataDir), "error", bridgeErr)
				}
			}()
		}
		if err := runRuntimeClient(runCtx, cancel, bridgeDone, client.Run); err != nil && !errors.Is(err, context.Canceled) {
			a.setConnectionStatus(node.ConnectionStatus{State: "error", Error: err.Error()})
		}
		a.clearRuntime(done)
	}()
}

func runRuntimeClient(ctx context.Context, cancel context.CancelFunc, bridgeDone <-chan struct{}, run func(context.Context) error) error {
	err := run(ctx)
	cancel()
	if bridgeDone != nil {
		<-bridgeDone
	}
	return err
}

func (a *App) clearRuntime(done chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runtimeDone == done {
		a.runtimeCancel = nil
		a.runtimeDone = nil
	}
}

func (a *App) setConnectionStatus(status node.ConnectionStatus) {
	a.mu.Lock()
	a.runtimeStatus = status.State
	a.runtimeError = status.Error
	a.mu.Unlock()
}

func (a *App) stopRuntime() {
	a.stopRuntimeWithin(5 * time.Second)
}

func (a *App) stopRuntimeWithin(timeout time.Duration) bool {
	a.mu.Lock()
	cancel := a.runtimeCancel
	done := a.runtimeDone
	a.mu.Unlock()
	if cancel == nil {
		return done == nil
	}
	cancel()
	if done == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		a.opts.Logger.Warn("Node runtime did not stop within timeout")
		return false
	}
}

func (a *App) restartRuntime() {
	a.restartRuntimeWithin(5 * time.Second)
}

func (a *App) restartRuntimeWithin(timeout time.Duration) {
	if a.stopRuntimeWithin(timeout) {
		a.startRuntime()
		return
	}

	a.mu.Lock()
	done := a.runtimeDone
	if done == nil {
		a.mu.Unlock()
		a.startRuntime()
		return
	}
	if a.runtimeRestartAfter == done {
		a.mu.Unlock()
		return
	}
	a.runtimeRestartAfter = done
	a.mu.Unlock()

	go func() {
		<-done
		a.mu.Lock()
		if a.runtimeRestartAfter != done {
			a.mu.Unlock()
			return
		}
		a.runtimeRestartAfter = nil
		a.mu.Unlock()
		a.startRuntime()
	}()
}

func decodeJSON(r *http.Request, output any) error {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type"))), "application/json") {
		return errors.New("content type must be application/json")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxUIRequestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func handleExistingInstance(ctx context.Context, uiURL string, noOpenWindow bool) error {
	if noOpenWindow {
		return nil
	}
	return openExistingUI(ctx, uiURL)
}

func openExistingUI(ctx context.Context, uiURL string) error {
	for attempt := 0; attempt < 20; attempt++ {
		if existingUIHealthy() {
			return openLocalUI(uiURL)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nodeinstance.ErrAlreadyRunning
}

// logOperation 记录 HTTP 请求操作日志
func (a *App) logOperation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 跳过健康检查和静态资源的日志记录
		switch r.URL.Path {
		case "/healthz", "/":
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		if a.operationLog == nil {
			return
		}
		duration := time.Since(start).Milliseconds()
		category := "http"
		action := "request"
		level := operationlog.LevelInfo
		if rw.status >= 500 {
			level = operationlog.LevelError
		} else if rw.status >= 400 {
			level = operationlog.LevelWarning
		}
		// 根据路径细化分类
		if strings.HasPrefix(r.URL.Path, "/api/browser") {
			category = "browser"
		} else if strings.HasPrefix(r.URL.Path, "/api/helper") {
			category = "hub"
		} else if strings.HasPrefix(r.URL.Path, "/api/working") {
			category = "working"
		} else if strings.HasPrefix(r.URL.Path, "/api/update") {
			category = "update"
		} else if strings.HasPrefix(r.URL.Path, "/api/ai-routing") {
			category = "agent"
		} else if strings.HasPrefix(r.URL.Path, "/api/components") {
			category = "component"
		} else if strings.HasPrefix(r.URL.Path, "/api/operation-logs") {
			category = "operationlog"
		}
		entry := operationlog.NewEntry(level, category, action, r.Method+" "+r.URL.Path).
			WithHTTP(r.Method, r.URL.Path, rw.status, duration, clientIPFromRequest(r))
		a.operationLog.Append(entry)
	})
}

// statusRecorder 用于捕获 HTTP 响应状态码
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (rw *statusRecorder) WriteHeader(status int) {
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}

func clientIPFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
		if i := strings.Index(fwd, ","); i > 0 {
			return strings.TrimSpace(fwd[:i])
		}
		return fwd
	}
	if r.RemoteAddr != "" {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err == nil {
			return host
		}
		return r.RemoteAddr
	}
	return ""
}

func (a *App) handleOperationLogs(w http.ResponseWriter, r *http.Request) {
	if a.operationLog == nil {
		writeJSON(w, http.StatusOK, map[string]any{"entries": []operationlog.Entry{}, "total": 0, "categories": []string{}, "retention_days": operationlog.RetentionDays})
		return
	}
	q := r.URL.Query()
	level := operationlog.Level(strings.TrimSpace(q.Get("level")))
	category := strings.TrimSpace(q.Get("category"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	entries, total := a.operationLog.Query(level, category, limit, offset)
	writeJSON(w, http.StatusOK, map[string]any{
		"entries":        entries,
		"total":          total,
		"categories":     a.operationLog.Categories(),
		"retention_days": operationlog.RetentionDays,
	})
}

func (a *App) handleOperationLogsCleanup(w http.ResponseWriter, r *http.Request) {
	if a.operationLog == nil {
		writeJSON(w, http.StatusOK, map[string]any{"removed": 0})
		return
	}
	removed := a.operationLog.PurgeExpired()
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "retention_days": operationlog.RetentionDays})
}

func (a *App) handleOperationLogsStats(w http.ResponseWriter, r *http.Request) {
	if a.operationLog == nil {
		writeJSON(w, http.StatusOK, map[string]any{"total": 0, "retention_days": operationlog.RetentionDays})
		return
	}
	writeJSON(w, http.StatusOK, a.operationLog.Stats())
}

// operationLogCleanupLoop 定期清理过期操作日志（每24小时一次）
func (a *App) operationLogCleanupLoop(ctx context.Context) {
	if a.operationLog == nil {
		return
	}
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			removed := a.operationLog.PurgeExpired()
			if removed > 0 {
				a.opts.Logger.Info("operation log: purged expired entries", "removed", removed, "retention_days", operationlog.RetentionDays)
			}
		}
	}
}

func existingUIHealthy() bool {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get("http://" + defaultListenAddress + "/healthz")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK && resp.Header.Get("X-Fast-Spider-UI") == "1"
}
