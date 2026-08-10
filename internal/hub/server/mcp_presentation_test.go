package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPresentationToolResultCreatesImageAndHubResourceLink(t *testing.T) {
	imageData := append([]byte("\x89PNG\r\n\x1a\n"), []byte("image-data")...)
	relay := newPresentationStore(t.TempDir())
	record := putPresentationForTest(t, relay, "owner-1", "machine-1", "shot.png", "image/png", imageData, time.Now().UTC())
	hub := &Server{config: Config{PublicBaseURL: "https://hub.example/fast-spider"}, presentations: relay}
	structured := map[string]any{"presentationId": record.ID}
	result := hub.presentationToolResult(context.Background(), "owner-1", structured)
	if result == nil || len(result.Content) != 2 {
		t.Fatalf("presentation result=%+v", result)
	}
	image, ok := result.Content[0].(*mcp.ImageContent)
	if !ok || image.MIMEType != "image/png" || !bytes.Equal(image.Data, imageData) {
		t.Fatalf("image content=%T %+v", result.Content[0], image)
	}
	link, ok := result.Content[1].(*mcp.ResourceLink)
	if !ok {
		t.Fatalf("second content type=%T want *mcp.ResourceLink", result.Content[1])
	}
	wantURL := "https://hub.example/fast-spider/api/v1/presentations/" + record.ID
	if link.URI != wantURL || link.Name != "shot.png" || link.MIMEType != "image/png" {
		t.Fatalf("resource link=%+v", link)
	}
	if got, _ := structured["publicUrl"].(string); got != wantURL {
		t.Fatalf("structured publicUrl=%q want=%q", got, wantURL)
	}
}

func TestPresentationToolResultOwnerIsolation(t *testing.T) {
	data := append([]byte("\x89PNG\r\n\x1a\n"), byte(1))
	relay := newPresentationStore(t.TempDir())
	record := putPresentationForTest(t, relay, "owner-1", "machine-1", "shot.png", "image/png", data, time.Now().UTC())
	hub := &Server{config: Config{PublicBaseURL: "https://hub.example"}, presentations: relay}
	if result := hub.presentationToolResult(context.Background(), "owner-2", map[string]any{"presentationId": record.ID}); result != nil {
		t.Fatalf("cross-owner presentation result=%+v", result)
	}
}

func TestPresentationToolResultNonImageReturnsLinkOnly(t *testing.T) {
	data := []byte("archive-data")
	relay := newPresentationStore(t.TempDir())
	record := putPresentationForTest(t, relay, "owner-1", "machine-1", "archive.zip", "application/zip", data, time.Now().UTC())
	hub := &Server{config: Config{PublicBaseURL: "https://hub.example"}, presentations: relay}
	result := hub.presentationToolResult(context.Background(), "owner-1", map[string]any{"presentationId": record.ID})
	if result == nil || len(result.Content) != 1 {
		t.Fatalf("presentation result=%+v", result)
	}
	if _, ok := result.Content[0].(*mcp.ResourceLink); !ok {
		t.Fatalf("content type=%T want *mcp.ResourceLink", result.Content[0])
	}
}

func TestPresentationStoreExpiresAndDeletesTemporaryFile(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	relay := newPresentationStore(t.TempDir())
	record := putPresentationForTest(t, relay, "owner-1", "machine-1", "file.txt", "text/plain", []byte("hello"), now)
	if _, err := relay.get(record.ID, now.Add(presentationTTL-time.Second)); err != nil {
		t.Fatalf("presentation expired too early: %v", err)
	}
	relay.cleanupExpired(now.Add(presentationTTL + time.Second))
	if _, err := relay.get(record.ID, now.Add(presentationTTL+time.Second)); err == nil {
		t.Fatal("expired presentation remained readable")
	}
}

func TestPresentationToolResultIgnoresOrdinaryCapabilityResults(t *testing.T) {
	hub := &Server{config: Config{PublicBaseURL: "https://hub.example"}, presentations: newPresentationStore(t.TempDir())}
	if result := hub.presentationToolResult(context.Background(), "owner-1", map[string]any{"status": "ok"}); result != nil {
		t.Fatalf("unexpected presentation result=%+v", result)
	}
}

func putPresentationForTest(t *testing.T, relay *presentationStore, ownerID, machineID, name, contentType string, data []byte, now time.Time) presentationRecord {
	t.Helper()
	sum := sha256.Sum256(data)
	record, err := relay.put(store.DeviceSession{OwnerID: ownerID, MachineID: machineID}, name, contentType, "sha256:"+hex.EncodeToString(sum[:]), int64(len(data)), bytes.NewReader(data), now)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
