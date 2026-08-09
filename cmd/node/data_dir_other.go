//go:build !windows

package main

import (
	"os"
	"path/filepath"
)

func platformDefaultDataDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return ".fast-spider-node"
	}
	return filepath.Join(base, "FastSpider", "node")
}
