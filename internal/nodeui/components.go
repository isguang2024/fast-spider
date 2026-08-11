package nodeui

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/componentmgr"
	"github.com/isguang2024/fast-spider/internal/node"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

const diagnosticTempPrefix = ".fast-spider-search-file-diagnostic-"

type componentEnsureFunc func(context.Context, string, string, string, string) (componentmgr.Installed, error)

type componentView struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Installed       bool   `json:"installed"`
	Version         string `json:"version,omitempty"`
	Platform        string `json:"platform"`
	Status          string `json:"status"`
	ExecutableReady bool   `json:"executableReady"`
	EngineReady     bool   `json:"engineReady"`
}

type componentsResponse struct {
	Components []componentView `json:"components"`
}

type capabilitySummary struct {
	Version string   `json:"version"`
	Actions []string `json:"actions"`
}

type searchFileStatusResponse struct {
	SearchEngine     string            `json:"searchEngine"`
	RipgrepInstalled bool              `json:"ripgrepInstalled"`
	RipgrepVerified  bool              `json:"ripgrepVerified"`
	NativeReady      bool              `json:"nativeReady"`
	FileRead         capabilitySummary `json:"fileRead"`
	FileEdit         capabilitySummary `json:"fileEdit"`
}

type searchFileSelfTestResponse struct {
	Status          string `json:"status"`
	Engine          string `json:"engine,omitempty"`
	FallbackReason  string `json:"fallbackReason,omitempty"`
	ElapsedMs       int64  `json:"elapsedMs"`
	FileRead        string `json:"fileRead"`
	FileEditPreview string `json:"fileEditPreview"`
	ErrorClass      string `json:"errorClass,omitempty"`
	PublicMessage   string `json:"publicMessage,omitempty"`
}

func knownComponent(componentID string) bool {
	return componentID == "browser" || componentID == "search-ripgrep"
}

func (a *App) handleComponents(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, componentsResponse{Components: a.componentViews()})
}

func (a *App) componentViews() []componentView {
	a.mu.Lock()
	cfg := a.config
	a.mu.Unlock()
	return []componentView{
		a.componentView("browser", "Browser", cfg),
		a.componentView("search-ripgrep", "ripgrep 搜索引擎", cfg),
	}
}

func (a *App) componentView(id, name string, cfg LocalConfig) componentView {
	view := componentView{ID: id, Name: name, Platform: runtime.GOOS + "-" + runtime.GOARCH, Status: "not_installed"}
	installed, err := componentmgr.FindInstalled(a.opts.DataDir, id)
	if err != nil {
		if !errors.Is(err, componentmgr.ErrComponentNotInstalled) {
			view.Status = "invalid"
		}
		return view
	}
	view.Installed = true
	view.Version = installed.Version
	view.Status = "installed"
	if id == "search-ripgrep" {
		name := "rg"
		if runtime.GOOS == "windows" {
			name = "rg.exe"
		}
		_, _, executableErr := componentmgr.FindInstalledExecutable(a.opts.DataDir, id, name)
		view.ExecutableReady = executableErr == nil
		view.EngineReady = view.ExecutableReady
		if view.EngineReady {
			view.Status = "ready"
		} else {
			view.Status = "invalid"
		}
		return view
	}
	view.EngineReady = sameComponentPath(cfg.BrowserSidecarDir, installed.Path)
	view.ExecutableReady = view.EngineReady
	if view.EngineReady {
		view.Status = "ready"
	}
	return view
}

func sameComponentPath(left, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	a, errA := filepath.Abs(left)
	b, errB := filepath.Abs(right)
	if errA != nil || errB != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func (a *App) handleComponentEnsure(w http.ResponseWriter, r *http.Request) {
	var req componentEnsureRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, errors.New("组件请求无效"))
		return
	}
	req.ComponentID = strings.TrimSpace(req.ComponentID)
	if !knownComponent(req.ComponentID) {
		writeAPIError(w, http.StatusBadRequest, errors.New("仅支持安装 Browser 或 search-ripgrep 组件"))
		return
	}
	state, err := node.LoadState(filepath.Join(a.opts.DataDir, "state.json"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, errors.New("请先连接并登记设备"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	installed, err := a.componentEnsure(ctx, a.opts.DataDir, state.HubURL, state.HubPublicKey, req.ComponentID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, errors.New("组件安装失败，请检查 Hub 与组件发布状态"))
		return
	}
	a.mu.Lock()
	current := a.config
	a.mu.Unlock()
	next, changed := configureInstalledComponent(current, installed)
	if changed {
		if err := saveLocalConfig(a.opts.DataDir, next); err != nil {
			writeAPIError(w, http.StatusInternalServerError, errors.New("组件已安装，但本地配置保存失败"))
			return
		}
		a.mu.Lock()
		a.config = next
		a.mu.Unlock()
		if installed.ID == "browser" {
			a.restartRuntime()
		}
	}
	if err := componentmgr.CleanupInstalled(a.opts.DataDir, installed); err != nil {
		a.opts.Logger.Warn("cleanup installed component files failed", "componentId", installed.ID, "version", installed.Version)
	}
	writeJSON(w, http.StatusOK, map[string]any{"component": a.componentView(installed.ID, componentDisplayName(installed.ID), next)})
}

func componentDisplayName(id string) string {
	if id == "browser" {
		return "Browser"
	}
	return "ripgrep 搜索引擎"
}

func (a *App) handleSearchFileStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.searchFileStatus())
}

