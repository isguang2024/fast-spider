package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/store"
	"github.com/isguang2024/fast-spider/internal/security"
)

const (
	MaxArtifactBytes        int64 = 100 << 20
	MaxArtifactChunkBytes         = 1 << 20
	MaxActiveArtifactUploads      = 4
	MaxMachineArtifactBytes int64 = 512 << 20
	MaxOwnerArtifactBytes   int64 = 2 << 30
	artifactUploadTTL             = 30 * time.Minute
	artifactRetention             = 30 * 24 * time.Hour
)

var artifactUploadMu sync.Mutex

type ArtifactCreateRequest struct {
	WorkspaceID string `json:"workspaceId,omitempty"`
	JobID       string `json:"jobId,omitempty"`
	LogicalName string `json:"logicalName"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
	SHA256      string `json:"sha256"`
}

type ArtifactCreateResult struct {
	ArtifactID    string    `json:"artifactId"`
	UploadID      string    `json:"uploadId"`
	ChunkBytes    int       `json:"chunkBytes"`
	ReceivedBytes int64     `json:"receivedBytes"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

func (s *Service) CreateArtifactUpload(ctx context.Context, session store.DeviceSession, req ArtifactCreateRequest) (ArtifactCreateResult, error) {
	artifactUploadMu.Lock()
	defer artifactUploadMu.Unlock()

	req.LogicalName = strings.TrimSpace(req.LogicalName)
	req.ContentType = strings.TrimSpace(req.ContentType)
	req.SHA256 = strings.ToLower(strings.TrimSpace(req.SHA256))
	if req.LogicalName == "" || len(req.LogicalName) > 255 || strings.ContainsAny(req.LogicalName, "\x00\r\n/\\") {
		return ArtifactCreateResult{}, store.ErrConflict
	}
	if req.SizeBytes < 0 || req.SizeBytes > MaxArtifactBytes {
		return ArtifactCreateResult{}, store.ErrConflict
	}
	if !validArtifactSHA256(req.SHA256) {
		return ArtifactCreateResult{}, store.ErrConflict
	}
	if req.ContentType == "" {
		req.ContentType = "application/octet-stream"
	}
	if len(req.ContentType) > 128 {
		return ArtifactCreateResult{}, store.ErrConflict
	}
	if _, _, err := mime.ParseMediaType(req.ContentType); err != nil {
		return ArtifactCreateResult{}, store.ErrConflict
	}
	if len(req.WorkspaceID) > 128 || len(req.JobID) > 128 {
		return ArtifactCreateResult{}, store.ErrConflict
	}
	now := s.now().UTC()
	if resumable, ok, err := s.store.FindResumableArtifactUpload(ctx, session.OwnerID, session.MachineID, strings.TrimSpace(req.WorkspaceID), strings.TrimSpace(req.JobID), req.LogicalName, req.ContentType, req.SizeBytes, req.SHA256, now); err != nil {
		return ArtifactCreateResult{}, err
	} else if ok {
		partPath := s.artifactUploadPath(resumable.ID)
		if info, statErr := os.Stat(partPath); statErr == nil && info.Mode().IsRegular() && info.Size() == resumable.ReceivedSize || errors.Is(statErr, os.ErrNotExist) && resumable.ReceivedSize == 0 {
			return ArtifactCreateResult{ArtifactID: resumable.ArtifactID, UploadID: resumable.ID, ChunkBytes: MaxArtifactChunkBytes, ReceivedBytes: resumable.ReceivedSize, ExpiresAt: resumable.ExpiresAt}, nil
		}
		_ = s.store.AbortArtifactUpload(ctx, session.MachineID, resumable.ID)
		_ = os.Remove(partPath)
	}
	usage, err := s.store.ArtifactUsage(ctx, session.OwnerID, session.MachineID, now)
	if err != nil {
		return ArtifactCreateResult{}, err
	}
	if usage.ActiveUploads >= MaxActiveArtifactUploads || usage.MachineBytes+req.SizeBytes > MaxMachineArtifactBytes || usage.OwnerBytes+req.SizeBytes > MaxOwnerArtifactBytes {
		return ArtifactCreateResult{}, &CapabilityCallError{Code: "ARTIFACT_QUOTA_EXCEEDED", Message: "artifact quota exceeded", Retryable: false}
	}
	artifactID, err := security.RandomOpaque("art_")
	if err != nil {
		return ArtifactCreateResult{}, err
	}
	uploadID, err := security.RandomOpaque("upl_")
	if err != nil {
		return ArtifactCreateResult{}, err
	}
	uploadExpires := now.Add(artifactUploadTTL)
	artifact := store.ArtifactRecord{
		ID: artifactID, OwnerID: session.OwnerID, MachineID: session.MachineID,
		WorkspaceID: strings.TrimSpace(req.WorkspaceID), JobID: strings.TrimSpace(req.JobID),
		LogicalName: req.LogicalName, ContentType: req.ContentType, SizeBytes: req.SizeBytes, SHA256: req.SHA256,
		Status: "uploading", CreatedAt: now, ExpiresAt: now.Add(artifactRetention),
	}
	upload := store.ArtifactUploadRecord{
		ID: uploadID, ArtifactID: artifactID, MachineID: session.MachineID,
		ExpectedSize: req.SizeBytes, ExpectedSHA256: req.SHA256, Status: "active", ExpiresAt: uploadExpires,
	}
	if err := s.store.CreateArtifactUpload(ctx, artifact, upload); err != nil {
		return ArtifactCreateResult{}, err
	}
	if err := os.MkdirAll(filepath.Join(s.dataDir, "artifacts", "uploads"), 0o700); err != nil {
		_ = s.store.AbortArtifactUpload(ctx, session.MachineID, uploadID)
		return ArtifactCreateResult{}, err
	}
	_ = s.audit(ctx, store.AuditEntry{OwnerID: session.OwnerID, MachineID: session.MachineID, ActorType: "node", ActorID: session.MachineID, Action: "artifact.create", Result: "success", Detail: map[string]any{"artifactId": artifactID, "workspaceId": artifact.WorkspaceID, "sizeBytes": artifact.SizeBytes}, CreatedAt: now})
	return ArtifactCreateResult{ArtifactID: artifactID, UploadID: uploadID, ChunkBytes: MaxArtifactChunkBytes, ReceivedBytes: 0, ExpiresAt: uploadExpires}, nil
}

func (s *Service) UploadArtifactChunk(ctx context.Context, machineID, uploadID string, offset int64, chunk []byte) (int64, error) {
	if offset < 0 || len(chunk) == 0 || len(chunk) > MaxArtifactChunkBytes || !strings.HasPrefix(uploadID, "upl_") {
		return 0, store.ErrConflict
	}
	artifactUploadMu.Lock()
	defer artifactUploadMu.Unlock()

	now := s.now().UTC()
	rec, err := s.store.GetArtifactUpload(ctx, machineID, uploadID, now)
	if err != nil {
		return 0, err
	}
	if rec.ReceivedSize != offset || offset+int64(len(chunk)) > rec.ExpectedSize {
		return rec.ReceivedSize, store.ErrConflict
	}
	path := s.artifactUploadPath(uploadID)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return rec.ReceivedSize, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return rec.ReceivedSize, err
	}
	if info.Size() != offset {
		return rec.ReceivedSize, store.ErrConflict
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return rec.ReceivedSize, err
	}
	if _, err := file.Write(chunk); err != nil {
		_ = file.Truncate(offset)
		return rec.ReceivedSize, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Truncate(offset)
		return rec.ReceivedSize, err
	}
	newOffset := offset + int64(len(chunk))
	if err := s.store.AdvanceArtifactUpload(ctx, machineID, uploadID, offset, newOffset, now); err != nil {
		_ = file.Truncate(offset)
		_ = file.Sync()
		return rec.ReceivedSize, err
	}
	return newOffset, nil
}

