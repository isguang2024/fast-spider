package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	ChatGPTAdvancedConfigFileName = "chatgpt-advanced-models.json"
	chatGPTAdvancedConfigVersion  = 1
	maxChatGPTAdvancedModels      = 64
	maxChatGPTAdvancedThinking    = 16
)

var chatGPTThinkingIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type ChatGPTAdvancedConfig struct {
	Version int                    `json:"version"`
	Models  []ChatGPTAdvancedModel `json:"models"`
}

type ChatGPTAdvancedModel struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Thinking       []string `json:"thinking"`
	CustomThinking []string `json:"customThinking,omitempty"`
}

type ChatGPTThinkingOption struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

func DefaultChatGPTAdvancedConfig() ChatGPTAdvancedConfig {
	return ChatGPTAdvancedConfig{Version: chatGPTAdvancedConfigVersion, Models: []ChatGPTAdvancedModel{}}
}

func LoadChatGPTAdvancedConfig(dataDir string) (ChatGPTAdvancedConfig, error) {
	path := filepath.Join(dataDir, ChatGPTAdvancedConfigFileName)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultChatGPTAdvancedConfig(), nil
	}
	if err != nil {
		return ChatGPTAdvancedConfig{}, fmt.Errorf("read ChatGPT advanced model config: %w", err)
	}
	defer file.Close()
	var cfg ChatGPTAdvancedConfig
	decoder := json.NewDecoder(io.LimitReader(file, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return ChatGPTAdvancedConfig{}, fmt.Errorf("decode ChatGPT advanced model config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ChatGPTAdvancedConfig{}, errors.New("ChatGPT advanced model config must contain one JSON value")
	}
	if err := normalizeAndValidateChatGPTAdvancedConfig(&cfg); err != nil {
		return ChatGPTAdvancedConfig{}, err
	}
	return cfg, nil
}

func SaveChatGPTAdvancedConfig(dataDir string, cfg ChatGPTAdvancedConfig) error {
	if err := normalizeAndValidateChatGPTAdvancedConfig(&cfg); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > 32<<10 {
		return errors.New("ChatGPT advanced model config exceeds 32 KiB")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dataDir, ChatGPTAdvancedConfigFileName)
	temp, err := os.CreateTemp(dataDir, ".chatgpt-advanced-models-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceAgentFile(tempPath, path); err != nil {
		return err
	}
	return syncAgentParentDirectory(path)
}

func normalizeAndValidateChatGPTAdvancedConfig(cfg *ChatGPTAdvancedConfig) error {
	if cfg.Version == 0 {
		cfg.Version = chatGPTAdvancedConfigVersion
	}
	if cfg.Version != chatGPTAdvancedConfigVersion {
		return fmt.Errorf("unsupported ChatGPT advanced model config version %d", cfg.Version)
	}
	if len(cfg.Models) > maxChatGPTAdvancedModels {
		return fmt.Errorf("ChatGPT advanced model config supports at most %d models", maxChatGPTAdvancedModels)
	}
	seenModels := make(map[string]struct{}, len(cfg.Models))
	for index := range cfg.Models {
		model := &cfg.Models[index]
		model.ID = strings.TrimSpace(model.ID)
		model.Title = strings.TrimSpace(model.Title)
		if model.ID == "" || len(model.ID) > 256 {
			return fmt.Errorf("advanced model %d id must be 1-256 characters", index+1)
		}
		if model.Title == "" || len(model.Title) > 128 {
			return fmt.Errorf("advanced model %q title must be 1-128 characters", model.ID)
		}
		if _, exists := seenModels[model.ID]; exists {
			return fmt.Errorf("duplicate advanced model id %q", model.ID)
		}
		seenModels[model.ID] = struct{}{}
		if len(model.Thinking)+len(model.CustomThinking) > maxChatGPTAdvancedThinking {
			return fmt.Errorf("advanced model %q has too many thinking options", model.ID)
		}
		seenThinking := make(map[string]struct{}, len(model.Thinking))
		for thinkingIndex, thinking := range model.Thinking {
			thinking = strings.ToLower(strings.TrimSpace(thinking))
			if !chatGPTThinkingIDPattern.MatchString(thinking) {
				return fmt.Errorf("advanced model %q has invalid thinking option %q", model.ID, thinking)
			}
			if _, exists := seenThinking[thinking]; exists {
				return fmt.Errorf("advanced model %q repeats thinking option %q", model.ID, thinking)
			}
			seenThinking[thinking] = struct{}{}
			model.Thinking[thinkingIndex] = thinking
		}
		for thinkingIndex, thinking := range model.CustomThinking {
			thinking = strings.ToLower(strings.TrimSpace(thinking))
			if !IsValidChatGPTThinkingValue(thinking) || thinking == "auto" {
				return fmt.Errorf("advanced model %q has invalid custom thinking option %q", model.ID, thinking)
			}
			if _, exists := seenThinking[thinking]; exists {
				return fmt.Errorf("advanced model %q repeats thinking option %q", model.ID, thinking)
			}
			seenThinking[thinking] = struct{}{}
			model.CustomThinking[thinkingIndex] = thinking
		}
	}
	if cfg.Models == nil {
		cfg.Models = []ChatGPTAdvancedModel{}
	}
	return nil
}

// IsValidChatGPTThinkingValue reports whether a thinking value is safe to use
// as a ChatGPT Cloud effort identifier. The provider still decides whether a
// custom value is supported by the selected model.
func IsValidChatGPTThinkingValue(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return chatGPTThinkingIDPattern.MatchString(value)
}

func chatGPTThinkingOptions(presets []map[string]any) []ChatGPTThinkingOption {
	options := []ChatGPTThinkingOption{{ID: "auto", Title: "Auto", Value: "", Source: "local_default"}}
	seen := map[string]struct{}{"auto": {}}
	for _, preset := range presets {
		value := strings.ToLower(strings.TrimSpace(mapString(preset, "thinking")))
		if value == "" || !chatGPTThinkingIDPattern.MatchString(value) {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		options = append(options, ChatGPTThinkingOption{
			ID: value, Title: firstNonEmptyString(mapString(preset, "title"), value), Value: value, Source: "chatgpt_cloud",
		})
	}
	return options
}

func filterChatGPTAdvancedModels(models []ChatGPTAdvancedModel, options []ChatGPTThinkingOption) []ChatGPTAdvancedModel {
	available := make(map[string]struct{}, len(options))
	for _, option := range options {
		available[option.ID] = struct{}{}
	}
	filtered := make([]ChatGPTAdvancedModel, len(models))
	for index, model := range models {
		filtered[index] = model
		filtered[index].Title = chatgptCloudModelDisplayTitle(model.ID, model.Title)
		filtered[index].Thinking = make([]string, 0, len(model.Thinking))
		for _, thinking := range model.Thinking {
			if _, ok := available[thinking]; ok {
				filtered[index].Thinking = append(filtered[index].Thinking, thinking)
			}
		}
		filtered[index].CustomThinking = append([]string(nil), model.CustomThinking...)
	}
	return filtered
}