func (a *App) searchFileStatus() searchFileStatusResponse {
	components := a.componentViews()
	ripgrepInstalled, ripgrepVerified := components[1].Installed, components[1].EngineReady
	engine := "native"
	if ripgrepVerified {
		engine = "ripgrep"
	}
	return searchFileStatusResponse{
		SearchEngine: engine, RipgrepInstalled: ripgrepInstalled, RipgrepVerified: ripgrepVerified, NativeReady: true,
		FileRead: capabilityByID("file.read"), FileEdit: capabilityByID("file.write"),
	}
}

func capabilityByID(id string) capabilitySummary {
	for _, capability := range protocolv1.NodeCapabilities {
		if capability.CapabilityId == id {
			return capabilitySummary{Version: capability.Version, Actions: append([]string(nil), capability.Actions...)}
		}
	}
	return capabilitySummary{}
}

func (a *App) handleSearchFileSelfTest(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, a.runSearchFileSelfTest(ctx))
}

func (a *App) runSearchFileSelfTest(ctx context.Context) searchFileSelfTestResponse {
	started := time.Now()
	result := searchFileSelfTestResponse{Status: "FAIL", FileRead: "FAIL", FileEditPreview: "FAIL"}
	dir, err := os.MkdirTemp(a.opts.DataDir, diagnosticTempPrefix)
	if err != nil {
		return failedSearchFileSelfTest(result, started, "runtime_unavailable")
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "fixture.txt")
	original := []byte("alpha\nneedle diagnostic\nomega\n")
	if os.WriteFile(path, original, 0o600) != nil {
		return failedSearchFileSelfTest(result, started, "runtime_unavailable")
	}
	client := node.NewLocalCapabilityClient(node.Config{DataDir: a.opts.DataDir, Version: a.opts.Version, Logger: a.opts.Logger})
	search := client.HandleLocalCapability(ctx, localCapabilityRequest("diagnostic-search", "code.search", "search", map[string]any{"path": dir, "query": "needle", "limit": 5}))
	if search.Error != nil {
		return failedSearchFileSelfTest(result, started, publicSelfTestErrorClass(search.Error.Code))
	}
	result.Engine, _ = search.Result["engine"].(string)
	result.FallbackReason, _ = search.Result["fallbackReason"].(string)
	if elapsed, ok := search.Result["elapsedMs"].(float64); ok {
		result.ElapsedMs = int64(elapsed)
	}
	read := client.HandleLocalCapability(ctx, localCapabilityRequest("diagnostic-read", "file.read", "read", map[string]any{"path": path, "headLines": 3}))
	if read.Error != nil {
		return failedSearchFileSelfTest(result, started, publicSelfTestErrorClass(read.Error.Code))
	}
	fileSHA, _ := read.Result["fileSha256"].(string)
	if fileSHA == "" {
		return failedSearchFileSelfTest(result, started, "unknown")
	}
	result.FileRead = "PASS"
	preview := client.HandleLocalCapability(ctx, localCapabilityRequest("diagnostic-preview", "file.write", "preview", map[string]any{
		"path": path, "previewOf": "replace", "oldText": "needle diagnostic", "newText": "preview only", "expectedFileSha256": fileSHA,
	}))
	if preview.Error != nil {
		return failedSearchFileSelfTest(result, started, publicSelfTestErrorClass(preview.Error.Code))
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(original) {
		return failedSearchFileSelfTest(result, started, "unknown")
	}
	changed, _ := preview.Result["changed"].(bool)
	if !changed {
		return failedSearchFileSelfTest(result, started, "unknown")
	}
	result.FileEditPreview = "PASS"
	result.Status = "PASS"
	result.ElapsedMs = time.Since(started).Milliseconds()
	return result
}

func localCapabilityRequest(id, capability, action string, params map[string]any) protocolv1.CapabilityRequest {
	return protocolv1.CapabilityRequest{RequestId: id, Capability: capability, Action: action, Params: params}
}

func failedSearchFileSelfTest(result searchFileSelfTestResponse, started time.Time, class string) searchFileSelfTestResponse {
	result.ElapsedMs = time.Since(started).Milliseconds()
	result.ErrorClass = class
	result.PublicMessage = selfTestPublicMessage(class)
	return result
}

func publicSelfTestErrorClass(code string) string {
	switch code {
	case "NOT_FOUND", "NOT_REGULAR_FILE", "NOT_TEXT", "OUTPUT_LIMIT", "INVALID_REQUEST":
		return "invalid_request"
	case "DEADLINE_EXCEEDED":
		return "runtime_unavailable"
	default:
		return "unknown"
	}
}

func selfTestPublicMessage(class string) string {
	if class == "invalid_request" {
		return "本地诊断数据未通过能力校验"
	}
	if class == "runtime_unavailable" {
		return "本地能力暂时不可用，请稍后重试"
	}
	return "本地自检未通过，请刷新状态后重试"
}
