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

func TestScreenshotCaptureUsesMachineBoundary(t *testing.T) {
	client, err := New(Config{DataDir: t.TempDir(), Version: "screenshot-test"})
	if err != nil {
		t.Fatal(err)
	}
	response := client.handleCapabilityRequest(context.Background(), protocolCapabilityRequest("screenshot.capture", "listDisplays"))
	if response.Error != nil && response.Error.Code != "SCREENSHOT_UNAVAILABLE" {
		t.Fatalf("screenshot=%+v", response.Error)
	}
}

func TestScreenshotCaptureRejectsInvalidParameters(t *testing.T) {
	client, err := New(Config{DataDir: t.TempDir(), Version: "screenshot-test"})
	if err != nil {
		t.Fatal(err)
	}
	displays, err := activeDisplays()
	if err != nil {
		t.Skipf("desktop unavailable: %v", err)
	}
	if _, err := client.screenshotCapture(context.Background(), "display", map[string]any{"displayIndex": len(displays)}); err == nil || !strings.Contains(err.Error(), "displayIndex") {
		t.Fatalf("displayIndex error=%v", err)
	}
	if _, err := client.screenshotCapture(context.Background(), "desktop", map[string]any{"format": "gif"}); err == nil || !strings.Contains(err.Error(), "format") {
		t.Fatalf("format error=%v", err)
	}
	if _, err := client.screenshotCapture(context.Background(), "desktop", map[string]any{"format": "jpeg", "quality": 10}); err == nil || !strings.Contains(err.Error(), "quality") {
		t.Fatalf("quality error=%v", err)
	}
}

func TestScreenshotSemaphoreDoesNotBlockFileRead(t *testing.T) {
	dataDir := t.TempDir()
	client, err := New(Config{DataDir: dataDir, Version: "screenshot-test"})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "read.txt")
	if err := os.WriteFile(path, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	client.screenshotSem <- struct{}{}
	defer func() { <-client.screenshotSem }()
	captureCtx, cancel := context.WithCancel(context.Background())
	captureErr := make(chan error, 1)
	go func() {
		_, err := client.captureRectArtifact(captureCtx, "desktop", screenshotCaptureParams{}, image.Rect(0, 0, 1, 1))
		captureErr <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if _, err := client.fileRead(context.Background(), map[string]any{"path": path}); err != nil {
		t.Fatalf("file read blocked: %v", err)
	}
	cancel()
	select {
	case err := <-captureErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("screenshot cancellation timeout")
	}
}

func TestWindowTokenRejectsExpiredAndTamperedTokens(t *testing.T) {
	key := windowTokenKey([]byte("window-token-test-private-key"))
	now := time.Unix(1_750_000_000, 0).UTC()
	identity := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	token := makeWindowToken(key, 1234, identity, now)
	if handle, got, err := parseWindowToken(key, token, now); err != nil || handle != 1234 || got != identity {
		t.Fatalf("valid token handle=%d identity=%v err=%v", handle, got, err)
	}
	if _, _, err := parseWindowToken(key, token+"tampered", now); !errors.Is(err, ErrWindowTokenInvalid) {
		t.Fatalf("tampered error=%v", err)
	}
	expired := makeWindowToken(key, 1234, identity, now.Add(-windowTokenTTL-time.Second))
	if _, _, err := parseWindowToken(key, expired, now); !errors.Is(err, ErrWindowTokenInvalid) {
		t.Fatalf("expired error=%v", err)
	}
}

func TestWindowTokenBindsWindowIdentity(t *testing.T) {
	key := windowTokenKey([]byte("window-token-identity-test"))
	now := time.Unix(1_750_000_000, 0).UTC()
	original := nativeWindowInfo{Handle: 1234, ProcessID: 4242, ClassName: "FastSpiderWindow", Title: "Fast Spider", Bounds: image.Rect(10, 20, 810, 620)}
	identity := windowIdentity(original)
	token := makeWindowToken(key, original.Handle, identity, now)
	_, expected, err := parseWindowToken(key, token, now)
	if err != nil {
		t.Fatal(err)
	}
	if windowIdentity(original) != expected {
		t.Fatal("identity did not round trip")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token[4:])
	if err != nil {
		t.Fatal(err)
	}
	raw[16] ^= 0xff
	if _, _, err := parseWindowToken(key, "win_"+base64.RawURLEncoding.EncodeToString(raw), now); !errors.Is(err, ErrWindowTokenInvalid) {
		t.Fatalf("forged error=%v", err)
	}
}

func protocolCapabilityRequest(capability, action string) protocolv1.CapabilityRequest {
	return protocolv1.CapabilityRequest{MessageType: protocolv1.MessageCapabilityRequest, RequestId: "req_screenshot_test_1234567890", Capability: capability, Action: action, Params: map[string]any{}}
}
