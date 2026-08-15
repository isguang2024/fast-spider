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
	maxPresentationUploadBytes              int64 = 64 << 20
	maxPresentationMachineBytes             int64 = 256 << 20
	maxPresentationOwnerBytes               int64 = 512 << 20
	maxPresentationGlobalBytes              int64 = 1 << 30
	maxPresentationEntries                        = 128
	maxConcurrentPresentationUploads              = 8
	maxConcurrentPresentationOwnerUploads         = 4
	maxConcurrentPresentationMachineUploads       = 2
	maxPresentationUploadDuration                 = 5 * time.Minute
	presentationTTL                               = 20 * time.Minute
)

var (
	errPresentationNotFound    = errors.New("presentation not found")
	errPresentationQuota       = errors.New("presentation relay quota exceeded")
	errPresentationUnavailable = errors.New("presentation relay is unavailable")
)

type presentationLimits struct {
	entries           int
	concurrent        int
	ownerConcurrent   int
	machineConcurrent int
	globalBytes       int64
	ownerBytes        int64
	machineBytes      int64
}

func defaultPresentationLimits() presentationLimits {
	return presentationLimits{
		entries:           maxPresentationEntries,
		concurrent:        maxConcurrentPresentationUploads,
		ownerConcurrent:   maxConcurrentPresentationOwnerUploads,
		machineConcurrent: maxConcurrentPresentationMachineUploads,
		globalBytes:       maxPresentationGlobalBytes,
		ownerBytes:        maxPresentationOwnerBytes,
		machineBytes:      maxPresentationMachineBytes,
	}
}

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
	root             string
	mu               sync.Mutex
	data             map[string]presentationRecord
	limits           presentationLimits
	uploading        int
	uploadingOwner   map[string]int
	uploadingMachine map[string]int
	reservedBytes    int64
	reservedOwner    map[string]int64
	reservedMachine  map[string]int64
	storedBytes      int64
	storedOwner      map[string]int64
	storedMachine    map[string]int64
	removeFile       func(string) error
	removeAll        func(string) error
	mkdirAll         func(string, os.FileMode) error
	ready            bool
}

type presentationReservation struct {
	store     *presentationStore
	ownerID   string
	machineID string
	sizeBytes int64
	active    bool
}

func presentationTempRoot(dataDir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(dataDir)))
	return filepath.Join(os.TempDir(), "fast-spider-presentations-"+hex.EncodeToString(sum[:6]))
}

func newPresentationStore(root string) *presentationStore {
	return newPresentationStoreWithFileOps(root, os.Remove, os.RemoveAll, os.MkdirAll)
}

func newPresentationStoreWithFileOps(
	root string,
	removeFile func(string) error,
	removeAll func(string) error,
	mkdirAll func(string, os.FileMode) error,
) *presentationStore {
	if removeFile == nil {
		removeFile = os.Remove
	}
	if removeAll == nil {
		removeAll = os.RemoveAll
	}
	if mkdirAll == nil {
		mkdirAll = os.MkdirAll
	}
	root = filepath.Clean(root)
	s := &presentationStore{
		root:             root,
		data:             make(map[string]presentationRecord),
		limits:           defaultPresentationLimits(),
		reservedOwner:    make(map[string]int64),
		reservedMachine:  make(map[string]int64),
		uploadingOwner:   make(map[string]int),
		uploadingMachine: make(map[string]int),
		storedOwner:      make(map[string]int64),
		storedMachine:    make(map[string]int64),
		removeFile:       removeFile,
		removeAll:        removeAll,
		mkdirAll:         mkdirAll,
	}
	s.initializeRootLocked()
	return s
}

func (s *presentationStore) put(session store.DeviceSession, fileName, contentType, expectedSHA string, sizeBytes int64, body io.Reader, now time.Time) (presentationRecord, error) {
	return s.putInternal(context.Background(), session, fileName, contentType, expectedSHA, sizeBytes, body, now)
}

