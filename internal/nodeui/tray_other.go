//go:build !windows

package nodeui

import (
	"context"
	"log/slog"
)

func traySupported() bool { return false }

func startTray(ctx context.Context, onOpen, onExit func(), logger *slog.Logger) (func(), error) {
	return func() {}, nil
}
