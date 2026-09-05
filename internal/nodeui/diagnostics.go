package nodeui

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

type diagnosticsResponse struct {
	RefreshedAt string                  `json:"refreshedAt"`
	Node        diagnosticNodeView      `json:"node"`
	Hub         diagnosticHubView       `json:"hub"`
	Agent       diagnosticAgentView     `json:"agent"`
	Workspace   diagnosticWorkspaceView `json:"workspace"`
	Local       diagnosticLocalView     `json:"local"`
	Errors      []diagnosticErrorView   `json:"errors"`
	Summary     diagnosticSummaryView   `json:"summary"`
}

type diagnosticNodeView struct {
	Version           string `json:"version"`
	ConfigStatus      string `json:"configStatus"`
	Registered        bool   `json:"registered"`
	RuntimeOwned      bool   `json:"runtimeOwned"`
	RuntimeStatus     string `json:"runtimeStatus"`
	AutoStartEnabled  bool   `json:"autoStartEnabled"`
	AutoUpdateEnabled bool   `json:"autoUpdateEnabled"`
}

type diagnosticHubView struct {
	Configured       bool   `json:"configured"`
	Host             string `json:"host,omitempty"`
	ConnectionStatus string `json:"connectionStatus"`
	LastKnownStatus  string `json:"lastKnownStatus"`
}

type diagnosticAgentRuntimeView struct {
	Runtime             string `json:"runtime"`
	RuntimeSource       string `json:"runtimeSource,omitempty"`
	ConfigurationSource string `json:"configurationSource,omitempty"`
	Available           bool   `json:"available"`
	Version             string `json:"version,omitempty"`
	AuthConfigured      *bool  `json:"authConfigured,omitempty"`
	Route               string `json:"route,omitempty"`
	ErrorClass          string `json:"errorClass,omitempty"`
	ErrorMessage        string `json:"errorMessage,omitempty"`
	ReadyForCreate      *bool  `json:"readyForSessionCreate,omitempty"`
	ReadinessCode       string `json:"readinessReasonCode,omitempty"`
	ReadinessMs         int64  `json:"readinessMs,omitempty"`
}

type diagnosticAgentView struct {
	Codex      diagnosticAgentRuntimeView `json:"codex"`
	ClaudeCode diagnosticAgentRuntimeView `json:"claudeCode"`
	CCSwitch   diagnosticCCSwitchView     `json:"ccSwitch"`
}

type diagnosticCCSwitchView struct {
	DBDetected          bool   `json:"dbDetected"`
	SchemaSupported     bool   `json:"schemaSupported"`
	SchemaFingerprint   string `json:"schemaFingerprint,omitempty"`
	CurrentRoute        string `json:"currentRoute,omitempty"`
	SelectionConsistent *bool  `json:"selectionConsistent,omitempty"`
	ErrorClass          string `json:"errorClass,omitempty"`
	ErrorMessage        string `json:"errorMessage,omitempty"`
}

type diagnosticWorkspaceView struct {
	Bound         bool   `json:"bound"`
	ProjectStatus string `json:"projectStatus"`
	Readable      bool   `json:"readable"`
	Exists        bool   `json:"exists"`
	Revision      string `json:"revision,omitempty"`
}

type diagnosticLocalView struct {
	LocalBridgeConfigured bool   `json:"localBridgeConfigured"`
	BrowserConfigured     bool   `json:"browserConfigured"`
	BrowserPresent        bool   `json:"browserPresent"`
	ComponentRootPresent  bool   `json:"componentRootPresent"`
	TraySupported         bool   `json:"traySupported"`
	TrayActive            bool   `json:"trayActive"`
	BrowserReady          bool   `json:"browserReady"`
	BrowserReasonCode     string `json:"browserReasonCode,omitempty"`
	BrowserReadinessMs    int64  `json:"browserReadinessMs,omitempty"`
	WSLAvailable          bool   `json:"wslAvailable"`
}

type diagnosticErrorView struct {
	Area          string `json:"area"`
	ErrorClass    string `json:"errorClass"`
	PublicMessage string `json:"publicMessage"`
}

