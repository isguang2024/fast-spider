//go:build windows

package nodeui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	defaultUIWindowWidth  = 1280
	defaultUIWindowHeight = 860
)

func openLocalUI(rawURL string) error {
	candidates := []string{}
	if path, err := exec.LookPath("msedge.exe"); err == nil {
		candidates = append(candidates, path)
	}
	for _, base := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA")} {
		if base == "" {
			continue
		}
		candidates = append(candidates, filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"))
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			cmd := exec.Command(path, localUIEdgeArguments(rawURL)...)
			if err := cmd.Start(); err == nil {
				return nil
			}
		}
	}
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", rawURL)
	if err := cmd.Start(); err != nil {
		return errors.New("unable to open local UI in Edge or the default browser")
	}
	return nil
}

func localUIEdgeArguments(rawURL string) []string {
	return []string{
		"--app=" + rawURL,
		fmt.Sprintf("--window-size=%d,%d", defaultUIWindowWidth, defaultUIWindowHeight),
	}
}

func openLocalFolder(path string) error {
	if err := exec.Command("explorer.exe", path).Start(); err != nil {
		return errors.New("unable to open the Markdown folder")
	}
	return nil
}
