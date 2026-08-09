//go:build windows

package main

import (
	"log/slog"
	"os"
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

func launchDefaultUI(logger *slog.Logger) {
	executable, err := os.Executable()
	fatalIf(err)
	cmd := exec.Command(executable, "ui")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if err := cmd.Start(); err != nil {
		logger.Warn("detached UI launch failed; running in foreground", "error", err)
		runUI(logger, nil)
	}
}