type diagnosticSummaryView struct {
	Node      string `json:"node"`
	Hub       string `json:"hub"`
	Agent     string `json:"agent"`
	Workspace string `json:"workspace"`
	Local     string `json:"local"`
}

func (a *App) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, a.buildDiagnostics(ctx))
}

func (a *App) buildDiagnostics(ctx context.Context) diagnosticsResponse {
	a.mu.Lock()
	cfg := a.config
	runtimeOwned, runtimeStatus, runtimeError := a.runtimeOwned, a.runtimeStatus, a.runtimeError
	trayActive := a.trayActive
	a.mu.Unlock()

	state, stateErr := node.LoadState(filepath.Join(a.opts.DataDir, "state.json"))
	registered := stateErr == nil
	nodeView := diagnosticNodeView{
		Version: publicAIText(a.opts.Version, 128), ConfigStatus: "loaded", Registered: registered,
		RuntimeOwned: runtimeOwned, RuntimeStatus: publicRuntimeStatus(runtimeStatus),
		AutoStartEnabled: cfg.AutoStartEnabled, AutoUpdateEnabled: cfg.AutoUpdateEnabled,
	}
	if nodeView.Version == "" {
		nodeView.Version = "unknown"
	}
	if nodeView.RuntimeStatus == "" {
		nodeView.RuntimeStatus = "unknown"
	}
	hubURL := cfg.HubURL
	if registered {
		hubURL = state.HubURL
	}
	hubView := diagnosticHubView{
		Configured: registered || strings.TrimSpace(hubURL) != "", Host: diagnosticHubHost(hubURL),
		ConnectionStatus: nodeView.RuntimeStatus, LastKnownStatus: nodeView.RuntimeStatus,
	}
	agentView, agentErrors := a.buildDiagnosticAgent(ctx)
	workspace := a.buildDiagnosticWorkspace(ctx, cfg)
	local := diagnosticLocalView{
		LocalBridgeConfigured: cfg.LocalBridgeEnabled,
		BrowserConfigured:     strings.TrimSpace(cfg.BrowserSidecarDir) != "",
		BrowserPresent:        diagnosticDirectoryPresent(cfg.BrowserSidecarDir),
		ComponentRootPresent:  diagnosticDirectoryPresent(filepath.Join(a.opts.DataDir, "components")),
		TraySupported:         traySupported(), TrayActive: trayActive,
	}
	if runtime.GOOS == "windows" {
		_, wslErr := exec.LookPath("wsl.exe")
		local.WSLAvailable = wslErr == nil
	}
	localClient := node.NewLocalCapabilityClient(node.Config{DataDir: a.opts.DataDir, BrowserSidecarDir: cfg.BrowserSidecarDir, Version: a.opts.Version, Logger: a.opts.Logger})
	browserReadiness := localClient.HandleLocalCapability(ctx, protocolv1.CapabilityRequest{
		RequestId: "nodeui-diagnostics-browser", Capability: "browser.automation", Action: "readiness", Params: map[string]any{},
	})
	if browserReadiness.Error == nil {
		local.BrowserReady, _ = browserReadiness.Result["ready"].(bool)
		local.BrowserReasonCode = publicAIText(browserReadiness.Result["reasonCode"], 64)
		if timing, ok := browserReadiness.Result["timing"].(map[string]any); ok {
			local.BrowserReadinessMs = diagnosticInt64(timing["totalMs"])
		}
	}
	errorsOut := append([]diagnosticErrorView(nil), agentErrors...)
	if class := classifyDiagnosticError(runtimeError); class != "" {
		errorsOut = append(errorsOut, diagnosticErrorView{Area: "node_connection", ErrorClass: class, PublicMessage: publicAIErrorMessage(class)})
	}
	response := diagnosticsResponse{
		RefreshedAt: time.Now().UTC().Format(time.RFC3339), Node: nodeView, Hub: hubView,
		Agent: agentView, Workspace: workspace, Local: local, Errors: errorsOut,
	}
	response.Summary = diagnosticSummaryView{
		Node:      nodeView.RuntimeStatus + " · v" + nodeView.Version,
		Hub:       diagnosticConfiguredText(hubView.Configured) + " · " + hubView.ConnectionStatus,
		Agent:     "Codex " + agentView.Codex.Runtime + " · Claude " + agentView.ClaudeCode.Runtime + " · CC Switch " + diagnosticConfiguredText(agentView.CCSwitch.DBDetected),
		Workspace: workspace.ProjectStatus + " · " + diagnosticReadableText(workspace.Readable),
		Local:     "Bridge " + diagnosticConfiguredText(local.LocalBridgeConfigured) + " · Browser " + diagnosticConfiguredText(local.BrowserPresent),
	}
	return response
}

