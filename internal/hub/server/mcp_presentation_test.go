package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
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
	result := hub.presentationToolResult(context.Background(), "owner-1", structured, true)
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

func TestPresentationToolResultImageOmitsLinkWhenNotRequested(t *testing.T) {
	imageData := append([]byte("\x89PNG\r\n\x1a\n"), []byte("image-data")...)
	relay := newPresentationStore(t.TempDir())
	record := putPresentationForTest(t, relay, "owner-1", "machine-1", "shot.png", "image/png", imageData, time.Now().UTC())
	hub := &Server{config: Config{PublicBaseURL: "https://hub.example/fast-spider"}, presentations: relay}
	structured := map[string]any{"presentationId": record.ID, "publicUrl": "stale"}
	result := hub.presentationToolResult(context.Background(), "owner-1", structured, false)
	if result == nil || len(result.Content) != 1 {
		t.Fatalf("presentation result=%+v", result)
	}
	if _, ok := result.Content[0].(*mcp.ImageContent); !ok {
		t.Fatalf("content type=%T want *mcp.ImageContent", result.Content[0])
	}
	if _, ok := structured["publicUrl"]; ok {
		t.Fatalf("non-share presentation leaked publicUrl=%v", structured["publicUrl"])
	}
}

func TestPresentationToolResultOwnerIsolation(t *testing.T) {
	data := append([]byte("\x89PNG\r\n\x1a\n"), byte(1))
	relay := newPresentationStore(t.TempDir())
	record := putPresentationForTest(t, relay, "owner-1", "machine-1", "shot.png", "image/png", data, time.Now().UTC())
	hub := &Server{config: Config{PublicBaseURL: "https://hub.example"}, presentations: relay}
	if result := hub.presentationToolResult(context.Background(), "owner-2", map[string]any{"presentationId": record.ID}, true); result != nil {
		t.Fatalf("cross-owner presentation result=%+v", result)
	}
}

func TestPresentationToolResultNonImageReturnsLinkOnly(t *testing.T) {
	data := []byte("archive-data")
	relay := newPresentationStore(t.TempDir())
	record := putPresentationForTest(t, relay, "owner-1", "machine-1", "archive.zip", "application/zip", data, time.Now().UTC())
	hub := &Server{config: Config{PublicBaseURL: "https://hub.example"}, presentations: relay}
	result := hub.presentationToolResult(context.Background(), "owner-1", map[string]any{"presentationId": record.ID}, true)
	if result == nil || len(result.Content) != 1 {
		t.Fatalf("presentation result=%+v", result)
	}
	if _, ok := result.Content[0].(*mcp.ResourceLink); !ok {
		t.Fatalf("content type=%T want *mcp.ResourceLink", result.Content[0])
	}
}

func TestDecoratePublishedAttachmentResultReturnsURLOnlyMetadata(t *testing.T) {
	now := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	data := []byte("attachment")
	relay := newPresentationStore(t.TempDir())
	sum := sha256.Sum256(data)
	record, err := relay.putInternal(
		context.Background(),
		store.DeviceSession{OwnerID: "owner-1", MachineID: "machine-1"},
		"attachment.png", "image/png", "sha256:"+hex.EncodeToString(sum[:]), int64(len(data)), bytes.NewReader(data), now, publishedAttachmentTTL,
	)
	if err != nil {
		t.Fatal(err)
	}
	hub := &Server{config: Config{PublicBaseURL: "https://hub.example/fast-spider"}, presentations: relay}
	structured := map[string]any{"presentationId": record.ID, "sha256": record.SHA256, "ignored": "value"}
	if err := hub.decoratePublishedAttachmentResult("owner-1", structured); err != nil {
		t.Fatal(err)
	}
	wantURL := "https://hub.example/fast-spider/api/v1/presentations/" + record.ID
	if got, _ := structured["url"].(string); got != wantURL {
		t.Fatalf("attachment url=%q want=%q", got, wantURL)
	}
	if len(structured) != 5 {
		t.Fatalf("attachment result keys=%v", structured)
	}
	if _, ok := structured["presentationId"]; ok {
		t.Fatalf("attachment result leaked presentationId=%v", structured["presentationId"])
	}
	if got, _ := structured["fileName"].(string); got != "attachment.png" {
		t.Fatalf("attachment fileName=%q", got)
	}
	if expiresAt, ok := structured["expiresAt"].(time.Time); !ok || !expiresAt.Equal(now.Add(publishedAttachmentTTL)) {
		t.Fatalf("attachment expiresAt=%v", structured["expiresAt"])
	}
}