func (s *presentationStore) putContext(ctx context.Context, session store.DeviceSession, fileName, contentType, expectedSHA string, sizeBytes int64, body io.ReadCloser, now time.Time) (presentationRecord, error) {
	stopClose := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stopClose()
	return s.putInternal(ctx, session, fileName, contentType, expectedSHA, sizeBytes, body, now)
}

func (s *presentationStore) putInternal(ctx context.Context, session store.DeviceSession, fileName, contentType, expectedSHA string, sizeBytes int64, body io.Reader, now time.Time) (presentationRecord, error) {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return presentationRecord{}, errPresentationUnavailable
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
	reservation, err := s.reserve(session, sizeBytes, now.UTC())
	if err != nil {
		return presentationRecord{}, err
	}
	defer reservation.release()
	if err := ctx.Err(); err != nil {
		return presentationRecord{}, err
	}
	if err := s.mkdirAll(s.root, 0o700); err != nil {
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
		if ctxErr := ctx.Err(); ctxErr != nil {
			return presentationRecord{}, ctxErr
		}
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
	if err := ctx.Err(); err != nil {
		_ = os.Remove(tempPath)
		return presentationRecord{}, err
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
	reservation.commit(record, now.UTC())
	return record, nil
}

func (s *presentationStore) reserve(session store.DeviceSession, sizeBytes int64, now time.Time) (*presentationReservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready {
		return nil, errPresentationUnavailable
	}
	s.cleanupExpiredLocked(now)
	limits := s.limits
	if limits.entries <= 0 || limits.concurrent <= 0 || limits.ownerConcurrent <= 0 || limits.machineConcurrent <= 0 || limits.globalBytes <= 0 || limits.ownerBytes <= 0 || limits.machineBytes <= 0 {
		return nil, errPresentationQuota
	}
	if len(s.data)+s.uploading >= limits.entries || s.uploading >= limits.concurrent ||
		s.uploadingOwner[session.OwnerID] >= limits.ownerConcurrent ||
		s.uploadingMachine[session.MachineID] >= limits.machineConcurrent ||
		s.storedBytes+s.reservedBytes+sizeBytes > limits.globalBytes ||
		s.storedOwner[session.OwnerID]+s.reservedOwner[session.OwnerID]+sizeBytes > limits.ownerBytes ||
		s.storedMachine[session.MachineID]+s.reservedMachine[session.MachineID]+sizeBytes > limits.machineBytes {
		return nil, errPresentationQuota
	}
	s.uploading++
	s.uploadingOwner[session.OwnerID]++
	s.uploadingMachine[session.MachineID]++
	s.reservedBytes += sizeBytes
	s.reservedOwner[session.OwnerID] += sizeBytes
	s.reservedMachine[session.MachineID] += sizeBytes
	return &presentationReservation{store: s, ownerID: session.OwnerID, machineID: session.MachineID, sizeBytes: sizeBytes, active: true}, nil
}

func (r *presentationReservation) release() {
	if r == nil || r.store == nil {
		return
	}
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if !r.active {
		return
	}
	r.store.releaseReservationLocked(r)
}

func (r *presentationReservation) commit(record presentationRecord, now time.Time) {
	if r == nil || r.store == nil {
		return
	}
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if !r.active {
		return
	}
	r.store.cleanupExpiredLocked(now)
	r.store.releaseReservationLocked(r)
	r.store.data[record.ID] = record
	r.store.storedBytes += record.SizeBytes
	r.store.storedOwner[record.OwnerID] += record.SizeBytes
	r.store.storedMachine[record.MachineID] += record.SizeBytes
}

func (s *presentationStore) releaseReservationLocked(reservation *presentationReservation) {
	reservation.active = false
	s.uploading--
	decrementPresentationCount(s.uploadingOwner, reservation.ownerID)
	decrementPresentationCount(s.uploadingMachine, reservation.machineID)
	s.reservedBytes -= reservation.sizeBytes
	decrementPresentationUsage(s.reservedOwner, reservation.ownerID, reservation.sizeBytes)
	decrementPresentationUsage(s.reservedMachine, reservation.machineID, reservation.sizeBytes)
}

func decrementPresentationCount(usage map[string]int, key string) {
	if remaining := usage[key] - 1; remaining > 0 {
		usage[key] = remaining
	} else {
		delete(usage, key)
	}
}

func decrementPresentationUsage(usage map[string]int64, key string, sizeBytes int64) {
	if remaining := usage[key] - sizeBytes; remaining > 0 {
		usage[key] = remaining
	} else {
		delete(usage, key)
	}
}

func (s *presentationStore) removeRecordLocked(id string, record presentationRecord) {
	delete(s.data, id)
	s.storedBytes -= record.SizeBytes
	decrementPresentationUsage(s.storedOwner, record.OwnerID, record.SizeBytes)
	decrementPresentationUsage(s.storedMachine, record.MachineID, record.SizeBytes)
}

func (s *presentationStore) deleteRecordFileLocked(id string, record presentationRecord) bool {
	removeFile := s.removeFile
	if removeFile == nil {
		removeFile = os.Remove
	}
	if err := removeFile(record.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false
	}
	s.removeRecordLocked(id, record)
	return true
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
		s.deleteRecordFileLocked(id, record)
		return presentationRecord{}, errPresentationNotFound
	}
	return record, nil
}

func (s *presentationStore) cleanupExpired(now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.ready && !s.initializeRootLocked() {
		s.mu.Unlock()
		return
	}
	s.cleanupExpiredLocked(now.UTC())
	s.mu.Unlock()
}

func (s *presentationStore) cleanupExpiredLocked(now time.Time) {
	if !s.ready {
		return
	}
	for id, record := range s.data {
		if !record.ExpiresAt.After(now) {
			s.deleteRecordFileLocked(id, record)
		}
	}
}

func (s *presentationStore) initializeRootLocked() bool {
	removeAll := s.removeAll
	if removeAll == nil {
		removeAll = os.RemoveAll
	}
	mkdirAll := s.mkdirAll
	if mkdirAll == nil {
		mkdirAll = os.MkdirAll
	}
	s.ready = false
	if err := removeAll(s.root); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false
	}
	s.resetStoredLocked()
	if err := mkdirAll(s.root, 0o700); err != nil {
		return false
	}
	s.ready = true
	return true
}

