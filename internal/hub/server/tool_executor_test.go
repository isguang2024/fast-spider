package server

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestValidateCloudCompletionToolInputReturnsRequestErrors(t *testing.T) {
	tests := []cloudCompletionInput{
		{Action: "unknown", ActorSessionID: "dispatcher"},
		{Action: "notify", ActorSessionID: "$self", TaskID: "task-1", Outcome: "completed"},
		{Action: "notify", ActorSessionID: "$self", CollaborationID: "collab-1", TaskID: "task-1", Outcome: "completed", SourceSessionID: "chat-1"},
		{Action: "notify", ActorSessionID: "$self", CollaborationID: "collab-1", TaskID: "task-1", Outcome: "completed", CallbackType: "unknown"},
		{Action: "notify", ActorSessionID: "$self", CollaborationID: "collab-1", TaskID: "task-1", Outcome: "completed", CallbackType: protocolv1.CloudCallbackTypeStatus, Text: "not allowed"},
		{Action: "notify", ActorSessionID: "$self", CollaborationID: "collab-1", TaskID: "task-1", Outcome: "completed", CallbackType: protocolv1.CloudCallbackTypeText, Text: strings.Repeat("界", protocolv1.CloudCallbackTextMaxRunes+1)},
		{Action: "claim", ActorSessionID: "bad actor"},
		{Action: "ack", ActorSessionID: "dispatcher", ClaimID: "bad claim"},
	}
	for _, input := range tests {
		err := validateCloudCompletionToolInput(input)
		var requestErr *toolRequestError
		if !errors.As(err, &requestErr) {
			t.Fatalf("input=%#v error=%v, want toolRequestError", input, err)
		}
	}
	for _, input := range []cloudCompletionInput{
		{Action: "notify", ActorSessionID: "$self", CollaborationID: "collab-1", TaskID: "task-1", Outcome: "completed"},
		{Action: "notify", ActorSessionID: "$self", CollaborationID: "collab-1", TaskID: "task-1", Outcome: "completed", CallbackType: protocolv1.CloudCallbackTypeText, Text: "short result"},
		{Action: "notify", ActorSessionID: "$self", CollaborationID: "collab-1", TaskID: "task-1", Outcome: "completed", CallbackType: protocolv1.CloudCallbackTypeStatus},
		{Action: "notify", ActorSessionID: "dispatcher", SourceSessionID: "chat-1", CollaborationID: "collab-1", TaskID: "task-1", Outcome: "failed"},
		{Action: "claim", ActorSessionID: "dispatcher", Limit: 64},
		{Action: "ack", ActorSessionID: "dispatcher", ClaimID: "claim-1"},
	} {
		if err := validateCloudCompletionToolInput(input); err != nil {
			t.Fatalf("valid input=%#v error=%v", input, err)
		}
	}
}

func TestAIControlRejectsPublicCallbackRouteCreation(t *testing.T) {
	executor := newToolExecutor(nil)
	for _, action := range []string{"session.callback.register", "session.callback.arm"} {
		_, err := executor.Execute(context.Background(), "owner", "ai_control", aiControlInput{Action: action})
		var capabilityErr *core.CapabilityCallError
		if !errors.As(err, &capabilityErr) || capabilityErr.Code != "CALLBACK_ROUTE_MANAGED_ONLY" {
			t.Fatalf("action=%s error=%v", action, err)
		}
	}
}

func TestTransportAdaptersUseSharedToolExecutor(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate tool executor test source")
	}
	dir := filepath.Dir(currentFile)
	for _, name := range []string{"mcp.go", "direct.go"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(".CallCapability(")) {
			t.Fatalf("%s bypasses shared toolExecutor with a direct CallCapability route", name)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "tool_executor.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(".CallCapability(")) || !bytes.Contains(raw, []byte("func (e *toolExecutor) Execute(")) {
		t.Fatal("shared tool executor does not own capability routing")
	}
}
