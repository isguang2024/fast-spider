package nodeui

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

type aiRoutingResponse struct {
	RefreshedAt string           `json:"refreshedAt"`
	Codex       aiProviderView   `json:"codex"`
	ClaudeCode  aiProviderView   `json:"claudeCode"`
	CCSwitch    ccSwitchView     `json:"ccSwitch"`
	HealthTest  aiHealthTestView `json:"healthTest"`
}

type aiProviderView struct {
	ProviderID            string                      `json:"providerId"`
	Runtime               string                      `json:"runtime"`
	Available             bool                        `json:"available"`
	Version               string                      `json:"version,omitempty"`
	AuthConfigured        *bool                       `json:"authConfigured,omitempty"`
	AuthStatus            string                      `json:"authStatus,omitempty"`
	ExecutionHealth       string                      `json:"executionHealth"`
	Models                []aiModelView               `json:"models"`
	SupportedActions      []string                    `json:"supportedActions"`
	EffectiveCapabilities map[string]aiCapabilityView `json:"effectiveCapabilities"`
	Route                 aiRouteView                 `json:"route"`
	ErrorClass            string                      `json:"errorClass,omitempty"`
	ErrorMessage          string                      `json:"errorMessage,omitempty"`
}

type aiModelView struct {
	ID            string `json:"id,omitempty"`
	Model         string `json:"model,omitempty"`
	DisplayName   string `json:"displayName,omitempty"`
	ClientAlias   string `json:"clientAlias,omitempty"`
	UpstreamModel string `json:"upstreamModel,omitempty"`
}

type aiCapabilityView struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type aiRouteView struct {
	Available             bool                        `json:"available"`
	Reason                string                      `json:"reason,omitempty"`
	ErrorClass            string                      `json:"errorClass,omitempty"`
	ErrorMessage          string                      `json:"errorMessage,omitempty"`
	RoutingMode           string                      `json:"routingMode,omitempty"`
	SchemaFingerprint     string                      `json:"schemaFingerprint,omitempty"`
	ProxyEnabled          bool                        `json:"proxyEnabled"`
	Takeover              bool                        `json:"takeover"`
	LiveTakeover          bool                        `json:"liveTakeover"`
	CurrentProvider       string                      `json:"currentProvider,omitempty"`
	ModelMapping          []aiModelView               `json:"modelMapping"`
	SelectionConsistent   *bool                       `json:"selectionConsistent,omitempty"`
	ProviderHealth        *aiProviderHealthView       `json:"providerHealth,omitempty"`
	EffectiveCapabilities map[string]aiCapabilityView `json:"effectiveCapabilities"`
}

type aiProviderHealthView struct {
	Healthy             bool `json:"healthy"`
	ConsecutiveFailures int  `json:"consecutiveFailures"`
}

type ccSwitchView struct {
	DBDetected            bool                        `json:"dbDetected"`
	SchemaSupported       bool                        `json:"schemaSupported"`
	SchemaFingerprint     string                      `json:"schemaFingerprint,omitempty"`
	ProxyEnabled          bool                        `json:"proxyEnabled"`
	Takeover              bool                        `json:"takeover"`
	LiveTakeover          bool                        `json:"liveTakeover"`
	CurrentProvider       string                      `json:"currentProvider,omitempty"`
	ModelMapping          []aiModelView               `json:"modelMapping"`
	SelectionConsistent   *bool                       `json:"selectionConsistent,omitempty"`
	ProviderHealth        []aiProviderHealthView      `json:"providerHealth"`
	EffectiveCapabilities map[string]aiCapabilityView `json:"effectiveCapabilities"`
	Reason                string                      `json:"reason,omitempty"`
	ErrorClass            string                      `json:"errorClass,omitempty"`
	ErrorMessage          string                      `json:"errorMessage,omitempty"`
}

type aiHealthTestView struct {
	Mode    string `json:"mode"`
	Message string `json:"message"`
}

