package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/isguang2024/fast-spider/internal/hub/store"
	"github.com/isguang2024/fast-spider/internal/security"
)

const (
	maxPresentationUploadBytes int64 = 64 << 20
	presentationTTL                  = 20 * time.Minute
)

var errPresentationNotFound = errors.New("presentation not found")

type presentationRecord struct {
	ID          string
	OwnerID     string
	MachineID   string
	FileName    string
	ContentType string
	SizeBytes   int64
	SHA256      string
	Path        string
	ExpiresAt   time.Time
}

type presentationStore struct {
	root string
	mu   sync.Mutex
	data map[string]presentationRecord
}

func presentationTempRoot(dataDir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(dataDir)))
	return filepath.Join(os.TempDir(), "fast-spider-presentations-"+hex.EncodeToString(sum[:6]))
}

func newPresentationStore(root string) *presentationStore {
	root = filepath.Clean(root)
	_ = os.RemoveAll(root)
	_ = os.MkdirAll(root, 0o700)
	return &presentationStore{root: root, data: make(map[string]presentationRecord)}
}

func (s *presentationStore) put(session store.DeviceSession, fileName, contentType, expectedSHA string, sizeBytes int64, body io.Reader, now time.Time) (presentationRecord, error) {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return presentationRecord{}, errors.New("presentation relay is unavailable")
	}
	if sizeBytes <= 0 || sizeBytes > maxPresentationUploadBytes {
		return presentationRecord{}, errors.New("presentation size is outside the allowed range")
	}
	fileName = safePresentationDownloadName(fileName)
	contentType = strings.TrimSpace(contentType)
	if contentType == "" || len(contentType) > 256 {
		return presentationRecord{}, errors.New("presentation content type is invalid")
	}
	if !validPresentationSHA256(expectedSHA) {
		return presentationRecord{}, errors.New("presentation SHA-256 is invalid")
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return presentationRecord{}, err
	}
	id, err := security.RandomOpaque("prs_")
	if err != nil {
		return presentationRecord{}, err
	}
	tempPath := filepath.Join(s.root, id+".upload")
	finalPath := filepath.Join(s.root, id+".bin")
	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return presentationRecord{}, err
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(body, maxPresentationUploadBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written != sizeBytes || written > maxPresentationUploadBytes {
		_ = os.Remove(tempPath)
		if copyErr != nil {
			return presentationRecord{}, copyErr
		}
		if closeErr != nil {
			return presentationRecord{}, closeErr
		}
		return presentationRecord{}, errors.New("presentation body size does not match Content-Length")
	}
	actualSHA := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actualSHA, expectedSHA) {
		_ = os.Remove(tempPath)
		return presentationRecord{}, errors.New("presentation SHA-256 mismatch")
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(tempPath)
		return presentationRecord{}, err
	}
	record := presentationRecord{
		ID: id, OwnerID: session.OwnerID, MachineID: session.MachineID,
		FileName: fileName, ContentType: contentType, SizeBytes: sizeBytes,
		SHA256: actualSHA, Path: finalPath, ExpiresAt: now.UTC().Add(presentationTTL),
	}
	s.mu.Lock()
	s.cleanupExpiredLocked(now.UTC())
	s.data[id] = record
	s.mu.Unlock()
	return record, nil
}

func (s *presentationStore) getForOwner(ownerID, id string, now time.Time) (presentationRecord, error) {
	record, err := s.get(id, now)
	if err != nil {
		return presentationRecord{}, err
	}
	if record.OwnerID != ownerID {
		return presentationRecord{}, errPresentationNotFound
	}
	return record, nil
}

func (s *presentationStore) get(id string, now time.Time) (presentationRecord, error) {
	if s == nil || !strings.HasPrefix(id, "prs_") {
		return presentationRecord{}, errPresentationNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now.UTC())
	record, ok := s.data[id]
	if !ok || !record.ExpiresAt.After(now.UTC()) {
		return presentationRecord{}, errPresentationNotFound
	}
	info, err := os.Stat(record.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != record.SizeBytes {
		delete(s.data, id)
		_ = os.Remove(record.Path)
		return presentationRecord{}, errPresentationNotFound
	}
	return record, nil
}

func (s *presentationStore) cleanupExpired(now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cleanupExpiredLocked(now.UTC())
	s.mu.Unlock()
}

func (s *presentationStore) cleanupExpiredLocked(now time.Time) {
	for id, record := range s.data {
		if !record.ExpiresAt.After(now) {
			delete(s.data, id)
			_ = os.Remove(record.Path)
		}
	}
}

func (s *presentationStore) clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.data = make(map[string]presentationRecord)
	s.mu.Unlock()
	_ = os.RemoveAll(s.root)
}

