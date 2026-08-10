package v1

import (
	"testing"
	"time"
)

func TestMessageTypeAndTimestamp(t *testing.T) {
	messageType, err := MessageType([]byte(`{"messageType":"heartbeat"}`))
	if err != nil || messageType != MessageHeartbeat {
		t.Fatalf("MessageType()=%q, err=%v", messageType, err)
	}
	if _, err := MessageType([]byte(`{"status":"ready"}`)); err == nil {
		t.Fatal("MessageType accepted a message without messageType")
	}
	if _, err := MessageType([]byte(`not-json`)); err == nil {
		t.Fatal("MessageType accepted malformed JSON")
	}

	when := time.Date(2026, time.August, 8, 12, 34, 56, 123456789, time.FixedZone("CST", 8*60*60))
	if got, want := Timestamp(when), "2026-08-08T04:34:56.123456789Z"; got != want {
		t.Fatalf("Timestamp()=%q, want %q", got, want)
	}
}

func TestAgentCapabilityAdvertisesProjectsList(t *testing.T) {
	for _, action := range AgentCapability.Actions {
		if action == "projects.list" {
			return
		}
	}
	t.Fatal("agent.control does not advertise projects.list")
}