func (a *App) handleAIRouting(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	view, err := a.buildAIRoutingView(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"errorClass": "runtime_unavailable", "error": "AI 运行时状态暂时不可用，请稍后手动刷新",
		})
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *App) buildAIRoutingView(ctx context.Context) (aiRoutingResponse, error) {
	if a.agentController == nil {
		return aiRoutingResponse{}, errors.New("agent controller unavailable")
	}
	providers, providersErr := a.agentController.Control(ctx, "providers.list", map[string]any{})
	codexModels, codexModelsErr := a.agentController.Control(ctx, "models.list", map[string]any{"providerId": "codex"})
	claudeModels, claudeModelsErr := a.agentController.Control(ctx, "models.list", map[string]any{"providerId": "claude_code"})
	codexCapabilities, codexCapabilitiesErr := a.agentController.Control(ctx, "provider.capabilities", map[string]any{"providerId": "codex"})
	claudeCapabilities, claudeCapabilitiesErr := a.agentController.Control(ctx, "provider.capabilities", map[string]any{"providerId": "claude_code"})
	routingStatus, routingErr := a.agentController.Control(ctx, "routing.status", map[string]any{})
	if providersErr != nil && codexModelsErr != nil && claudeModelsErr != nil && routingErr != nil {
		return aiRoutingResponse{}, errors.New("AI discovery unavailable")
	}

	providerFacts := providerFactsByID(providers["providers"])
	routes := routeFactsByApp(routingStatus)
	codex := buildProviderView("codex", providerFacts["codex"], codexModels, codexCapabilities, routes["codex"])
	claude := buildProviderView("claude_code", providerFacts["claude_code"], claudeModels, claudeCapabilities, routes["claude"])
	applyReadError(&codex, providersErr, codexModelsErr, codexCapabilitiesErr)
	applyReadError(&claude, providersErr, claudeModelsErr, claudeCapabilitiesErr)
	ccswitch := buildCCSwitchView(routes)
	if routingErr != nil {
		ccswitch.ErrorClass = "unknown"
		ccswitch.ErrorMessage = publicAIErrorMessage(ccswitch.ErrorClass)
	}
	return aiRoutingResponse{
		RefreshedAt: time.Now().UTC().Format(time.RFC3339),
		Codex:       codex, ClaudeCode: claude, CCSwitch: ccswitch,
		HealthTest: aiHealthTestView{Mode: "manual_session_required", Message: "真实模型健康测试需用户手动从会话执行；本页面不会自动生成请求或消耗额度。"},
	}, nil
}

func providerFactsByID(raw any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, item := range publicMapSlice(raw) {
		id := publicAIText(item["providerId"], 64)
		if id == "codex" || id == "claude_code" {
			out[id] = item
		}
	}
	return out
}

func routeFactsByApp(status map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, route := range publicMapSlice(status["routes"]) {
		appType := publicAIText(route["appType"], 32)
		if appType == "codex" || appType == "claude" || appType == "claude-desktop" {
			out[appType] = route
		}
	}
	if route, ok := status["route"].(map[string]any); ok {
		appType := publicAIText(route["appType"], 32)
		if appType != "" {
			out[appType] = route
		}
	}
	return out
}

func buildProviderView(id string, provider, models, capabilities, route map[string]any) aiProviderView {
	available, _ := provider["available"].(bool)
	view := aiProviderView{
		ProviderID: id, Available: available, Runtime: "unavailable", Models: []aiModelView{},
		SupportedActions:      publicStringSlice(provider["supportedActions"], 64, 128),
		EffectiveCapabilities: publicCapabilities(capabilities["effectiveCapabilities"]),
		Route:                 publicRoute(route), ExecutionHealth: publicExecutionHealth(provider["executionHealth"]),
	}
	if available {
		view.Runtime = "available"
	}
	view.Version = publicAIText(provider["version"], 128)
	view.Models = publicModels(models["models"])
	if id == "claude_code" {
		configured := false
		if auth, ok := provider["authConfiguration"].(map[string]any); ok {
			configured, _ = auth["configured"].(bool)
		}
		view.AuthConfigured = &configured
		if configured {
			view.AuthStatus = "configured"
		} else if available {
			view.AuthStatus = "not_configured"
		} else {
			view.AuthStatus = "runtime_unavailable"
		}
	}
	if view.ExecutionHealth == "" {
		view.ExecutionHealth = "unknown_until_turn"
	}
	view.ErrorClass = publicErrorClass(provider["errorClass"])
	if view.ErrorClass != "" {
		view.ErrorMessage = publicAIErrorMessage(view.ErrorClass)
	}
	return view
}