func TestDecoratePublishedAttachmentResultSanitizesOrdinaryResult(t *testing.T) {
	hub := &Server{config: Config{PublicBaseURL: "https://hub.example"}, presentations: newPresentationStore(t.TempDir())}
	structured := map[string]any{"status": "ok", "publicUrl": "https://stale.example/resource"}
	if err := hub.decoratePublishedAttachmentResult("owner-1", structured); err != nil {
		t.Fatal(err)
	}
	if _, ok := structured["publicUrl"]; ok {
		t.Fatalf("ordinary capability result leaked publicUrl=%v", structured["publicUrl"])
	}
	if structured["status"] != "ok" {
		t.Fatalf("ordinary capability result changed=%v", structured)
	}
}

func TestDecoratePublishedAttachmentResultFailsClosedWithoutPublicURL(t *testing.T) {
	now := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	data := []byte("attachment")
	sum := sha256.Sum256(data)
	relay := newPresentationStore(t.TempDir())
	record, err := relay.putInternal(
		context.Background(), store.DeviceSession{OwnerID: "owner-1", MachineID: "machine-1"},
		"attachment.png", "image/png", "sha256:"+hex.EncodeToString(sum[:]), int64(len(data)), bytes.NewReader(data), now, publishedAttachmentTTL,
	)
	if err != nil {
		t.Fatal(err)
	}
	hub := &Server{presentations: relay}
	if err := hub.decoratePublishedAttachmentResult("owner-1", map[string]any{"presentationId": record.ID}); err == nil {
		t.Fatal("missing public base URL unexpectedly accepted")
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

func TestPresentationStoreSupports48HourAttachmentTTL(t *testing.T) {
	now := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	data := []byte("attachment")
	sum := sha256.Sum256(data)
	relay := newPresentationStore(t.TempDir())
	record, err := relay.putInternal(
		context.Background(),
		store.DeviceSession{OwnerID: "owner-1", MachineID: "machine-1"},
		"attachment.bin", "application/octet-stream", "sha256:"+hex.EncodeToString(sum[:]), int64(len(data)), bytes.NewReader(data), now, publishedAttachmentTTL,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !record.ExpiresAt.Equal(now.Add(48 * time.Hour)) {
		t.Fatalf("attachment expiry=%s want=%s", record.ExpiresAt, now.Add(48*time.Hour))
	}
	if _, err := relay.get(record.ID, now.Add(48*time.Hour-time.Second)); err != nil {
		t.Fatalf("attachment expired too early: %v", err)
	}
	relay.cleanupExpired(now.Add(48*time.Hour + time.Second))
	if _, err := relay.get(record.ID, now.Add(48*time.Hour+time.Second)); !errors.Is(err, errPresentationNotFound) {
		t.Fatalf("expired attachment remained readable: %v", err)
	}
}

func TestPresentationStoreRetriesFailedExpiryDeletionWithoutReleasingQuota(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	relay := newPresentationStore(t.TempDir())
	relay.limits = presentationLimits{entries: 1, concurrent: 1, ownerConcurrent: 1, machineConcurrent: 1, globalBytes: 5, ownerBytes: 5, machineBytes: 5}
	record := putPresentationForTest(t, relay, "owner-1", "machine-1", "file.txt", "text/plain", []byte("hello"), now)
	removeCalls := 0
	relay.removeFile = func(path string) error {
		removeCalls++
		if removeCalls == 1 {
			return errors.New("injected remove failure")
		}
		return os.Remove(path)
	}
	expiredAt := now.Add(presentationTTL + time.Second)
	relay.cleanupExpired(expiredAt)
	if _, ok := relay.data[record.ID]; !ok {
		t.Fatal("failed deletion removed the presentation from tracking")
	}
	if _, err := os.Stat(record.Path); err != nil {
		t.Fatalf("failed deletion unexpectedly removed the file: %v", err)
	}
	if relay.storedBytes != int64(len("hello")) || relay.storedOwner["owner-1"] != int64(len("hello")) || relay.storedMachine["machine-1"] != int64(len("hello")) {
		t.Fatalf("failed deletion released accounting: total=%d owner=%d machine=%d", relay.storedBytes, relay.storedOwner["owner-1"], relay.storedMachine["machine-1"])
	}
	relay.cleanupExpired(expiredAt.Add(time.Second))
	if _, ok := relay.data[record.ID]; ok {
		t.Fatal("successful retry retained the presentation record")
	}
	if _, err := os.Stat(record.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful retry retained the file: %v", err)
	}
	body := []byte("x")
	sum := sha256.Sum256(body)
	if _, err := relay.put(store.DeviceSession{OwnerID: "owner-2", MachineID: "machine-2"}, "next.bin", "application/octet-stream", "sha256:"+hex.EncodeToString(sum[:]), 1, bytes.NewReader(body), expiredAt.Add(time.Second)); err != nil {
		t.Fatalf("successful retry did not release quota: %v", err)
	}
}

func TestPresentationStoreFailsClosedUntilStartupCleanupRetrySucceeds(t *testing.T) {
	now := time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "relay")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	stalePath := filepath.Join(root, "stale.bin")
	if err := os.WriteFile(stalePath, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	removeCalls := 0
	relay := newPresentationStoreWithFileOps(root, os.Remove, func(path string) error {
		removeCalls++
		if removeCalls == 1 {
			return errors.New("injected startup cleanup failure")
		}
		return os.RemoveAll(path)
	}, os.MkdirAll)
	if relay.ready {
		t.Fatal("relay became ready after startup cleanup failure")
	}
	body := bytes.NewReader([]byte("x"))
	sum := sha256.Sum256([]byte("x"))
	if _, err := relay.put(store.DeviceSession{OwnerID: "owner-1", MachineID: "machine-1"}, "file.bin", "application/octet-stream", "sha256:"+hex.EncodeToString(sum[:]), 1, body, now); !errors.Is(err, errPresentationUnavailable) {
		t.Fatalf("startup cleanup failure upload error=%v", err)
	}
	if body.Len() != 1 {
		t.Fatal("unavailable relay consumed the upload body")
	}
	if _, err := os.Stat(stalePath); err != nil {
		t.Fatalf("failed startup cleanup unexpectedly removed stale file: %v", err)
	}
	relay.cleanupExpired(now)
	if !relay.ready {
		t.Fatal("maintenance retry did not restore relay readiness")
	}
	if _, err := os.Stat(stalePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("maintenance retry retained stale root content: %v", err)
	}
	if _, err := relay.put(store.DeviceSession{OwnerID: "owner-1", MachineID: "machine-1"}, "file.bin", "application/octet-stream", "sha256:"+hex.EncodeToString(sum[:]), 1, bytes.NewReader([]byte("x")), now); err != nil {
		t.Fatalf("restored relay rejected upload: %v", err)
	}
}

func TestPresentationStoreClearRetainsTrackingUntilRootCleanupRetrySucceeds(t *testing.T) {
	now := time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)
	relay := newPresentationStore(t.TempDir())
	record := putPresentationForTest(t, relay, "owner-1", "machine-1", "file.bin", "application/octet-stream", []byte("hello"), now)
	removeCalls := 0
	relay.removeAll = func(path string) error {
		removeCalls++
		if removeCalls == 1 {
			return errors.New("injected clear cleanup failure")
		}
		return os.RemoveAll(path)
	}
	relay.clear()
	if relay.ready {
		t.Fatal("relay remained ready after clear cleanup failure")
	}
	if _, ok := relay.data[record.ID]; !ok || relay.storedBytes != record.SizeBytes {
		t.Fatalf("clear failure lost tracking: present=%v stored=%d", ok, relay.storedBytes)
	}
	if _, err := os.Stat(record.Path); err != nil {
		t.Fatalf("clear failure unexpectedly removed presentation: %v", err)
	}
	relay.cleanupExpired(now)
	if !relay.ready {
		t.Fatal("maintenance retry did not restore relay after clear failure")
	}
	if len(relay.data) != 0 || relay.storedBytes != 0 {
		t.Fatalf("successful clear retry retained tracking: records=%d stored=%d", len(relay.data), relay.storedBytes)
	}
	if _, err := os.Stat(record.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful clear retry retained presentation file: %v", err)
	}
}

func TestPresentationStoreEnforcesAggregateQuotasBeforeReadingBody(t *testing.T) {
	now := time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		limits     presentationLimits
		firstOwner string
		firstNode  string
		nextOwner  string
		nextNode   string
	}{
		{
			name:       "machine bytes",
			limits:     presentationLimits{entries: 4, concurrent: 2, ownerConcurrent: 2, machineConcurrent: 2, globalBytes: 32, ownerBytes: 32, machineBytes: 4},
			firstOwner: "owner-1", firstNode: "machine-1", nextOwner: "owner-1", nextNode: "machine-1",
		},
		{
			name:       "owner bytes",
			limits:     presentationLimits{entries: 4, concurrent: 2, ownerConcurrent: 2, machineConcurrent: 2, globalBytes: 32, ownerBytes: 4, machineBytes: 32},
			firstOwner: "owner-1", firstNode: "machine-1", nextOwner: "owner-1", nextNode: "machine-2",
		},
		{
			name:       "global bytes",
			limits:     presentationLimits{entries: 4, concurrent: 2, ownerConcurrent: 2, machineConcurrent: 2, globalBytes: 4, ownerBytes: 32, machineBytes: 32},
			firstOwner: "owner-1", firstNode: "machine-1", nextOwner: "owner-2", nextNode: "machine-2",
		},
		{
			name:       "entry count",
			limits:     presentationLimits{entries: 1, concurrent: 2, ownerConcurrent: 2, machineConcurrent: 2, globalBytes: 32, ownerBytes: 32, machineBytes: 32},
			firstOwner: "owner-1", firstNode: "machine-1", nextOwner: "owner-2", nextNode: "machine-2",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			relay := newPresentationStore(t.TempDir())
			relay.limits = testCase.limits
			putPresentationForTest(t, relay, testCase.firstOwner, testCase.firstNode, "first.bin", "application/octet-stream", []byte("1234"), now)
			body := bytes.NewReader([]byte("x"))
			sum := sha256.Sum256([]byte("x"))
			_, err := relay.put(store.DeviceSession{OwnerID: testCase.nextOwner, MachineID: testCase.nextNode}, "next.bin", "application/octet-stream", "sha256:"+hex.EncodeToString(sum[:]), 1, body, now)
			if !errors.Is(err, errPresentationQuota) {
				t.Fatalf("quota error=%v", err)
			}
			if body.Len() != 1 {
				t.Fatalf("quota rejection consumed %d body bytes", 1-body.Len())
			}
		})
	}
}

