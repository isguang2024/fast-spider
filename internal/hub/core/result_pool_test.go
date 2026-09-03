package core

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/store"
	"github.com/isguang2024/fast-spider/internal/security"
)

func TestResultPoolLifecycleAndArtifactBinding(t *testing.T) {
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
	session, _ := artifactTestSession(t, ctx, service)

	data := []byte("result page")
	upload, err := service.CreateArtifactUpload(ctx, session, ArtifactCreateRequest{LogicalName: "page.txt", ContentType: "text/plain", SizeBytes: int64(len(data)), SHA256: artifactTestSHA(data)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UploadArtifactChunk(ctx, session.MachineID, upload.UploadID, 0, data); err != nil {
		t.Fatal(err)
	}
	artifact, err := service.CompleteArtifactUpload(ctx, session, upload.UploadID)
	if err != nil {
		t.Fatal(err)
	}

	created, err := service.CreateResult(ctx, session, ResultCreateRequest{IdempotencyKey: "result-idempotency-001", RequestHash: "sha256:request-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.ResultID) != len("res_")+22 || created.Status != "open" {
		t.Fatalf("created=%+v", created)
	}
	if repeated, err := service.CreateResult(ctx, session, ResultCreateRequest{IdempotencyKey: "result-idempotency-001", RequestHash: "sha256:request-1"}); err != nil || repeated.ResultID != created.ResultID {
		t.Fatalf("idempotent create=%+v err=%v", repeated, err)
	}
	if _, err := service.CreateResult(ctx, session, ResultCreateRequest{IdempotencyKey: "result-idempotency-001", RequestHash: "sha256:request-2"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("request hash mismatch=%v", err)
	}

	attached, err := service.AttachResultPage(ctx, session, created.ResultID, ResultAttachPageRequest{PageNo: 0, ArtifactID: artifact.ID, ExpectedRevision: created.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if attached.Revision != created.Revision+1 {
		t.Fatalf("attached=%+v", attached)
	}
	if _, err := service.AttachResultPage(ctx, session, created.ResultID, ResultAttachPageRequest{PageNo: 0, ArtifactID: artifact.ID, ExpectedRevision: created.Revision}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale attach=%v", err)
	}
	ready, err := service.CommitResult(ctx, session, created.ResultID, ResultCommitRequest{ExpectedRevision: attached.Revision, Manifest: []byte(`{"summary":"done"}`)})
	if err != nil || ready.Status != "ready" {
		t.Fatalf("commit=%+v err=%v", ready, err)
	}
	if _, err := service.CommitResult(ctx, session, created.ResultID, ResultCommitRequest{ExpectedRevision: ready.Revision, Manifest: []byte(`{"summary":"changed"}`)}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("ready result changed: %v", err)
	}
	page, file, err := service.ReadResultPage(ctx, session.OwnerID, created.ResultID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(got, data) || page.ArtifactID != artifact.ID {
		t.Fatalf("page=%+v bytes=%q err=%v", page, got, err)
	}
	if _, err := service.CommitResult(ctx, session, created.ResultID, ResultCommitRequest{ExpectedRevision: ready.Revision, Manifest: []byte(`{"page":{"artifactId":"leak"}}`)}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("manifest artifact id accepted: %v", err)
	}
}

func TestResultPoolIdempotencyCannotCrossMachinesForOneOwner(t *testing.T) {
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
	first, ownerID := artifactTestSession(t, ctx, service)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := service.RegisterMachine(ctx, ownerID, MachineRegistrationRequest{
		DisplayName: "second-result-node", OS: "linux", Arch: "amd64", NodeVersion: "test", PublicKey: security.EncodePublicKey(pub),
	}, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	second := store.DeviceSession{OwnerID: ownerID, MachineID: registered.MachineID}
	if _, err := service.CreateResult(ctx, first, ResultCreateRequest{IdempotencyKey: "cross-machine-key-001", RequestHash: "sha256:cross-machine"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateResult(ctx, second, ResultCreateRequest{IdempotencyKey: "cross-machine-key-001", RequestHash: "sha256:cross-machine"}); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatalf("cross-machine idempotency was accepted: %v", err)
	}
}
