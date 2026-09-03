package server

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidateCloudCompletionToolInputReturnsRequestErrors(t *testing.T) {
	tests := []cloudCompletionInput{
		{Action: "unknown", ActorSessionID: "dispatcher"},
		{Action: "notify", ActorSessionID: "$self", TaskID: "task-1", Outcome: "completed"},
		{Action: "notify", ActorSessionID: "$self", CollaborationID: "collab-1", TaskID: "task-1", Outcome: "completed", SourceSessionID: "chat-1"},
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
		{Action: "notify", ActorSessionID: "dispatcher", SourceSessionID: "chat-1", CollaborationID: "collab-1", TaskID: "task-1", Outcome: "failed"},
		{Action: "claim", ActorSessionID: "dispatcher", Limit: 64},
		{Action: "ack", ActorSessionID: "dispatcher", ClaimID: "claim-1"},
	} {
		if err := validateCloudCompletionToolInput(input); err != nil {
			t.Fatalf("valid input=%#v error=%v", input, err)
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
