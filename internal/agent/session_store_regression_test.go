package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
)

func readClaudeIndexRaw(t *testing.T, path string) claudeSessionIndex {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var index claudeSessionIndex
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	return index
}

func TestClaudeCreateFailsClosedAtIndexCapacity(t *testing.T) {
	dataDir := t.TempDir()
	adapter := NewClaudeCodeAdapter(dataDir, nil, nil)
	record := &ClaudeSessionRecord{
		SessionID: "session-at-capacity", WorkingDirectory: dataDir, Status: "completed",
		CreatedAt: "2026-08-15T00:00:00Z", UpdatedAt: "2026-08-15T00:00:00Z",
	}
	record.LatestResult = "x"
	index := claudeSessionIndex{SchemaVersion: 1, Sessions: []*ClaudeSessionRecord{record}}
	raw, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	record.LatestResult = strings.Repeat("x", claudeCodeIndexMaxBytes-(len(raw)-1))
	adapter.sessions[record.SessionID] = record
	adapter.mu.Lock()
	if _, err := adapter.saveIndexLocked(); err != nil {
		adapter.mu.Unlock()
		t.Fatal(err)
	}
	adapter.mu.Unlock()

	if _, err := adapter.Create(context.Background(), dataDir, "hello", "", "", "", nil); err == nil || !strings.Contains(err.Error(), "byte capacity") {
		t.Fatalf("capacity create error=%v", err)
	}
	if len(adapter.sessions) != 1 {
		t.Fatalf("failed create changed in-memory session count: %d", len(adapter.sessions))
	}
	reloaded := NewClaudeCodeAdapter(dataDir, nil, nil)
	if len(reloaded.sessions) != 1 {
		t.Fatalf("failed create changed persisted session count: %d", len(reloaded.sessions))
	}
}

func TestClaudeVisibleMutationsRollbackOnPersistenceFailure(t *testing.T) {
	dataDir := t.TempDir()
	adapter := NewClaudeCodeAdapter(dataDir, nil, nil)
	record := &ClaudeSessionRecord{
		SessionID: "session-1", Name: "before", WorkingDirectory: dataDir, Status: "completed",
		CreatedAt: "2026-08-15T00:00:00Z", UpdatedAt: "2026-08-15T00:00:00Z",
	}
	adapter.sessions[record.SessionID] = record
	adapter.mu.Lock()
	if _, err := adapter.saveIndexLocked(); err != nil {
		adapter.mu.Unlock()
		t.Fatal(err)
	}
	adapter.mu.Unlock()

	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter.indexPath = filepath.Join(blocker, "claude-code-sessions.json")
	if _, err := adapter.Rename(record.SessionID, "after"); err == nil {
		t.Fatal("rename reported success when persistence failed")
	}
	if record.Name != "before" {
		t.Fatalf("rename was not rolled back: %#v", record)
	}
	if _, err := adapter.SetArchived(record.SessionID, true); err == nil {
		t.Fatal("archive reported success when persistence failed")
	}
	if record.Archived {
		t.Fatalf("archive was not rolled back: %#v", record)
	}
	if _, err := adapter.Delete(record.SessionID); err == nil {
		t.Fatal("delete reported success when persistence failed")
	}
	if adapter.sessions[record.SessionID] != record {
		t.Fatal("delete did not restore the in-memory session")
	}
}

func TestClaudeAsyncPersistenceFailureEmitsWarning(t *testing.T) {
	dataDir := t.TempDir()
	adapter := NewClaudeCodeAdapter(dataDir, nil, nil)
	record := &ClaudeSessionRecord{
		SessionID: "session-async", WorkingDirectory: dataDir, Status: "running",
		CreatedAt: "2026-08-15T00:00:00Z", UpdatedAt: "2026-08-15T00:00:00Z",
	}
	adapter.sessions[record.SessionID] = record
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter.indexPath = filepath.Join(blocker, "claude-code-sessions.json")
	adapter.handleStreamLine(record.SessionID, "turn-1", []byte(`{"type":"system","subtype":"init","model":"claude-test"}`))

	events, _, _, err := adapter.Watch(context.Background(), record.SessionID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "warning" && event.State == "persistence_failed" {
			return
		}
	}
	t.Fatalf("persistence failure warning was not emitted: %#v", events)
}