func buildCCSwitchView(routes map[string]map[string]any) ccSwitchView {
	view := ccSwitchView{ModelMapping: []aiModelView{}, ProviderHealth: []aiProviderHealthView{}, EffectiveCapabilities: map[string]aiCapabilityView{}}
	providers := make([]string, 0, len(routes))
	selectionKnown, selectionOK := false, true
	unsupportedSchema := false
	for _, appType := range []string{"codex", "claude", "claude-desktop"} {
		raw := routes[appType]
		if raw == nil {
			continue
		}
		route := publicRoute(raw)
		if route.SchemaFingerprint != "" {
			view.DBDetected = true
			if view.SchemaFingerprint == "" {
				view.SchemaFingerprint = route.SchemaFingerprint
			}
		}
		if route.Available {
			view.DBDetected, view.SchemaSupported = true, true
		}
		if route.Reason == "unsupported_schema" {
			view.DBDetected, unsupportedSchema = true, true
		}
		view.ProxyEnabled = view.ProxyEnabled || route.ProxyEnabled
		view.Takeover = view.Takeover || route.Takeover
		view.LiveTakeover = view.LiveTakeover || route.LiveTakeover
		if route.CurrentProvider != "" {
			providers = append(providers, appType+": "+route.CurrentProvider)
		}
		view.ModelMapping = append(view.ModelMapping, route.ModelMapping...)
		if route.ProviderHealth != nil {
			view.ProviderHealth = append(view.ProviderHealth, *route.ProviderHealth)
		}
		if route.SelectionConsistent != nil {
			selectionKnown = true
			selectionOK = selectionOK && *route.SelectionConsistent
		}
		for name, capability := range route.EffectiveCapabilities {
			view.EffectiveCapabilities[appType+"."+name] = capability
		}
		if route.Reason != "" {
			view.Reason = route.Reason
		}
		if route.ErrorClass != "" {
			view.ErrorClass, view.ErrorMessage = route.ErrorClass, route.ErrorMessage
		}
	}
	view.CurrentProvider = strings.Join(providers, " · ")
	if selectionKnown {
		view.SelectionConsistent = &selectionOK
	}
	if unsupportedSchema {
		view.SchemaSupported = false
	}
	return view
}

func publicRoute(raw map[string]any) aiRouteView {
	view := aiRouteView{ModelMapping: []aiModelView{}, EffectiveCapabilities: map[string]aiCapabilityView{}}
	if raw == nil {
		return view
	}
	view.Available, _ = raw["available"].(bool)
	view.Reason = publicRouteReason(raw["reason"])
	view.ErrorClass = publicErrorClass(raw["errorClass"])
	if view.ErrorClass != "" {
		view.ErrorMessage = publicAIErrorMessage(view.ErrorClass)
	}
	view.RoutingMode = publicEnum(raw["routingMode"], "direct", "cc_switch")
	view.SchemaFingerprint = publicFingerprint(raw["schemaFingerprint"])
	if proxy, ok := raw["proxy"].(map[string]any); ok {
		view.ProxyEnabled, _ = proxy["proxyEnabled"].(bool)
		view.Takeover, _ = proxy["takeoverEnabled"].(bool)
		view.LiveTakeover, _ = proxy["liveTakeoverActive"].(bool)
	}
	if provider, ok := raw["currentProvider"].(map[string]any); ok {
		view.CurrentProvider = publicAIText(provider["name"], 128)
		if view.CurrentProvider == "" {
			view.CurrentProvider = publicAIText(provider["providerId"], 128)
		}
		view.ModelMapping = publicModels(provider["models"])
		if health, ok := provider["health"].(map[string]any); ok {
			item := aiProviderHealthView{}
			item.Healthy, _ = health["healthy"].(bool)
			item.ConsecutiveFailures = publicInt(health["consecutiveFailures"], 0, 1_000_000)
			view.ProviderHealth = &item
		}
	}
	if value, ok := raw["selectionConsistent"].(bool); ok {
		view.SelectionConsistent = &value
	}
	view.EffectiveCapabilities = publicCapabilities(raw["effectiveCapabilities"])
	return view
}

func publicModels(raw any) []aiModelView {
	items := publicMapSlice(raw)
	if len(items) > 128 {
		items = items[:128]
	}
	out := make([]aiModelView, 0, len(items))
	for _, item := range items {
		model := aiModelView{
			ID: publicAIText(item["id"], 256), Model: publicAIText(item["model"], 256),
			DisplayName: publicAIText(item["displayName"], 256), ClientAlias: publicAIText(item["clientAlias"], 128),
			UpstreamModel: publicAIText(item["upstreamModel"], 256),
		}
		if model.ID != "" || model.Model != "" || model.DisplayName != "" || model.UpstreamModel != "" {
			out = append(out, model)
		}
	}
	return out
}