func (s *Service) CompleteArtifactUpload(ctx context.Context, session store.DeviceSession, uploadID string) (store.ArtifactRecord, error) {
	artifactUploadMu.Lock()
	defer artifactUploadMu.Unlock()

	now := s.now().UTC()
	upload, err := s.store.GetArtifactUploadState(ctx, session.MachineID, uploadID)
	if err != nil {
		return store.ArtifactRecord{}, err
	}
	if upload.Status == "complete" {
		artifact, err := s.store.GetArtifactByMachine(ctx, session.MachineID, upload.ArtifactID)
		if err != nil {
			return store.ArtifactRecord{}, err
		}
		if artifact.Status != "complete" || artifact.StorageKey == "" {
			return store.ArtifactRecord{}, store.ErrConflict
		}
		return artifact, nil
	}
	if upload.Status != "active" || !upload.ExpiresAt.After(now) {
		if !upload.ExpiresAt.After(now) {
			return store.ArtifactRecord{}, store.ErrExpired
		}
		return store.ArtifactRecord{}, store.ErrConflict
	}
	if upload.ReceivedSize != upload.ExpectedSize {
		return store.ArtifactRecord{}, store.ErrConflict
	}
	partPath := s.artifactUploadPath(uploadID)
	file, err := os.Open(partPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && upload.ExpectedSize == 0 {
			if err := os.WriteFile(partPath, nil, 0o600); err != nil {
				return store.ArtifactRecord{}, err
			}
			file, err = os.Open(partPath)
		}
		if err != nil {
			return store.ArtifactRecord{}, err
		}
	}
	hasher := sha256.New()
	written, hashErr := io.Copy(hasher, io.LimitReader(file, MaxArtifactBytes+1))
	closeErr := file.Close()
	if hashErr != nil {
		return store.ArtifactRecord{}, hashErr
	}
	if closeErr != nil {
		return store.ArtifactRecord{}, closeErr
	}
	if written != upload.ExpectedSize || written > MaxArtifactBytes {
		return store.ArtifactRecord{}, store.ErrConflict
	}
	actualSHA := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if actualSHA != upload.ExpectedSHA256 {
		_ = s.store.AbortArtifactUpload(ctx, session.MachineID, uploadID)
		_ = os.Remove(partPath)
		return store.ArtifactRecord{}, &CapabilityCallError{Code: "HASH_MISMATCH", Message: "artifact SHA-256 mismatch", Retryable: false}
	}
	storageKey := artifactStorageKey(actualSHA)
	blobPath, err := s.artifactBlobPath(storageKey)
	if err != nil {
		return store.ArtifactRecord{}, err
	}
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o700); err != nil {
		return store.ArtifactRecord{}, err
	}
	createdBlob := false
	reusedBlob := false
	if existing, err := os.Stat(blobPath); err == nil {
		if !existing.Mode().IsRegular() || existing.Size() != upload.ExpectedSize {
			return store.ArtifactRecord{}, store.ErrConflict
		}
		existingSHA, err := hashArtifactFile(blobPath, MaxArtifactBytes)
		if err != nil {
			return store.ArtifactRecord{}, err
		}
		if existingSHA != actualSHA {
			return store.ArtifactRecord{}, &CapabilityCallError{Code: "ARTIFACT_BLOB_CORRUPT", Message: "existing artifact blob failed integrity verification", Retryable: false}
		}
		reusedBlob = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return store.ArtifactRecord{}, err
	} else {
		if err := os.Rename(partPath, blobPath); err != nil {
			return store.ArtifactRecord{}, err
		}
		createdBlob = true
	}
	artifact, err := s.store.CompleteArtifactUpload(ctx, session.MachineID, uploadID, storageKey, now)
	if err != nil {
		if createdBlob {
			_ = os.Rename(blobPath, partPath)
		}
		return store.ArtifactRecord{}, err
	}
	if reusedBlob {
		_ = os.Remove(partPath)
	}
	_ = s.audit(ctx, store.AuditEntry{OwnerID: session.OwnerID, MachineID: session.MachineID, ActorType: "node", ActorID: session.MachineID, Action: "artifact.complete", Result: "success", Detail: map[string]any{"artifactId": artifact.ID, "sizeBytes": artifact.SizeBytes}, CreatedAt: now})
	return artifact, nil
}