func (s *Server) handlePresentationUpload(w http.ResponseWriter, r *http.Request) {
	session, err := s.service.AuthenticateDevice(r.Context(), bearerToken(r.Header.Get("Authorization")))
	if err != nil {
		writeError(w, err)
		return
	}
	if r.ContentLength <= 0 || r.ContentLength > maxPresentationUploadBytes {
		writeArtifactError(w, http.StatusRequestEntityTooLarge, "PRESENTATION_SIZE_INVALID", "presentation size is outside the allowed range", false)
		return
	}
	fileNameRaw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(r.Header.Get("X-Fast-Spider-File-Name")))
	if err != nil || len(fileNameRaw) == 0 || len(fileNameRaw) > 1024 || !utf8.Valid(fileNameRaw) {
		writeArtifactError(w, http.StatusBadRequest, "PRESENTATION_NAME_INVALID", "presentation file name is invalid", false)
		return
	}
	record, err := s.presentations.put(
		session,
		string(fileNameRaw),
		r.Header.Get("Content-Type"),
		strings.TrimSpace(r.Header.Get("X-Fast-Spider-SHA256")),
		r.ContentLength,
		r.Body,
		time.Now().UTC(),
	)
	if err != nil {
		writeArtifactError(w, http.StatusBadRequest, "PRESENTATION_UPLOAD_FAILED", err.Error(), true)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"presentationId": record.ID,
		"fileName":       record.FileName,
		"contentType":    record.ContentType,
		"sizeBytes":      record.SizeBytes,
		"sha256":         record.SHA256,
		"expiresAt":      record.ExpiresAt,
	})
}

func (s *Server) handlePresentationDownload(w http.ResponseWriter, r *http.Request) {
	record, err := s.presentations.get(strings.TrimSpace(r.PathValue("presentationId")), time.Now().UTC())
	if err != nil {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(record.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()

	disposition := "attachment"
	lowerType := strings.ToLower(strings.TrimSpace(strings.SplitN(record.ContentType, ";", 2)[0]))
	if lowerType == "image/png" || lowerType == "image/jpeg" {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", record.ContentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": record.FileName}))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, record.FileName, time.Time{}, file)
}

func safePresentationDownloadName(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	value = path.Base(value)
	value = strings.Map(func(char rune) rune {
		if char < 32 || char == 127 || char == '/' || char == '\\' {
			return -1
		}
		return char
	}, value)
	if value == "" || value == "." || value == ".." {
		return "file"
	}
	return value
}

func validPresentationSHA256(value string) bool {
	if !strings.HasPrefix(strings.ToLower(value), "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil
}

func (s *Server) presentationPublicURL(id string) string {
	base := strings.TrimRight(strings.TrimSpace(s.config.PublicBaseURL), "/")
	if base == "" || !strings.HasPrefix(id, "prs_") {
		return ""
	}
	return base + "/api/v1/presentations/" + id
}

func readPresentationImage(ctx context.Context, record presentationRecord, maxBytes int64) ([]byte, error) {
	if record.SizeBytes <= 0 || record.SizeBytes > maxBytes {
		return nil, errors.New("presentation image exceeds inline MCP limit")
	}
	select {
	case <-ctx.Done():
		return nil, errors.New("presentation image read canceled")
	default:
	}
	file, err := os.Open(record.Path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBytes || int64(len(raw)) != record.SizeBytes {
		return nil, errors.New("presentation image size changed")
	}
	return raw, nil
}

func verifyPresentationImageBytes(mimeType string, data []byte) error {
	switch mimeType {
	case "image/png":
		if len(data) < 8 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
			return fmt.Errorf("presentation image data does not match image/png")
		}
	case "image/jpeg":
		if len(data) < 3 || data[0] != 0xff || data[1] != 0xd8 || data[2] != 0xff {
			return fmt.Errorf("presentation image data does not match image/jpeg")
		}
	default:
		return errors.New("presentation image MIME type is unsupported")
	}
	return nil
}

func (s *Server) StartMaintenance(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.presentations.cleanupExpired(now.UTC())
		}
	}
}
