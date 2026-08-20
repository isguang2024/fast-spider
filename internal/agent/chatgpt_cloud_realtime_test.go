package agent

import (
	"context"
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
