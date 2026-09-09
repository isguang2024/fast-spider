//go:build windows

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestMain(m *testing.M) {
	if os.Getenv("FAST_SPIDER_TEST_BACKGROUND_GIT") == "1" {
		handle, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
		if handle != 0 {
			fmt.Fprintln(os.Stderr, "background probe created a console window")
			os.Exit(2)
		}
		fmt.Println("background-probe-ok")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestAgentBackgroundProbesHaveNoConsole(t *testing.T) {
	bin := t.TempDir()
	executable, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	git := filepath.Join(bin, "git.exe")
	if err := os.WriteFile(git, executable, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAST_SPIDER_TEST_BACKGROUND_GIT", "1")
	output, err := runAgentGit(context.Background(), bin, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(output) != "background-probe-ok" {
		t.Fatalf("Git probe: output=%q err=%v", output, err)
	}
	cmd, err := codexCommand(context.Background(), git, "--version")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(raw)) != "background-probe-ok" {
		t.Fatalf("Codex probe: output=%q err=%v", raw, err)
	}
}
