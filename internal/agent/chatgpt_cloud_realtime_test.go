package agent

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestChatgptHandleWSFrames(t *testing.T) {
	frame := `[{"type":"message","topic_id":"conversations","payload":{"type":"conversation-turn-complete","payload":{"conversation_id":"abc-123"},"metadata":null}}]`
	var gotTopic, gotType, gotID string
	chatgptHandleWSFrames([]byte(frame), func(topic, payloadType, conversationID string) {
		gotTopic, gotType, gotID = topic, payloadType, conversationID
	})
	if gotTopic != "conversations" || gotType != "conversation-turn-complete" || gotID != "abc-123" {
		t.Fatalf("frame parse got topic=%q type=%q id=%q", gotTopic, gotType, gotID)
	}
}

func TestChatgptHandleWSFramesSingleFrame(t *testing.T) {
	frame := `{"type":"message","topic_id":"conversation-abc","payload":{"type":"conversation-created","conversation_id":"abc"}}`
	var calls []string
	chatgptHandleWSFrames([]byte(frame), func(topic, payloadType, conversationID string) {
		calls = append(calls, topic+"/"+payloadType+"/"+conversationID)
	})
	if len(calls) != 1 || calls[0] != "conversation-abc/conversation-created/abc" {
		t.Fatalf("single frame parse got %v", calls)
	}
}

func TestChatgptHandleWSFramesStableEventKeyAcrossTopics(t *testing.T) {
	frame := `{"type":"message","topic_id":"conversations","payload":{"type":"conversation-turn-complete","conversation_id":"abc","turn_id":"turn-1","metadata":{"attempt":1}}}`
	var first, second string
	chatgptHandleWSFramesWithKey([]byte(frame), func(_, _, _, eventKey string) { first = eventKey })
	frame = `{"type":"message","topic_id":"conversation-abc","payload":{"type":"conversation-turn-complete","conversation_id":"abc","turn_id":"turn-1","metadata":{"attempt":2}}}`
	chatgptHandleWSFramesWithKey([]byte(frame), func(_, _, _, eventKey string) { second = eventKey })
	if first == "" || first != second {
		t.Fatalf("event keys first=%q second=%q", first, second)
	}
}

func TestChatgptHandleWSFramesIgnoresReplies(t *testing.T) {
	frame := `[{"id":1,"type":"reply","reply":{"type":"subscribe","topic_id":"conversations","recovered":false}}]`
	chatgptHandleWSFrames([]byte(frame), func(topic, payloadType, conversationID string) {
		t.Fatalf("reply frame should not emit a message event: %s/%s/%s", topic, payloadType, conversationID)
	})
}

