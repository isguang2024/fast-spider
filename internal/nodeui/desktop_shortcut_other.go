//go:build !windows

package nodeui

import "context"

func ensureDesktopShortcut(context.Context) error { return nil }