func publicCapabilities(raw any) map[string]aiCapabilityView {
	record, _ := raw.(map[string]any)
	out := map[string]aiCapabilityView{}
	for _, name := range []string{"toolCalls", "mcp", "webSearch", "vision", "thinking", "resume", "imageGeneration", "namespaceTools"} {
		capability, _ := record[name].(map[string]any)
		state := publicEnum(capability["state"], "supported", "unsupported", "unknown")
		if state == "" {
			continue
		}
		out[name] = aiCapabilityView{State: state, Reason: publicAIText(capability["reason"], 512)}
	}
	return out
}

func publicMapSlice(raw any) []map[string]any {
	switch values := raw.(type) {
	case []map[string]any:
		return values
	case []any:
		out := make([]map[string]any, 0, len(values))
		for _, value := range values {
			if item, ok := value.(map[string]any); ok {
				out = append(out, item)
			}
		}
		return out
	default:
		return nil
	}
}

func publicStringSlice(raw any, limit, maxLength int) []string {
	values := make([]string, 0)
	switch items := raw.(type) {
	case []string:
		values = append(values, items...)
	case []any:
		for _, value := range items {
			if text := publicAIText(value, maxLength); text != "" {
				values = append(values, text)
			}
		}
	}
	if len(values) > limit {
		values = values[:limit]
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if text := publicAIText(value, maxLength); text != "" {
			out = append(out, text)
		}
	}
	sort.Strings(out)
	return out
}

func publicAIText(raw any, maxLength int) string {
	value, _ := raw.(string)
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxLength {
		return ""
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"api_key", "api key", "apikey", "token", "bearer", "cookie", "orgid", "org_id", "authorization", "secret", "settings_config", "settingsconfig"} {
		if strings.Contains(lower, marker) {
			return ""
		}
	}
	if strings.Contains(value, "://") || strings.Contains(value, "@") || strings.Contains(value, `\`) || strings.Contains(value, ":/") || strings.HasPrefix(value, "/") {
		return ""
	}
	return value
}

func publicFingerprint(raw any) string {
	value, _ := raw.(string)
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return ""
	}
	for _, char := range value[len("sha256:"):] {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return ""
		}
	}
	return value
}

func publicEnum(raw any, allowed ...string) string {
	value, _ := raw.(string)
	for _, item := range allowed {
		if value == item {
			return value
		}
	}
	return ""
}

func publicRouteReason(raw any) string {
	return publicEnum(raw, "unsupported_schema", "database_unavailable", "route_inspection_failed")
}

func publicExecutionHealth(raw any) string {
	return publicEnum(raw, "healthy", "degraded", "unavailable", "unknown", "unknown_until_turn")
}

func publicErrorClass(raw any) string {
	return publicEnum(raw, "auth_failed", "rate_limited", "provider_unavailable", "network_failed", "invalid_model", "runtime_unavailable", "route_mismatch", "unknown")
}

func publicAIErrorMessage(class string) string {
	switch class {
	case "auth_failed":
		return "AI 提供方认证失败"
	case "rate_limited":
		return "AI 提供方已触发速率限制"
	case "provider_unavailable":
		return "AI 提供方当前不可用"
	case "network_failed":
		return "AI 提供方网络请求失败"
	case "invalid_model":
		return "所选 AI 模型不可用"
	case "runtime_unavailable":
		return "AI 运行时不可用"
	case "route_mismatch":
		return "AI 路由选择与当前上游不一致"
	case "unknown":
		return "AI 状态读取失败"
	default:
		return ""
	}
}

func applyReadError(view *aiProviderView, errs ...error) {
	for _, err := range errs {
		if err != nil {
			view.ErrorClass = "unknown"
			view.ErrorMessage = publicAIErrorMessage(view.ErrorClass)
			return
		}
	}
}

func publicInt(raw any, minimum, maximum int) int {
	value, ok := raw.(int)
	if !ok {
		if number, numberOK := raw.(float64); numberOK {
			value, ok = int(number), true
		}
	}
	if !ok || value < minimum || value > maximum {
		return minimum
	}
	return value
}
