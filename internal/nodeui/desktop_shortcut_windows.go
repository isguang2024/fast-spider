//go:build windows

package nodeui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	shortcutFileName = "Fast Spider Node"
	createNoWindow   = 0x08000000
)

// ensureDesktopShortcut creates the per-user shortcut only when it is
// missing. It intentionally uses the Windows Script Host COM shortcut API
// through a hidden PowerShell process so no extra console or UI is created.
func ensureDesktopShortcut(parent context.Context) error {
	executable, err := preferredNodeExecutable()
	if err != nil {
		return err
	}
	script := desktopShortcutScript(executable)
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
	cmd.Dir = filepath.Dir(executable)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if output, err := cmd.CombinedOutput(); err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return fmt.Errorf("create Desktop shortcut: %w", err)
		}
		return fmt.Errorf("create Desktop shortcut: %w: %s", err, message)
	}
	return nil
}

func preferredNodeExecutable() (string, error) {
	current, err := os.Executable()
	if err != nil {
		return "", err
	}
	current, err = filepath.Abs(current)
	if err != nil {
		return "", err
	}
	base := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if base == "" {
		base, err = os.UserCacheDir()
		if err != nil {
			return "", err
		}
	}
	installed := filepath.Join(base, "FastSpider", "bin", "fast-spider-node.exe")
	installed, err = filepath.Abs(installed)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(installed); statErr == nil && info.Mode().IsRegular() {
		return installed, nil
	}
	return current, nil
}

func desktopShortcutScript(executable string) string {
	executable = powershellSingleQuoted(executable)
	return "$desktop = [Environment]::GetFolderPath('Desktop'); " +
		"if ([string]::IsNullOrWhiteSpace($desktop)) { exit 2 }; " +
		"$shortcutPath = Join-Path -Path $desktop -ChildPath '" + shortcutFileName + ".lnk'; " +
		"if (-not (Test-Path -LiteralPath $shortcutPath)) { " +
		"$shell = New-Object -ComObject WScript.Shell; " +
		"$shortcut = $shell.CreateShortcut($shortcutPath); " +
		"$shortcut.TargetPath = '" + executable + "'; " +
		"$shortcut.Arguments = ''; " +
		"$shortcut.WorkingDirectory = Split-Path -Parent '" + executable + "'; " +
		"$shortcut.IconLocation = '" + executable + ",0'; " +
		"$shortcut.Description = 'Fast Spider Node'; " +
		"$shortcut.Save() }"
}

func powershellSingleQuoted(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}
