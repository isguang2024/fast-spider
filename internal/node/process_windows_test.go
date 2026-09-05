//go:build windows

package node

import (
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

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
