//go:build windows

package nodeui

import (
	"strings"
	"testing"
)

func TestWindowsAutostartUsesSameNodeInHiddenTrayMode(t *testing.T) {
	if !traySupported() {
		t.Fatal("Windows tray support is disabled")
	}
	command, err := expectedAutostartCommand(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, ` ui --background --data-dir `) {
		t.Fatalf("autostart does not use hidden tray mode: %q", command)
	}
}