func TestClaudeDeleteProtectsActiveSessionAndNativeHistory(t *testing.T) {
	dataDir := t.TempDir()
	nativeHistory := filepath.Join(dataDir, "native-claude-history.jsonl")
	if err := os.WriteFile(nativeHistory, []byte("native history"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := NewClaudeCodeAdapter(dataDir, nil, nil)
	record := &ClaudeSessionRecord{
		SessionID: "session-active", WorkingDirectory: dataDir, Status: "running",
		CreatedAt: "2026-08-15T00:00:00Z", UpdatedAt: "2026-08-15T00:00:00Z",
	}
	adapter.sessions[record.SessionID] = record
	adapter.active[record.SessionID] = &claudeRun{turnID: "turn-1"}
	if _, err := adapter.Delete(record.SessionID); err == nil {
		t.Fatal("active Claude session was deleted")
	}
	delete(adapter.active, record.SessionID)
	if result, err := adapter.Delete(record.SessionID); err != nil || result["nativeHistoryPreserved"] != true {
		t.Fatalf("inactive delete result=%#v err=%v", result, err)
	}
	if raw, err := os.ReadFile(nativeHistory); err != nil || string(raw) != "native history" {
		t.Fatalf("native Claude history was modified: %q err=%v", raw, err)
	}
}

func TestClaudeIndexLoadFailureBlocksAllReplacement(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "corrupt", raw: []byte(`{"schemaVersion":1,"sessions":[`)},
		{name: "over limit", raw: []byte(strings.Repeat("x", claudeCodeIndexMaxBytes+1))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			indexPath := filepath.Join(dataDir, "agent", "claude-code-sessions.json")
			if err := os.MkdirAll(filepath.Dir(indexPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(indexPath, test.raw, 0o600); err != nil {
				t.Fatal(err)
			}
			adapter := NewClaudeCodeAdapter(dataDir, nil, nil)
			if adapter.indexLoadErr == nil {
				t.Fatal("invalid index unexpectedly loaded")
			}
			if _, err := adapter.Create(context.Background(), dataDir, "hello", "", "", "", nil); err == nil || !strings.Contains(err.Error(), "repair the existing index") {
				t.Fatalf("mutation error=%v", err)
			}
			after, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, test.raw) {
				t.Fatal("failed-closed mutation replaced the invalid index")
			}
		})
	}
}

func TestClaudeCommittedSaveFailureKeepsMemoryAlignedWithReplacedIndex(t *testing.T) {
	postCommitErr := errors.New("injected parent sync failure")
	seed := func(t *testing.T) (*ClaudeCodeAdapter, *ClaudeSessionRecord) {
		t.Helper()
		dataDir := t.TempDir()
		adapter := NewClaudeCodeAdapter(dataDir, nil, nil)
		record := &ClaudeSessionRecord{
			SessionID: "session-1", Name: "before", WorkingDirectory: dataDir, Status: "completed",
			CreatedAt: "2026-08-15T00:00:00Z", UpdatedAt: "2026-08-15T00:00:00Z",
		}
		adapter.sessions[record.SessionID] = record
		adapter.mu.Lock()
		if _, err := adapter.saveIndexLocked(); err != nil {
			adapter.mu.Unlock()
			t.Fatal(err)
		}
		adapter.mu.Unlock()
		adapter.syncParentOverride = func(string) error { return postCommitErr }
		return adapter, record
	}
	assertRaw := func(t *testing.T, adapter *ClaudeCodeAdapter, want []*ClaudeSessionRecord) {
		t.Helper()
		got := readClaudeIndexRaw(t, adapter.indexPath).Sessions
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("persisted sessions=%#v want=%#v", got, want)
		}
	}

	t.Run("create", func(t *testing.T) {
		dataDir := t.TempDir()
		adapter := NewClaudeCodeAdapter(dataDir, nil, nil)
		adapter.syncParentOverride = func(string) error { return postCommitErr }
		if _, err := adapter.Create(context.Background(), dataDir, "hello", "", "", "", nil); !errors.Is(err, postCommitErr) {
			t.Fatalf("create error=%v", err)
		}
		if len(adapter.sessions) != 1 {
			t.Fatalf("memory sessions=%d want=1", len(adapter.sessions))
		}
		assertRaw(t, adapter, []*ClaudeSessionRecord{func() *ClaudeSessionRecord {
			for _, record := range adapter.sessions {
				copy := *record
				return &copy
			}
			return nil
		}()})
	})

	t.Run("rename", func(t *testing.T) {
		adapter, record := seed(t)
		if _, err := adapter.Rename(record.SessionID, "after"); !errors.Is(err, postCommitErr) {
			t.Fatalf("rename error=%v", err)
		}
		if record.Name != "after" {
			t.Fatalf("memory rename rolled back after commit: %#v", record)
		}
		copy := *record
		assertRaw(t, adapter, []*ClaudeSessionRecord{&copy})
	})

	t.Run("archive", func(t *testing.T) {
		adapter, record := seed(t)
		if _, err := adapter.SetArchived(record.SessionID, true); !errors.Is(err, postCommitErr) {
			t.Fatalf("archive error=%v", err)
		}
		if !record.Archived {
			t.Fatal("memory archive rolled back after commit")
		}
		copy := *record
		assertRaw(t, adapter, []*ClaudeSessionRecord{&copy})
	})

	t.Run("delete", func(t *testing.T) {
		adapter, record := seed(t)
		if _, err := adapter.Delete(record.SessionID); !errors.Is(err, postCommitErr) {
			t.Fatalf("delete error=%v", err)
		}
		if len(adapter.sessions) != 0 {
			t.Fatal("memory delete rolled back after commit")
		}
		assertRaw(t, adapter, []*ClaudeSessionRecord{})
	})

	t.Run("turn start", func(t *testing.T) {
		adapter, record := seed(t)
		adapter.executable = os.Args[0]
		if _, err := adapter.startTurn(context.Background(), record, "hello", true, nil); !errors.Is(err, postCommitErr) {
			t.Fatalf("turn start error=%v", err)
		}
		if record.Status != "interrupted" || record.LatestTurnID == "" {
			t.Fatalf("memory turn state rolled back after commit: %#v", record)
		}
		reloaded := NewClaudeCodeAdapter(adapter.dataDir, nil, nil)
		if got := reloaded.sessions[record.SessionID]; !reflect.DeepEqual(got, record) {
			t.Fatalf("memory/reloaded turn state diverged: memory=%#v reloaded=%#v", record, got)
		}
	})
}

