//go:build !windows

package nodeui

import (
	"fmt"
	"os/exec"
	"runtime"
)

func openLocalUI(rawURL string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{rawURL}
	default:
		name, args = "xdg-open", []string{rawURL}
	}
	if err := exec.Command(name, args...).Start(); err != nil {
		return fmt.Errorf("open local UI: %w", err)
	}
	return nil
}