func (a *App) buildDiagnosticAgent(ctx context.Context) (diagnosticAgentView, []diagnosticErrorView) {
	view := diagnosticAgentView{
		Codex:      diagnosticAgentRuntimeView{Runtime: "unavailable"},
		ClaudeCode: diagnosticAgentRuntimeView{Runtime: "unavailable"},
	}
	if a.agentController == nil {
		problem := diagnosticErrorView{Area: "agent", ErrorClass: "runtime_unavailable", PublicMessage: publicAIErrorMessage("runtime_unavailable")}
		return view, []diagnosticErrorView{problem}
	}
	providers, providersErr := a.agentController.Control(ctx, "providers.list", map[string]any{})
	routingStatus, routingErr := a.agentController.Control(ctx, "routing.status", map[string]any{})
	readiness, readinessErr := a.agentController.Control(ctx, "provider.readiness", map[string]any{"providerId": "codex", "mode": "safe"})
	providerFacts := providerFactsByID(providers["providers"])
	routes := routeFactsByApp(routingStatus)
	view.Codex = diagnosticProviderRuntime(providerFacts["codex"], routes["codex"], false)
	if readinessErr == nil {
		ready, _ := readiness["readyForSessionCreate"].(bool)
		view.Codex.ReadyForCreate = &ready
		view.Codex.ReadinessCode = publicAIText(readiness["reasonCode"], 64)
		view.Codex.ReadinessMs = diagnosticInt64(readiness["elapsedMs"])
	}
	view.ClaudeCode = diagnosticProviderRuntime(providerFacts["claude_code"], routes["claude"], true)
	cc := buildCCSwitchView(routes)
	view.CCSwitch = diagnosticCCSwitchView{
		DBDetected: cc.DBDetected, SchemaSupported: cc.SchemaSupported, SchemaFingerprint: cc.SchemaFingerprint,
		CurrentRoute: diagnosticRouteSummary(routes), SelectionConsistent: cc.SelectionConsistent,
		ErrorClass: cc.ErrorClass, ErrorMessage: cc.ErrorMessage,
	}
	problems := make([]diagnosticErrorView, 0, 4)
	for area, runtime := range map[string]diagnosticAgentRuntimeView{"codex": view.Codex, "claude_code": view.ClaudeCode} {
		if runtime.ErrorClass != "" {
			problems = append(problems, diagnosticErrorView{Area: area, ErrorClass: runtime.ErrorClass, PublicMessage: runtime.ErrorMessage})
		}
	}
	if view.CCSwitch.ErrorClass != "" {
		problems = append(problems, diagnosticErrorView{Area: "cc_switch", ErrorClass: view.CCSwitch.ErrorClass, PublicMessage: view.CCSwitch.ErrorMessage})
	}
	if providersErr != nil || routingErr != nil || readinessErr != nil {
		problems = append(problems, diagnosticErrorView{Area: "agent_discovery", ErrorClass: "unknown", PublicMessage: publicAIErrorMessage("unknown")})
	}
	return view, problems
}

func diagnosticInt64(value any) int64 {
	switch number := value.(type) {
	case int64:
		return number
	case int:
		return int64(number)
	case float64:
		return int64(number)
	default:
		return 0
	}
}

func diagnosticRouteSummary(routes map[string]map[string]any) string {
	parts := make([]string, 0, 2)
	for _, appType := range []string{"codex", "claude"} {
		route := publicRoute(routes[appType])
		if mode := publicDiagnosticRoute(route.RoutingMode); mode != "unknown" {
			parts = append(parts, appType+": "+mode)
		}
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, " · ")
}

