package agent

import (
	"testing"
	"time"
)

func TestTTLCacheOverwriteAtCapacityDoesNotEvictAnotherKey(t *testing.T) {
	now := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	cache := newTTLCache[string](time.Minute, 2, nil)
	cache.now = func() time.Time { return now }
	cache.set("a", "first")
	now = now.Add(time.Second)
	cache.set("b", "second")
	now = now.Add(time.Second)
	cache.set("a", "refreshed")

	if got, ok := cache.get("b"); !ok || got != "second" {
		t.Fatalf("same-key overwrite evicted b: value=%q ok=%v", got, ok)
	}
	cache.set("c", "third")
	if _, ok := cache.get("b"); ok {
		t.Fatal("new key did not evict the oldest entry")
	}
	if got, ok := cache.get("a"); !ok || got != "refreshed" {
		t.Fatalf("refreshed entry was not retained: value=%q ok=%v", got, ok)
	}
}

func TestCloneAgentMapRecursivelyClonesAnySlices(t *testing.T) {
	original := map[string]any{
		"items": []any{
			map[string]any{"nested": []any{map[string]any{"value": "original"}}},
		},
	}
	cloned := cloneAgentMap(original)

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
