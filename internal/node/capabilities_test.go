package node

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

type testAgentCapabilityError struct{}

func (testAgentCapabilityError) Error() string { return "sensitive config detail" }
func (testAgentCapabilityError) CapabilityError() (string, string, bool) {
	return "AGENT_CONFIG_INVALID", "AI runtime configuration is incompatible", false
}

type disconnectedCreateTestAgent struct {
	contextError error
	hasDeadline  bool
}

func (a *disconnectedCreateTestAgent) Control(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
	a.contextError = ctx.Err()
	_, a.hasDeadline = ctx.Deadline()
	return map[string]any{"sessionId": "cloud-after-disconnect"}, nil
}

func (*disconnectedCreateTestAgent) Close(context.Context) error { return nil }

func TestCapabilityErrorPreservesSanitizedAgentClassification(t *testing.T) {
	err := capabilityError(testAgentCapabilityError{})
	if err.Code != "AGENT_CONFIG_INVALID" || err.Message != "AI runtime configuration is incompatible" || err.Retryable {
		t.Fatalf("protocol error=%+v", err)
	}
}

func TestSessionCreateSurvivesTransportCancellationWithinOperationDeadline(t *testing.T) {
	agent := &disconnectedCreateTestAgent{}
	client, err := New(Config{DataDir: t.TempDir(), Version: "disconnect-test", Agent: agent, AgentCallerOwned: true})
	if err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	response := client.handleCapabilityRequest(parent, protocolv1.CapabilityRequest{
		MessageType: protocolv1.MessageCapabilityRequest,
		RequestId:   "req_disconnect_create_01",
		Capability:  "agent.control",
		Action:      "session.create",
		Params:      map[string]any{},
		Deadline:    protocolv1.Timestamp(time.Now().Add(time.Second)),
	})
	if response.Error != nil || response.Result["sessionId"] != "cloud-after-disconnect" {
		t.Fatalf("response=%+v", response)
	}
	if agent.contextError != nil || !agent.hasDeadline {
		t.Fatalf("agent context error=%v hasDeadline=%v", agent.contextError, agent.hasDeadline)
	}
}

func TestClientCapabilitiesAdvertiseOSSpecificScreenshot(t *testing.T) {
	for _, descriptor := range protocolv1.NodeCapabilities {
		if descriptor.CapabilityId == protocolv1.ScreenshotCapability.CapabilityId {
			t.Fatal("screenshot capability must not be part of the static NodeCapabilities baseline")
		}
	}

	client, err := New(Config{DataDir: t.TempDir(), Version: "capability-test"})
	if err != nil {
		t.Fatal(err)
	}
	var advertised []protocolv1.CapabilityDescriptor
	for _, descriptor := range client.Capabilities() {
		if descriptor.CapabilityId == protocolv1.ScreenshotCapability.CapabilityId {
			advertised = append(advertised, descriptor)
		}
	}
	if len(advertised) != 1 {
		t.Fatalf("advertised screenshot descriptors=%d, want 1", len(advertised))
	}
	want := protocolv1.ScreenshotCapabilityForOS(runtime.GOOS)
	got := advertised[0]
	if got.CapabilityId != want.CapabilityId || got.Version != want.Version || len(got.Actions) != len(want.Actions) {
		t.Fatalf("advertised screenshot capability=%+v, want=%+v", got, want)
	}
	for index := range want.Actions {
		if got.Actions[index] != want.Actions[index] {
			t.Fatalf("advertised screenshot actions=%v, want=%v", got.Actions, want.Actions)
		}
	}

	linux := protocolv1.ScreenshotCapabilityForOS("linux")
	for _, action := range linux.Actions {
		if action == "listWindows" || action == "window" {
			t.Fatalf("Linux screenshot capability falsely advertises window action: %v", linux.Actions)
		}
	}
	windows := protocolv1.ScreenshotCapabilityForOS("windows")
	for _, action := range []string{"listWindows", "window"} {
		found := false
		for _, advertisedAction := range windows.Actions {
			if advertisedAction == action {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("Windows screenshot capability omitted %q: %v", action, windows.Actions)
		}
	}
}

func TestPhase2CapabilityReadSearchAndDenials(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package demo\n\n// needle\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "utf8.txt"), []byte("甲乙丙"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{DataDir: dataDir, Version: "test", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}

	call := func(capability, action string, params map[string]any) protocolv1.CapabilityResponse {
		return client.handleCapabilityRequest(context.Background(), protocolv1.CapabilityRequest{
			MessageType: protocolv1.MessageCapabilityRequest,
			RequestId:   "req_test_1234567890",
			Capability:  capability,
			Action:      action,
			Params:      params,
			Deadline:    protocolv1.Timestamp(time.Now().Add(5 * time.Second)),
			Timestamp:   protocolv1.Timestamp(time.Now()),
		})
	}

	read := call("file.read", "read", map[string]any{"path": filepath.Join(root, "main.go"), "limit": 1024})
	if read.Error != nil {
		t.Fatalf("file.read error=%+v", read.Error)
	}
	if got, _ := read.Result["content"].(string); got == "" {
		t.Fatalf("file.read returned empty content: %#v", read.Result)
	}

	utf8Chunk := call("file.read", "read", map[string]any{"path": filepath.Join(root, "utf8.txt"), "limit": 4})
	if utf8Chunk.Error != nil || utf8Chunk.Result["content"] != "甲" || utf8Chunk.Result["bytesRead"] != float64(3) {
		t.Fatalf("utf8 chunk response=%+v", utf8Chunk)
	}

	search := call("code.search", "search", map[string]any{"path": root, "query": "needle", "limit": 10})
	if search.Error != nil {
		t.Fatalf("code.search error=%+v", search.Error)
	}
	matches, ok := search.Result["matches"].([]any)
	if !ok || len(matches) != 1 {
		t.Fatalf("unexpected search matches: %#v", search.Result["matches"])
	}

	relative := call("file.read", "read", map[string]any{"path": "main.go"})
	if relative.Error == nil || relative.Error.Code != "ABSOLUTE_PATH_REQUIRED" {
		t.Fatalf("relative path response=%+v", relative)
	}
	binary := call("file.read", "read", map[string]any{"path": filepath.Join(root, "binary.bin")})
	if binary.Error == nil || binary.Error.Code != "NOT_TEXT" {
		t.Fatalf("binary response=%+v", binary)
	}
	tooLarge := call("file.read", "read", map[string]any{"path": filepath.Join(root, "main.go"), "limit": maxFileReadBytes + 1})
	if tooLarge.Error == nil || tooLarge.Error.Code != "OUTPUT_LIMIT" {
		t.Fatalf("large read response=%+v", tooLarge)
	}

}
