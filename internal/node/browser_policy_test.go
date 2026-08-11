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

func TestSanitizeBrowserParamsAllowsRefsAndBoundedBatch(t *testing.T) {
	click, err := sanitizeBrowserParams("click", map[string]any{
		"browserSessionId": "brs_test",
		"pageId":           "pg_test",
		"ref":              "e_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if click["ref"] != "e_1" {
		t.Fatalf("click ref=%v", click["ref"])
	}

	batch, err := sanitizeBrowserParams("batch", map[string]any{
		"browserSessionId": "brs_test",
		"pageId":           "pg_test",
		"steps": []any{
			map[string]any{"action": "type", "ref": "e_1", "text": "Fast Spider"},
			map[string]any{"action": "click", "ref": "e_2"},
		},
		"snapshotAfter": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	steps, ok := batch["steps"].([]any)
	if !ok || len(steps) != 2 {
		t.Fatalf("batch steps=%T %+v", batch["steps"], batch["steps"])
	}
}

func TestSanitizeBrowserParamsRejectsBatchEscape(t *testing.T) {
	_, err := sanitizeBrowserParams("batch", map[string]any{
		"browserSessionId": "brs_test",
		"pageId":           "pg_test",
		"steps": []any{
			map[string]any{"action": "click", "ref": "e_1", "javascript": "alert(1)"},
		},
	})
	if err == nil {
		t.Fatal("batch accepted an unsupported nested parameter")
	}

	_, err = sanitizeBrowserParams("batch", map[string]any{
		"browserSessionId": "brs_test",
		"pageId":           "pg_test",
		"steps": []any{
			map[string]any{"action": "evaluate", "ref": "e_1"},
		},
	})
	if err == nil {
		t.Fatal("batch accepted an unsupported nested action")
	}
}
