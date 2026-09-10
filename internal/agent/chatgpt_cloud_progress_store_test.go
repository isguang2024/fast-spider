package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestChatGPTCloudProgressStoreAppendSnapshotRedactsAndBounds(t *testing.T) {
	store, err := newChatGPTCloudProgressStore(filepath.Join(t.TempDir(), "progress.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Append("conversation-1", "delta", `{"text":"safe","authorization":"secret","nested":{"cookie":"secret"}}`); err != nil {
		t.Fatal(err)
	}
	if err := store.Append("conversation-1", "done", "[DONE]"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < chatGPTCloudProgressMaxFrames+1; i++ {
		if err := store.Append("conversation-2", "delta", `{"n":1}`); err != nil {
			t.Fatal(err)
		}
	}
	rows, cursor, err := store.Snapshot("conversation-1", 0)
	if err != nil || len(rows) != 2 || cursor != 2 {
		t.Fatalf("snapshot=%#v cursor=%d err=%v", rows, cursor, err)
	}
	if strings.Contains(rows[0]["data"].(string), "secret") || rows[1]["data"] != "[DONE]" {
		t.Fatalf("unexpected sanitized rows=%#v", rows)
	}
	rows, cursor, err = store.Snapshot("conversation-2", 0)
	if err != nil || len(rows) != chatGPTCloudProgressMaxFrames || cursor <= int64(chatGPTCloudProgressMaxFrames) {
		t.Fatalf("bounded snapshot len=%d cursor=%d err=%v", len(rows), cursor, err)
	}
}

func TestChatGPTCloudProgressStoreRejectsUnknownRawAndOversizedData(t *testing.T) {
	store, err := newChatGPTCloudProgressStore(filepath.Join(t.TempDir(), "progress.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Append("conversation-1", "delta", "not-json"); err == nil {
		t.Fatal("unknown raw SSE data was accepted")
	}
	if err := store.Append("conversation-1", "delta", strings.Repeat("x", chatGPTCloudProgressMaxBytes)); err == nil {
		t.Fatal("oversized SSE data was accepted")
	}
	if err := store.Append("conversation-1", "delta", `{"token":"secret"}`); err != nil {
		t.Fatal(err)
	}
}

func TestChatGPTCloudProgressStoreAppendUniqueIsIdempotent(t *testing.T) {
	store, err := newChatGPTCloudProgressStore(filepath.Join(t.TempDir(), "progress.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	inserted, err := store.AppendUnique("conversation-1", "frame-1", "delta", `{"p":"x"}`)
	if err != nil || !inserted {
		t.Fatalf("first append inserted=%v err=%v", inserted, err)
	}
	inserted, err = store.AppendUnique("conversation-1", "frame-1", "delta", `{"p":"x"}`)
	if err != nil || inserted {
		t.Fatalf("replay inserted=%v err=%v", inserted, err)
	}
	rows, cursor, err := store.Snapshot("conversation-1", 0)
	if err != nil || len(rows) != 1 || cursor != 1 {
		t.Fatalf("snapshot=%#v cursor=%d err=%v", rows, cursor, err)
	}
}

func TestChatGPTCloudProgressStoreReceiptSurvivesFrameTrim(t *testing.T) {
	store, err := newChatGPTCloudProgressStore(filepath.Join(t.TempDir(), "progress.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i := 0; i < chatGPTCloudProgressMaxFrames+1; i++ {
		inserted, err := store.AppendUnique("conversation-1", "frame-"+fmt.Sprint(i), "delta", `{"n":1}`)
		if err != nil || !inserted {
			t.Fatalf("append %d inserted=%v err=%v", i, inserted, err)
		}
	}
	inserted, err := store.AppendUnique("conversation-1", "frame-0", "delta", `{"n":1}`)
	if err != nil || inserted {
		t.Fatalf("trimmed frame replay inserted=%v err=%v", inserted, err)
	}
}

func TestChatGPTCloudProgressStoreFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX mode bits")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "progress.sqlite3")
	store, err := newChatGPTCloudProgressStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode=%v err=%v", info.Mode().Perm(), err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode=%v err=%v", info.Mode().Perm(), err)
	}
}
