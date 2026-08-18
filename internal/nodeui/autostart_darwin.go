//go:build darwin

package nodeui

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const launchAgentLabel = "app.fastspider.node"

func autostartSupported() bool { return true }

func setAutostart(enabled bool, dataDir string) error {
	path, err := launchAgentPath()
	if err != nil {
		return err
	}
	if !enabled {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	dataDir, err = filepath.Abs(strings.TrimSpace(dataDir))
	if err != nil || dataDir == "" {
		return errors.New("Node data directory is invalid")
	}
	quote := func(value string) string {
		var escaped strings.Builder
		_ = xml.EscapeText(&escaped, []byte(value))
		return escaped.String()
	}
	content := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
		"<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n" +
		"<plist version=\"1.0\"><dict>" +
		"<key>Label</key><string>" + quote(launchAgentLabel) + "</string>" +
		"<key>ProgramArguments</key><array><string>" + quote(executable) + "</string><string>ui</string><string>--background</string><string>--data-dir</string><string>" + quote(dataDir) + "</string></array>" +
		"<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>" +
		"</dict></plist>\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".fast-spider-launch-agent-*.plist")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func autostartEnabled(_ string) (bool, error) {
	path, err := launchAgentPath()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", errors.New("macOS home directory is unavailable")
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist"), nil
}
