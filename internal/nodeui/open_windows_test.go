//go:build windows

package nodeui

import (
	"strings"
	"testing"
)

func TestLocalUIUsesStableDefaultWindowSize(t *testing.T) {
	args := localUIEdgeArguments("http://127.0.0.1:17891/")
	if len(args) != 2 || args[0] != "--app=http://127.0.0.1:17891/" {
		t.Fatalf("unexpected Edge app arguments: %#v", args)
	}
	wantSize := "--window-size=1280,860"
	if !strings.Contains(args[1], wantSize) {
		t.Fatalf("window size argument=%q, want %q", args[1], wantSize)
	}
}
