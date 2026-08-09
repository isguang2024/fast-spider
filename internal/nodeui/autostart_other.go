//go:build !windows

package nodeui

import "errors"

func autostartSupported() bool { return false }

func setAutostart(enabled bool, dataDir string) error {
	if enabled {
		return errors.New("automatic startup is currently supported on Windows only")
	}
	return nil
}

func autostartEnabled(dataDir string) (bool, error) { return false, nil }
