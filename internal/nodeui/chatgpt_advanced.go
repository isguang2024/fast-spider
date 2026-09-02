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
		options, err := a.currentChatGPTThinkingOptions(r)
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, errors.New("无法从 ChatGPT Cloud 读取当前思考档位"))
			return
		}
		writeJSON(w, http.StatusOK, chatGPTAdvancedResponse{
			Version: cfg.Version, Models: cfg.Models, ThinkingOptions: options,
			ConfigFile: filepath.Join(a.opts.DataDir, agent.ChatGPTAdvancedConfigFileName),
		})
	case http.MethodPost:
		var cfg agent.ChatGPTAdvancedConfig
		if err := decodeJSON(r, &cfg); err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		options, err := a.currentChatGPTThinkingOptions(r)
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, errors.New("保存前无法确认 ChatGPT Cloud 当前思考档位"))
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
		writeJSON(w, http.StatusOK, chatGPTAdvancedResponse{
			Version: saved.Version, Models: saved.Models, ThinkingOptions: options,
			ConfigFile: filepath.Join(a.opts.DataDir, agent.ChatGPTAdvancedConfigFileName),
		})
	default:
		methodNotAllowed(w, r)
	}
}

func (a *App) currentChatGPTThinkingOptions(r *http.Request) ([]agent.ChatGPTThinkingOption, error) {
	if a.agentController == nil {
		return nil, errors.New("agent controller unavailable")
	}
	catalog, err := a.agentController.Control(r.Context(), "models.list", map[string]any{
		"providerId": "codex", "backend": "chatgpt_cloud",
	})
	if err != nil {
		return nil, err
	}
	options, ok := catalog["thinkingOptions"].([]agent.ChatGPTThinkingOption)
	if !ok || len(options) == 0 {
		return nil, errors.New("ChatGPT Cloud thinking options unavailable")
	}
	return options, nil
}
