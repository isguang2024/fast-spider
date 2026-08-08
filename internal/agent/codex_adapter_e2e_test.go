//go:build codexe2e

package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCodexAdapterRealE2E(t *testing.T) {
	if os.Getenv("FAST_SPIDER_CODEX_E2E") != "1" {
		t.Skip("set FAST_SPIDER_CODEX_E2E=1 to run the real local Codex app-server test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	adapter := NewCodexAdapter(nil)
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		_ = adapter.Close(closeCtx)
	}()

	if version, err := adapter.Availability(ctx); err != nil || version == "" {
		t.Fatalf("Codex availability version=%q err=%v", version, err)
	}
	models, err := adapter.ListModels(ctx)
	if err != nil {
		t.Fatalf("model/list: %v", err)
	}
	items, ok := models["data"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("model/list result has no usable data field: %#v", models)
	}
	firstModel, _ := items[0].(map[string]any)
	modelID := mapString(firstModel, "id")
	if modelID == "" {
		modelID = mapString(firstModel, "model")
	}
	if modelID == "" {
		t.Fatalf("model/list first item has no model ID: %#v", firstModel)
	}

	root := t.TempDir()
	threadResult, err := adapter.StartThread(ctx, root, modelID, "")
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	sessionID := mapNestedString(threadResult, "thread", "id")
	if sessionID == "" {
		t.Fatalf("thread/start returned no session ID: %#v", threadResult)
	}
	defer func() {
		archiveCtx, archiveCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer archiveCancel()
		_ = adapter.ArchiveThread(archiveCtx, sessionID)
	}()

	preTurnRead, err := adapter.ReadThread(ctx, sessionID)
	if err != nil {
		t.Fatalf("thread/read before first turn: %v", err)
	}
	preTurnThread, _ := preTurnRead["thread"].(map[string]any)
	if mapString(preTurnThread, "id") != sessionID {
		t.Fatalf("metadata-only thread/read returned wrong session: %#v", preTurnRead)
	}

	turnResult, err := adapter.StartTurn(ctx, sessionID, "只回复 OK，不调用任何工具。", root, modelID, "")
	if err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	turnID := mapNestedString(turnResult, "turn", "id")
	if turnID == "" {
		t.Fatalf("turn/start returned no turn ID: %#v", turnResult)
	}

	var final string
	for ctx.Err() == nil {
		threadRead, readErr := adapter.ReadThread(ctx, sessionID)
		if readErr != nil {
			t.Fatalf("thread/read: %v", readErr)
		}
		thread, _ := threadRead["thread"].(map[string]any)
		result := normalizeCodexResult(thread)
		status, _ := result["status"].(string)
		final, _ = result["finalAgentMessage"].(string)
		if status == "completed" {
			break
		}
		if status == "failed" || status == "canceled" {
			t.Fatalf("turn %s ended with status %s: %#v", turnID, status, result)
		}
		timer := time.NewTimer(300 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	if final == "" {
		events, _, _, watchErr := adapter.Watch(ctx, sessionID, 0, 0)
		if watchErr != nil {
			t.Fatalf("session watch: %v", watchErr)
		}
		for i := len(events) - 1; i >= 0; i-- {
			if strings.HasPrefix(events[i].Type, "assistant") && events[i].Text != "" {
				final = events[i].Text
				break
			}
			if events[i].Type == "error" && events[i].Text != "" {
				t.Fatalf("Codex emitted error event: %s", events[i].Text)
			}
		}
	}
	if !strings.Contains(strings.ToUpper(final), "OK") {
		t.Fatalf("unexpected final Codex message %q", final)
	}
}