func diagnosticProviderRuntime(provider, route map[string]any, includeAuth bool) diagnosticAgentRuntimeView {
	available, _ := provider["available"].(bool)
	view := diagnosticAgentRuntimeView{Available: available, Runtime: "unavailable", Version: publicAIText(provider["version"], 128)}
	view.RuntimeSource = publicEnum(publicAIText(provider["runtimeSource"], 64), "cli", "desktop_bundled", "configured", "unavailable")
	view.ConfigurationSource = publicEnum(publicAIText(provider["configurationSource"], 64), "user_codex", "codex_home")
	if available {
		view.Runtime = "available"
	}
	publicRoute := publicRoute(route)
	view.Route = publicDiagnosticRoute(publicRoute.RoutingMode)
	view.ErrorClass = publicErrorClass(provider["errorClass"])
	if view.ErrorClass == "" {
		view.ErrorClass = publicRoute.ErrorClass
	}
	if view.ErrorClass != "" {
		view.ErrorMessage = publicAIErrorMessage(view.ErrorClass)
	}
	if includeAuth {
		configured := false
		if auth, ok := provider["authConfiguration"].(map[string]any); ok {
			configured, _ = auth["configured"].(bool)
		}
		view.AuthConfigured = &configured
	}
	return view
}

func (a *App) buildDiagnosticWorkspace(ctx context.Context, cfg LocalConfig) diagnosticWorkspaceView {
	view := diagnosticWorkspaceView{ProjectStatus: "not_bound"}
	projectPath := strings.TrimSpace(cfg.WorkingProjectPath)
	if projectPath == "" {
		return view
	}
	view.Bound, view.ProjectStatus = true, "bound_project"
	client, err := node.New(node.Config{DataDir: a.opts.DataDir, Version: a.opts.Version, Logger: a.opts.Logger})
	if err != nil {
		return view
	}
	params := map[string]any{"projectPath": projectPath}
	result := client.HandleLocalCapability(ctx, protocolv1.CapabilityRequest{RequestId: "nodeui-diagnostics-working", Capability: "working.context", Action: "get", Params: params})
	if result.Error != nil {
		return view
	}
	view.Readable = true
	view.Exists, _ = result.Result["exists"].(bool)
	view.Revision = publicRevision(result.Result["revision"])
	return view
}

func diagnosticHubHost(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return ""
	}
	host := parsed.Hostname()
	if port := parsed.Port(); port != "" {
		host += ":" + port
	}
	return publicAIText(host, 255)
}

func diagnosticDirectoryPresent(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func publicRuntimeStatus(raw string) string {
	return publicEnum(raw, "online", "connecting", "reconnecting", "starting", "stopped", "not_registered", "external_running", "error")
}

func publicDiagnosticRoute(raw string) string {
	if value := publicEnum(raw, "direct", "cc_switch"); value != "" {
		return value
	}
	value := publicAIText(raw, 256)
	if value == "" {
		return "unknown"
	}
	return value
}

func publicRevision(raw any) string {
	value := publicAIText(raw, 160)
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && !strings.ContainsRune("-_:.", char) {
			return ""
		}
	}
	return value
}

func classifyDiagnosticError(raw string) string {
	lower := strings.ToLower(raw)
	if strings.TrimSpace(lower) == "" {
		return ""
	}
	switch {
	case strings.Contains(lower, "auth"), strings.Contains(lower, "unauthorized"), strings.Contains(lower, "credential"):
		return "auth_failed"
	case strings.Contains(lower, "rate limit"), strings.Contains(lower, "429"):
		return "rate_limited"
	case strings.Contains(lower, "connection"), strings.Contains(lower, "network"), strings.Contains(lower, "timeout"), strings.Contains(lower, "tls"):
		return "network_failed"
	case strings.Contains(lower, "runtime"), strings.Contains(lower, "executable"):
		return "runtime_unavailable"
	default:
		return "unknown"
	}
}

func diagnosticConfiguredText(value bool) string {
	if value {
		return "configured"
	}
	return "not_configured"
}

func diagnosticReadableText(value bool) string {
	if value {
		return "readable"
	}
	return "unavailable"
}
