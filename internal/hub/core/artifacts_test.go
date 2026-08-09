package core

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/store"
	"github.com/isguang2024/fast-spider/internal/security"
)

func TestArtifactUploadIntegrityAndCleanup(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := New(st, registry.New(), Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	session, ownerID := artifactTestSession(t, ctx, service)

	data := []byte("artifact-data\n")
	correctHash := artifactTestSHA(data)
	wrongHash := artifactTestSHA([]byte("different"))
	bad, err := service.CreateArtifactUpload(ctx, session, ArtifactCreateRequest{LogicalName: "bad.log", ContentType: "text/plain", SizeBytes: int64(len(data)), SHA256: wrongHash})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UploadArtifactChunk(ctx, session.MachineID, bad.UploadID, 1, data); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("wrong offset error=%v", err)
	}
	if _, err := service.UploadArtifactChunk(ctx, session.MachineID, bad.UploadID, 0, data); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteArtifactUpload(ctx, session, bad.UploadID); err == nil {
		t.Fatal("hash mismatch upload unexpectedly completed")
	} else {
		var callErr *CapabilityCallError
		if !errors.As(err, &callErr) || callErr.Code != "HASH_MISMATCH" {
			t.Fatalf("hash mismatch error=%v", err)
		}
	}

	good, err := service.CreateArtifactUpload(ctx, session, ArtifactCreateRequest{JobID: "job_test", LogicalName: "good.log", ContentType: "text/plain; charset=utf-8", SizeBytes: int64(len(data)), SHA256: correctHash})
	if err != nil {
		t.Fatal(err)
	}
	half := len(data) / 2
	if received, err := service.UploadArtifactChunk(ctx, session.MachineID, good.UploadID, 0, data[:half]); err != nil || received != int64(half) {
		t.Fatalf("first chunk received=%d err=%v", received, err)
	}
	resumed, err := service.CreateArtifactUpload(ctx, session, ArtifactCreateRequest{JobID: "job_test", LogicalName: "good.log", ContentType: "text/plain; charset=utf-8", SizeBytes: int64(len(data)), SHA256: correctHash})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.UploadID != good.UploadID || resumed.ArtifactID != good.ArtifactID || resumed.ReceivedBytes != int64(half) {
		t.Fatalf("resume result=%+v original=%+v", resumed, good)
	}
	if received, err := service.UploadArtifactChunk(ctx, session.MachineID, resumed.UploadID, resumed.ReceivedBytes, data[half:]); err != nil || received != int64(len(data)) {
		t.Fatalf("second chunk received=%d err=%v", received, err)
	}
	artifact, err := service.CompleteArtifactUpload(ctx, session, good.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Status != "complete" || artifact.SHA256 != correctHash || artifact.StorageKey == "" {
		t.Fatalf("completed artifact=%+v", artifact)
	}
	completedAgain, err := service.CompleteArtifactUpload(ctx, session, good.UploadID)
	if err != nil {
		t.Fatalf("idempotent complete error=%v", err)
	}
	if completedAgain.ID != artifact.ID || completedAgain.StorageKey != artifact.StorageKey || completedAgain.Status != "complete" {
		t.Fatalf("idempotent complete returned different artifact: first=%+v again=%+v", artifact, completedAgain)
	}
	got, file, err := service.OpenArtifact(ctx, ownerID, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(data) || got.ID != artifact.ID {
		t.Fatalf("artifact content=%q metadata=%+v", raw, got)
	}
	blobPath, err := service.artifactBlobPath(artifact.StorageKey)
	if err != nil {
		t.Fatal(err)
	}
	duplicateUpload, err := service.CreateArtifactUpload(ctx, session, ArtifactCreateRequest{LogicalName: "duplicate.log", ContentType: "text/plain; charset=utf-8", SizeBytes: int64(len(data)), SHA256: correctHash})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UploadArtifactChunk(ctx, session.MachineID, duplicateUpload.UploadID, 0, data); err != nil {
		t.Fatal(err)
	}
	duplicate, err := service.CompleteArtifactUpload(ctx, session, duplicateUpload.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ID == artifact.ID || duplicate.StorageKey != artifact.StorageKey {
		t.Fatalf("content-addressed dedup failed: first=%+v duplicate=%+v", artifact, duplicate)
	}

	var activeUploads []string
	for i := 0; i < MaxActiveArtifactUploads; i++ {
		created, err := service.CreateArtifactUpload(ctx, session, ArtifactCreateRequest{LogicalName: fmt.Sprintf("quota-%d.log", i), ContentType: "text/plain", SizeBytes: int64(len(data)), SHA256: correctHash})
		if err != nil {
			t.Fatalf("create quota upload %d: %v", i, err)
		}
		activeUploads = append(activeUploads, created.UploadID)
	}
	if _, err := service.CreateArtifactUpload(ctx, session, ArtifactCreateRequest{LogicalName: "quota-over.log", ContentType: "text/plain", SizeBytes: int64(len(data)), SHA256: correctHash}); err == nil {
		t.Fatal("fifth active artifact upload unexpectedly succeeded")
	} else {
		var callErr *CapabilityCallError
		if !errors.As(err, &callErr) || callErr.Code != "ARTIFACT_QUOTA_EXCEEDED" {
			t.Fatalf("artifact quota error=%v", err)
		}
	}
	for _, uploadID := range activeUploads {
		if err := service.AbortArtifactUpload(ctx, session, uploadID); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(blobPath, data[:len(data)-1], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.OpenArtifact(ctx, ownerID, artifact.ID); err == nil {
		t.Fatal("artifact with blob size mismatch was opened")
	} else {
		var callErr *CapabilityCallError
		if !errors.As(err, &callErr) || callErr.Code != "ARTIFACT_BLOB_CORRUPT" {
			t.Fatalf("blob size mismatch error=%v", err)
		}
	}

	service.cleanupArtifacts(ctx, time.Now().UTC().Add(31*24*time.Hour))
	if _, err := service.GetArtifact(ctx, ownerID, artifact.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired artifact lookup error=%v", err)
	}
	if _, err := os.Stat(blobPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired artifact blob still exists or stat failed unexpectedly: %v", err)
	}
}

func TestArtifactResumeReuseQuotaAndLifecycle(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := New(st, registry.New(), Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	session, ownerID := artifactTestSession(t, ctx, service)

	data := []byte("resumable-artifact")
	req := ArtifactCreateRequest{JobID: "job_resume", LogicalName: "resume.txt", ContentType: "text/plain", SizeBytes: int64(len(data)), SHA256: artifactTestSHA(data)}
	first, err := service.CreateArtifactUpload(ctx, session, req)
	if err != nil {
		t.Fatal(err)
	}
	half := len(data) / 2
	if received, err := service.UploadArtifactChunk(ctx, session.MachineID, first.UploadID, 0, data[:half]); err != nil || received != int64(half) {
		t.Fatalf("first resume chunk received=%d err=%v", received, err)
	}
	resumed, err := service.CreateArtifactUpload(ctx, session, req)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.UploadID != first.UploadID || resumed.ArtifactID != first.ArtifactID || resumed.ReceivedBytes != int64(half) {
		t.Fatalf("same request did not reuse upload: first=%+v resumed=%+v", first, resumed)
	}
	if received, err := service.UploadArtifactChunk(ctx, session.MachineID, first.UploadID, 0, data[half:]); !errors.Is(err, store.ErrConflict) || received != int64(half) {
		t.Fatalf("offset conflict received=%d err=%v", received, err)
	}
	if received, err := service.UploadArtifactChunk(ctx, session.MachineID, first.UploadID, int64(half), data[half:]); err != nil || received != int64(len(data)) {
		t.Fatalf("resume chunk received=%d err=%v", received, err)
	}
	if _, err := service.GetArtifact(ctx, ownerID, first.ArtifactID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("uploading artifact was remotely readable: %v", err)
	}
	completed, err := service.CompleteArtifactUpload(ctx, session, first.UploadID)
	if err != nil {
		t.Fatal(err)
	}

	usage, err := st.ArtifactUsage(ctx, ownerID, session.MachineID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if usage.ActiveUploads != 0 || usage.MachineBytes < int64(len(data)) || usage.OwnerBytes < int64(len(data)) {
		t.Fatalf("artifact usage after completion=%+v", usage)
	}

	reuseReq := req
	reuseReq.LogicalName = "reuse.txt"
	reuse, err := service.CreateArtifactUpload(ctx, session, reuseReq)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UploadArtifactChunk(ctx, session.MachineID, reuse.UploadID, 0, data); err != nil {
		t.Fatal(err)
	}
	reused, err := service.CompleteArtifactUpload(ctx, session, reuse.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	if reused.StorageKey != completed.StorageKey {
		t.Fatalf("same blob was not reused: completed=%q reused=%q", completed.StorageKey, reused.StorageKey)
	}

	blobPath, err := service.artifactBlobPath(completed.StorageKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blobPath, bytes.Repeat([]byte("x"), len(data)), 0o600); err != nil {
		t.Fatal(err)
	}
	corruptReq := reuseReq
	corruptReq.LogicalName = "corrupt-reuse.txt"
	corrupt, err := service.CreateArtifactUpload(ctx, session, corruptReq)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UploadArtifactChunk(ctx, session.MachineID, corrupt.UploadID, 0, data); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteArtifactUpload(ctx, session, corrupt.UploadID); err == nil {
		t.Fatal("corrupt existing blob was accepted")
	} else {
		var callErr *CapabilityCallError
		if !errors.As(err, &callErr) || callErr.Code != "ARTIFACT_BLOB_CORRUPT" {
			t.Fatalf("corrupt existing blob error=%v", err)
		}
	}

	stale, err := service.CreateArtifactUpload(ctx, session, ArtifactCreateRequest{LogicalName: "stale.txt", ContentType: "text/plain", SizeBytes: 0, SHA256: artifactTestSHA(nil)})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return stale.ExpiresAt.Add(time.Second) }
	if _, err := service.GetArtifact(ctx, ownerID, stale.ArtifactID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired uploading artifact was remotely readable: %v", err)
	}
	if _, err := service.UploadArtifactChunk(ctx, session.MachineID, stale.UploadID, 0, []byte("x")); !errors.Is(err, store.ErrExpired) {
		t.Fatalf("expired upload chunk error=%v", err)
	}
	if _, err := service.CompleteArtifactUpload(ctx, session, stale.UploadID); !errors.Is(err, store.ErrExpired) {
		t.Fatalf("complete after upload expiry error=%v", err)
	}
	service.cleanupArtifacts(ctx, stale.ExpiresAt.Add(time.Second))
	usage, err = st.ArtifactUsage(ctx, ownerID, session.MachineID, stale.ExpiresAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if usage.ActiveUploads != 0 {
		t.Fatalf("cleanup did not release active upload quota: %+v", usage)
	}

	service.now = time.Now
	for i := 0; i < MaxActiveArtifactUploads; i++ {
		if _, err := service.CreateArtifactUpload(ctx, session, ArtifactCreateRequest{LogicalName: fmt.Sprintf("active-%d.txt", i), ContentType: "text/plain", SizeBytes: 0, SHA256: artifactTestSHA(nil)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.CreateArtifactUpload(ctx, session, ArtifactCreateRequest{LogicalName: "active-over-limit.txt", ContentType: "text/plain", SizeBytes: 0, SHA256: artifactTestSHA(nil)}); err == nil {
		t.Fatal("active upload quota was not enforced")
	} else {
		var callErr *CapabilityCallError
		if !errors.As(err, &callErr) || callErr.Code != "ARTIFACT_QUOTA_EXCEEDED" {
			t.Fatalf("active upload quota error=%v", err)
		}
	}
}

func artifactTestSession(t *testing.T, ctx context.Context, service *Service) (store.DeviceSession, string) {
	t.Helper()
	bootstrap, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	account, err := service.BootstrapAccount(ctx, bootstrap, "owner", "Owner", "correct horse battery staple", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := service.RegisterMachine(ctx, account.OwnerID, MachineRegistrationRequest{
		DisplayName: "artifact-node", OS: "windows", Arch: "amd64", NodeVersion: "test", PublicKey: security.EncodePublicKey(pub),
	}, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return store.DeviceSession{MachineID: registered.MachineID, OwnerID: account.OwnerID}, account.OwnerID
}

func artifactTestSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