func (s *Service) AbortArtifactUpload(ctx context.Context, session store.DeviceSession, uploadID string) error {
	artifactUploadMu.Lock()
	defer artifactUploadMu.Unlock()
	if err := s.store.AbortArtifactUpload(ctx, session.MachineID, uploadID); err != nil {
		return err
	}
	_ = os.Remove(s.artifactUploadPath(uploadID))
	_ = s.audit(ctx, store.AuditEntry{OwnerID: session.OwnerID, MachineID: session.MachineID, ActorType: "node", ActorID: session.MachineID, Action: "artifact.abort", Result: "success", Detail: map[string]any{"uploadId": uploadID}, CreatedAt: s.now().UTC()})
	return nil
}

func (s *Service) GetArtifact(ctx context.Context, ownerID, artifactID string) (store.ArtifactRecord, error) {
	rec, err := s.store.GetArtifact(ctx, ownerID, artifactID)
	if err != nil {
		return store.ArtifactRecord{}, err
	}
	if rec.Status != "complete" || !rec.ExpiresAt.After(s.now().UTC()) || rec.StorageKey == "" {
		return store.ArtifactRecord{}, store.ErrNotFound
	}
	return rec, nil
}

func (s *Service) OpenArtifact(ctx context.Context, ownerID, artifactID string) (store.ArtifactRecord, *os.File, error) {
	rec, err := s.GetArtifact(ctx, ownerID, artifactID)
	if err != nil {
		return store.ArtifactRecord{}, nil, err
	}
	path, err := s.artifactBlobPath(rec.StorageKey)
	if err != nil {
		return store.ArtifactRecord{}, nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store.ArtifactRecord{}, nil, store.ErrNotFound
		}
		return store.ArtifactRecord{}, nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != rec.SizeBytes {
		_ = file.Close()
		if err != nil {
			return store.ArtifactRecord{}, nil, err
		}
		return store.ArtifactRecord{}, nil, &CapabilityCallError{Code: "ARTIFACT_BLOB_CORRUPT", Message: "artifact blob size does not match metadata", Retryable: false}
	}
	return rec, file, nil
}

func (s *Service) artifactUploadPath(uploadID string) string {
	return filepath.Join(s.dataDir, "artifacts", "uploads", uploadID+".part")
}

func (s *Service) artifactBlobPath(storageKey string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(storageKey))
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", store.ErrConflict
	}
	return filepath.Join(s.dataDir, "artifacts", "blobs", clean), nil
}

func artifactStorageKey(sha string) string {
	digest := strings.TrimPrefix(sha, "sha256:")
	return filepath.ToSlash(filepath.Join(digest[:2], digest[2:4], digest))
}

func hashArtifactFile(path string, limit int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(file, limit+1))
	if err != nil {
		return "", err
	}
	if written > limit {
		return "", store.ErrConflict
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

func validArtifactSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
