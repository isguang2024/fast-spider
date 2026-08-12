package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxSessionCreateRecords = 4096

type sessionCreateRecord struct {
	Key       string         `json:"key"`
	SpecHash  string         `json:"specHash"`
	State     string         `json:"state"`
	Result    map[string]any `json:"result,omitempty"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

type sessionCreateIndex struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Records       []sessionCreateRecord `json:"records"`
}

type sessionCreateStore struct {
	mu      sync.Mutex
	path    string
	records map[string]sessionCreateRecord
	loadErr error
}

type createIdempotencyError struct{ code, message string }

func (e *createIdempotencyError) Error() string { return e.message }
func (e *createIdempotencyError) CapabilityError() (string, string, bool) {
	return e.code, e.message, e.code == "AGENT_CREATE_IN_PROGRESS"
}

func newSessionCreateStore(dataDir string) *sessionCreateStore {
	store := &sessionCreateStore{path: filepath.Join(dataDir, "agent", "session-create-idempotency.json"), records: map[string]sessionCreateRecord{}}
	store.loadErr = store.load()
	return store
}

func sessionCreateSpecHash(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *sessionCreateStore) load() error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var index sessionCreateIndex
	if err := json.Unmarshal(raw, &index); err != nil || index.SchemaVersion != 1 || len(index.Records) > maxSessionCreateRecords {
		return fmt.Errorf("invalid session create idempotency index")
	}
	now := time.Now().UTC()
	seen := make(map[string]struct{}, len(index.Records))
	for _, record := range index.Records {
		if err := validateSessionCreateRecord(record); err != nil {
			return fmt.Errorf("invalid session create idempotency index: %w", err)
		}
		if _, duplicate := seen[record.Key]; duplicate {
			return fmt.Errorf("invalid session create idempotency index: duplicate key")
		}
		seen[record.Key] = struct{}{}
		if record.State == "reserved" || record.State == "thread_created" {
			record.State = "in_doubt"
			record.UpdatedAt = now
		}
		s.records[record.Key] = record
	}
	return nil
}

func validateSessionCreateRecord(record sessionCreateRecord) error {
	if record.Key == "" || len(record.Key) > 160 || strings.ContainsAny(record.Key, "\x00\r\n") {
		return fmt.Errorf("invalid key")
	}
	decoded, err := hex.DecodeString(record.SpecHash)
	if err != nil || len(decoded) != sha256.Size || record.SpecHash != strings.ToLower(record.SpecHash) {
		return fmt.Errorf("invalid spec hash")
	}
	if record.UpdatedAt.IsZero() {
		return fmt.Errorf("missing update time")
	}
	sessionID := ""
	if record.Result != nil {
		var ok bool
		sessionID, ok = record.Result["sessionId"].(string)
		if !ok || strings.TrimSpace(sessionID) == "" || len(sessionID) > 256 || strings.ContainsAny(sessionID, "\x00\r\n") {
			return fmt.Errorf("invalid result session")
		}
	}
	switch record.State {
	case "reserved":
		if record.Result != nil {
			return fmt.Errorf("reserved record has result")
		}
	case "thread_created", "succeeded":
		if sessionID == "" {
			return fmt.Errorf("completed record has no session")
		}
	case "in_doubt":
	case "deleting":
		if sessionID == "" {
			return fmt.Errorf("deleting record has no session")
		}
	default:
		return fmt.Errorf("invalid state")
	}
	return nil
}

func (s *sessionCreateStore) abort(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return &createIdempotencyError{code: "AGENT_IDEMPOTENCY_STORE_UNAVAILABLE", message: "session.create idempotency state is unavailable"}
	}
	record, ok := s.records[key]
	if !ok {
		return nil
	}
	if record.State != "reserved" {
		return fmt.Errorf("session create reservation is no longer abortable")
	}
	delete(s.records, key)
	if err := s.saveLocked(); err != nil {
		s.records[key] = record
		return err
	}
	return nil
}

func (s *sessionCreateStore) releaseUnresolved(key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return false, &createIdempotencyError{code: "AGENT_IDEMPOTENCY_STORE_UNAVAILABLE", message: "session.create idempotency state is unavailable"}
	}
	record, ok := s.records[key]
	if !ok {
		return false, nil
	}
	if record.State != "in_doubt" || record.Result != nil {
		return false, &createIdempotencyError{code: "IDEMPOTENCY_CONFLICT", message: "only an unresolved session.create without a known session can be released by idempotencyKey"}
	}
	delete(s.records, key)
	if err := s.saveLocked(); err != nil {
		s.records[key] = record
		return false, err
	}
	return true, nil
}

func (s *sessionCreateStore) begin(key, specHash string) (map[string]any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, false, &createIdempotencyError{code: "AGENT_IDEMPOTENCY_STORE_UNAVAILABLE", message: "session.create idempotency state could not be loaded; repair or remove it only after checking existing sessions"}
	}
	if err := validateSessionCreateRecord(sessionCreateRecord{Key: key, SpecHash: specHash, State: "reserved", UpdatedAt: time.Now().UTC()}); err != nil {
		return nil, false, &createIdempotencyError{code: "INVALID_REQUEST", message: "session.create idempotency key or spec hash is invalid"}
	}
	if current, ok := s.records[key]; ok {
		if current.SpecHash != specHash {
			return nil, false, &createIdempotencyError{code: "IDEMPOTENCY_CONFLICT", message: "idempotencyKey was reused with different session.create parameters"}
		}
		switch current.State {
		case "succeeded":
			return cloneAgentMap(current.Result), true, nil
		case "reserved":
			return nil, false, &createIdempotencyError{code: "AGENT_CREATE_IN_PROGRESS", message: "session.create with this idempotencyKey is already in progress"}
		default:
			return nil, false, &createIdempotencyError{code: "AGENT_CREATE_IN_DOUBT", message: "a prior session.create may have created a session; inspect session.list before retrying"}
		}
	}
	if len(s.records) >= maxSessionCreateRecords {
		return nil, false, &createIdempotencyError{code: "RESOURCE_LIMIT", message: "session.create idempotency store is full"}
	}
	s.records[key] = sessionCreateRecord{Key: key, SpecHash: specHash, State: "reserved", UpdatedAt: time.Now().UTC()}
	if err := s.saveLocked(); err != nil {
		delete(s.records, key)
		return nil, false, err
	}
	return nil, false, nil
}

func (s *sessionCreateStore) update(key, state string, result map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return &createIdempotencyError{code: "AGENT_IDEMPOTENCY_STORE_UNAVAILABLE", message: "session.create idempotency state is unavailable"}
	}
	record, ok := s.records[key]
	if !ok {
		return fmt.Errorf("session create reservation is missing")
	}
	record.State = state
	record.Result = cloneAgentMap(result)
	record.UpdatedAt = time.Now().UTC()
	if err := validateSessionCreateRecord(record); err != nil {
		return err
	}
	s.records[key] = record
	return s.saveLocked()
}

func (s *sessionCreateStore) prepareSessionDelete(sessionID string) (bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return false, &createIdempotencyError{code: "AGENT_IDEMPOTENCY_STORE_UNAVAILABLE", message: "session.create idempotency state is unavailable"}
	}
	previous := map[string]sessionCreateRecord{}
	for key, record := range s.records {
		storedSessionID, _ := record.Result["sessionId"].(string)
		if storedSessionID == sessionID {
			if record.State == "deleting" {
				continue
			}
			previous[key] = record
			record.State = "deleting"
			record.UpdatedAt = time.Now().UTC()
			s.records[key] = record
		}
	}
	if len(previous) == 0 {
		for _, record := range s.records {
			storedSessionID, _ := record.Result["sessionId"].(string)
			if storedSessionID == sessionID && record.State == "deleting" {
				return true, nil
			}
		}
		return false, nil
	}
	if err := s.saveLocked(); err != nil {
		for key, record := range previous {
			s.records[key] = record
		}
		return false, err
	}
	return true, nil
}

func (s *sessionCreateStore) finalizeSessionDelete(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return &createIdempotencyError{code: "AGENT_IDEMPOTENCY_STORE_UNAVAILABLE", message: "session.create idempotency state is unavailable"}
	}
	removed := map[string]sessionCreateRecord{}
	for key, record := range s.records {
		storedSessionID, _ := record.Result["sessionId"].(string)
		if storedSessionID == sessionID && record.State == "deleting" {
			removed[key] = record
			delete(s.records, key)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	if err := s.saveLocked(); err != nil {
		for key, record := range removed {
			s.records[key] = record
		}
		return err
	}
	return nil
}

func (s *sessionCreateStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	records := make([]sessionCreateRecord, 0, len(s.records))
	for _, record := range s.records {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].UpdatedAt.Before(records[j].UpdatedAt) })
	raw, err := json.Marshal(sessionCreateIndex{SchemaVersion: 1, Records: records})
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".session-create-idempotency-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceAgentFile(tempPath, s.path); err != nil {
		return err
	}
	return syncAgentParentDirectory(s.path)
}
