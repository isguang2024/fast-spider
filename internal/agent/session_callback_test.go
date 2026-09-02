package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
)

func testCallbackRegistration(source, target, task string, generation int64) sessionCallbackRegistration {
	return sessionCallbackRegistration{
		SourceSessionID: source,
		TargetSessionID: target,
		MissionID:       "mission-1",
		TaskID:          task,
		Generation:      generation,
	}
}

func testCallbackEvent(source string, sequence int64) chatgptCloudEvent {
	return chatgptCloudEvent{
		Sequence:       sequence,
		EventKey:       fmt.Sprintf("provider_evt_test_%d", sequence),
		Type:           "conversation.turn.complete",
		ConversationID: source,
		EventType:      "conversation-turn-complete",
		Timestamp:      time.Date(2026, 9, 2, 12, 0, int(sequence), 0, time.UTC),
	}
}

func TestSessionCallbackStorePersistsCoalescesAndFencesGeneration(t *testing.T) {
	dataDir := t.TempDir()
	store := newSessionCallbackStore(dataDir)
	registered, replayed, err := store.register(testCallbackRegistration("source-1", "target-1", "task-1", 1))
	if err != nil || replayed || registered.Generation != 1 {
		t.Fatalf("register=%#v replayed=%v err=%v", registered, replayed, err)
	}
	if queued, err := store.enqueue(testCallbackEvent("source-1", 10)); err != nil || !queued {
		t.Fatalf("enqueue first queued=%v err=%v", queued, err)
	}
	duplicateLater := testCallbackEvent("source-1", 12)
	duplicateLater.EventKey = "provider_evt_test_10"
	if queued, err := store.enqueue(duplicateLater); err != nil || queued {
		t.Fatalf("duplicate with a new local sequence queued=%v err=%v", queued, err)
	}
	if queued, err := store.enqueue(testCallbackEvent("source-1", 10)); err != nil || queued {
		t.Fatalf("duplicate queued=%v err=%v", queued, err)
	}
	if queued, err := store.enqueue(testCallbackEvent("source-1", 11)); err != nil || !queued {
		t.Fatalf("coalesced newer queued=%v err=%v", queued, err)
	}

	reloaded := newSessionCallbackStore(dataDir)
	items, pendingCounts, err := reloaded.registrationsSnapshot("source-1", "")
	if err != nil || len(items) != 1 || pendingCounts["source-1"] != 1 || items[0].LastEventSequence != 11 {
		t.Fatalf("reloaded items=%#v pending=%#v err=%v", items, pendingCounts, err)
	}
	grouped, err := reloaded.pendingByTarget()
	if err != nil || len(grouped["target-1"]) != 1 || grouped["target-1"][0].EventSequence != 11 {
		t.Fatalf("pending=%#v err=%v", grouped, err)
	}
	if _, _, err := reloaded.register(testCallbackRegistration("source-1", "target-1", "task-1", 0)); err == nil {
		t.Fatal("stale callback generation was accepted")
	}
	upgraded, replayed, err := reloaded.register(testCallbackRegistration("source-1", "target-1", "task-1", 2))
	if err != nil || replayed || upgraded.Generation != 2 || upgraded.LastEventSequence != 0 {
		t.Fatalf("upgrade=%#v replayed=%v err=%v", upgraded, replayed, err)
	}
	grouped, _ = reloaded.pendingByTarget()
	if len(grouped) != 0 {
		t.Fatalf("generation upgrade retained old pending events: %#v", grouped)
	}
	if queued, err := reloaded.enqueue(testCallbackEvent("source-1", 11)); err != nil || !queued {
		t.Fatalf("generation+sequence pair did not accept the new generation: queued=%v err=%v", queued, err)
	}
	if removed, err := reloaded.unregister("source-1", 1); err == nil || removed {
		t.Fatalf("stale unregister removed=%v err=%v", removed, err)
	}
	if removed, err := reloaded.unregister("source-1", 2); err != nil || !removed {
		t.Fatalf("unregister removed=%v err=%v", removed, err)
	}
}