func TestPresentationStoreConcurrentReservationAndExpiryReleaseCapacity(t *testing.T) {
	now := time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)
	relay := newPresentationStore(t.TempDir())
	relay.limits = presentationLimits{entries: 2, concurrent: 1, ownerConcurrent: 1, machineConcurrent: 1, globalBytes: 8, ownerBytes: 8, machineBytes: 8}
	first, err := relay.reserve(store.DeviceSession{OwnerID: "owner-1", MachineID: "machine-1"}, 4, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relay.reserve(store.DeviceSession{OwnerID: "owner-2", MachineID: "machine-2"}, 1, now); !errors.Is(err, errPresentationQuota) {
		t.Fatalf("concurrent reservation error=%v", err)
	}
	first.release()

	record := putPresentationForTest(t, relay, "owner-1", "machine-1", "first.bin", "application/octet-stream", []byte("12345678"), now)
	body := bytes.NewReader([]byte("x"))
	sum := sha256.Sum256([]byte("x"))
	if _, err := relay.put(store.DeviceSession{OwnerID: "owner-2", MachineID: "machine-2"}, "next.bin", "application/octet-stream", "sha256:"+hex.EncodeToString(sum[:]), 1, body, now); !errors.Is(err, errPresentationQuota) {
		t.Fatalf("full relay error=%v", err)
	}
	relay.cleanupExpired(now.Add(presentationTTL + time.Second))
	if _, err := relay.get(record.ID, now.Add(presentationTTL+time.Second)); !errors.Is(err, errPresentationNotFound) {
		t.Fatalf("expired record error=%v", err)
	}
	if _, err := relay.put(store.DeviceSession{OwnerID: "owner-2", MachineID: "machine-2"}, "next.bin", "application/octet-stream", "sha256:"+hex.EncodeToString(sum[:]), 1, bytes.NewReader([]byte("x")), now.Add(presentationTTL+time.Second)); err != nil {
		t.Fatalf("capacity was not released after expiry: %v", err)
	}
}

