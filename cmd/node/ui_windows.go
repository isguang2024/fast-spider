//go:build windows

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/isguang2024/fast-spider/internal/releaseinfo"
	"github.com/isguang2024/fast-spider/internal/version"
)

const createNoWindow = 0x08000000

func launchDefaultUI(logger *slog.Logger) {
	executable, err := ensureInstalledExecutable()
	if err != nil {
		logger.Warn("prepare stable Fast Spider executable failed; using current file", "error", err)
		executable, err = os.Executable()
		fatalIf(err)
	}
	cmd := exec.Command(executable, "ui")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if err := cmd.Start(); err != nil {
		logger.Warn("detached UI launch failed; running in foreground", "error", err)
		runUI(logger, nil)
	}
}

func ensureInstalledExecutable() (string, error) {
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
	target := filepath.Join(base, "FastSpider", "bin", "fast-spider-node.exe")
	target, err = filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if strings.EqualFold(filepath.Clean(current), filepath.Clean(target)) {
		return target, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", err
	}
	tmp := target + ".new"
	_ = os.Remove(tmp)
	input, err := os.Open(current)
	if err != nil {
		return "", err
	}
	output, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		input.Close()
		return "", err
	}
	_, copyErr := io.Copy(output, input)
	closeOutErr := output.Close()
	closeInErr := input.Close()
	if copyErr != nil || closeOutErr != nil || closeInErr != nil {
		_ = os.Remove(tmp)
		if copyErr != nil {
			return "", copyErr
		}
		if closeOutErr != nil {
			return "", closeOutErr
		}
		return "", closeInErr
	}
	if _, err := os.Stat(target); err == nil {
		if installedVersion, ok := executableVersion(target); ok {
			if comparison, compareErr := releaseinfo.Compare(installedVersion, version.Version); compareErr == nil && comparison >= 0 {
				_ = os.Remove(tmp)
				return target, nil
			}
		}
		if err := os.Remove(target); err != nil {
			_ = os.Remove(tmp)
			return target, nil
		}
	} else if !os.IsNotExist(err) {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return target, nil
}

func executableVersion(path string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", false
	}
	value := strings.TrimSpace(string(raw))
	if _, err := releaseinfo.ParseVersion(value); err != nil {
		return "", false
	}
	return value, true
}
