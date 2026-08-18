package operationlog

import "testing"

func TestQueryClampsNegativeOffset(t *testing.T) {
	store := &Store{
		entries: []Entry{
			{Timestamp: 1, Level: LevelInfo, Category: "http"},
			{Timestamp: 2, Level: LevelInfo, Category: "http"},
		},
	}

	entries, total := store.Query("", "", 10, -1)
	if total != 2 {
		t.Fatalf("total=%d, want 2", total)
	}
	if len(entries) != 2 {
		t.Fatalf("entries=%d, want 2", len(entries))
	}
}
