//go:build windows

package nodeui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
			cmd := exec.Command(path, "--app="+rawURL)
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