func TestClaudeConcurrentSendBusyLoserCannotOverwriteReservedTurn(t *testing.T) {
	dataDir := t.TempDir()
	adapter := NewClaudeCodeAdapter(dataDir, nil, nil)
	adapter.executable = os.Args[0]
	record := &ClaudeSessionRecord{
		SessionID: "session-concurrent", WorkingDirectory: dataDir, RequestedModel: "original", Status: "completed",
		CreatedAt: "2026-08-15T00:00:00Z", UpdatedAt: "2026-08-15T00:00:00Z",
	}
	adapter.sessions[record.SessionID] = record
	adapter.mu.Lock()
	if _, err := adapter.saveIndexLocked(); err != nil {
		adapter.mu.Unlock()
		t.Fatal(err)
	}
	adapter.mu.Unlock()

	loserPaused := make(chan struct{})
	releaseLoser := make(chan struct{})
	winnerReserved := make(chan struct{})
	releaseWinner := make(chan struct{})
	adapter.beforeSendStartOverride = func(_ string, model string) {
		if model == "loser-model" {
			close(loserPaused)
			<-releaseLoser
		}
	}
	adapter.afterTurnReservedOverride = func(_ string, model string) {
		if model == "winner-model" {
			close(winnerReserved)
			<-releaseWinner
		}
	}

	loserDone := make(chan error, 1)
	go func() {
		_, err := adapter.Send(context.Background(), record.SessionID, "loser", "", "loser-model", "", nil)
		loserDone <- err
	}()
	select {
	case <-loserPaused:
	case <-time.After(time.Second):
		t.Fatal("losing send did not reach the controlled interleaving")
	}
	winnerDone := make(chan error, 1)
	go func() {
		_, err := adapter.Send(context.Background(), record.SessionID, "winner", "", "winner-model", "", nil)
		winnerDone <- err
	}()
	select {
	case <-winnerReserved:
	case <-time.After(time.Second):
		t.Fatal("winning send did not reserve the session")
	}
	close(releaseLoser)
	if err := <-loserDone; !errors.Is(err, node.ErrAgentSessionBusy) {
		t.Fatalf("losing send error=%v want busy", err)
	}
	close(releaseWinner)
	if err := <-winnerDone; err != nil {
		t.Fatalf("winning send failed: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		adapter.mu.Lock()
		_, active := adapter.active[record.SessionID]
		adapter.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("winning test process did not exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	adapter.mu.Lock()
	gotModel := record.RequestedModel
	gotTurn := record.LatestTurnID
	adapter.mu.Unlock()
	if gotModel != "winner-model" || gotTurn == "" {
		t.Fatalf("busy loser overwrote winning turn: model=%q turn=%q", gotModel, gotTurn)
	}
}