func TestPresentationStoreEnforcesOwnerAndMachineConcurrencyFairness(t *testing.T) {
	now := time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)
	relay := newPresentationStore(t.TempDir())
	relay.limits = presentationLimits{
		entries:           8,
		concurrent:        8,
		ownerConcurrent:   4,
		machineConcurrent: 2,
		globalBytes:       64,
		ownerBytes:        64,
		machineBytes:      64,
	}

	reserve := func(ownerID, machineID string) *presentationReservation {
		t.Helper()
		reservation, err := relay.reserve(store.DeviceSession{OwnerID: ownerID, MachineID: machineID}, 1, now)
		if err != nil {
			t.Fatalf("reserve %s/%s: %v", ownerID, machineID, err)
		}
		return reservation
	}
	reservations := []*presentationReservation{
		reserve("owner-1", "machine-1"),
		reserve("owner-1", "machine-1"),
	}
	if _, err := relay.reserve(store.DeviceSession{OwnerID: "owner-1", MachineID: "machine-1"}, 1, now); !errors.Is(err, errPresentationQuota) {
		t.Fatalf("machine concurrency error=%v", err)
	}
	reservations = append(reservations,
		reserve("owner-1", "machine-2"),
		reserve("owner-1", "machine-2"),
	)
	if _, err := relay.reserve(store.DeviceSession{OwnerID: "owner-1", MachineID: "machine-3"}, 1, now); !errors.Is(err, errPresentationQuota) {
		t.Fatalf("owner concurrency error=%v", err)
	}
	reservations = append(reservations, reserve("owner-2", "machine-9"))
	for _, reservation := range reservations {
		reservation.release()
	}
}

