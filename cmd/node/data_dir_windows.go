//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
)

func platformDefaultDataDir() string {
	if roaming, err := os.UserConfigDir(); err == nil {
		legacy := filepath.Join(roaming, "FastSpider", "node")
		if _, err := os.Stat(filepath.Join(legacy, "state.json")); err == nil {
			return legacy
		}
		if _, err := os.Stat(filepath.Join(legacy, "config.json")); err == nil {
			return legacy
		}
	}
	base := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if base == "" {
		if cache, err := os.UserCacheDir(); err == nil {
			base = cache
		}
	}
	if base == "" {
		return ".fast-spider-node"
	}
	return filepath.Join(base, "FastSpider", "node")
}