func TestSessionCallbackStoreFailsClosedWhenPendingSequenceDisagrees(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "agent", "session-callbacks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const corrupt = `{"schemaVersion":1,"registrations":[{"sourceSessionId":"source-1","targetSessionId":"target-1","missionId":"mission-1","taskId":"task-1","generation":1,"lastEventSequence":2,"registeredAt":"2026-09-02T12:00:00Z","updatedAt":"2026-09-02T12:00:00Z"}],"pending":[{"sourceSessionId":"source-1","targetSessionId":"target-1","missionId":"mission-1","taskId":"task-1","generation":1,"eventSequence":1,"eventType":"conversation.turn.complete","occurredAt":"2026-09-02T12:00:01Z"}]}`
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newSessionCallbackStore(dataDir)
	if _, _, err := store.registrationsSnapshot("", ""); err == nil {
		t.Fatal("sequence-mismatched callback index was accepted")
	}
}

func TestSessionCallbackStoreDoesNotPersistFallbackIdentityForever(t *testing.T) {
	dataDir := t.TempDir()
	store := newSessionCallbackStore(dataDir)
	if _, _, err := store.register(testCallbackRegistration("source-1", "target-1", "task-1", 1)); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	payload := map[string]any{"conversation_id": "source-1", "type": "conversation-turn-complete"}
	firstKey := chatgptRealtimeEventKeyAt("source-1", "conversation-turn-complete", payload, now)
	laterKey := chatgptRealtimeEventKeyAt("source-1", "conversation-turn-complete", payload, now.Add(chatgptRealtimeFallbackDedupWindow+time.Second))
	first := testCallbackEvent("source-1", 1)
	first.EventKey = firstKey
	if queued, err := store.enqueue(first); err != nil || !queued {
		t.Fatalf("first fallback queued=%v err=%v", queued, err)
	}
	grouped, err := store.pendingByTarget()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.acknowledge("target-1", sessionCallbackEnvelopeID("target-1", grouped["target-1"]), grouped["target-1"], time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	reloaded := newSessionCallbackStore(dataDir)
	items, _, err := reloaded.registrationsSnapshot("source-1", "")
	if err != nil || len(items) != 1 || len(items[0].RecentEventKeys) != 0 {
		t.Fatalf("fallback identity became permanent: items=%#v err=%v", items, err)
	}
	duplicate := testCallbackEvent("source-1", 2)
	duplicate.EventKey = firstKey
	if queued, err := reloaded.enqueue(duplicate); err != nil || queued {
		t.Fatalf("fallback duplicate after restart queued=%v err=%v", queued, err)
	}
	later := testCallbackEvent("source-1", 2)
	later.EventKey = laterKey
	if queued, err := reloaded.enqueue(later); err != nil || !queued {
		t.Fatalf("fallback event after window queued=%v err=%v", queued, err)
	}
}

func TestSessionCallbackRecoveryRestoresGenerationAndUnregisterReleasesWatcher(t *testing.T) {
	dataDir := t.TempDir()
	seed := newSessionCallbackStore(dataDir)
	if _, _, err := seed.register(testCallbackRegistration("source-restart", "target-1", "task-1", 7)); err != nil {
		t.Fatal(err)
	}
	manager := New(dataDir, nil)
	defer manager.Close(context.Background())
	manager.callbackDispatcher.reconcileSubscriptions()
	manager.chatgptCloud.realtime.mu.Lock()
	watcher := manager.chatgptCloud.realtime.watching["source-restart"]
	manager.chatgptCloud.realtime.mu.Unlock()
	if watcher == nil || watcher.generation != 7 || !watcher.persistent {
		t.Fatalf("recovered watcher=%#v", watcher)
	}
	removed, err := manager.callbackStore.unregister("source-restart", 7)
	if err != nil || !removed {
		t.Fatalf("unregister removed=%v err=%v", removed, err)
	}
	manager.chatgptCloud.ReleaseCallbackRealtimeForGeneration("source-restart", 7)
	manager.chatgptCloud.realtime.mu.Lock()
	_, exists := manager.chatgptCloud.realtime.watching["source-restart"]
	manager.chatgptCloud.realtime.mu.Unlock()
	if exists {
		t.Fatal("unregister left the generation-fenced watcher active")
	}
}

func TestSessionCallbackManagerObserverInboxDispatchesAfterCoordinatorIdle(t *testing.T) {
	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	if _, _, err := manager.callbackStore.register(testCallbackRegistration("source-integration", "coordinator-local", "task-1", 1)); err != nil {
		t.Fatal(err)
	}
	active := true
	var activeMu sync.Mutex
	manager.callbackDispatcher.active = func(string) bool {
		activeMu.Lock()
		defer activeMu.Unlock()
		return active
	}
	sent := make(chan string, 1)
	manager.callbackDispatcher.send = func(_ context.Context, target, prompt string) error {
		if target != "coordinator-local" {
			t.Errorf("callback target=%q", target)
		}
		sent <- prompt
		return nil
	}
	manager.chatgptCloud.realtime.emit("source-integration", "conversation-turn-complete", "provider_evt_integration")
	grouped, err := manager.callbackStore.pendingByTarget()
	if err != nil || len(grouped["coordinator-local"]) != 1 {
		t.Fatalf("observer did not create durable inbox entry: pending=%#v err=%v", grouped, err)
	}
	select {
	case prompt := <-sent:
		t.Fatal("active coordinator received callback: ", prompt)
	case <-time.After(50 * time.Millisecond):
	}
	activeMu.Lock()
	active = false
	activeMu.Unlock()
	manager.handleCodexCallbackEvent(AgentEvent{Type: "turn.completed", SessionID: "coordinator-local"})
	select {
	case prompt := <-sent:
		if !strings.Contains(prompt, "FAST_SPIDER_SESSION_CALLBACK_V1") || !strings.Contains(prompt, "source_session=source-integration") || strings.Contains(prompt, "Worker-authored content:") {
			t.Fatalf("unexpected callback envelope=%q", prompt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal signal did not dispatch callback")
	}
	grouped, err = manager.callbackStore.pendingByTarget()
	if err != nil || len(grouped) != 0 {
		t.Fatalf("durable inbox not acknowledged: pending=%#v err=%v", grouped, err)
	}
}

func TestSessionCallbackStoreFailsClosedWhenCorrupt(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "agent", "session-callbacks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"registrations":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newSessionCallbackStore(dataDir)
	if _, _, err := store.register(testCallbackRegistration("source-1", "target-1", "task-1", 1)); err == nil {
		t.Fatal("corrupt callback registry accepted a mutation")
	}
	if _, _, err := store.registrationsSnapshot("", ""); err == nil {
		t.Fatal("corrupt callback registry returned a list")
	}
}

func TestSessionCallbackDispatcherBatchesAndWaitsForIdleTarget(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	for _, registration := range []sessionCallbackRegistration{
		testCallbackRegistration("source-a", "coordinator", "task-a", 1),
		testCallbackRegistration("source-b", "coordinator", "task-b", 1),
	} {
		if _, _, err := store.register(registration); err != nil {
			t.Fatal(err)
		}
	}
	if queued, err := store.enqueue(testCallbackEvent("source-a", 1)); err != nil || !queued {
		t.Fatalf("enqueue source-a queued=%v err=%v", queued, err)
	}
	if queued, err := store.enqueue(testCallbackEvent("source-b", 2)); err != nil || !queued {
		t.Fatalf("enqueue source-b queued=%v err=%v", queued, err)
	}

	active := true
	var prompts []string
	dispatcher := newSessionCallbackDispatcher(
		store,
		nil,
		func(string) bool { return active },
		func(_ context.Context, target, prompt string) error {
			if target != "coordinator" {
				t.Fatalf("target=%q", target)
			}
			prompts = append(prompts, prompt)
			return nil
		},
		nil,
	)
	dispatcher.dispatchOnce()
	if len(prompts) != 0 {
		t.Fatalf("active coordinator received callback: %v", prompts)
	}
	active = false
	dispatcher.dispatchOnce()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "source_session=source-a") || !strings.Contains(prompts[0], "source_session=source-b") {
		t.Fatalf("batched prompt=%q", prompts)
	}
	if strings.Contains(prompts[0], "Worker-authored content:") || !strings.Contains(prompts[0], "not Worker-authored content") {
		t.Fatalf("callback envelope did not keep the fixed trust boundary: %q", prompts[0])
	}
	grouped, err := store.pendingByTarget()
	if err != nil || len(grouped) != 0 {
		t.Fatalf("delivered events remain pending: %#v err=%v", grouped, err)
	}
}

func TestSessionCallbackDispatcherKeepsBusyDeliveryPending(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	if _, _, err := store.register(testCallbackRegistration("source-1", "coordinator", "task-1", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.enqueue(testCallbackEvent("source-1", 1)); err != nil {
		t.Fatal(err)
	}
	busy := true
	var sends int
	dispatcher := newSessionCallbackDispatcher(
		store,
		nil,
		func(string) bool { return false },
		func(context.Context, string, string) error {
			sends++
			if busy {
				return node.ErrAgentSessionBusy
			}
			return nil
		},
		nil,
	)
	dispatcher.dispatchOnce()
	grouped, _ := store.pendingByTarget()
	if sends != 1 || len(grouped["coordinator"]) != 1 {
		t.Fatalf("busy sends=%d pending=%#v", sends, grouped)
	}
	busy = false
	dispatcher.dispatchOnce()
	grouped, _ = store.pendingByTarget()
	if sends != 2 || len(grouped) != 0 {
		t.Fatalf("retry sends=%d pending=%#v", sends, grouped)
	}
}

func TestSessionCallbackStoreConcurrentEventsCoalescePerSource(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	if _, _, err := store.register(testCallbackRegistration("source-1", "target-1", "task-1", 1)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for sequence := int64(1); sequence <= 20; sequence++ {
		wg.Add(1)
		go func(sequence int64) {
			defer wg.Done()
			_, _ = store.enqueue(testCallbackEvent("source-1", sequence))
		}(sequence)
	}
	wg.Wait()
	grouped, err := store.pendingByTarget()
	if err != nil || len(grouped["target-1"]) != 1 {
		t.Fatalf("pending=%#v err=%v", grouped, err)
	}
	if sequence := grouped["target-1"][0].EventSequence; sequence < 1 || sequence > 20 {
		t.Fatalf("invalid coalesced sequence=%d", sequence)
	}
}

func TestSessionCallbackStoreRetainsProviderReplayFenceBeyondThirtyTwoEvents(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	if _, _, err := store.register(testCallbackRegistration("source-replay", "target-1", "task-1", 1)); err != nil {
		t.Fatal(err)
	}
	for sequence := int64(1); sequence <= 64; sequence++ {
		event := testCallbackEvent("source-replay", sequence)
		event.EventKey = fmt.Sprintf("provider_evt_replay_%d", sequence)
		if queued, err := store.enqueue(event); err != nil || !queued {
			t.Fatalf("enqueue sequence=%d queued=%v err=%v", sequence, queued, err)
		}
	}
	oldReplay := testCallbackEvent("source-replay", 65)
	oldReplay.EventKey = "provider_evt_replay_1"
	if queued, err := store.enqueue(oldReplay); err != nil || queued {
		t.Fatalf("old provider replay queued=%v err=%v", queued, err)
	}
	grouped, err := store.pendingByTarget()
	if err != nil || len(grouped["target-1"]) != 1 || grouped["target-1"][0].EventSequence != 64 {
		t.Fatalf("old replay replaced newer pending=%#v err=%v", grouped, err)
	}
}

func TestSessionCallbackEnvelopeChangesAfterProviderFenceEviction(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	if _, _, err := store.register(testCallbackRegistration("source-envelope", "target-1", "task-1", 1)); err != nil {
		t.Fatal(err)
	}
	for sequence := int64(1); sequence <= maxRecentCallbackEventKeys+1; sequence++ {
		event := testCallbackEvent("source-envelope", sequence)
		event.EventKey = fmt.Sprintf("provider_evt_envelope_%d", sequence)
		if queued, err := store.enqueue(event); err != nil || !queued {
			t.Fatalf("enqueue sequence=%d queued=%v err=%v", sequence, queued, err)
		}
	}
	grouped, err := store.pendingByTarget()
	if err != nil || len(grouped["target-1"]) != 1 {
		t.Fatalf("pending before replay=%#v err=%v", grouped, err)
	}
	first := grouped["target-1"][0]
	firstEnvelope := sessionCallbackEnvelopeID("target-1", []sessionCallbackEvent{first})
	oldReplay := testCallbackEvent("source-envelope", maxRecentCallbackEventKeys+2)
	oldReplay.EventKey = "provider_evt_envelope_1"
	if queued, err := store.enqueue(oldReplay); err != nil || !queued {
		t.Fatalf("evicted provider replay queued=%v err=%v", queued, err)
	}
	grouped, err = store.pendingByTarget()
	if err != nil || len(grouped["target-1"]) != 1 {
		t.Fatalf("pending after replay=%#v err=%v", grouped, err)
	}
	second := grouped["target-1"][0]
	secondEnvelope := sessionCallbackEnvelopeID("target-1", []sessionCallbackEvent{second})
	if firstEnvelope == secondEnvelope || second.EventSequence != maxRecentCallbackEventKeys+2 {
		t.Fatalf("replayed event reused envelope or lost sequence: first=%q second=%q event=%#v", firstEnvelope, secondEnvelope, second)
	}
}

func TestSessionCallbackProviderReplayCannotReplaceInFlightPendingEnvelope(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	if _, _, err := store.register(testCallbackRegistration("source-inflight", "target-1", "task-1", 1)); err != nil {
		t.Fatal(err)
	}
	for sequence := int64(1); sequence <= 64; sequence++ {
		event := testCallbackEvent("source-inflight", sequence)
		event.EventKey = fmt.Sprintf("provider_evt_inflight_%d", sequence)
		if queued, err := store.enqueue(event); err != nil || !queued {
			t.Fatalf("enqueue sequence=%d queued=%v err=%v", sequence, queued, err)
		}
	}
	sendStarted := make(chan struct{})
	releaseSend := make(chan struct{})
	dispatcher := newSessionCallbackDispatcher(store, nil, nil, func(context.Context, string, string) error {
		close(sendStarted)
		<-releaseSend
		return nil
	}, nil)
	done := make(chan struct{})
	go func() {
		dispatcher.dispatchOnce()
		close(done)
	}()
	<-sendStarted
	oldReplay := testCallbackEvent("source-inflight", 65)
	oldReplay.EventKey = "provider_evt_inflight_1"
	if queued, err := store.enqueue(oldReplay); err != nil || queued {
		t.Fatalf("old replay during in-flight delivery queued=%v err=%v", queued, err)
	}
	close(releaseSend)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("in-flight callback dispatch did not finish")
	}
	grouped, err := store.pendingByTarget()
	if err != nil || len(grouped) != 0 {
		t.Fatalf("in-flight envelope was not acknowledged cleanly: pending=%#v err=%v", grouped, err)
	}
}

func TestSessionCallbackRecoveryCannotCreateWatcherAfterUnregister(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	if _, _, err := store.register(testCallbackRegistration("source-race", "target-1", "task-1", 1)); err != nil {
		t.Fatal(err)
	}
	realtime := newChatGPTCloudRealtime(nil, "https://chatgpt.com", nil, nil)
	defer realtime.Close(context.Background())
	started := make(chan struct{})
	allowCreate := make(chan struct{})
	dispatcher := newSessionCallbackDispatcher(store, nil, nil, nil, func(_ context.Context, source string, generation int64) error {
		close(started)
		<-allowCreate
		return realtime.ensurePersistentWatchingForGeneration(context.Background(), source, generation)
	})
	unregisterDone := make(chan struct{})
	go dispatcher.reconcileSubscriptions()
	<-started
	go func() {
		removed, err := store.unregister("source-race", 1)
		if err != nil || !removed {
			t.Errorf("unregister removed=%v err=%v", removed, err)
		}
		realtime.releasePersistentWatching("source-race", 1)
		close(unregisterDone)
	}()
	select {
	case <-unregisterDone:
		t.Fatal("unregister crossed watcher recovery critical section")
	case <-time.After(50 * time.Millisecond):
	}
	close(allowCreate)
	select {
	case <-unregisterDone:
	case <-time.After(time.Second):
		t.Fatal("unregister did not complete after recovery")
	}
	realtime.mu.Lock()
	_, exists := realtime.watching["source-race"]
	realtime.mu.Unlock()
	if exists {
		t.Fatal("recovery left an orphan watcher after unregister")
	}
}

func TestSessionCallbackRegisterEnsureCannotCreateWatcherAfterUnregister(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	if _, _, err := store.register(testCallbackRegistration("source-register-race", "target-1", "task-1", 1)); err != nil {
		t.Fatal(err)
	}
	realtime := newChatGPTCloudRealtime(nil, "https://chatgpt.com", nil, nil)
	defer realtime.Close(context.Background())
	started := make(chan struct{})
	allowCreate := make(chan struct{})
	ensureDone := make(chan struct{})
	go func() {
		_, err := store.withCurrentRegistration("source-register-race", 1, func() error {
			close(started)
			<-allowCreate
			return realtime.ensurePersistentWatchingForGeneration(context.Background(), "source-register-race", 1)
		})
		if err != nil {
			t.Errorf("register ensure: %v", err)
		}
		close(ensureDone)
	}()
	<-started
	unregisterDone := make(chan struct{})
	go func() {
		removed, err := store.unregister("source-register-race", 1)
		if err != nil || !removed {
			t.Errorf("unregister removed=%v err=%v", removed, err)
		}
		realtime.releasePersistentWatching("source-register-race", 1)
		close(unregisterDone)
	}()
	select {
	case <-unregisterDone:
		t.Fatal("unregister crossed register ensure critical section")
	case <-time.After(50 * time.Millisecond):
	}
	close(allowCreate)
	select {
	case <-ensureDone:
	case <-time.After(time.Second):
		t.Fatal("register ensure did not finish")
	}
	select {
	case <-unregisterDone:
	case <-time.After(time.Second):
		t.Fatal("unregister did not finish after register ensure")
	}
	realtime.mu.Lock()
	_, exists := realtime.watching["source-register-race"]
	realtime.mu.Unlock()
	if exists {
		t.Fatal("register ensure left an orphan watcher after unregister")
	}
}

func TestSessionCallbackEnvelopeIDIsStable(t *testing.T) {
	events := []sessionCallbackEvent{{SourceSessionID: "source", Generation: 1, EventSequence: 2}}
	first := sessionCallbackEnvelopeID("target", events)
	second := sessionCallbackEnvelopeID("target", events)
	if first == "" || first != second {
		t.Fatalf("envelope IDs first=%q second=%q", first, second)
	}
}

func TestSessionCallbackActionsRegisterListAndUnregister(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/conversation/source-cloud" {
			http.NotFound(w, r)
			return
		}
		writeChatGPTCloudTestJSON(t, w, map[string]any{
			"conversation_id": "source-cloud",
			"mapping":         map[string]any{},
		})
	}))
	defer server.Close()

	manager := New(t.TempDir(), nil)
	defer manager.Close(context.Background())
	if err := manager.chatgptCloud.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.chatgptCloud = NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
	manager.chatgptCloud.baseURL = server.URL
	manager.chatgptCloud.http = server.Client()
	manager.chatgptCloud.realtime.baseURL = server.URL
	manager.chatgptCloud.realtime.http = server.Client()
	manager.codex.requestOverride = func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		if method != "thread/read" || params["threadId"] != "coordinator-local" {
			return nil, fmt.Errorf("unexpected Codex request %s %#v", method, params)
		}
		return map[string]any{"thread": map[string]any{"id": "coordinator-local", "cwd": t.TempDir()}}, nil
	}

	registered, err := manager.Control(context.Background(), "session.callback.register", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"sessionId": "source-cloud", "callbackTargetSessionId": "coordinator-local",
		"callbackMissionId": "mission-1", "callbackTaskId": "task-1", "callbackGeneration": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if registered["deliveryPolicy"] != "callback-first" || registered["recoveryPolicy"] != "heartbeat-fallback" {
		t.Fatalf("register=%#v", registered)
	}
	listed, err := manager.Control(context.Background(), "session.callback.list", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud, "sessionId": "source-cloud",
	})
	if err != nil {
		t.Fatal(err)
	}
	callbacks, _ := listed["callbacks"].([]map[string]any)
	if len(callbacks) != 1 || callbacks[0]["targetSessionId"] != "coordinator-local" || callbacks[0]["generation"] != int64(1) {
		t.Fatalf("callbacks=%#v", callbacks)
	}
	unregistered, err := manager.Control(context.Background(), "session.callback.unregister", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"sessionId": "source-cloud", "callbackGeneration": 1,
	})
	if err != nil || unregistered["unregistered"] != true {
		t.Fatalf("unregister=%#v err=%v", unregistered, err)
	}
}

