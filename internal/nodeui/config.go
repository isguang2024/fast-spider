package nodeui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	localConfigVersion = 2
	defaultHubURL      = "https://sharedservices.tibbs.app/fast-spider"
)

type LocalConfig struct {
	Version               int    `json:"version"`
	HubURL                string `json:"hubUrl"`
	MachineName           string `json:"machineName"`
	BrowserSidecarDir     string `json:"browserSidecarDir,omitempty"`
	LocalBridgeEnabled    bool   `json:"localBridgeEnabled"`
	AutoStartEnabled      bool   `json:"autoStartEnabled"`
	AutoUpdateEnabled     bool   `json:"autoUpdateEnabled"`
	AllowInsecureLocalHub bool   `json:"allowInsecureLocalHub"`
}

func defaultLocalConfig(machineName string) LocalConfig {
	return LocalConfig{
		Version:            localConfigVersion,
		HubURL:             defaultHubURL,
		MachineName:        strings.TrimSpace(machineName),
		LocalBridgeEnabled: true,
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
	if cfg.Version == 1 {
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
	return cfg, nil
}

func saveLocalConfig(dataDir string, cfg LocalConfig) error {
	cfg.Version = localConfigVersion
	cfg.HubURL = strings.TrimSpace(cfg.HubURL)
	cfg.MachineName = strings.TrimSpace(cfg.MachineName)
	cfg.BrowserSidecarDir = strings.TrimSpace(cfg.BrowserSidecarDir)
	if len(cfg.HubURL) > 2048 || len(cfg.MachineName) > 128 || len(cfg.BrowserSidecarDir) > 4096 {
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