func (s *presentationStore) resetStoredLocked() {
	s.data = make(map[string]presentationRecord)
	s.storedBytes = 0
	s.storedOwner = make(map[string]int64)
	s.storedMachine = make(map[string]int64)
}

func (s *presentationStore) clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.ready = false
	removeAll := s.removeAll
	if removeAll == nil {
		removeAll = os.RemoveAll
	}
	if err := removeAll(s.root); err == nil || errors.Is(err, os.ErrNotExist) {
		s.resetStoredLocked()
	}
	s.mu.Unlock()
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
	uploadCtx, cancel := context.WithTimeout(r.Context(), maxPresentationUploadDuration)
	defer cancel()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(maxPresentationUploadDuration))
	defer controller.SetReadDeadline(time.Time{})
	record, err := s.presentations.putContext(
		uploadCtx,
		session,
		string(fileNameRaw),
		r.Header.Get("Content-Type"),
		strings.TrimSpace(r.Header.Get("X-Fast-Spider-SHA256")),
		r.ContentLength,
		r.Body,
		time.Now().UTC(),
	)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			writeArtifactError(w, http.StatusRequestTimeout, "PRESENTATION_UPLOAD_TIMEOUT", "presentation upload did not complete before its deadline", true)
			return
		}
		if errors.Is(err, errPresentationQuota) {
			writeArtifactError(w, http.StatusTooManyRequests, "PRESENTATION_QUOTA_EXCEEDED", err.Error(), true)
			return
		}
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
