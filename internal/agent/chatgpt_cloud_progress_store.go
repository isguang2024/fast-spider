package agent

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	chatGPTCloudProgressMaxFrames = 200
	chatGPTCloudProgressMaxBytes  = 256 << 10
	chatGPTCloudProgressTTL       = 24 * time.Hour
)

type chatGPTCloudProgressStore struct {
	mu          sync.Mutex
	db          *sql.DB
	lastCleanup time.Time
}

func newChatGPTCloudProgressStore(path string) (*chatGPTCloudProgressStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("progress store path is required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create progress store directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("protect progress store directory: %w", err)
	}
	dsnPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(dsnPath, "/") {
		dsnPath = "/" + dsnPath
	}
	dsn := (&url.URL{Scheme: "file", Path: dsnPath, RawQuery: "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open progress store: %w", err)
	}
	closeWithErr := func(err error) (*chatGPTCloudProgressStore, error) {
		_ = db.Close()
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return closeWithErr(fmt.Errorf("ping progress store: %w", err))
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return closeWithErr(fmt.Errorf("protect progress store file: %w", err))
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS sse_progress (
        sequence INTEGER PRIMARY KEY AUTOINCREMENT,
        conversation_id TEXT NOT NULL,
        event_type TEXT NOT NULL,
        data TEXT NOT NULL,
        event_key TEXT NOT NULL DEFAULT '',
        created_at INTEGER NOT NULL
    )`); err != nil {
		return closeWithErr(fmt.Errorf("create progress store schema: %w", err))
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_sse_progress_event_key
        ON sse_progress(conversation_id,event_key) WHERE event_key <> ''`); err != nil {
		return closeWithErr(fmt.Errorf("create progress event key index: %w", err))
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_sse_progress_conversation_sequence
        ON sse_progress(conversation_id, sequence)`); err != nil {
		return closeWithErr(fmt.Errorf("create progress store index: %w", err))
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS sse_progress_receipts (
        conversation_id TEXT NOT NULL,
        event_key TEXT NOT NULL,
        created_at INTEGER NOT NULL,
        PRIMARY KEY(conversation_id,event_key)
    )`); err != nil {
		return closeWithErr(fmt.Errorf("create progress receipt schema: %w", err))
	}
	s := &chatGPTCloudProgressStore{db: db}
	if err := s.cleanupLocked(time.Now().Add(-chatGPTCloudProgressTTL)); err != nil {
		return closeWithErr(fmt.Errorf("clean progress store: %w", err))
	}
	return s, nil
}

