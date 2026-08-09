//go:build windows

package nodeupdate

import (
	"os/exec"
	"testing"
	"time"
)

func TestWaitForProcessExitWaitsForRealPidTermination(t *testing.T) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", "Start-Sleep -Milliseconds 500")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := waitForProcessExit(cmd.Process.Pid, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 250*time.Millisecond {
		t.Fatalf("process wait returned too early: %v", elapsed)
	}
	_ = cmd.Wait()
}

func TestWaitForProcessExitRejectsInvalidPid(t *testing.T) {
	if err := waitForProcessExit(0, time.Second); err == nil {
		t.Fatal("invalid pid was accepted")
	}
}
