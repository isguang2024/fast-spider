package v1

import (
	"reflect"
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

func TestAgentCapabilityAdvertisesCurrentActionContract(t *testing.T) {
	want := []string{
		"routing.status", "providers.list", "models.list", "provider.capabilities", "projects.list", "skills.list", "hooks.list", "permissions.list",
		"plugins.list", "plugins.installed", "plugins.get", "plugin.skill.read", "mcp.status.list",
		"session.list", "session.get", "session.create", "session.send", "session.steer", "session.respond", "session.watch", "session.cancel", "session.result", "session.rename", "session.archive",
		"session.unarchive", "session.delete", "session.fork", "session.compact", "session.rollback", "session.goal.get", "session.goal.set", "session.goal.clear", "session.settings.update", "session.review",
	}
	if !reflect.DeepEqual(AgentCapability.Actions, want) {
		t.Fatalf("agent.control actions=%v want=%v", AgentCapability.Actions, want)
	}
}
