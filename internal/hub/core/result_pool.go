package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/store"
)

const (
	MaxResultManifestBytes int   = 64 << 10
	MaxResultPageBytes     int64 = 1 << 20
	MaxResultBytes         int64 = 100 << 20
	resultRetention              = 30 * 24 * time.Hour
)

type ResultCreateRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
	RequestHash    string `json:"requestHash"`
}

type ResultCreateResult struct {
	ResultID  string    `json:"resultId"`
	Revision  int64     `json:"revision"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type ResultAttachPageRequest struct {
	PageNo           int    `json:"pageNo"`
	ArtifactID       string `json:"artifactId"`
	ExpectedRevision int64  `json:"expectedRevision"`
}

type ResultCommitRequest struct {
	Manifest         json.RawMessage `json:"manifest"`
	ExpectedRevision int64           `json:"expectedRevision"`
}

type ResultFailRequest struct {
	Code             string `json:"code"`
	Message          string `json:"message"`
	ExpectedRevision int64  `json:"expectedRevision"`
}

func (s *Service) CreateResult(ctx context.Context, session store.DeviceSession, req ResultCreateRequest) (ResultCreateResult, error) {
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	req.RequestHash = strings.TrimSpace(req.RequestHash)
	if len(req.IdempotencyKey) < 12 || len(req.IdempotencyKey) > 128 || len(req.RequestHash) < 1 || len(req.RequestHash) > 128 {
		return ResultCreateResult{}, store.ErrConflict
	}
	if existing, err := s.store.LookupResult(ctx, session.OwnerID, req.IdempotencyKey); err == nil {
		if existing.RequestHash != req.RequestHash {
			return ResultCreateResult{}, store.ErrConflict
		}
		if existing.MachineID != session.MachineID {
			return ResultCreateResult{}, store.ErrUnauthorized
		}
		return resultCreateResult(existing), nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return ResultCreateResult{}, err
	}
	resultID, err := randomResultID()
	if err != nil {
		return ResultCreateResult{}, err
	}
	now := s.now().UTC()
	rec, err := s.store.CreateResult(ctx, store.ResultRecord{
		ResultID: resultID, OwnerID: session.OwnerID, MachineID: session.MachineID,
		IdempotencyKey: req.IdempotencyKey, RequestHash: req.RequestHash,
		Status: "open", Revision: 1, CreatedAt: now, ExpiresAt: now.Add(resultRetention),
	})
	if err != nil {
		if existing, lookupErr := s.store.LookupResult(ctx, session.OwnerID, req.IdempotencyKey); lookupErr == nil {
			if existing.RequestHash != req.RequestHash {
				return ResultCreateResult{}, store.ErrConflict
			}
			if existing.MachineID != session.MachineID {
				return ResultCreateResult{}, store.ErrUnauthorized
			}
			return resultCreateResult(existing), nil
		}
		return ResultCreateResult{}, err
	}
	return resultCreateResult(rec), nil
}

func (s *Service) AttachResultPage(ctx context.Context, session store.DeviceSession, resultID string, req ResultAttachPageRequest) (ResultCreateResult, error) {
	if req.PageNo < 0 || req.PageNo > 1_000_000 || strings.TrimSpace(resultID) == "" || strings.TrimSpace(req.ArtifactID) == "" || req.ExpectedRevision < 1 {
		return ResultCreateResult{}, store.ErrConflict
	}
	result, err := s.store.GetResult(ctx, session.OwnerID, resultID)
	if err != nil {
		return ResultCreateResult{}, err
	}
	if result.MachineID != session.MachineID {
		return ResultCreateResult{}, store.ErrUnauthorized
	}
	artifact, err := s.store.GetArtifactByMachine(ctx, session.MachineID, req.ArtifactID)
	if err != nil {
		return ResultCreateResult{}, err
	}
	if artifact.OwnerID != session.OwnerID || artifact.Status != "complete" || artifact.StorageKey == "" || artifact.SizeBytes > MaxResultPageBytes || !artifact.ExpiresAt.After(s.now().UTC()) {
		return ResultCreateResult{}, store.ErrConflict
	}
	if total, err := s.store.ResultPageBytes(ctx, session.OwnerID, resultID); err != nil {
		return ResultCreateResult{}, err
	} else if total+artifact.SizeBytes > MaxResultBytes {
		return ResultCreateResult{}, store.ErrResourceLimit
	}
	rec, err := s.store.AttachResultPage(ctx, session.OwnerID, resultID, req.PageNo, req.ArtifactID, req.ExpectedRevision, s.now().UTC())
	if err != nil {
		return ResultCreateResult{}, err
	}
	return resultCreateResult(rec), nil
}

func (s *Service) CommitResult(ctx context.Context, session store.DeviceSession, resultID string, req ResultCommitRequest) (ResultCreateResult, error) {
	if req.ExpectedRevision < 1 {
		return ResultCreateResult{}, store.ErrConflict
	}
	if err := validateResultManifest(req.Manifest); err != nil {
		return ResultCreateResult{}, err
	}
	result, err := s.store.GetResult(ctx, session.OwnerID, resultID)
	if err != nil {
		return ResultCreateResult{}, err
	}
	if result.MachineID != session.MachineID {
		return ResultCreateResult{}, store.ErrUnauthorized
	}
	rec, err := s.store.CommitResult(ctx, session.OwnerID, resultID, string(req.Manifest), req.ExpectedRevision, s.now().UTC())
	if err != nil {
		return ResultCreateResult{}, err
	}
	return resultCreateResult(rec), nil
}

func (s *Service) GetResultManifest(ctx context.Context, ownerID, resultID string) (store.ResultRecord, error) {
	rec, err := s.store.GetResult(ctx, ownerID, resultID)
	if err != nil {
		return store.ResultRecord{}, err
	}
	if !rec.ExpiresAt.After(s.now().UTC()) {
		return store.ResultRecord{}, store.ErrNotFound
	}
	return rec, nil
}

func (s *Service) ReadResultPage(ctx context.Context, ownerID, resultID string, pageNo int) (store.ResultPageRecord, *os.File, error) {
	page, err := s.store.GetResultPage(ctx, ownerID, resultID, pageNo, s.now().UTC())
	if err != nil {
		return store.ResultPageRecord{}, nil, err
	}
	_, file, err := s.OpenArtifact(ctx, ownerID, page.ArtifactID)
	if err != nil {
		return store.ResultPageRecord{}, nil, err
	}
	return page, file, nil
}

func (s *Service) LookupResult(ctx context.Context, ownerID, idempotencyKey, requestHash string) (store.ResultRecord, error) {
	rec, err := s.store.LookupResult(ctx, ownerID, strings.TrimSpace(idempotencyKey))
	if err != nil {
		return store.ResultRecord{}, err
	}
	if requestHash != "" && rec.RequestHash != strings.TrimSpace(requestHash) {
		return store.ResultRecord{}, store.ErrConflict
	}
	return rec, nil
}

func (s *Service) AbortResult(ctx context.Context, session store.DeviceSession, resultID string, expectedRevision int64) (ResultCreateResult, error) {
	return s.transitionResult(ctx, session, resultID, expectedRevision, "abort", "", "")
}

func (s *Service) FailResult(ctx context.Context, session store.DeviceSession, resultID string, req ResultFailRequest) (ResultCreateResult, error) {
	req.Code = strings.TrimSpace(req.Code)
	req.Message = strings.TrimSpace(req.Message)
	if req.ExpectedRevision < 1 || req.Code == "" || len(req.Code) > 64 || len(req.Message) > 512 {
		return ResultCreateResult{}, store.ErrConflict
	}
	return s.transitionResult(ctx, session, resultID, req.ExpectedRevision, "fail", req.Code, req.Message)
}

func (s *Service) transitionResult(ctx context.Context, session store.DeviceSession, resultID string, expectedRevision int64, action, code, message string) (ResultCreateResult, error) {
	if expectedRevision < 1 || strings.TrimSpace(resultID) == "" {
		return ResultCreateResult{}, store.ErrConflict
	}
	result, err := s.store.GetResult(ctx, session.OwnerID, resultID)
	if err != nil {
		return ResultCreateResult{}, err
	}
	if result.MachineID != session.MachineID {
		return ResultCreateResult{}, store.ErrUnauthorized
	}
	var rec store.ResultRecord
	if action == "abort" {
		rec, err = s.store.AbortResult(ctx, session.OwnerID, resultID, expectedRevision, s.now().UTC())
	} else {
		rec, err = s.store.FailResult(ctx, session.OwnerID, resultID, code, message, expectedRevision, s.now().UTC())
	}
	if err != nil {
		return ResultCreateResult{}, err
	}
	return resultCreateResult(rec), nil
}

func resultCreateResult(rec store.ResultRecord) ResultCreateResult {
	return ResultCreateResult{ResultID: rec.ResultID, Revision: rec.Revision, Status: rec.Status, ExpiresAt: rec.ExpiresAt}
}

func randomResultID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "res_" + base64.RawURLEncoding.EncodeToString(buf), nil
}

func validateResultManifest(raw json.RawMessage) error {
	if len(raw) == 0 || len(raw) > MaxResultManifestBytes {
		return store.ErrConflict
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return store.ErrConflict
	}
	if _, ok := value.(map[string]any); !ok {
		return store.ErrConflict
	}
	if containsArtifactID(value) {
		return store.ErrConflict
	}
	return nil
}

func containsArtifactID(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if strings.EqualFold(strings.TrimSpace(key), "artifactId") || containsArtifactID(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsArtifactID(child) {
				return true
			}
		}
	}
	return false
}
