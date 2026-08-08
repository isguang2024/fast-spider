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
	workspace, err := NewWorkspaceStore(dataDir).Add(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{DataDir: dataDir, Version: "test", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}

	call := func(capability, action, workspaceID string, params map[string]any) protocolv1.CapabilityResponse {
		return client.handleCapabilityRequest(context.Background(), protocolv1.CapabilityRequest{
			MessageType: protocolv1.MessageCapabilityRequest,
			RequestId:   "req_test_1234567890",
			Capability:  capability,
			Action:      action,
			WorkspaceId: workspaceID,
			Params:      params,
			Deadline:    protocolv1.Timestamp(time.Now().Add(5 * time.Second)),
			Timestamp:   protocolv1.Timestamp(time.Now()),
		})
	}

	read := call("file.read", "read", workspace.WorkspaceID, map[string]any{"path": "main.go", "limit": 1024})
	if read.Error != nil {
		t.Fatalf("file.read error=%+v", read.Error)
	}
	if got, _ := read.Result["content"].(string); got == "" {
		t.Fatalf("file.read returned empty content: %#v", read.Result)
	}

	utf8Chunk := call("file.read", "read", workspace.WorkspaceID, map[string]any{"path": "utf8.txt", "limit": 4})
	if utf8Chunk.Error != nil || utf8Chunk.Result["content"] != "甲" || utf8Chunk.Result["bytesRead"] != float64(3) {
		t.Fatalf("utf8 chunk response=%+v", utf8Chunk)
	}

	search := call("code.search", "search", workspace.WorkspaceID, map[string]any{"query": "needle", "limit": 10})
	if search.Error != nil {
		t.Fatalf("code.search error=%+v", search.Error)
	}
	matches, ok := search.Result["matches"].([]any)
	if !ok || len(matches) != 1 {
		t.Fatalf("unexpected search matches: %#v", search.Result["matches"])
	}

	traversal := call("file.read", "read", workspace.WorkspaceID, map[string]any{"path": "../outside.txt"})
	if traversal.Error == nil || traversal.Error.Code != "PATH_OUTSIDE_WORKSPACE" {
		t.Fatalf("traversal response=%+v", traversal)
	}
	absolute := call("file.read", "read", workspace.WorkspaceID, map[string]any{"path": filepath.Join(root, "main.go")})
	if absolute.Error == nil || absolute.Error.Code != "PATH_OUTSIDE_WORKSPACE" {
		t.Fatalf("absolute path response=%+v", absolute)
	}
	binary := call("file.read", "read", workspace.WorkspaceID, map[string]any{"path": "binary.bin"})
	if binary.Error == nil || binary.Error.Code != "NOT_TEXT" {
		t.Fatalf("binary response=%+v", binary)
	}
	tooLarge := call("file.read", "read", workspace.WorkspaceID, map[string]any{"path": "main.go", "limit": maxFileReadBytes + 1})
	if tooLarge.Error == nil || tooLarge.Error.Code != "OUTPUT_LIMIT" {
		t.Fatalf("large read response=%+v", tooLarge)
	}

	if err := NewWorkspaceStore(dataDir).SetEnabled(workspace.WorkspaceID, false); err != nil {
		t.Fatal(err)
	}
	disabled := call("file.read", "read", workspace.WorkspaceID, map[string]any{"path": "main.go"})
	if disabled.Error == nil || disabled.Error.Code != "WORKSPACE_DISABLED" {
		t.Fatalf("disabled workspace response=%+v", disabled)
	}
}
