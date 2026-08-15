package routing

import (
	"fmt"
	"testing"
	"time"
)

func TestRouteCacheOverwriteAtCapacityDoesNotEvictAnotherKey(t *testing.T) {
	now := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	inspector := New(Config{RouteTTL: time.Minute, Now: func() time.Time { return now }})
	for index := 0; index < maxRouteCacheKeys; index++ {
		inspector.store(fmt.Sprintf("app-%d", index), map[string]any{"value": index})
		now = now.Add(time.Second)
	}
	inspector.store("app-0", map[string]any{"value": "refreshed"})

	if value, ok := inspector.cached("app-1"); !ok || value["value"] != 1 {
		t.Fatalf("same-key overwrite evicted app-1: value=%#v ok=%v", value, ok)
	}
	inspector.store("new-app", map[string]any{"value": "new"})
	if _, ok := inspector.cached("app-1"); ok {
		t.Fatal("new key did not evict the oldest route entry")
	}
	if value, ok := inspector.cached("app-0"); !ok || value["value"] != "refreshed" {
		t.Fatalf("refreshed route entry was not retained: value=%#v ok=%v", value, ok)
	}
}

func TestCloneMapRecursivelyClonesAnySlices(t *testing.T) {
	original := map[string]any{
		"items": []any{map[string]any{"nested": []any{map[string]any{"value": "original"}}}},
	}
	cloned := cloneMap(original)
	originalNested := original["items"].([]any)[0].(map[string]any)["nested"].([]any)[0].(map[string]any)
	originalNested["value"] = "changed-input"
	clonedNested := cloned["items"].([]any)[0].(map[string]any)["nested"].([]any)[0].(map[string]any)
	if clonedNested["value"] != "original" {
		t.Fatalf("clone shared nested input state: %#v", cloned)
	}
	clonedNested["value"] = "changed-output"
	if originalNested["value"] != "changed-input" {
		t.Fatalf("input shared nested output state: %#v", original)
	}
}
