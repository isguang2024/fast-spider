package agent

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestDesktopCallbackWakeRealE2E(t *testing.T) {
	target := os.Getenv("FAST_SPIDER_CALLBACK_WAKE_SESSION")
	if target == "" {
		t.Skip("explicit callback wake target required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	result, err := startDesktopCallbackTurn(ctx, target, []map[string]any{{"type": "text", "text": "FS_WAKE_E2E_IPC_DIAGNOSTIC: This is an authorized Fast Spider callback transport test, not a business result. Please report receipt to task 01a06dd4-be6a-74a2-be86-72f4302b7f78 using the native task messaging tool, then end this turn. No business submissions should be replayed and no callbacks should be manually claimed for this transport-only message."}}, dialCodexDesktopIPC)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("desktop callback confirmed turn=%s", mapNestedString(result, "turn", "id"))
}
