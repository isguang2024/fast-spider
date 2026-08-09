package node

import (
	"context"
	"testing"
)

func TestBrowserManagerUsesMachineBoundary(t *testing.T) {
	manager := NewBrowserManager(t.TempDir(), t.TempDir(), nil)
	if _, err := manager.Execute(context.Background(), "unsupported", map[string]any{}); err == nil {
		t.Fatal("unsupported browser action unexpectedly succeeded")
	}
}