func TestSessionCallbackRegisterKeepsDurableOwnerWhenWatcherFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/conversation/source-cloud" {
			http.NotFound(w, r)
			return
		}
		writeChatGPTCloudTestJSON(t, w, map[string]any{"conversation_id": "source-cloud", "mapping": map[string]any{}})
	}))
	defer server.Close()

	dataDir := t.TempDir()
	manager := New(dataDir, nil)
	defer manager.Close(context.Background())
	if err := manager.chatgptCloud.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.chatgptCloud = NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
	manager.chatgptCloud.baseURL = server.URL
	manager.chatgptCloud.http = server.Client()
	manager.chatgptCloud.realtime.baseURL = server.URL
	manager.chatgptCloud.realtime.http = server.Client()
	if err := manager.chatgptCloud.realtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.codex.requestOverride = func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		if method != "thread/read" || params["threadId"] != "coordinator-local" {
			return nil, fmt.Errorf("unexpected Codex request %s %#v", method, params)
		}
		return map[string]any{"thread": map[string]any{"id": "coordinator-local", "cwd": t.TempDir()}}, nil
	}
	if _, err := manager.Control(context.Background(), "session.callback.register", map[string]any{
		"providerId": "codex", "backend": sessionBackendChatGPTCloud,
		"sessionId": "source-cloud", "callbackTargetSessionId": "coordinator-local",
		"callbackMissionId": "mission-1", "callbackTaskId": "task-1", "callbackGeneration": 1,
	}); err == nil {
		t.Fatal("watcher failure was reported as a successful registration")
	}
	store := newSessionCallbackStore(dataDir)
	items, _, err := store.registrationsSnapshot("source-cloud", "")
	if err != nil || len(items) != 1 || items[0].Generation != 1 {
		t.Fatalf("durable registration after watcher failure=%#v err=%v", items, err)
	}
}
