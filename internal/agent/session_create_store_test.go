package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type testCapabilityError interface {
	CapabilityError() (string, string, bool)
}

func TestSessionCreateStoreRejectsSemanticIndexCorruption(t *testing.T) {
	validHash := sessionCreateSpecHash("valid")
	tests := []struct {
		name    string
		records []sessionCreateRecord
	}{
		{name: "empty key", records: []sessionCreateRecord{{SpecHash: validHash, State: "in_doubt", UpdatedAt: time.Now().UTC()}}},
		{name: "invalid hash", records: []sessionCreateRecord{{Key: "create-invalid-hash", SpecHash: "bad", State: "in_doubt", UpdatedAt: time.Now().UTC()}}},
		{name: "invalid state", records: []sessionCreateRecord{{Key: "create-invalid-state", SpecHash: validHash, State: "failed", UpdatedAt: time.Now().UTC()}}},
		{name: "success without result", records: []sessionCreateRecord{{Key: "create-no-result-01", SpecHash: validHash, State: "succeeded", UpdatedAt: time.Now().UTC()}}},
		{name: "duplicate key", records: []sessionCreateRecord{
			{Key: "create-duplicate-01", SpecHash: validHash, State: "in_doubt", UpdatedAt: time.Now().UTC()},
			{Key: "create-duplicate-01", SpecHash: validHash, State: "in_doubt", UpdatedAt: time.Now().UTC()},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			path := filepath.Join(dataDir, "agent", "session-create-idempotency.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(sessionCreateIndex{SchemaVersion: 1, Records: test.records})
			if err != nil || os.WriteFile(path, raw, 0o600) != nil {
				t.Fatalf("write fixture: %v", err)
			}
			store := newSessionCreateStore(dataDir)
			_, _, err = store.begin("create-after-corrupt", validHash)
			var capabilityErr testCapabilityError
			if !errors.As(err, &capabilityErr) {
				t.Fatalf("error=%T %v", err, err)
			}
			code, _, _ := capabilityErr.CapabilityError()
			if code != "AGENT_IDEMPOTENCY_STORE_UNAVAILABLE" {
				t.Fatalf("code=%s", code)
			}
		})
	}
}

func TestSessionCreateStoreReclaimsRecordsWhenSessionIsDeleted(t *testing.T) {
	store := newSessionCreateStore(t.TempDir())
	for index := 0; index < maxSessionCreateRecords; index++ {
		key := fmt.Sprintf("create-capacity-%04d", index)
		store.records[key] = sessionCreateRecord{
			Key: key, SpecHash: sessionCreateSpecHash(index), State: "succeeded",
			Result: map[string]any{"sessionId": fmt.Sprintf("session-%04d", index)}, UpdatedAt: time.Now().UTC(),
		}
	}
	if _, _, err := store.begin("create-over-capacity", sessionCreateSpecHash("new")); err == nil {
		t.Fatal("full store accepted a new record")
	}
	if prepared, err := store.prepareSessionDelete("session-0000"); err != nil || !prepared {
		t.Fatalf("prepare delete prepared=%v err=%v", prepared, err)
	}
	if err := store.finalizeSessionDelete("session-0000"); err != nil {
		t.Fatal(err)
	}
	if _, replayed, err := store.begin("create-after-delete", sessionCreateSpecHash("new")); err != nil || replayed {
		t.Fatalf("begin after delete replayed=%v err=%v", replayed, err)
	}
}

func TestSessionCreateStoreReleasesOnlyConfirmedUnresolvedKey(t *testing.T) {
	store := newSessionCreateStore(t.TempDir())
	for index := 0; index < maxSessionCreateRecords; index++ {
		key := fmt.Sprintf("create-unresolved-%04d", index)
		store.records[key] = sessionCreateRecord{
			Key: key, SpecHash: sessionCreateSpecHash(index), State: "in_doubt", UpdatedAt: time.Now().UTC(),
		}
	}
	if _, _, err := store.begin("create-over-capacity", sessionCreateSpecHash("new")); err == nil {
		t.Fatal("full unresolved store accepted a new record")
	}
	released, err := store.releaseUnresolved("create-unresolved-0000")
	if err != nil || !released {
		t.Fatalf("release unresolved released=%v err=%v", released, err)
	}
	if _, replayed, err := store.begin("create-after-resolution", sessionCreateSpecHash("new")); err != nil || replayed {
		t.Fatalf("begin after resolution replayed=%v err=%v", replayed, err)
	}
	store.records["known-session"] = sessionCreateRecord{
		Key: "known-session", SpecHash: sessionCreateSpecHash("known"), State: "in_doubt",
		Result: map[string]any{"sessionId": "session-known"}, UpdatedAt: time.Now().UTC(),
	}
	if _, err := store.releaseUnresolved("known-session"); err == nil {
		t.Fatal("released an unresolved record that already identifies a session")
	}
}

func TestSessionCreateStoreDeleteIntentSurvivesFinalizeFailure(t *testing.T) {
	dataDir := t.TempDir()
	store := newSessionCreateStore(dataDir)
	key := "create-delete-retry-01"
	if _, _, err := store.begin(key, sessionCreateSpecHash("delete")); err != nil {
		t.Fatal(err)
	}
	if err := store.update(key, "succeeded", map[string]any{"sessionId": "session-delete-retry"}); err != nil {
		t.Fatal(err)
	}
	prepared, err := store.prepareSessionDelete("session-delete-retry")
	if err != nil || !prepared || store.records[key].State != "deleting" {
		t.Fatalf("prepare delete prepared=%v state=%q err=%v", prepared, store.records[key].State, err)
	}
	originalPath := store.path
	blockedPath := filepath.Join(t.TempDir(), "blocked")
	if err := os.Mkdir(blockedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	store.path = blockedPath
	if err := store.finalizeSessionDelete("session-delete-retry"); err == nil {
		t.Fatal("finalize unexpectedly succeeded with a directory as its destination")
	}
	if store.records[key].State != "deleting" {
		t.Fatalf("delete intent was not restored after persistence failure: %#v", store.records[key])
	}
	store.path = originalPath
	if prepared, err := store.prepareSessionDelete("session-delete-retry"); err != nil || !prepared {
		t.Fatalf("retry prepare prepared=%v err=%v", prepared, err)
	}
	if err := store.finalizeSessionDelete("session-delete-retry"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.records[key]; ok {
		t.Fatal("delete intent remained after retry completed")
	}
}

func TestSessionCreateStoreAbortOnlyRemovesReservation(t *testing.T) {
	store := newSessionCreateStore(t.TempDir())
	if _, _, err := store.begin("create-abort-0001", sessionCreateSpecHash("abort")); err != nil {
		t.Fatal(err)
	}
	if err := store.abort("create-abort-0001"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.records["create-abort-0001"]; ok {
		t.Fatal("aborted reservation remains")
	}
	store.records["create-success-0001"] = sessionCreateRecord{
		Key: "create-success-0001", SpecHash: sessionCreateSpecHash("success"), State: "succeeded",
		Result: map[string]any{"sessionId": "session-success"}, UpdatedAt: time.Now().UTC(),
	}
	if err := store.abort("create-success-0001"); err == nil {
		t.Fatal("abort removed a completed record")
	}
}

func TestAgentControlRequiresExplicitReconciliationToReleaseUnresolvedCreate(t *testing.T) {
	manager := New(t.TempDir(), nil)
	key := "create-reconcile-0001"
	storeKey := "codex:" + key
	manager.createStore.records[storeKey] = sessionCreateRecord{
		Key: storeKey, SpecHash: sessionCreateSpecHash("unknown"), State: "in_doubt", UpdatedAt: time.Now().UTC(),
	}
	params := map[string]any{"idempotencyKey": key}
	if _, err := manager.Control(context.Background(), "session.delete", params); err == nil {
		t.Fatal("unconfirmed unresolved create was released")
	}
	if _, ok := manager.createStore.records[storeKey]; !ok {
		t.Fatal("unconfirmed unresolved record was removed")
	}
	params["decision"] = "confirm_not_created"
	result, err := manager.Control(context.Background(), "session.delete", params)
	if err != nil {
		t.Fatal(err)
	}
	if result["idempotencyReservationReleased"] != true || result["resolution"] != "not_created" {
		t.Fatalf("unexpected resolution result: %#v", result)
	}
	if _, ok := manager.createStore.records[storeKey]; ok {
		t.Fatal("confirmed unresolved record remains")
	}
}

func TestSessionCreateStoreFailsClosedWhenIndexCannotBeLoaded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(string) error
	}{
		{name: "corrupt", setup: func(path string) error { return os.WriteFile(path, []byte("{truncated"), 0o600) }},
		{name: "unreadable entry", setup: func(path string) error { return os.Mkdir(path, 0o700) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			path := filepath.Join(dataDir, "agent", "session-create-idempotency.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := tc.setup(path); err != nil {
				t.Fatal(err)
			}
			store := newSessionCreateStore(dataDir)
			_, _, err := store.begin("create-fail-closed-01", sessionCreateSpecHash("same"))
			var capabilityErr testCapabilityError
			if !errors.As(err, &capabilityErr) {
				t.Fatalf("error=%T %v", err, err)
			}
			code, _, retryable := capabilityErr.CapabilityError()
			if code != "AGENT_IDEMPOTENCY_STORE_UNAVAILABLE" || retryable {
				t.Fatalf("code=%s retryable=%v", code, retryable)
			}
		})
	}
}

func TestSessionCreateStoreNeverExpiresSucceededOrInDoubtRecords(t *testing.T) {
	store := newSessionCreateStore(t.TempDir())
	old := time.Now().UTC().Add(-365 * 24 * time.Hour)
	successHash, doubtHash := sessionCreateSpecHash("success"), sessionCreateSpecHash("doubt")
	for _, record := range []sessionCreateRecord{
		{Key: "old-success-0001", SpecHash: successHash, State: "succeeded", Result: map[string]any{"sessionId": "session-old"}, UpdatedAt: old},
		{Key: "old-doubt-00001", SpecHash: doubtHash, State: "in_doubt", UpdatedAt: old},
	} {
		store.records[record.Key] = record
	}
	if _, err := store.saveLocked(); err != nil {
		t.Fatal(err)
	}
	reloaded := newSessionCreateStore(filepath.Dir(filepath.Dir(store.path)))
	if got, replayed, err := reloaded.begin("old-success-0001", successHash); err != nil || !replayed || got["sessionId"] != "session-old" {
		t.Fatalf("old success got=%v replayed=%v err=%v", got, replayed, err)
	}
	if _, _, err := reloaded.begin("old-doubt-00001", doubtHash); err == nil {
		t.Fatal("old in-doubt record expired and allowed a duplicate create")
	}
}

func TestSessionCreateStorePersistsReplayAndRejectsConflicts(t *testing.T) {
	dataDir := t.TempDir()
	store := newSessionCreateStore(dataDir)
	key := "create-idem-0001"
	hash := sessionCreateSpecHash(map[string]any{"cwd": "example", "model": "test"})
	if _, replayed, err := store.begin(key, hash); err != nil || replayed {
		t.Fatalf("begin replayed=%v err=%v", replayed, err)
	}
	result := map[string]any{"sessionId": "session-one", "turnId": "turn-one"}
	if err := store.update(key, "succeeded", result); err != nil {
		t.Fatal(err)
	}
	reloaded := newSessionCreateStore(dataDir)
	got, replayed, err := reloaded.begin(key, hash)
	if err != nil || !replayed || got["sessionId"] != "session-one" {
		t.Fatalf("replay=%v replayed=%v err=%v", got, replayed, err)
	}
	if _, _, err := reloaded.begin(key, sessionCreateSpecHash("different")); err == nil {
		t.Fatal("different spec reused the idempotency key")
	} else {
		var capabilityErr testCapabilityError
		if !errors.As(err, &capabilityErr) {
			t.Fatalf("conflict error=%T %v", err, err)
		}
		code, _, _ := capabilityErr.CapabilityError()
		if code != "IDEMPOTENCY_CONFLICT" {
			t.Fatalf("conflict code=%s", code)
		}
	}
}

func TestSessionCreateStoreUpdateRestoresMemoryAfterPersistenceFailure(t *testing.T) {
	dataDir := t.TempDir()
	store := newSessionCreateStore(dataDir)
	key := "create-update-rollback"
	hash := sessionCreateSpecHash("rollback")
	result := map[string]any{"sessionId": "session-rollback"}
	if _, _, err := store.begin(key, hash); err != nil {
		t.Fatal(err)
	}
	if err := store.update(key, "in_doubt", result); err != nil {
		t.Fatal(err)
	}
	persistErr := errors.New("injected persistence failure")
	store.beforeCommitSaveOverride = func() error { return persistErr }
	if err := store.update(key, "succeeded", result); !errors.Is(err, persistErr) {
		t.Fatalf("update error=%v want injected persistence failure", err)
	}
	current := store.records[key]
	if current.State != "in_doubt" || current.Result["sessionId"] != "session-rollback" {
		t.Fatalf("memory record was not restored: %#v", current)
	}
	reloaded := newSessionCreateStore(dataDir)
	persisted := reloaded.records[key]
	if persisted.State != current.State || persisted.Result["sessionId"] != current.Result["sessionId"] {
		t.Fatalf("memory and reloaded records diverged: memory=%#v reloaded=%#v", current, persisted)
	}
}

func TestSessionCreateStoreKeepsConservativeInDoubtMemoryAfterPersistenceFailure(t *testing.T) {
	store := newSessionCreateStore(t.TempDir())
	key := "create-in-doubt-failure"
	if _, _, err := store.begin(key, sessionCreateSpecHash("ambiguous")); err != nil {
		t.Fatal(err)
	}
	persistErr := errors.New("injected in-doubt persistence failure")
	store.beforeCommitSaveOverride = func() error { return persistErr }
	if err := store.update(key, "in_doubt", nil); !errors.Is(err, persistErr) {
		t.Fatalf("update error=%v want persistence failure", err)
	}
	if state := store.records[key].State; state != "in_doubt" {
		t.Fatalf("memory state=%q want conservative in_doubt", state)
	}
}

func TestSessionCreateCombinesAmbiguousRPCAndInDoubtPersistenceErrors(t *testing.T) {
	rpcErr := context.DeadlineExceeded
	persistErr := errors.New("injected in-doubt persistence failure")
	tests := []struct {
		name         string
		failSaveCall int
		request      func(context.Context, string, map[string]any) (map[string]any, error)
		wantSession  string
	}{
		{
			name: "thread start", failSaveCall: 2,
			request: func(_ context.Context, method string, _ map[string]any) (map[string]any, error) {
				switch method {
				case "model/list":
					return map[string]any{"data": []any{map[string]any{"id": "gpt-test", "isDefault": true}}}, nil
				case "thread/start":
					return nil, rpcErr
				default:
					return nil, fmt.Errorf("unexpected Codex method %q", method)
				}
			},
		},
		{
			name: "initial turn", failSaveCall: 3, wantSession: "thread-ambiguous-persist",
			request: func(_ context.Context, method string, _ map[string]any) (map[string]any, error) {
				switch method {
				case "model/list":
					return map[string]any{"data": []any{map[string]any{"id": "gpt-test", "isDefault": true}}}, nil
				case "thread/start":
					return map[string]any{"thread": map[string]any{"id": "thread-ambiguous-persist"}}, nil
				case "turn/start":
					return nil, rpcErr
				case "thread/archive":
					return map[string]any{}, nil
				default:
					return nil, fmt.Errorf("unexpected Codex method %q", method)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			manager := New(dataDir, nil)
			manager.codexStatePath = filepath.Join(dataDir, "codex-state.json")
			manager.codex.requestOverride = test.request
			saveCalls := 0
			manager.createStore.beforeCommitSaveOverride = func() error {
				saveCalls++
				if saveCalls == test.failSaveCall {
					return persistErr
				}
				return nil
			}
			key := "create-combined-" + strings.ReplaceAll(test.name, " ", "-")
			input := agentControlParams{WorkingDirectory: t.TempDir(), Prompt: "hello", IdempotencyKey: key}
			_, err := manager.sessionCreate(context.Background(), input)
			if !errors.Is(err, rpcErr) || !errors.Is(err, persistErr) {
				t.Fatalf("combined error=%v; want RPC and persistence errors", err)
			}
			record := manager.createStore.records["codex:"+key]
			if record.State != "in_doubt" {
				t.Fatalf("memory record=%#v want in_doubt", record)
			}
			if test.wantSession != "" && record.Result["sessionId"] != test.wantSession {
				t.Fatalf("memory result=%#v want session=%q", record.Result, test.wantSession)
			}
		})
	}
}

func TestSessionCreateStoreKeepsCommittedStateAfterParentSyncFailure(t *testing.T) {
	postCommitErr := errors.New("injected parent sync failure")
	installFailure := func(store *sessionCreateStore) {
		store.syncParentOverride = func(string) error { return postCommitErr }
	}
	assertReloadMatches := func(t *testing.T, dataDir, key string, store *sessionCreateStore) {
		t.Helper()
		memory, memoryExists := store.records[key]
		reloaded := newSessionCreateStore(dataDir)
		disk, diskExists := reloaded.records[key]
		if memoryExists != diskExists {
			t.Fatalf("record presence diverged: memory=%v reload=%v", memoryExists, diskExists)
		}
		if memoryExists && (memory.State != disk.State || memory.SpecHash != disk.SpecHash || !reflect.DeepEqual(memory.Result, disk.Result)) {
			t.Fatalf("record diverged: memory=%#v reload=%#v", memory, disk)
		}
	}

	t.Run("begin", func(t *testing.T) {
		dataDir := t.TempDir()
		store := newSessionCreateStore(dataDir)
		key := "create-post-commit-begin"
		installFailure(store)
		if _, _, err := store.begin(key, sessionCreateSpecHash("begin")); !errors.Is(err, postCommitErr) {
			t.Fatalf("begin error=%v want parent sync failure", err)
		}
		if state := store.records[key].State; state != "in_doubt" {
			t.Fatalf("post-commit begin state=%q want in_doubt", state)
		}
		assertReloadMatches(t, dataDir, key, store)
	})

	t.Run("update", func(t *testing.T) {
		dataDir := t.TempDir()
		store := newSessionCreateStore(dataDir)
		key := "create-post-commit-update"
		if _, _, err := store.begin(key, sessionCreateSpecHash("update")); err != nil {
			t.Fatal(err)
		}
		installFailure(store)
		if err := store.update(key, "thread_created", map[string]any{"sessionId": "session-update"}); !errors.Is(err, postCommitErr) {
			t.Fatalf("update error=%v want parent sync failure", err)
		}
		if state := store.records[key].State; state != "in_doubt" {
			t.Fatalf("post-commit transitional update state=%q want in_doubt", state)
		}
		assertReloadMatches(t, dataDir, key, store)
	})

	t.Run("abort", func(t *testing.T) {
		dataDir := t.TempDir()
		store := newSessionCreateStore(dataDir)
		key := "create-post-commit-abort"
		if _, _, err := store.begin(key, sessionCreateSpecHash("abort")); err != nil {
			t.Fatal(err)
		}
		installFailure(store)
		if err := store.abort(key); !errors.Is(err, postCommitErr) {
			t.Fatalf("abort error=%v want parent sync failure", err)
		}
		assertReloadMatches(t, dataDir, key, store)
	})

	t.Run("prepare delete", func(t *testing.T) {
		dataDir := t.TempDir()
		store := newSessionCreateStore(dataDir)
		key := "create-post-commit-prepare-delete"
		if _, _, err := store.begin(key, sessionCreateSpecHash("prepare delete")); err != nil {
			t.Fatal(err)
		}
		if err := store.update(key, "succeeded", map[string]any{"sessionId": "session-prepare-delete"}); err != nil {
			t.Fatal(err)
		}
		installFailure(store)
		if _, err := store.prepareSessionDelete("session-prepare-delete"); !errors.Is(err, postCommitErr) {
			t.Fatalf("prepare delete error=%v want parent sync failure", err)
		}
		assertReloadMatches(t, dataDir, key, store)
	})

	t.Run("finalize delete", func(t *testing.T) {
		dataDir := t.TempDir()
		store := newSessionCreateStore(dataDir)
		key := "create-post-commit-finalize-delete"
		if _, _, err := store.begin(key, sessionCreateSpecHash("finalize delete")); err != nil {
			t.Fatal(err)
		}
		if err := store.update(key, "succeeded", map[string]any{"sessionId": "session-finalize-delete"}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.prepareSessionDelete("session-finalize-delete"); err != nil {
			t.Fatal(err)
		}
		installFailure(store)
		if err := store.finalizeSessionDelete("session-finalize-delete"); !errors.Is(err, postCommitErr) {
			t.Fatalf("finalize delete error=%v want parent sync failure", err)
		}
		assertReloadMatches(t, dataDir, key, store)
	})
}

func TestSessionCreateRetriesSameKeyAfterDefinitiveInitialTurnRejection(t *testing.T) {
	dataDir := t.TempDir()
	manager := New(dataDir, nil)
	manager.codexStatePath = filepath.Join(dataDir, "codex-state.json")
	key := "create-turn-retry-01"
	turnStarts := 0
	deleted := 0
	manager.codex.requestOverride = func(_ context.Context, method string, _ map[string]any) (map[string]any, error) {
		switch method {
		case "model/list":
			return map[string]any{"data": []any{map[string]any{"id": "gpt-test", "isDefault": true}}}, nil
		case "thread/start":
			return map[string]any{"thread": map[string]any{"id": fmt.Sprintf("thread-%d", turnStarts+1)}}, nil
		case "turn/start":
			turnStarts++
			if turnStarts == 1 {
				return nil, &codexRPCResponseError{ExecutionError: newExecutionError("codex", method, "invalid initial input"), code: -32602}
			}
			return map[string]any{"turn": map[string]any{"id": "turn-retry"}}, nil
		case "thread/delete":
			deleted++
			return map[string]any{}, nil
		default:
			return nil, fmt.Errorf("unexpected Codex method %q", method)
		}
	}
	input := agentControlParams{WorkingDirectory: t.TempDir(), Prompt: "hello", IdempotencyKey: key}
	if _, err := manager.sessionCreate(context.Background(), input); err == nil {
		t.Fatal("definitive initial turn rejection unexpectedly succeeded")
	}
	if deleted != 1 {
		t.Fatalf("deleted threads=%d want=1", deleted)
	}
	if _, exists := manager.createStore.records["codex:"+key]; exists {
		t.Fatal("definitive rejection retained the idempotency reservation")
	}
	result, err := manager.sessionCreate(context.Background(), input)
	if err != nil || result["turnId"] != "turn-retry" {
		t.Fatalf("same-key retry result=%#v err=%v", result, err)
	}
}

func TestSessionCreateKeepsInDoubtStateAfterAmbiguousInitialTurnFailure(t *testing.T) {
	dataDir := t.TempDir()
	manager := New(dataDir, nil)
	manager.codexStatePath = filepath.Join(dataDir, "codex-state.json")
	key := "create-turn-doubt-01"
	archived := 0
	manager.codex.requestOverride = func(_ context.Context, method string, _ map[string]any) (map[string]any, error) {
		switch method {
		case "model/list":
			return map[string]any{"data": []any{map[string]any{"id": "gpt-test", "isDefault": true}}}, nil
		case "thread/start":
			return map[string]any{"thread": map[string]any{"id": "thread-ambiguous"}}, nil
		case "turn/start":
			return nil, context.DeadlineExceeded
		case "thread/archive":
			archived++
			return map[string]any{}, nil
		default:
			return nil, fmt.Errorf("unexpected Codex method %q", method)
		}
	}
	input := agentControlParams{WorkingDirectory: t.TempDir(), Prompt: "hello", IdempotencyKey: key}
	if _, err := manager.sessionCreate(context.Background(), input); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ambiguous initial turn error=%v", err)
	}
	if archived != 1 {
		t.Fatalf("archived threads=%d want=1", archived)
	}
	record := manager.createStore.records["codex:"+key]
	if record.State != "in_doubt" || record.Result["sessionId"] != "thread-ambiguous" {
		t.Fatalf("ambiguous failure record=%#v", record)
	}
	if _, err := manager.sessionCreate(context.Background(), input); err == nil {
		t.Fatal("ambiguous failure allowed an unreconciled same-key retry")
	}
}

func TestSessionCreateStoreAllowsOnlyOneConcurrentReservation(t *testing.T) {
	store := newSessionCreateStore(t.TempDir())
	const callers = 16
	var wait sync.WaitGroup
	var successes int
	var mu sync.Mutex
	for i := 0; i < callers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _, err := store.begin("create-concurrent-01", sessionCreateSpecHash("same"))
			if err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wait.Wait()
	if successes != 1 {
		t.Fatalf("successful reservations=%d want=1", successes)
	}
}

func TestSessionCreateStoreMarksInterruptedReservationInDoubt(t *testing.T) {
	dataDir := t.TempDir()
	store := newSessionCreateStore(dataDir)
	hash := sessionCreateSpecHash("same")
	if _, _, err := store.begin("create-restart-0001", hash); err != nil {
		t.Fatal(err)
	}
	reloaded := newSessionCreateStore(dataDir)
	_, _, err := reloaded.begin("create-restart-0001", hash)
	var capabilityErr testCapabilityError
	if !errors.As(err, &capabilityErr) {
		t.Fatalf("restart error=%T %v", err, err)
	}
	code, _, _ := capabilityErr.CapabilityError()
	if code != "AGENT_CREATE_IN_DOUBT" {
		t.Fatalf("restart code=%s", code)
	}
}

func TestSessionCreateStorePersistsCloudReconciliationAttemptAcrossRestart(t *testing.T) {
	dataDir := t.TempDir()
	store := newSessionCreateStore(dataDir)
	key := "create-cloud-attempt-01"
	hash := sessionCreateSpecHash("cloud-attempt")
	if _, _, err := store.begin(key, hash); err != nil {
		t.Fatal(err)
	}
	attempt := sessionCreateAttempt{
		Backend: sessionBackendChatGPTCloud, RequestMessageID: "provider-message-attempt-01", StartedAt: time.Now().UTC(),
	}
	if err := store.setAttempt(key, attempt); err != nil {
		t.Fatal(err)
	}
	reloaded := newSessionCreateStore(dataDir)
	got, ok := reloaded.attempt(key)
	if !ok || got != attempt {
		t.Fatalf("attempt=%#v ok=%v", got, ok)
	}
	if state := reloaded.records[key].State; state != "in_doubt" {
		t.Fatalf("reloaded state=%q want in_doubt", state)
	}
}
