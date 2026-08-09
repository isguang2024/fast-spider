//go:build !windows

package main

import "log/slog"

func launchDefaultUI(logger *slog.Logger) {
	runUI(logger, nil)
}
