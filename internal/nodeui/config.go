package nodeui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const localConfigVersion = 5

const defaultHubURL = ""

type LocalConfig struct {
	Version                      int    `json:"version"`
	HubURL                       string `json:"hubUrl"`
	MachineName                  string `json:"machineName"`
	BrowserSidecarDir            string `json:"browserSidecarDir,omitempty"`
	LocalBridgeEnabled           bool   `json:"localBridgeEnabled"`
	AutoStartEnabled             bool   `json:"autoStartEnabled"`
	AutoUpdateEnabled            bool   `json:"autoUpdateEnabled"`
	AllowInsecureLocalHub        bool   `json:"allowInsecureLocalHub"`
	CodexDesktopBridgeEnabled    bool   `json:"codexDesktopBridgeEnabled"`
	CodexDesktopBridgeConfigured bool   `json:"codexDesktopBridgeConfigured"`
	ChatGPTDefaultCreateMode     string `json:"chatgptDefaultCreateMode"`
	ChatGPTDefaultModel          string `json:"chatgptDefaultModel"`
	ChatGPTDefaultThinking       string `json:"chatgptDefaultThinking"`
	WorkingProjectPath           string `json:"workingProjectPath,omitempty"`
	WorkingPlanID                string `json:"workingPlanId,omitempty"`
}

func defaultLocalConfig(machineName string) LocalConfig {
	return LocalConfig{
		Version:                      localConfigVersion,
		HubURL:                       defaultHubURL,
		MachineName:                  strings.TrimSpace(machineName),
		LocalBridgeEnabled:           true,
		CodexDesktopBridgeEnabled:    false,
		CodexDesktopBridgeConfigured: false,
		ChatGPTDefaultCreateMode:     "complete",
	}
}

func loadLocalConfig(dataDir, machineName string) (LocalConfig, error) {
	path := filepath.Join(dataDir, "config.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultLocalConfig(machineName), nil
	}
	if err != nil {
		return LocalConfig{}, fmt.Errorf("read local config: %w", err)
	}
	var cfg LocalConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return LocalConfig{}, fmt.Errorf("decode local config: %w", err)
	}
	if cfg.Version >= 1 && cfg.Version < localConfigVersion {
		cfg.Version = localConfigVersion
	} else if cfg.Version != localConfigVersion {
		return LocalConfig{}, fmt.Errorf("unsupported local config version %d", cfg.Version)
	}
	if strings.TrimSpace(cfg.HubURL) == "" {
		cfg.HubURL = defaultHubURL
	}
	if strings.TrimSpace(cfg.MachineName) == "" {
		cfg.MachineName = strings.TrimSpace(machineName)
	}
	if err := normalizeChatGPTDefaults(&cfg); err != nil {
		return LocalConfig{}, err
	}
	return cfg, nil
}

func saveLocalConfig(dataDir string, cfg LocalConfig) error {
	cfg.Version = localConfigVersion
	cfg.HubURL = strings.TrimSpace(cfg.HubURL)
	cfg.MachineName = strings.TrimSpace(cfg.MachineName)
	cfg.BrowserSidecarDir = strings.TrimSpace(cfg.BrowserSidecarDir)
	if err := normalizeChatGPTDefaults(&cfg); err != nil {
		return err
	}
	cfg.WorkingProjectPath = strings.TrimSpace(cfg.WorkingProjectPath)
	cfg.WorkingPlanID = strings.TrimSpace(cfg.WorkingPlanID)
	if len(cfg.HubURL) > 2048 || len(cfg.MachineName) > 128 || len(cfg.BrowserSidecarDir) > 4096 || len(cfg.ChatGPTDefaultModel) > 256 || len(cfg.WorkingProjectPath) > 4096 || len(cfg.WorkingPlanID) > 128 {
		return errors.New("local config field exceeds limit")
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dataDir, "config.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func normalizeChatGPTDefaults(cfg *LocalConfig) error {
	cfg.ChatGPTDefaultCreateMode = strings.ToLower(strings.TrimSpace(cfg.ChatGPTDefaultCreateMode))
	if cfg.ChatGPTDefaultCreateMode == "" {
		cfg.ChatGPTDefaultCreateMode = "complete"
	}
	if cfg.ChatGPTDefaultCreateMode != "complete" && cfg.ChatGPTDefaultCreateMode != "quick_chat" {
		return errors.New("ChatGPT Cloud 默认返回模式必须是 complete 或 quick_chat")
	}
	cfg.ChatGPTDefaultModel = strings.TrimSpace(cfg.ChatGPTDefaultModel)
	cfg.ChatGPTDefaultThinking = strings.ToLower(strings.TrimSpace(cfg.ChatGPTDefaultThinking))
	if cfg.ChatGPTDefaultThinking == "auto" {
		cfg.ChatGPTDefaultThinking = ""
	}
	if cfg.ChatGPTDefaultThinking != "" && !stringInLocalSet(cfg.ChatGPTDefaultThinking, "standard", "extended", "min", "max", "ultra", "xhigh", "zero") {
		return errors.New("ChatGPT Cloud 默认思考程度不是当前客户端支持的档位")
	}
	return nil
}

func stringInLocalSet(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}
