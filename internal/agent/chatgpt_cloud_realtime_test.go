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
