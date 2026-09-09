//go:build windows

package node

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestBackgroundCommandHasNoConsole(t *testing.T) {
	if os.Getenv("FAST_SPIDER_TEST_NO_CONSOLE") == "1" {
		handle, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
		if handle != 0 {
			t.Fatal("background child has a console window")
		}
		return
	}
	for _, existing := range []bool{false, true} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestBackgroundCommandHasNoConsole$")
		cmd.Env = append(os.Environ(), "FAST_SPIDER_TEST_NO_CONSOLE=1")
		want := uint32(windows.CREATE_NO_WINDOW)
		if existing {
			cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_UNICODE_ENVIRONMENT}
			want |= windows.CREATE_UNICODE_ENVIRONMENT
		}
		configureBackgroundCommand(cmd)
		if !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags != want {
			t.Fatalf("unexpected background attributes: %+v", cmd.SysProcAttr)
		}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("console probe: %v\n%s", err, output)
		}
	}
}

func TestConfigureProcessTreeNoWindow(t *testing.T) {
	for _, existing := range []bool{false, true} {
		cmd := exec.Command("cmd.exe", "/d", "/c", "more")
		if existing {
			cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_UNICODE_ENVIRONMENT}
		}
		configureProcessTree(cmd)
		want := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW)
		if existing {
			want |= windows.CREATE_UNICODE_ENVIRONMENT
		}
		if !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags != want {
			t.Fatalf("unexpected process attributes: %+v", cmd.SysProcAttr)
		}
		cmd.Stdin = strings.NewReader("background-pipe-ok\r\n")
		output, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != "background-pipe-ok" {
			t.Fatalf("background stdio: output=%q err=%v", output, err)
		}
	}
}
