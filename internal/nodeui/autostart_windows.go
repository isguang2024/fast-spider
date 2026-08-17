//go:build windows

package nodeui

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const autostartValueName = "FastSpiderNode"

func autostartSupported() bool { return true }

func setAutostart(enabled bool, dataDir string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if !enabled {
		if err := key.DeleteValue(autostartValueName); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	command, err := expectedAutostartCommand(dataDir)
	if err != nil {
		return err
	}
	return key.SetStringValue(autostartValueName, command)
}

func autostartEnabled(dataDir string) (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE)
	if err == registry.ErrNotExist {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer key.Close()
	actual, _, err := key.GetStringValue(autostartValueName)
	if err == registry.ErrNotExist {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	expected, err := expectedAutostartCommand(dataDir)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(actual), strings.TrimSpace(expected)), nil
}

func expectedAutostartCommand(dataDir string) (string, error) {
	executable, err := preferredNodeExecutable()
	if err != nil {
		return "", err
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(executable, "\r\n\"") || strings.ContainsAny(dataDir, "\r\n\"") {
		return "", fmt.Errorf("autostart path contains unsupported characters")
	}
	return `"` + executable + `" ui --background --data-dir "` + dataDir + `"`, nil
}
