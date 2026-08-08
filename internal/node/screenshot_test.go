package node

import (
	"context"
	"encoding/base64"
	"errors"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestScreenshotCaptureDoesNotRequireWorkspacePermission(t *testing.T) {
	dataDir := t.TempDir()
	client, err := New(Config{DataDir: dataDir, Version: "screenshot-test"})
	if err != nil {
		t.Fatal(err)
	}
	store := NewWorkspaceStore(dataDir)
	workspace, err := store.Add(t.TempDir(), "screenshot-permission")
	if err != nil {
		t.Fatal(err)
	}
	response := client.handleCapabilityRequest(context.Background(), protocolCapabilityRequest("screenshot.capture", "listDisplays", workspace.WorkspaceID))
	if response.Error != nil && response.Error.Code != "SCREENSHOT_UNAVAILABLE" {
		t.Fatalf("screenshot unexpectedly rejected without a graphical session: %+v", response.Error)
	}
}

func TestScreenshotCaptureRejectsInvalidParameters(t *testing.T) {
	dataDir := t.TempDir()
	store := NewWorkspaceStore(dataDir)
	workspace, err := store.Add(t.TempDir(), "screenshot-params")
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{DataDir: dataDir, Version: "screenshot-test"})
	if err != nil {
		t.Fatal(err)
	}

	displays, err := activeDisplays()
	if err != nil {
		t.Skipf("desktop session unavailable: %v", err)
	}
	if _, err := client.screenshotCapture(context.Background(), workspace.WorkspaceID, "display", map[string]any{"displayIndex": len(displays)}); err == nil || !strings.Contains(err.Error(), "displayIndex") {
		t.Fatalf("invalid displayIndex error=%v", err)
	}
	if _, err := client.screenshotCapture(context.Background(), workspace.WorkspaceID, "desktop", map[string]any{"format": "gif"}); err == nil || !strings.Contains(err.Error(), "format") {
		t.Fatalf("invalid screenshot format error=%v", err)
	}
	if _, err := client.screenshotCapture(context.Background(), workspace.WorkspaceID, "desktop", map[string]any{"format": "jpeg", "quality": 10}); err == nil || !strings.Contains(err.Error(), "quality") {
		t.Fatalf("invalid jpeg quality error=%v", err)
	}
}

func TestScreenshotSemaphoreIsBoundedAndCancellationDoesNotBlockFileRead(t *testing.T) {
	dataDir := t.TempDir()
	client, err := New(Config{DataDir: dataDir, Version: "screenshot-test"})
	if err != nil {
		t.Fatal(err)
	}
	if cap(client.screenshotSem) != 1 {
		t.Fatalf("screenshot semaphore capacity=%d, want 1", cap(client.screenshotSem))
	}
	store := NewWorkspaceStore(dataDir)
	workspace, err := store.Add(t.TempDir(), "screenshot-semaphore")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace.Root, "read.txt"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	client.screenshotSem <- struct{}{}
	defer func() { <-client.screenshotSem }()

	captureCtx, cancel := context.WithCancel(context.Background())
	captureErr := make(chan error, 1)
	go func() {
		_, captureErrValue := client.captureRectArtifact(captureCtx, workspace.WorkspaceID, "desktop", screenshotCaptureParams{}, image.Rect(0, 0, 1, 1))
		captureErr <- captureErrValue
	}()
	time.Sleep(20 * time.Millisecond)
	if _, err := client.fileRead(context.Background(), workspace.WorkspaceID, map[string]any{"path": "read.txt"}); err != nil {
		t.Fatalf("file_read was blocked by screenshot semaphore: %v", err)
	}
	cancel()
	select {
	case err := <-captureErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting screenshot cancellation error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting screenshot did not honor context cancellation")
	}
}

func TestWindowTokenRejectsExpiredTamperedAndCrossWorkspaceTokens(t *testing.T) {
	key := windowTokenKey([]byte("window-token-test-private-key"))
	now := time.Unix(1_750_000_000, 0).UTC()
	identity := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	token := makeWindowToken(key, "ws-a", 1234, identity, now)

	if handle, gotIdentity, err := parseWindowToken(key, "ws-a", token, now); err != nil || handle != 1234 || gotIdentity != identity {
		t.Fatalf("valid window token handle=%d identity=%v err=%v", handle, gotIdentity, err)
	}
	if _, _, err := parseWindowToken(key, "ws-b", token, now); !errors.Is(err, ErrWindowTokenInvalid) {
		t.Fatalf("cross-workspace token error=%v, want ErrWindowTokenInvalid", err)
	}
	if _, _, err := parseWindowToken(key, "ws-a", token+"tampered", now); !errors.Is(err, ErrWindowTokenInvalid) {
		t.Fatalf("tampered token error=%v, want ErrWindowTokenInvalid", err)
	}

	expired := makeWindowToken(key, "ws-a", 1234, identity, now.Add(-windowTokenTTL-time.Second))
	if _, _, err := parseWindowToken(key, "ws-a", expired, now); !errors.Is(err, ErrWindowTokenInvalid) {
		t.Fatalf("expired token error=%v, want ErrWindowTokenInvalid", err)
	}
}

func TestWindowTokenBindsWindowIdentity(t *testing.T) {
	key := windowTokenKey([]byte("window-token-identity-test"))
	now := time.Unix(1_750_000_000, 0).UTC()
	original := nativeWindowInfo{Handle: 1234, ProcessID: 4242, ClassName: "FastSpiderWindow", Title: "Fast Spider", Bounds: image.Rect(10, 20, 810, 620)}
	identity := windowIdentity(original)
	token := makeWindowToken(key, "ws-a", original.Handle, identity, now)

	_, expectedIdentity, err := parseWindowToken(key, "ws-a", token, now)
	if err != nil {
		t.Fatal(err)
	}
	if windowIdentity(original) != expectedIdentity {
		t.Fatal("original window identity did not round-trip through token")
	}
	movedWindow := original
	movedWindow.Title = "Renamed window"
	movedWindow.Bounds = image.Rect(100, 200, 900, 800)
	if windowIdentity(movedWindow) != expectedIdentity {
		t.Fatal("moving or renaming a window changed its stable identity")
	}
	changedProcess := original
	changedProcess.ProcessID++
	if windowIdentity(changedProcess) == expectedIdentity {
		t.Fatal("changed process ID retained the original identity")
	}
	changedClass := original
	changedClass.ClassName = "ReusedWindowClass"
	if windowIdentity(changedClass) == expectedIdentity {
		t.Fatal("changed window class retained the original identity")
	}

	raw, err := base64.RawURLEncoding.DecodeString(token[4:])
	if err != nil {
		t.Fatal(err)
	}
	raw[16] ^= 0xff
	forgedIdentity := "win_" + base64.RawURLEncoding.EncodeToString(raw)
	if _, _, err := parseWindowToken(key, "ws-a", forgedIdentity, now); !errors.Is(err, ErrWindowTokenInvalid) {
		t.Fatalf("forged identity token error=%v, want ErrWindowTokenInvalid", err)
	}
}

func protocolCapabilityRequest(capability, action, workspaceID string) protocolv1.CapabilityRequest {
	return protocolv1.CapabilityRequest{
		MessageType: protocolv1.MessageCapabilityRequest,
		RequestId:   "req_screenshot_test_1234567890",
		Capability:  capability,
		Action:      action,
		WorkspaceId: workspaceID,
		Params:      map[string]any{},
	}
}