func TestPresentationStoreTimeoutClosesBodyAndReleasesCapacity(t *testing.T) {
	now := time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)
	relay := newPresentationStore(t.TempDir())
	relay.limits = presentationLimits{
		entries:           1,
		concurrent:        1,
		ownerConcurrent:   1,
		machineConcurrent: 1,
		globalBytes:       8,
		ownerBytes:        8,
		machineBytes:      8,
	}
	body := newBlockingPresentationBody()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	sum := sha256.Sum256([]byte("x"))
	_, err := relay.putContext(
		ctx,
		store.DeviceSession{OwnerID: "owner-1", MachineID: "machine-1"},
		"slow.bin",
		"application/octet-stream",
		"sha256:"+hex.EncodeToString(sum[:]),
		1,
		body,
		now,
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error=%v", err)
	}
	select {
	case <-body.closed:
	default:
		t.Fatal("timed-out upload body was not closed")
	}
	if _, err := relay.put(
		store.DeviceSession{OwnerID: "owner-1", MachineID: "machine-1"},
		"next.bin",
		"application/octet-stream",
		"sha256:"+hex.EncodeToString(sum[:]),
		1,
		bytes.NewReader([]byte("x")),
		now,
	); err != nil {
		t.Fatalf("timeout did not release presentation capacity: %v", err)
	}
	entries, err := os.ReadDir(relay.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".upload" {
			t.Fatalf("timed-out upload retained temporary file %q", entry.Name())
		}
	}
}

type blockingPresentationBody struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingPresentationBody() *blockingPresentationBody {
	return &blockingPresentationBody{closed: make(chan struct{})}
}

func (b *blockingPresentationBody) Read(_ []byte) (int, error) {
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *blockingPresentationBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestPresentationToolResultIgnoresOrdinaryCapabilityResults(t *testing.T) {
	hub := &Server{config: Config{PublicBaseURL: "https://hub.example"}, presentations: newPresentationStore(t.TempDir())}
	structured := map[string]any{"status": "ok", "publicUrl": "https://stale.example/resource"}
	if result := hub.presentationToolResult(context.Background(), "owner-1", structured, false); result != nil {
		t.Fatalf("unexpected presentation result=%+v", result)
	}
	if _, ok := structured["publicUrl"]; ok {
		t.Fatalf("ordinary capability result leaked publicUrl=%v", structured["publicUrl"])
	}
}

func TestPresentationToolResultInvalidPresentationOmitsLinkWhenNotRequested(t *testing.T) {
	hub := &Server{config: Config{PublicBaseURL: "https://hub.example"}, presentations: newPresentationStore(t.TempDir())}
	structured := map[string]any{"presentationId": "missing", "publicUrl": "https://stale.example/resource"}
	if result := hub.presentationToolResult(context.Background(), "owner-1", structured, false); result != nil {
		t.Fatalf("unexpected presentation result=%+v", result)
	}
	if _, ok := structured["publicUrl"]; ok {
		t.Fatalf("invalid presentation leaked publicUrl=%v", structured["publicUrl"])
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
