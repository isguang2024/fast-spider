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
		"routing.status", "providers.list", "provider.readiness", "models.list", "provider.capabilities", "projects.list", "skills.list", "hooks.list", "permissions.list",
		"plugins.list", "plugins.installed", "plugins.get", "plugin.skill.read", "mcp.status.list",
		"session.list", "session.get", "session.create", "session.send", "session.steer", "session.respond", "session.watch", "session.cancel", "session.result", "session.rename", "session.archive",
		"session.unarchive", "session.delete", "session.fork", "session.compact", "session.rollback", "session.goal.get", "session.goal.set", "session.goal.clear", "session.settings.update", "session.review",
	}
	if !reflect.DeepEqual(AgentCapability.Actions, want) {
		t.Fatalf("agent.control actions=%v want=%v", AgentCapability.Actions, want)
	}
	if AgentCapability.Version != "1.2" {
		t.Fatalf("agent.control version=%q want 1.2", AgentCapability.Version)
	}
}

func TestWorkingContextCapabilityAdvertisesPlanAndMarkdownActions(t *testing.T) {
	want := []string{"get", "set", "clear", "plan.init", "plan.get", "plan.list", "plan.sync", "task.update", "markdown.list", "markdown.read", "markdown.append", "progress.watch"}
	for _, capability := range NodeCapabilities {
		if capability.CapabilityId == "working.context" {
			if capability.Version != "1.1" || !reflect.DeepEqual(capability.Actions, want) {
				t.Fatalf("working.context=%+v want actions=%v", capability, want)
			}
			return
		}
	}
	t.Fatal("working.context capability is missing")
}

func TestCodeSearchCapabilityAdvertisesVersionTwoWithoutNewAction(t *testing.T) {
	for _, capability := range NodeCapabilities {
		if capability.CapabilityId == "code.search" {
			if capability.Version != "2.1" || !reflect.DeepEqual(capability.Actions, []string{"search"}) {
				t.Fatalf("code.search=%+v", capability)
			}
			return
		}
	}
	t.Fatal("code.search capability is missing")
}

func TestFileReadCapabilityAdvertisesVersionTwoWithoutNewAction(t *testing.T) {
	for _, capability := range NodeCapabilities {
		if capability.CapabilityId == "file.read" {
			if capability.Version != "2.0" || !reflect.DeepEqual(capability.Actions, []string{"read"}) {
				t.Fatalf("file.read=%+v", capability)
			}
			return
		}
	}
	t.Fatal("file.read capability is missing")
}

func TestFileWriteCapabilityAdvertisesVersionTwoAndLegacyEdit(t *testing.T) {
	want := []string{"edit", "create", "replace", "editMany", "preview"}
	for _, capability := range NodeCapabilities {
		if capability.CapabilityId == "file.write" {
			if capability.Version != "2.1" || !reflect.DeepEqual(capability.Actions, want) {
				t.Fatalf("file.write=%+v want actions=%v", capability, want)
			}
			return
		}
	}
	t.Fatal("file.write capability is missing")
}