func (s *chatGPTCloudProgressStore) Append(conversationID, eventType, data string) error {
	if s == nil {
		return errors.New("progress store is closed")
	}
	conversationID = strings.TrimSpace(conversationID)
	eventType = strings.TrimSpace(eventType)
	if conversationID == "" || eventType == "" {
		return errors.New("conversationID and eventType are required")
	}
	clean, ok := sanitizeProgressData(data)
	if !ok || len([]byte(clean)) > chatGPTCloudProgressMaxBytes {
		return errors.New("invalid or oversized SSE progress data")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return errors.New("progress store is closed")
	}
	if err := s.cleanupIfDueLocked(time.Now().UTC()); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if _, err = tx.Exec(`INSERT INTO sse_progress(conversation_id,event_type,data,created_at) VALUES(?,?,?,?)`, conversationID, eventType, clean, now.Unix()); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err = tx.Exec(`DELETE FROM sse_progress WHERE conversation_id = ? AND sequence NOT IN (
        SELECT sequence FROM sse_progress WHERE conversation_id = ? ORDER BY sequence DESC LIMIT ?
    )`, conversationID, conversationID, chatGPTCloudProgressMaxFrames); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// AppendUnique stores one replay-safe projection frame. An existing event key
// is an idempotent replay and returns (false, nil).
func (s *chatGPTCloudProgressStore) AppendUnique(conversationID, eventKey, eventType, data string) (bool, error) {
	if s == nil {
		return false, errors.New("progress store is closed")
	}
	conversationID = strings.TrimSpace(conversationID)
	eventKey = strings.TrimSpace(eventKey)
	eventType = strings.TrimSpace(eventType)
	if conversationID == "" || eventKey == "" || eventType == "" {
		return false, errors.New("conversationID, eventKey and eventType are required")
	}
	clean, ok := sanitizeProgressData(data)
	if !ok || len([]byte(clean)) > chatGPTCloudProgressMaxBytes {
		return false, errors.New("invalid or oversized SSE progress data")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return false, errors.New("progress store is closed")
	}
	if err := s.cleanupIfDueLocked(time.Now().UTC()); err != nil {
		return false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	now := time.Now().UTC()
	receipt, err := tx.Exec(`INSERT OR IGNORE INTO sse_progress_receipts(conversation_id,event_key,created_at) VALUES(?,?,?)`, conversationID, eventKey, now.Unix())
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	received, err := receipt.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if received == 0 {
		if err = tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if _, err = tx.Exec(`INSERT INTO sse_progress(conversation_id,event_type,data,event_key,created_at) VALUES(?,?,?,?,?)`, conversationID, eventType, clean, eventKey, now.Unix()); err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if _, err = tx.Exec(`DELETE FROM sse_progress WHERE conversation_id = ? AND sequence NOT IN (
        SELECT sequence FROM sse_progress WHERE conversation_id = ? ORDER BY sequence DESC LIMIT ?
    )`, conversationID, conversationID, chatGPTCloudProgressMaxFrames); err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *chatGPTCloudProgressStore) Snapshot(conversationID string, after int64) ([]map[string]any, int64, error) {
	if s == nil {
		return nil, after, errors.New("progress store is closed")
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" || after < 0 {
		return nil, after, errors.New("conversationID and non-negative cursor are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, after, errors.New("progress store is closed")
	}
	if err := s.cleanupIfDueLocked(time.Now().UTC()); err != nil {
		return nil, after, err
	}
	rows, err := s.db.Query(`SELECT sequence,event_type,data,created_at FROM sse_progress
        WHERE conversation_id = ? AND sequence > ? ORDER BY sequence ASC`, conversationID, after)
	if err != nil {
		return nil, after, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	next := after
	for rows.Next() {
		var sequence, createdAt int64
		var eventType, data string
		if err := rows.Scan(&sequence, &eventType, &data, &createdAt); err != nil {
			return nil, after, err
		}
		var value any
		if json.Unmarshal([]byte(data), &value) == nil {
			data = valueToProgressJSON(value)
		}
		out = append(out, map[string]any{"sequence": sequence, "eventType": eventType, "data": data, "createdAt": createdAt})
		next = sequence
	}
	if err := rows.Err(); err != nil {
		return nil, after, err
	}
	return out, next, nil
}

func (s *chatGPTCloudProgressStore) cleanupLocked(cutoff time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("progress store is closed")
	}
	if _, err := s.db.Exec(`DELETE FROM sse_progress WHERE created_at < ?`, cutoff.Unix()); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM sse_progress_receipts WHERE created_at < ?`, cutoff.Unix())
	return err
}

func (s *chatGPTCloudProgressStore) cleanupIfDueLocked(now time.Time) error {
	if !s.lastCleanup.IsZero() && now.Sub(s.lastCleanup) < time.Minute {
		return nil
	}
	if err := s.cleanupLocked(now.Add(-chatGPTCloudProgressTTL)); err != nil {
		return err
	}
	s.lastCleanup = now
	return nil
}

func (s *chatGPTCloudProgressStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func sanitizeProgressData(raw string) (string, bool) {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err == nil {
		redactProgressValue(&value)
		encoded, err := json.Marshal(value)
		return string(encoded), err == nil
	}
	if raw == "[DONE]" || raw == "v1" {
		return raw, true
	}
	return "", false
}

func redactProgressValue(value *any) {
	switch current := (*value).(type) {
	case map[string]any:
		for key := range current {
			lower := strings.ToLower(key)
			if lower == "token" || lower == "authorization" || lower == "cookie" || strings.HasSuffix(lower, "_token") || strings.HasSuffix(lower, "-token") {
				delete(current, key)
				continue
			}
			child := current[key]
			redactProgressValue(&child)
			current[key] = child
		}
	case []any:
		for i := range current {
			redactProgressValue(&current[i])
		}
	}
}

func valueToProgressJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(encoded)
}