func TestChatgptCloudRealtimeEmitNormalizes(t *testing.T) {
	r := newChatGPTCloudRealtime(nil, "https://chatgpt.com", nil, nil)
	defer r.Close(context.Background())
	r.emit("abc", "conversation-turn-complete")
	r.emit("abc", "conversation-created")
	r.emit("abc", "some-other-update")
	events, next, err := r.watch(context.Background(), "abc", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || next != events[2].Sequence {
		t.Fatalf("watch got %d events next=%d", len(events), next)
	}
	want := []string{"conversation.turn.complete", "conversation.created", "conversation.updated"}
	for i, e := range events {
		if e.Type != want[i] {
			t.Errorf("event[%d].Type=%q want %q", i, e.Type, want[i])
		}
	}
}

func TestChatgptCloudRealtimeWatchTimeout(t *testing.T) {
	r := newChatGPTCloudRealtime(nil, "https://chatgpt.com", nil, nil)
	defer r.Close(context.Background())
	start := time.Now()
	events, _, err := r.watch(context.Background(), "missing", 0, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("expected no events, got %d", len(events))
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Fatalf("watch returned too fast (%v)", elapsed)
	}
}

func TestChatgptCloudRealtimeSubscriptionsAreBoundedAndCloseable(t *testing.T) {
	r := newChatGPTCloudRealtime(nil, "https://chatgpt.com", nil, nil)
	for i := 0; i < maxChatGPTCloudRealtimeSubscriptions; i++ {
		if _, err := r.ensureWatching(context.Background(), fmt.Sprintf("conversation-%d", i)); err != nil {
			t.Fatalf("ensureWatching(%d): %v", i, err)
		}
	}
	if _, err := r.ensureWatching(context.Background(), "conversation-over-limit"); err != nil {
		t.Fatalf("idle subscription should be evictable: %v", err)
	}
	r.mu.Lock()
	count := len(r.watching)
	r.mu.Unlock()
	if count != maxChatGPTCloudRealtimeSubscriptions {
		t.Fatalf("realtime subscriptions=%d want %d", count, maxChatGPTCloudRealtimeSubscriptions)
	}
	if err := r.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r.mu.Lock()
	deferred := len(r.watching)
	closed := r.closed
	r.mu.Unlock()
	if !closed || deferred != 0 {
		t.Fatalf("realtime close state closed=%v watching=%d", closed, deferred)
	}
}

func TestChatgptCloudRealtimeDoesNotEvictActiveWaiter(t *testing.T) {
	r := newChatGPTCloudRealtime(nil, "https://chatgpt.com", nil, nil)
	defer r.Close(context.Background())
	if _, err := r.ensureWatching(context.Background(), "conversation-active", true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxChatGPTCloudRealtimeSubscriptions-1; i++ {
		if _, err := r.ensureWatching(context.Background(), fmt.Sprintf("conversation-idle-%d", i)); err != nil {
			t.Fatalf("ensure idle %d: %v", i, err)
		}
	}
	if _, err := r.ensureWatching(context.Background(), "conversation-new"); err != nil {
		t.Fatalf("idle eviction with active waiter failed: %v", err)
	}
	r.mu.Lock()
	_, active := r.watching["conversation-active"]
	_, added := r.watching["conversation-new"]
	count := len(r.watching)
	r.mu.Unlock()
	if !active || !added || count != maxChatGPTCloudRealtimeSubscriptions {
		t.Fatalf("active=%v added=%v count=%d", active, added, count)
	}
}

func TestChatgptCloudRealtimeObserverUsesPersistentSequenceFloor(t *testing.T) {
	r := newChatGPTCloudRealtime(nil, "https://chatgpt.com", nil, nil)
	defer r.Close(context.Background())
	r.setSequenceFloor(40)
	var observed chatgptCloudEvent
	r.setObserver(func(event chatgptCloudEvent) { observed = event })
	r.emit("conversation-callback", "conversation-turn-complete")
	if observed.Sequence != 41 || observed.Type != "conversation.turn.complete" || observed.ConversationID != "conversation-callback" {
		t.Fatalf("observed=%#v", observed)
	}
}

func TestChatgptCloudRealtimeDeduplicatesStableEventsBeforeSequencing(t *testing.T) {
	r := newChatGPTCloudRealtime(nil, "https://chatgpt.com", nil, nil)
	defer r.Close(context.Background())
	key := chatgptRealtimeEventKey("conversation-callback", "conversation-turn-complete", map[string]any{"turn_id": "turn-1"})
	r.emit("conversation-callback", "conversation-turn-complete", key)
	r.emit("conversation-callback", "conversation-turn-complete", key)
	events, _, err := r.watch(context.Background(), "conversation-callback", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventKey != key || events[0].Sequence != 1 {
		t.Fatalf("deduplicated events=%#v", events)
	}
}

func TestChatgptCloudRealtimeFallbackDedupIsShortLived(t *testing.T) {
	r := newChatGPTCloudRealtime(nil, "https://chatgpt.com", nil, nil)
	defer r.Close(context.Background())
	payload := map[string]any{"conversation_id": "conversation-callback", "type": "conversation-turn-complete"}
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	shortKey := chatgptRealtimeEventKeyAt("conversation-callback", "conversation-turn-complete", payload, now)
	longKey := chatgptRealtimeEventKeyAt("conversation-callback", "conversation-turn-complete", payload, now.Add(chatgptRealtimeFallbackDedupWindow+time.Second))
	if shortKey == longKey || len(shortKey) == 0 || shortKey[:len("fallback_evt_")] != "fallback_evt_" {
		t.Fatalf("fallback keys short=%q long=%q", shortKey, longKey)
	}
	r.emit("conversation-callback", "conversation-turn-complete", shortKey)
	r.emit("conversation-callback", "conversation-turn-complete", shortKey)
	r.emit("conversation-callback", "conversation-turn-complete", longKey)
	events, _, err := r.watch(context.Background(), "conversation-callback", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Sequence != 1 || events[1].Sequence != 2 {
		t.Fatalf("fallback dedup events=%#v", events)
	}
}

func TestChatgptCloudRealtimeReleaseFencesGeneration(t *testing.T) {
	r := newChatGPTCloudRealtime(nil, "https://chatgpt.com", nil, nil)
	defer r.Close(context.Background())
	if err := r.ensurePersistentWatchingForGeneration(context.Background(), "conversation-callback", 1); err != nil {
		t.Fatal(err)
	}
	if err := r.ensurePersistentWatchingForGeneration(context.Background(), "conversation-callback", 2); err != nil {
		t.Fatal(err)
	}
	r.releasePersistentWatching("conversation-callback", 1)
	r.mu.Lock()
	current := r.watching["conversation-callback"]
	r.mu.Unlock()
	if current == nil || current.generation != 2 || !current.persistent {
		t.Fatalf("generation-1 release touched current watcher: %#v", current)
	}
	r.releasePersistentWatching("conversation-callback", 2)
}

func TestChatgptCloudRealtimeDoesNotEvictPersistentCallback(t *testing.T) {
	r := newChatGPTCloudRealtime(nil, "https://chatgpt.com", nil, nil)
	defer r.Close(context.Background())
	if err := r.ensurePersistentWatching(context.Background(), "conversation-callback"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxChatGPTCloudRealtimeSubscriptions-1; i++ {
		if _, err := r.ensureWatching(context.Background(), fmt.Sprintf("conversation-idle-%d", i)); err != nil {
			t.Fatalf("ensure idle %d: %v", i, err)
		}
	}
	if _, err := r.ensureWatching(context.Background(), "conversation-new"); err != nil {
		t.Fatalf("idle eviction with persistent callback failed: %v", err)
	}
	r.mu.Lock()
	callback := r.watching["conversation-callback"]
	r.mu.Unlock()
	if callback == nil || !callback.persistent {
		t.Fatalf("persistent callback was evicted: %#v", callback)
	}
	r.releasePersistentWatching("conversation-callback")
	r.mu.Lock()
	_, exists := r.watching["conversation-callback"]
	r.mu.Unlock()
	if exists {
		t.Fatal("released persistent callback subscription remained active without waiters")
	}
}
