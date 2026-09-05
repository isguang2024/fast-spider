package nodeui

import (
	"errors"
	"net/http"
	"path/filepath"

	"github.com/isguang2024/fast-spider/internal/agent"
)

type chatGPTAdvancedResponse struct {
	Version         int                           `json:"version"`
	Models          []agent.ChatGPTAdvancedModel  `json:"models"`
	LiveModels      []map[string]any              `json:"liveModels"`
	ModelPresets    []map[string]any              `json:"modelPresets"`
	CreationModes   []map[string]any              `json:"creationModes"`
	DefaultModel    string                        `json:"defaultModel"`
	ThinkingOptions []agent.ChatGPTThinkingOption `json:"thinkingOptions"`
	ConfigFile      string                        `json:"configFile"`
}

func (a *App) handleChatGPTAdvancedModels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := agent.LoadChatGPTAdvancedConfig(a.opts.DataDir)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		catalog, options, err := a.currentChatGPTCatalog(r)
		if err != nil {
			writeChatGPTCatalogError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, buildChatGPTAdvancedResponse(a.opts.DataDir, cfg, catalog, options))
	case http.MethodPost:
		var cfg agent.ChatGPTAdvancedConfig
		if err := decodeJSON(r, &cfg); err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		catalog, options, err := a.currentChatGPTCatalog(r)
		if err != nil {
			writeChatGPTCatalogError(w, err)
			return
		}
		available := make(map[string]struct{}, len(options))
		for _, option := range options {
			available[option.ID] = struct{}{}
		}
		for _, model := range cfg.Models {
			for _, thinking := range model.Thinking {
				if _, ok := available[thinking]; !ok {
					writeAPIError(w, http.StatusBadRequest, errors.New("高级模型包含 ChatGPT Cloud 当前未提供的思考档位"))
					return
				}
			}
		}
		if err := agent.SaveChatGPTAdvancedConfig(a.opts.DataDir, cfg); err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		saved, err := agent.LoadChatGPTAdvancedConfig(a.opts.DataDir)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, buildChatGPTAdvancedResponse(a.opts.DataDir, saved, catalog, options))
	default:
		methodNotAllowed(w, r)
	}
}

func writeChatGPTCatalogError(w http.ResponseWriter, err error) {
	code := "CHATGPT_CLOUD_CATALOG_UNAVAILABLE"
	message := "无法读取 ChatGPT 模型配置，本次操作未保存。"
	var capabilityErr interface{ CapabilityError() (string, string, bool) }
	if errors.As(err, &capabilityErr) {
		candidate, _, _ := capabilityErr.CapabilityError()
		messages := map[string]string{
			"CHATGPT_CLOUD_NOT_AUTHENTICATED":       "未取得 ChatGPT 登录凭据，请在当前电脑的 Codex CLI 或客户端内置 Codex 中使用 ChatGPT 账号登录。",
			"CHATGPT_CLOUD_AUTH_RPC_TIMEOUT":        "读取 Codex 登录状态超时，请在当前任务结束后重启 Fast Spider 再试。",
			"CHATGPT_CLOUD_AUTH_RPC_FAILED":         "无法读取 Codex 登录状态，请检查 Codex CLI 或客户端内置 Codex 是否能正常启动。",
			"CHATGPT_CLOUD_NETWORK_TIMEOUT":         "连接 ChatGPT 模型接口超时。当前请求使用直连，请检查这台电脑访问 ChatGPT 的网络。",
			"CHATGPT_CLOUD_NETWORK_FAILED":          "无法连接 ChatGPT 模型接口。当前请求使用直连，请检查这台电脑的网络与证书。",
			"CHATGPT_CLOUD_MODELS_UNAUTHORIZED":     "ChatGPT 模型接口拒绝登录凭据（HTTP 401），请重新登录 ChatGPT 账号后再试。",
			"CHATGPT_CLOUD_MODELS_FORBIDDEN":        "ChatGPT 模型接口拒绝访问（HTTP 403），请检查当前账号与网络是否允许访问。",
			"CHATGPT_CLOUD_MODELS_RATE_LIMITED":     "ChatGPT 模型接口请求过于频繁（HTTP 429），请稍后重试。",
			"CHATGPT_CLOUD_MODELS_HTTP_ERROR":       "ChatGPT 模型接口返回服务错误，请稍后重试。",
			"CHATGPT_CLOUD_MODELS_INVALID_RESPONSE": "ChatGPT 模型接口返回了无法识别的数据，需要检查接口兼容性。",
		}
		if publicMessage, ok := messages[candidate]; ok {
			code, message = candidate, publicMessage+" 本次操作未保存。"
		}
	}
	writeJSON(w, http.StatusBadGateway, map[string]any{"error": message, "code": code})
}

func (a *App) currentChatGPTCatalog(r *http.Request) (map[string]any, []agent.ChatGPTThinkingOption, error) {
	if a.agentController == nil {
		return nil, nil, errors.New("agent controller unavailable")
	}
	catalog, err := a.agentController.Control(r.Context(), "models.list", map[string]any{
		"providerId": "codex", "backend": "chatgpt_cloud",
	})
	if err != nil {
		return nil, nil, err
	}
	options, ok := catalog["thinkingOptions"].([]agent.ChatGPTThinkingOption)
	if !ok || len(options) == 0 {
		return nil, nil, errors.New("ChatGPT Cloud thinking options unavailable")
	}
	return catalog, options, nil
}

func buildChatGPTAdvancedResponse(dataDir string, cfg agent.ChatGPTAdvancedConfig, catalog map[string]any, options []agent.ChatGPTThinkingOption) chatGPTAdvancedResponse {
	liveModels, _ := catalog["models"].([]map[string]any)
	modelPresets, _ := catalog["modelPresets"].([]map[string]any)
	creationModes, _ := catalog["creationModes"].([]map[string]any)
	defaultModel, _ := catalog["defaultModel"].(string)
	return chatGPTAdvancedResponse{
		Version: cfg.Version, Models: cfg.Models, LiveModels: liveModels, ModelPresets: modelPresets,
		CreationModes: creationModes, DefaultModel: defaultModel, ThinkingOptions: options,
		ConfigFile: filepath.Join(dataDir, agent.ChatGPTAdvancedConfigFileName),
	}
}
