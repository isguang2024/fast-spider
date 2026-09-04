package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/isguang2024/fast-spider/internal/security"
)

const (
	sessionCallbackStoreSchemaVersion = 3
	maxSessionCallbacks               = 64
	maxRecentCallbackEventKeys        = 256
	maxSessionCallbackClaimBatch      = 64
	sessionCallbackClaimLease         = 5 * time.Minute
)

type sessionCallbackError struct {
	code      string
	message   string
	retryable bool
}

func (e *sessionCallbackError) Error() string { return e.message }
func (e *sessionCallbackError) CapabilityError() (string, string, bool) {
	return e.code, e.message, e.retryable
}

type sessionCallbackRegistration struct {
	SourceSessionID       string    `json:"sourceSessionId"`
	TargetSessionID       string    `json:"targetSessionId"`
	MissionID             string    `json:"missionId"`
	TaskID                string    `json:"taskId"`
	Generation            int64     `json:"generation"`
	CallbackType          string    `json:"callbackType"`
	DeliverablePath       string    `json:"deliverablePath,omitempty"`
	BaselineIdentity      string    `json:"baselineIdentity,omitempty"`
	ImmediateWake         bool      `json:"immediateWake,omitempty"`
	Armed                 bool      `json:"armed"`
	ArmedAt               time.Time `json:"armedAt,omitempty"`
	LastEventSequence     int64     `json:"lastEventSequence,omitempty"`
	LastEventKey          string    `json:"lastEventKey,omitempty"`
	RecentEventKeys       []string  `json:"recentEventKeys,omitempty"`
	LastFallbackEventKey  string    `json:"lastFallbackEventKey,omitempty"`
	LastFallbackEventAt   time.Time `json:"lastFallbackEventAt,omitempty"`
	LastDeliveredAt       time.Time `json:"lastDeliveredAt,omitempty"`
	LastDeliveredEnvelope string    `json:"lastDeliveredEnvelope,omitempty"`
	LastNudgeAt           time.Time `json:"lastNudgeAt,omitempty"`
	LastNudgeEnvelope     string    `json:"lastNudgeEnvelope,omitempty"`
	LastResultID          string    `json:"lastResultId,omitempty"`
	LastResultStatus      string    `json:"lastResultStatus,omitempty"`
	LastResultBytes       int64     `json:"lastResultBytes,omitempty"`
	LastResultSHA256      string    `json:"lastResultSHA256,omitempty"`
	LastResultPageCount   int       `json:"lastResultPageCount,omitempty"`
	RegisteredAt          time.Time `json:"registeredAt"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

type sessionCallbackEvent struct {
	SourceSessionID   string    `json:"sourceSessionId"`
	TargetSessionID   string    `json:"targetSessionId"`
	MissionID         string    `json:"missionId"`
	TaskID            string    `json:"taskId"`
	Generation        int64     `json:"generation"`
	EventSequence     int64     `json:"eventSequence"`
	EventKey          string    `json:"eventKey"`
	EventType         string    `json:"eventType"`
	OccurredAt        time.Time `json:"occurredAt"`
	CallbackType      string    `json:"callbackType"`
	ResultText        string    `json:"resultText,omitempty"`
	CallbackOutcome   string    `json:"callbackOutcome"`
	CallbackErrorCode string    `json:"callbackErrorCode,omitempty"`
	ResultID          string    `json:"resultId,omitempty"`
	ResultStatus      string    `json:"resultStatus,omitempty"`
	ResultBytes       int64     `json:"resultBytes,omitempty"`
	ResultSHA256      string    `json:"resultSHA256,omitempty"`
	ResultPageCount   int       `json:"resultPageCount,omitempty"`
	DeliverablePath   string    `json:"deliverablePath,omitempty"`
	DeliverableStatus string    `json:"deliverableStatus,omitempty"`
	ImmediateWake     bool      `json:"immediateWake,omitempty"`
	ClaimID           string    `json:"claimId,omitempty"`
	ClaimedAt         time.Time `json:"claimedAt,omitempty"`
}

type callbackResultMetadata struct {
	ResultID  string
	Status    string
	Bytes     int64
	SHA256    string
	PageCount int
}

type sessionCallbackIndex struct {
	SchemaVersion int                           `json:"schemaVersion"`
	Registrations []sessionCallbackRegistration `json:"registrations"`
	Pending       []sessionCallbackEvent        `json:"pending"`
}

type sessionCallbackStore struct {
	mu                       sync.Mutex
	path                     string
	registrations            map[string]sessionCallbackRegistration
	pending                  map[string]sessionCallbackEvent
	loadErr                  error
	beforeCommitSaveOverride func() error
	syncParentOverride       func(string) error
}

// withCurrentRegistration serializes recovery's watcher creation with owner
// mutations. The callback starts the watcher while the registration lock is
// held, so a snapshot that races unregister cannot leave an orphan watcher.
func (s *sessionCallbackStore) withCurrentRegistration(sourceSessionID string, generation int64, ensure func() error) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return false, callbackStoreUnavailableError()
	}
	registration, exists := s.registrations[sourceSessionID]
	if !exists || registration.Generation != generation {
		return false, nil
	}
	return true, ensure()
}

func newSessionCallbackStore(dataDir string) *sessionCallbackStore {
	store := &sessionCallbackStore{
		path:          filepath.Join(dataDir, "agent", "session-callbacks.json"),
		registrations: map[string]sessionCallbackRegistration{},
		pending:       map[string]sessionCallbackEvent{},
	}
	store.loadErr = store.load()
	return store
}

func (s *sessionCallbackStore) load() error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var index sessionCallbackIndex
	if err := json.Unmarshal(raw, &index); err != nil || (index.SchemaVersion != 1 && index.SchemaVersion != 2 && index.SchemaVersion != sessionCallbackStoreSchemaVersion) || len(index.Registrations) > maxSessionCallbacks || len(index.Pending) > maxSessionCallbacks {
		return fmt.Errorf("invalid session callback index")
	}
	for _, registration := range index.Registrations {
		// Schema 1 registrations predate the explicit register/arm handshake and
		// were live immediately after registration. Preserve that behavior while
		// new schema-2 records can remain durably unarmed across a restart.
		if index.SchemaVersion == 1 {
			registration.Armed = true
			registration.ArmedAt = registration.UpdatedAt
			if registration.ArmedAt.IsZero() {
				registration.ArmedAt = registration.RegisteredAt
			}
		}
		if registration.CallbackType == "" {
			if registration.DeliverablePath != "" {
				registration.CallbackType = protocolv1.CloudCallbackTypeLocalFile
			} else {
				registration.CallbackType = protocolv1.CloudCallbackTypeStatus
			}
		}
		if err := validateSessionCallbackRegistration(registration); err != nil {
			return fmt.Errorf("invalid session callback index: %w", err)
		}
		if _, exists := s.registrations[registration.SourceSessionID]; exists {
			return fmt.Errorf("invalid session callback index: duplicate source session")
		}
		// Older schema-1 records used an unscoped evt_ payload hash. Do not
		// retain it as a permanent identity; the current generation can still
		// deliver its pending item and establish a new bounded key.
		if !isPersistentCallbackEventKey(registration.LastEventKey) {
			registration.LastEventKey = ""
		}
		persistentKeys := registration.RecentEventKeys[:0]
		for _, key := range registration.RecentEventKeys {
			if isPersistentCallbackEventKey(key) {
				persistentKeys = append(persistentKeys, key)
			}
		}
		registration.RecentEventKeys = persistentKeys
		s.registrations[registration.SourceSessionID] = registration
	}
	for _, event := range index.Pending {
		if event.CallbackType == "" {
			if registration, exists := s.registrations[event.SourceSessionID]; exists {
				event.CallbackType = registration.CallbackType
			}
		}
		if event.CallbackOutcome == "" {
			event.CallbackOutcome = "completed"
		}
		if err := validateSessionCallbackEvent(event); err != nil {
			return fmt.Errorf("invalid session callback index: %w", err)
		}
		registration, exists := s.registrations[event.SourceSessionID]
		if !exists || registration.TargetSessionID != event.TargetSessionID || registration.MissionID != event.MissionID || registration.TaskID != event.TaskID || registration.Generation != event.Generation || registration.CallbackType != event.CallbackType || registration.DeliverablePath != event.DeliverablePath || registration.ImmediateWake != event.ImmediateWake {
			return fmt.Errorf("invalid session callback index: pending event has no matching registration")
		}
		if event.EventSequence != registration.LastEventSequence {
			return fmt.Errorf("invalid session callback index: pending event sequence does not match registration")
		}
		if event.EventKey != "" && registration.LastEventKey != "" && event.EventKey != registration.LastEventKey {
			return fmt.Errorf("invalid session callback index: pending event key does not match registration")
		}
		if _, exists := s.pending[event.SourceSessionID]; exists {
			return fmt.Errorf("invalid session callback index: duplicate pending source")
		}
		s.pending[event.SourceSessionID] = event
	}
	return nil
}

func validateSessionCallbackRegistration(registration sessionCallbackRegistration) error {
	if err := validateCallbackOpaqueID(registration.SourceSessionID, "source session ID", 256); err != nil {
		return err
	}
	if err := validateCallbackOpaqueID(registration.TargetSessionID, "target session ID", 256); err != nil {
		return err
	}
	if err := validateCallbackKey(registration.MissionID, "mission ID"); err != nil {
		return err
	}
	if err := validateCallbackKey(registration.TaskID, "task ID"); err != nil {
		return err
	}
	if registration.Generation <= 0 {
		return fmt.Errorf("generation must be positive")
	}
	if registration.CallbackType != protocolv1.CloudCallbackTypeLocalFile && registration.CallbackType != protocolv1.CloudCallbackTypeText && registration.CallbackType != protocolv1.CloudCallbackTypeStatus {
		return fmt.Errorf("invalid callback type")
	}
	if registration.DeliverablePath != "" {
		if !filepath.IsAbs(registration.DeliverablePath) || len(registration.DeliverablePath) > 4096 || strings.ContainsAny(registration.DeliverablePath, "\x00\r\n") {
			return fmt.Errorf("deliverable path must be an absolute local path")
		}
	}
	if registration.CallbackType == protocolv1.CloudCallbackTypeLocalFile && registration.DeliverablePath == "" {
		return fmt.Errorf("local_file callback requires an absolute local path")
	}
	if registration.CallbackType != protocolv1.CloudCallbackTypeLocalFile && registration.DeliverablePath != "" {
		return fmt.Errorf("only local_file callback may have a deliverable path")
	}
	if err := validateCallbackIdentity(registration.BaselineIdentity, "baseline identity"); err != nil {
		return err
	}
	if registration.Armed && registration.ArmedAt.IsZero() {
		return fmt.Errorf("armed callback registration requires armed timestamp")
	}
	if !registration.Armed && !registration.ArmedAt.IsZero() {
		return fmt.Errorf("unarmed callback registration cannot have armed timestamp")
	}
	if registration.LastEventSequence < 0 {
		return fmt.Errorf("last event sequence cannot be negative")
	}
	if err := validateCallbackEventKey(registration.LastEventKey); err != nil {
		return err
	}
	if err := validateCallbackEventKey(registration.LastFallbackEventKey); err != nil {
		return err
	}
	if !registration.LastFallbackEventAt.IsZero() && registration.LastFallbackEventKey == "" {
		return fmt.Errorf("fallback event timestamp has no key")
	}
	if len(registration.RecentEventKeys) > maxRecentCallbackEventKeys {
		return fmt.Errorf("too many recent callback event keys")
	}
	for _, key := range registration.RecentEventKeys {
		if err := validateCallbackEventKey(key); err != nil {
			return err
		}
	}
	if registration.RegisteredAt.IsZero() || registration.UpdatedAt.IsZero() {
		return fmt.Errorf("callback registration timestamps are required")
	}
	if len(registration.LastDeliveredEnvelope) > 128 || strings.ContainsAny(registration.LastDeliveredEnvelope, "\x00\r\n") {
		return fmt.Errorf("invalid delivered envelope ID")
	}
	if len(registration.LastNudgeEnvelope) > 128 || strings.ContainsAny(registration.LastNudgeEnvelope, "\x00\r\n") {
		return fmt.Errorf("invalid callback nudge envelope ID")
	}
	if !registration.LastNudgeAt.IsZero() && registration.LastNudgeEnvelope == "" {
		return fmt.Errorf("callback nudge timestamp has no envelope ID")
	}
	if err := validateCallbackResultMetadata(callbackResultMetadata{registration.LastResultID, registration.LastResultStatus, registration.LastResultBytes, registration.LastResultSHA256, registration.LastResultPageCount}); err != nil {
		return err
	}
	return nil
}

func validateSessionCallbackEvent(event sessionCallbackEvent) error {
	registration := sessionCallbackRegistration{
		SourceSessionID: event.SourceSessionID,
		TargetSessionID: event.TargetSessionID,
		MissionID:       event.MissionID,
		TaskID:          event.TaskID,
		Generation:      event.Generation,
		CallbackType:    event.CallbackType,
		DeliverablePath: event.DeliverablePath,
		RegisteredAt:    time.Unix(1, 0),
		UpdatedAt:       time.Unix(1, 0),
	}
	if err := validateSessionCallbackRegistration(registration); err != nil {
		return err
	}
	if event.EventSequence <= 0 {
		return fmt.Errorf("event sequence must be positive")
	}
	if event.EventKey != "" {
		if err := validateCallbackEventKey(event.EventKey); err != nil {
			return err
		}
	}
	if event.EventType != "conversation.turn.complete" {
		return fmt.Errorf("unsupported callback event type")
	}
	if event.OccurredAt.IsZero() {
		return fmt.Errorf("callback event timestamp is required")
	}
	if event.CallbackType != registration.CallbackType {
		return fmt.Errorf("callback event type does not match registration")
	}
	if event.CallbackOutcome != "completed" && event.CallbackOutcome != "blocked" && event.CallbackOutcome != "failed" {
		return fmt.Errorf("invalid callback outcome")
	}
	if event.CallbackErrorCode != "" && (len(event.CallbackErrorCode) > 64 || strings.ContainsAny(event.CallbackErrorCode, "\x00\r\n\t ")) {
		return fmt.Errorf("invalid callback error code")
	}
	if event.CallbackType == protocolv1.CloudCallbackTypeText {
		if !utf8.ValidString(event.ResultText) || strings.IndexByte(event.ResultText, 0) >= 0 || len(event.ResultText) > protocolv1.CloudCallbackTextMaxBytes || utf8.RuneCountInString(event.ResultText) > protocolv1.CloudCallbackTextMaxRunes || event.CallbackOutcome == "completed" && strings.TrimSpace(event.ResultText) == "" {
			return fmt.Errorf("invalid callback text")
		}
	} else if event.ResultText != "" {
		return fmt.Errorf("callback text is only valid for text callbacks")
	}
	if err := validateCallbackResultMetadata(callbackResultMetadata{event.ResultID, event.ResultStatus, event.ResultBytes, event.ResultSHA256, event.ResultPageCount}); err != nil {
		return err
	}
	if event.DeliverablePath != "" {
		if !filepath.IsAbs(event.DeliverablePath) || len(event.DeliverablePath) > 4096 || strings.ContainsAny(event.DeliverablePath, "\x00\r\n") {
			return fmt.Errorf("invalid callback deliverable path")
		}
		if event.DeliverableStatus != "" && !stringInSet(event.DeliverableStatus, "ready", "missing", "invalid", "unreadable", "too_large") {
			return fmt.Errorf("invalid callback deliverable status")
		}
	} else if event.DeliverableStatus != "" {
		return fmt.Errorf("callback deliverable status has no path")
	}
	if event.ClaimID != "" {
		if err := validateSessionCallbackClaimID(event.ClaimID); err != nil {
			return err
		}
		if event.ClaimedAt.IsZero() {
			return fmt.Errorf("callback claim timestamp is required")
		}
	} else if !event.ClaimedAt.IsZero() {
		return fmt.Errorf("callback claim timestamp has no claim ID")
	}
	return nil
}

func validateCallbackResultMetadata(metadata callbackResultMetadata) error {
	if metadata.ResultID != "" {
		if err := validateCallbackOpaqueID(metadata.ResultID, "result ID", 256); err != nil {
			return err
		}
	}
	if metadata.Status != "" && !stringInSet(metadata.Status, "open", "ready", "failed", "aborted", "running", "completed", "canceled", "unknown") {
		return fmt.Errorf("invalid callback result status")
	}
	if metadata.Bytes < 0 || metadata.Bytes > 256<<20 || metadata.PageCount < 0 || metadata.PageCount > 8 {
		return fmt.Errorf("invalid callback result bounds")
	}
	if metadata.SHA256 != "" && (len(metadata.SHA256) != len("sha256:")+64 || !strings.HasPrefix(metadata.SHA256, "sha256:")) {
		return fmt.Errorf("invalid callback result sha256")
	}
	return nil
}

func validateCallbackEventKey(key string) error {
	if key == "" {
		return nil
	}
	if len(key) > 128 || strings.ContainsAny(key, "\x00\r\n\t ") {
		return fmt.Errorf("invalid callback event key")
	}
	return nil
}

func validateCallbackIdentity(value, label string) error {
	if value == "" {
		return nil
	}
	if len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("invalid callback %s", label)
	}
	return nil
}

func isPersistentCallbackEventKey(key string) bool {
	return strings.HasPrefix(key, "provider_evt_") || strings.HasPrefix(key, "completion_")
}

func validateCallbackOpaqueID(value, label string, limit int) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > limit || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("%s is required and must be at most %d safe characters", label, limit)
	}
	return nil
}

func validateCallbackKey(value, label string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n\t ") {
		return fmt.Errorf("%s is required and must be at most 128 non-whitespace characters", label)
	}
	return nil
}

func validateSessionCallbackClaimID(value string) error {
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n\t ") {
		return fmt.Errorf("invalid callback claim ID")
	}
	return nil
}

func callbackClaimActive(event sessionCallbackEvent, now time.Time) bool {
	return event.ClaimID != "" && !event.ClaimedAt.IsZero() && now.Before(event.ClaimedAt.UTC().Add(sessionCallbackClaimLease))
}

func (s *sessionCallbackStore) register(request sessionCallbackRegistration) (sessionCallbackRegistration, bool, error) {
	now := time.Now().UTC()
	request.SourceSessionID = strings.TrimSpace(request.SourceSessionID)
	request.TargetSessionID = strings.TrimSpace(request.TargetSessionID)
	request.MissionID = strings.TrimSpace(request.MissionID)
	request.TaskID = strings.TrimSpace(request.TaskID)
	request.CallbackType = strings.TrimSpace(request.CallbackType)
	if request.CallbackType == "" {
		if request.DeliverablePath != "" {
			request.CallbackType = protocolv1.CloudCallbackTypeLocalFile
		} else {
			request.CallbackType = protocolv1.CloudCallbackTypeStatus
		}
	}
	request.BaselineIdentity = strings.TrimSpace(request.BaselineIdentity)
	request.RegisteredAt = now
	request.UpdatedAt = now
	if request.Armed {
		request.ArmedAt = now
	} else {
		request.ArmedAt = time.Time{}
	}
	if err := validateSessionCallbackRegistration(request); err != nil {
		return sessionCallbackRegistration{}, false, &sessionCallbackError{code: "INVALID_REQUEST", message: err.Error()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return sessionCallbackRegistration{}, false, callbackStoreUnavailableError()
	}
	current, exists := s.registrations[request.SourceSessionID]
	if exists {
		if current.TargetSessionID != request.TargetSessionID || current.MissionID != request.MissionID || current.TaskID != request.TaskID || current.CallbackType != request.CallbackType || current.ImmediateWake != request.ImmediateWake || !callbackDeliverablePathEqual(current.DeliverablePath, request.DeliverablePath) {
			return sessionCallbackRegistration{}, false, &sessionCallbackError{code: "CALLBACK_OWNER_CONFLICT", message: "source session already has a different callback owner"}
		}
		if request.Generation < current.Generation {
			return sessionCallbackRegistration{}, false, &sessionCallbackError{code: "CALLBACK_GENERATION_STALE", message: "callback generation is older than the registered owner"}
		}
		if request.Generation == current.Generation {
			if request.BaselineIdentity != "" && request.BaselineIdentity != current.BaselineIdentity {
				return sessionCallbackRegistration{}, false, &sessionCallbackError{code: "CALLBACK_BASELINE_CONFLICT", message: "callback baseline does not match the registered task attempt"}
			}
			// Compatibility for an older Hub talking to a newer Node: legacy
			// registration requests are immediately armed. A current Hub sets the
			// explicit arm-required flag and uses session.callback.arm instead.
			if request.Armed && !current.Armed {
				previous := current
				current.Armed = true
				current.ArmedAt = now
				current.UpdatedAt = now
				s.registrations[request.SourceSessionID] = current
				if committed, err := s.saveLocked(); err != nil {
					if !committed {
						s.registrations[request.SourceSessionID] = previous
					}
					return sessionCallbackRegistration{}, false, err
				}
				return current, false, nil
			}
			return current, true, nil
		}
		request.RegisteredAt = current.RegisteredAt
	}
	if !exists && len(s.registrations) >= maxSessionCallbacks {
		return sessionCallbackRegistration{}, false, &sessionCallbackError{code: "RESOURCE_LIMIT", message: "session callback registry is full"}
	}
	previousPending, hadPending := s.pending[request.SourceSessionID]
	s.registrations[request.SourceSessionID] = request
	delete(s.pending, request.SourceSessionID)
	if committed, err := s.saveLocked(); err != nil {
		if !committed {
			if exists {
				s.registrations[request.SourceSessionID] = current
			} else {
				delete(s.registrations, request.SourceSessionID)
			}
			if hadPending {
				s.pending[request.SourceSessionID] = previousPending
			}
		}
		return sessionCallbackRegistration{}, false, err
	}
	return request, false, nil
}

func (s *sessionCallbackStore) arm(sourceSessionID string, generation int64, expectedOwner sessionCallbackRegistration) (sessionCallbackRegistration, bool, error) {
	sourceSessionID = strings.TrimSpace(sourceSessionID)
	if err := validateCallbackOpaqueID(sourceSessionID, "source session ID", 256); err != nil {
		return sessionCallbackRegistration{}, false, &sessionCallbackError{code: "INVALID_REQUEST", message: err.Error()}
	}
	if generation <= 0 {
		return sessionCallbackRegistration{}, false, &sessionCallbackError{code: "INVALID_REQUEST", message: "callbackGeneration must be positive"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return sessionCallbackRegistration{}, false, callbackStoreUnavailableError()
	}
	current, exists := s.registrations[sourceSessionID]
	if !exists {
		return sessionCallbackRegistration{}, false, &sessionCallbackError{code: "CALLBACK_ROUTE_NOT_FOUND", message: "callback route is not registered"}
	}
	if current.Generation != generation {
		return sessionCallbackRegistration{}, false, &sessionCallbackError{code: "CALLBACK_GENERATION_STALE", message: "callback generation does not match the registered owner"}
	}
	if strings.TrimSpace(expectedOwner.TargetSessionID) != "" && current.TargetSessionID != strings.TrimSpace(expectedOwner.TargetSessionID) ||
		strings.TrimSpace(expectedOwner.MissionID) != "" && current.MissionID != strings.TrimSpace(expectedOwner.MissionID) ||
		strings.TrimSpace(expectedOwner.TaskID) != "" && current.TaskID != strings.TrimSpace(expectedOwner.TaskID) {
		return sessionCallbackRegistration{}, false, &sessionCallbackError{code: "CALLBACK_OWNER_CONFLICT", message: "callback owner does not match the arm request"}
	}
	if current.Armed {
		return current, true, nil
	}
	previous := current
	now := time.Now().UTC()
	current.Armed = true
	current.ArmedAt = now
	current.UpdatedAt = now
	s.registrations[sourceSessionID] = current
	if committed, err := s.saveLocked(); err != nil {
		if !committed {
			s.registrations[sourceSessionID] = previous
		}
		return sessionCallbackRegistration{}, false, err
	}
	return current, false, nil
}

func callbackDeliverablePathEqual(left, right string) bool {
	left, right = strings.TrimSpace(left), strings.TrimSpace(right)
	if left == "" || right == "" {
		return left == right
	}
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func (s *sessionCallbackStore) unregister(sourceSessionID string, generation int64, expectedOwner ...sessionCallbackRegistration) (bool, error) {
	sourceSessionID = strings.TrimSpace(sourceSessionID)
	if err := validateCallbackOpaqueID(sourceSessionID, "source session ID", 256); err != nil {
		return false, &sessionCallbackError{code: "INVALID_REQUEST", message: err.Error()}
	}
	if generation <= 0 {
		return false, &sessionCallbackError{code: "INVALID_REQUEST", message: "callbackGeneration must be positive"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return false, callbackStoreUnavailableError()
	}
	current, exists := s.registrations[sourceSessionID]
	if !exists {
		return false, nil
	}
	if current.Generation != generation {
		return false, &sessionCallbackError{code: "CALLBACK_GENERATION_STALE", message: "callback generation does not match the registered owner"}
	}
	if len(expectedOwner) > 0 {
		expected := expectedOwner[0]
		if strings.TrimSpace(expected.TargetSessionID) != "" && current.TargetSessionID != strings.TrimSpace(expected.TargetSessionID) ||
			strings.TrimSpace(expected.MissionID) != "" && current.MissionID != strings.TrimSpace(expected.MissionID) ||
			strings.TrimSpace(expected.TaskID) != "" && current.TaskID != strings.TrimSpace(expected.TaskID) {
			return false, &sessionCallbackError{code: "CALLBACK_OWNER_CONFLICT", message: "callback owner does not match the unregister request"}
		}
	}
	previousPending, hadPending := s.pending[sourceSessionID]
	delete(s.registrations, sourceSessionID)
	delete(s.pending, sourceSessionID)
	if committed, err := s.saveLocked(); err != nil {
		if !committed {
			s.registrations[sourceSessionID] = current
			if hadPending {
				s.pending[sourceSessionID] = previousPending
			}
		}
		return false, err
	}
	return true, nil
}

func (s *sessionCallbackStore) enqueue(event chatgptCloudEvent) (bool, error) {
	if event.Type != "conversation.turn.complete" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return false, callbackStoreUnavailableError()
	}
	registration, exists := s.registrations[event.ConversationID]
	if !exists {
		return false, nil
	}
	if !registration.Armed {
		return false, nil
	}
	if event.DeliverablePath != "" && registration.DeliverablePath != event.DeliverablePath {
		return false, &sessionCallbackError{code: "INVALID_REQUEST", message: "callback deliverable path does not match the registered source"}
	}
	// Node is the durable wake queue for Hub-pushed completions and the recovery
	// queue for missed Provider events. It never copies local files or uploads
	// result artifacts; it retains only bounded inline text when requested.
	event.CallbackType = registration.CallbackType
	if event.CallbackOutcome == "" {
		event.CallbackOutcome = "completed"
	}
	event.DeliverablePath = registration.DeliverablePath
	event.DeliverableStatus = ""
	event.ResultID = ""
	event.ResultStatus = ""
	event.ResultBytes = 0
	event.ResultSHA256 = ""
	event.ResultPageCount = 0
	if registration.CallbackType != protocolv1.CloudCallbackTypeText {
		event.ResultText = ""
		event.CallbackErrorCode = ""
	}
	if event.Sequence <= registration.LastEventSequence {
		return false, nil
	}
	previousRegistration := registration
	previousPending, hadPending := s.pending[event.ConversationID]
	eventKey := sessionCallbackCompletionEventKey(registration)
	now := time.Now().UTC()
	if isPersistentCallbackEventKey(eventKey) {
		for _, recentKey := range registration.RecentEventKeys {
			if recentKey == eventKey {
				return false, nil
			}
		}
		if eventKey == registration.LastEventKey {
			return false, nil
		}
	} else if eventKey == registration.LastFallbackEventKey && !registration.LastFallbackEventAt.IsZero() && now.Sub(registration.LastFallbackEventAt) < chatgptRealtimeFallbackDedupWindow {
		return false, nil
	}
	registration.LastEventSequence = event.Sequence
	if isPersistentCallbackEventKey(eventKey) {
		registration.LastEventKey = eventKey
		registration.RecentEventKeys = append(registration.RecentEventKeys, eventKey)
		if len(registration.RecentEventKeys) > maxRecentCallbackEventKeys {
			registration.RecentEventKeys = append([]string(nil), registration.RecentEventKeys[len(registration.RecentEventKeys)-maxRecentCallbackEventKeys:]...)
		}
	} else {
		registration.LastFallbackEventKey = eventKey
		registration.LastFallbackEventAt = now
	}
	registration.UpdatedAt = now
	pending := sessionCallbackEvent{
		SourceSessionID:   registration.SourceSessionID,
		TargetSessionID:   registration.TargetSessionID,
		MissionID:         registration.MissionID,
		TaskID:            registration.TaskID,
		Generation:        registration.Generation,
		EventSequence:     event.Sequence,
		EventKey:          eventKey,
		EventType:         event.Type,
		OccurredAt:        event.Timestamp.UTC(),
		CallbackType:      event.CallbackType,
		ResultText:        event.ResultText,
		CallbackOutcome:   event.CallbackOutcome,
		CallbackErrorCode: event.CallbackErrorCode,
		ResultID:          event.ResultID,
		ResultStatus:      event.ResultStatus,
		ResultBytes:       event.ResultBytes,
		ResultSHA256:      event.ResultSHA256,
		ResultPageCount:   event.ResultPageCount,
		DeliverablePath:   event.DeliverablePath,
		DeliverableStatus: event.DeliverableStatus,
		ImmediateWake:     registration.ImmediateWake,
	}
	if err := validateSessionCallbackEvent(pending); err != nil {
		return false, err
	}
	s.registrations[event.ConversationID] = registration
	registration.LastResultID = pending.ResultID
	registration.LastResultStatus = pending.ResultStatus
	registration.LastResultBytes = pending.ResultBytes
	registration.LastResultSHA256 = pending.ResultSHA256
	registration.LastResultPageCount = pending.ResultPageCount
	s.registrations[event.ConversationID] = registration
	s.pending[event.ConversationID] = pending
	if committed, err := s.saveLocked(); err != nil {
		if !committed {
			s.registrations[event.ConversationID] = previousRegistration
			if hadPending {
				s.pending[event.ConversationID] = previousPending
			} else {
				delete(s.pending, event.ConversationID)
			}
		}
		return false, err
	}
	return true, nil
}

func sessionCallbackCompletionEventKey(registration sessionCallbackRegistration) string {
	sum := sha256.Sum256([]byte(registration.MissionID + "\x00" + registration.TaskID + "\x00" + fmt.Sprintf("%d", registration.Generation) + "\x00completion"))
	return "completion_" + hex.EncodeToString(sum[:])[:48]
}

func (s *sessionCallbackStore) resultFor(sourceSessionID string) (callbackResultMetadata, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return callbackResultMetadata{}, false, callbackStoreUnavailableError()
	}
	registration, ok := s.registrations[strings.TrimSpace(sourceSessionID)]
	if !ok || (registration.LastResultID == "" && registration.LastResultStatus == "") {
		return callbackResultMetadata{}, false, nil
	}
	return callbackResultMetadata{registration.LastResultID, registration.LastResultStatus, registration.LastResultBytes, registration.LastResultSHA256, registration.LastResultPageCount}, true, nil
}

func (s *sessionCallbackStore) registrationFor(sourceSessionID string) (sessionCallbackRegistration, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return sessionCallbackRegistration{}, false, callbackStoreUnavailableError()
	}
	registration, ok := s.registrations[strings.TrimSpace(sourceSessionID)]
	return registration, ok, nil
}

func (s *sessionCallbackStore) registrationsSnapshot(sourceSessionID, targetSessionID string) ([]sessionCallbackRegistration, map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, nil, callbackStoreUnavailableError()
	}
	items := make([]sessionCallbackRegistration, 0, len(s.registrations))
	pendingCounts := map[string]int{}
	for source, event := range s.pending {
		pendingCounts[source]++
		_ = event
	}
	for _, registration := range s.registrations {
		if sourceSessionID != "" && registration.SourceSessionID != sourceSessionID {
			continue
		}
		if targetSessionID != "" && registration.TargetSessionID != targetSessionID {
			continue
		}
		items = append(items, registration)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].TargetSessionID != items[j].TargetSessionID {
			return items[i].TargetSessionID < items[j].TargetSessionID
		}
		return items[i].SourceSessionID < items[j].SourceSessionID
	})
	return items, pendingCounts, nil
}

func (s *sessionCallbackStore) pendingByTarget() (map[string][]sessionCallbackEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, callbackStoreUnavailableError()
	}
	grouped := map[string][]sessionCallbackEvent{}
	for _, event := range s.pending {
		grouped[event.TargetSessionID] = append(grouped[event.TargetSessionID], event)
	}
	for target := range grouped {
		sort.Slice(grouped[target], func(i, j int) bool {
			left, right := grouped[target][i], grouped[target][j]
			if left.MissionID != right.MissionID {
				return left.MissionID < right.MissionID
			}
			if left.TaskID != right.TaskID {
				return left.TaskID < right.TaskID
			}
			return left.SourceSessionID < right.SourceSessionID
		})
	}
	return grouped, nil
}

func (s *sessionCallbackStore) pendingSnapshot(sourceSessionID, targetSessionID string) ([]sessionCallbackEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, callbackStoreUnavailableError()
	}
	events := make([]sessionCallbackEvent, 0, len(s.pending))
	for _, event := range s.pending {
		if sourceSessionID != "" && event.SourceSessionID != sourceSessionID {
			continue
		}
		if targetSessionID != "" && event.TargetSessionID != targetSessionID {
			continue
		}
		events = append(events, event)
	}
	sort.Slice(events, func(i, j int) bool {
		left, right := events[i], events[j]
		if left.TargetSessionID != right.TargetSessionID {
			return left.TargetSessionID < right.TargetSessionID
		}
		if left.MissionID != right.MissionID {
			return left.MissionID < right.MissionID
		}
		if left.TaskID != right.TaskID {
			return left.TaskID < right.TaskID
		}
		return left.SourceSessionID < right.SourceSessionID
	})
	return events, nil
}

// claim reserves a bounded batch for one target. A caller-supplied claim ID is
// idempotent while the lease is active and after it has been acknowledged;
// omitting it allocates a fresh opaque ID.
func (s *sessionCallbackStore) claim(targetSessionID, requestedClaimID string, limit int, now time.Time) (string, []sessionCallbackEvent, error) {
	targetSessionID = strings.TrimSpace(targetSessionID)
	if err := validateCallbackOpaqueID(targetSessionID, "callback target session ID", 256); err != nil {
		return "", nil, &sessionCallbackError{code: "INVALID_REQUEST", message: err.Error()}
	}
	if limit == 0 {
		limit = maxSessionCallbackClaimBatch
	}
	if limit < 1 || limit > maxSessionCallbackClaimBatch {
		return "", nil, &sessionCallbackError{code: "INVALID_REQUEST", message: fmt.Sprintf("callbackClaimLimit must be between 1 and %d", maxSessionCallbackClaimBatch)}
	}
	requestedClaimID = strings.TrimSpace(requestedClaimID)
	if requestedClaimID != "" {
		if err := validateSessionCallbackClaimID(requestedClaimID); err != nil {
			return "", nil, &sessionCallbackError{code: "INVALID_REQUEST", message: err.Error()}
		}
	}
	now = now.UTC()
	claimID := requestedClaimID
	if claimID == "" {
		var err error
		claimID, err = security.RandomOpaque("claim_")
		if err != nil {
			return "", nil, err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return "", nil, callbackStoreUnavailableError()
	}
	previousRegistrations := cloneSessionCallbackRegistrations(s.registrations)
	previousPending := cloneSessionCallbackEvents(s.pending)
	restore := func() {
		s.registrations = previousRegistrations
		s.pending = previousPending
	}
	released := false
	persistReleased := func() error {
		if !released {
			return nil
		}
		if _, err := s.saveLocked(); err != nil {
			restore()
			return err
		}
		return nil
	}

	// Expired leases are immediately reusable. This is done under the same lock
	// as selection so two concurrent claimers cannot observe the same item.
	for source, event := range s.pending {
		if event.ClaimID != "" && !callbackClaimActive(event, now) {
			event.ClaimID = ""
			event.ClaimedAt = time.Time{}
			s.pending[source] = event
			released = true
		}
	}

	var existing []sessionCallbackEvent
	for _, event := range s.pending {
		if event.ClaimID != claimID {
			continue
		}
		if event.TargetSessionID != targetSessionID {
			return "", nil, &sessionCallbackError{code: "CALLBACK_CLAIM_CONFLICT", message: "callback claim ID belongs to another target session"}
		}
		if callbackClaimActive(event, now) {
			existing = append(existing, event)
		}
	}
	if len(existing) > 0 {
		if err := persistReleased(); err != nil {
			return "", nil, err
		}
		sortSessionCallbackEvents(existing)
		return claimID, existing, nil
	}
	if requestedClaimID != "" {
		for _, registration := range s.registrations {
			if registration.TargetSessionID == targetSessionID && registration.LastDeliveredEnvelope == claimID {
				if err := persistReleased(); err != nil {
					return "", nil, err
				}
				return claimID, nil, nil
			}
		}
	}

	available := make([]sessionCallbackEvent, 0, len(s.pending))
	for _, event := range s.pending {
		if event.TargetSessionID != targetSessionID || event.ClaimID != "" {
			continue
		}
		available = append(available, event)
	}
	sortSessionCallbackEvents(available)
	if len(available) > limit {
		available = available[:limit]
	}
	textBytes := 0
	bounded := available[:0]
	for _, event := range available {
		next := textBytes + len(event.ResultText)
		if len(bounded) > 0 && next > protocolv1.CloudCallbackClaimMaxTextBytes {
			break
		}
		if next > protocolv1.CloudCallbackClaimMaxTextBytes {
			return "", nil, &sessionCallbackError{code: "RESOURCE_LIMIT", message: "callback text exceeds the claim payload budget"}
		}
		bounded = append(bounded, event)
		textBytes = next
	}
	available = bounded
	if len(available) == 0 {
		if err := persistReleased(); err != nil {
			return "", nil, err
		}
		return requestedClaimID, nil, nil
	}
	for _, selected := range available {
		event := s.pending[selected.SourceSessionID]
		event.ClaimID = claimID
		event.ClaimedAt = now
		s.pending[selected.SourceSessionID] = event
		registration := s.registrations[selected.SourceSessionID]
		registration.UpdatedAt = now
		s.registrations[selected.SourceSessionID] = registration
	}
	if _, err := s.saveLocked(); err != nil {
		restore()
		return "", nil, err
	}
	for i := range available {
		available[i].ClaimID = claimID
		available[i].ClaimedAt = now
	}
	return claimID, available, nil
}

func (s *sessionCallbackStore) acknowledgeClaim(targetSessionID, claimID string, now time.Time) (int, error) {
	targetSessionID = strings.TrimSpace(targetSessionID)
	claimID = strings.TrimSpace(claimID)
	if err := validateCallbackOpaqueID(targetSessionID, "callback target session ID", 256); err != nil {
		return 0, &sessionCallbackError{code: "INVALID_REQUEST", message: err.Error()}
	}
	if err := validateSessionCallbackClaimID(claimID); err != nil {
		return 0, &sessionCallbackError{code: "INVALID_REQUEST", message: err.Error()}
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return 0, callbackStoreUnavailableError()
	}
	previousRegistrations := cloneSessionCallbackRegistrations(s.registrations)
	previousPending := cloneSessionCallbackEvents(s.pending)
	acked := 0
	expired := false
	for source, event := range s.pending {
		if event.TargetSessionID != targetSessionID || event.ClaimID != claimID {
			continue
		}
		if !callbackClaimActive(event, now) {
			expired = true
			continue
		}
		delete(s.pending, source)
		registration := s.registrations[source]
		registration.LastDeliveredAt = now
		registration.LastDeliveredEnvelope = claimID
		registration.UpdatedAt = now
		s.registrations[source] = registration
		acked++
	}
	if acked == 0 {
		if expired {
			return 0, &sessionCallbackError{code: "CALLBACK_CLAIM_EXPIRED", message: "callback claim lease expired; claim the queue again before acknowledging"}
		}
		for _, registration := range s.registrations {
			if registration.TargetSessionID == targetSessionID && registration.LastDeliveredEnvelope == claimID {
				return 0, nil
			}
		}
		return 0, &sessionCallbackError{code: "CALLBACK_CLAIM_NOT_FOUND", message: "callback claim is not active for the target session"}
	}
	if _, err := s.saveLocked(); err != nil {
		s.registrations = previousRegistrations
		s.pending = previousPending
		return 0, err
	}
	return acked, nil
}

func (s *sessionCallbackStore) releaseExpiredClaims(now time.Time) (int, error) {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return 0, callbackStoreUnavailableError()
	}
	previousRegistrations := cloneSessionCallbackRegistrations(s.registrations)
	previousPending := cloneSessionCallbackEvents(s.pending)
	released := 0
	for source, event := range s.pending {
		if event.ClaimID == "" || callbackClaimActive(event, now) {
			continue
		}
		event.ClaimID = ""
		event.ClaimedAt = time.Time{}
		s.pending[source] = event
		registration := s.registrations[source]
		registration.UpdatedAt = now
		s.registrations[source] = registration
		released++
	}
	if released == 0 {
		return 0, nil
	}
	if _, err := s.saveLocked(); err != nil {
		s.registrations = previousRegistrations
		s.pending = previousPending
		return 0, err
	}
	return released, nil
}

func (s *sessionCallbackStore) nudgeSchedule(targetSessionID string, now time.Time, interval time.Duration) (bool, time.Time, error) {
	targetSessionID = strings.TrimSpace(targetSessionID)
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return false, time.Time{}, callbackStoreUnavailableError()
	}
	var latest time.Time
	found := false
	for _, registration := range s.registrations {
		if registration.TargetSessionID != targetSessionID {
			continue
		}
		found = true
		if registration.LastNudgeAt.After(latest) {
			latest = registration.LastNudgeAt
		}
	}
	if !found {
		return false, time.Time{}, nil
	}
	if interval <= 0 {
		interval = sessionCallbackNudgeInterval
	}
	if latest.IsZero() {
		return true, now, nil
	}
	next := latest.UTC().Add(interval)
	return !now.Before(next), next, nil
}

func (s *sessionCallbackStore) recordNudge(targetSessionID, envelopeID string, now time.Time) error {
	targetSessionID = strings.TrimSpace(targetSessionID)
	if err := validateSessionCallbackClaimID(envelopeID); err != nil {
		return &sessionCallbackError{code: "INVALID_REQUEST", message: err.Error()}
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return callbackStoreUnavailableError()
	}
	previous := cloneSessionCallbackRegistrations(s.registrations)
	updated := 0
	for source, registration := range s.registrations {
		if registration.TargetSessionID != targetSessionID {
			continue
		}
		registration.LastNudgeAt = now
		registration.LastNudgeEnvelope = envelopeID
		registration.UpdatedAt = now
		s.registrations[source] = registration
		updated++
	}
	if updated == 0 {
		return nil
	}
	if _, err := s.saveLocked(); err != nil {
		s.registrations = previous
		return err
	}
	return nil
}

func cloneSessionCallbackRegistrations(input map[string]sessionCallbackRegistration) map[string]sessionCallbackRegistration {
	output := make(map[string]sessionCallbackRegistration, len(input))
	for key, value := range input {
		value.RecentEventKeys = append([]string(nil), value.RecentEventKeys...)
		output[key] = value
	}
	return output
}

func cloneSessionCallbackEvents(input map[string]sessionCallbackEvent) map[string]sessionCallbackEvent {
	output := make(map[string]sessionCallbackEvent, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func sortSessionCallbackEvents(events []sessionCallbackEvent) {
	sort.Slice(events, func(i, j int) bool {
		left, right := events[i], events[j]
		if left.TargetSessionID != right.TargetSessionID {
			return left.TargetSessionID < right.TargetSessionID
		}
		if left.MissionID != right.MissionID {
			return left.MissionID < right.MissionID
		}
		if left.TaskID != right.TaskID {
			return left.TaskID < right.TaskID
		}
		return left.SourceSessionID < right.SourceSessionID
	})
}

func sessionCallbackEnvelopeID(targetSessionID string, events []sessionCallbackEvent) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(targetSessionID))
	for _, event := range events {
		eventKey := event.EventKey
		if eventKey == "" {
			eventKey = fmt.Sprintf("legacy:%s:%d:%d", event.SourceSessionID, event.Generation, event.EventSequence)
		}
		_, _ = fmt.Fprintf(hash, "\x00%s\x00%d\x00%d\x00%s", event.SourceSessionID, event.Generation, event.EventSequence, eventKey)
	}
	return "cb_" + hex.EncodeToString(hash.Sum(nil))[:32]
}

func (s *sessionCallbackStore) acknowledge(targetSessionID, envelopeID string, delivered []sessionCallbackEvent, deliveredAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return callbackStoreUnavailableError()
	}
	previousRegistrations := map[string]sessionCallbackRegistration{}
	previousPending := map[string]sessionCallbackEvent{}
	for _, event := range delivered {
		current, exists := s.pending[event.SourceSessionID]
		if !exists || current.TargetSessionID != targetSessionID || current.Generation != event.Generation || current.EventSequence != event.EventSequence {
			continue
		}
		previousPending[event.SourceSessionID] = current
		delete(s.pending, event.SourceSessionID)
		registration := s.registrations[event.SourceSessionID]
		previousRegistrations[event.SourceSessionID] = registration
		registration.LastDeliveredAt = deliveredAt.UTC()
		registration.LastDeliveredEnvelope = envelopeID
		registration.UpdatedAt = deliveredAt.UTC()
		s.registrations[event.SourceSessionID] = registration
	}
	if len(previousPending) == 0 {
		return nil
	}
	if committed, err := s.saveLocked(); err != nil {
		if !committed {
			for source, event := range previousPending {
				s.pending[source] = event
				s.registrations[source] = previousRegistrations[source]
			}
		}
		return err
	}
	return nil
}

func (s *sessionCallbackStore) maxEventSequence() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var maximum int64
	for _, registration := range s.registrations {
		if registration.LastEventSequence > maximum {
			maximum = registration.LastEventSequence
		}
	}
	return maximum
}

func (s *sessionCallbackStore) saveLocked() (bool, error) {
	if s.beforeCommitSaveOverride != nil {
		return false, s.beforeCommitSaveOverride()
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return false, err
	}
	registrations := make([]sessionCallbackRegistration, 0, len(s.registrations))
	for _, registration := range s.registrations {
		registrations = append(registrations, registration)
	}
	sort.Slice(registrations, func(i, j int) bool { return registrations[i].SourceSessionID < registrations[j].SourceSessionID })
	pending := make([]sessionCallbackEvent, 0, len(s.pending))
	for _, event := range s.pending {
		pending = append(pending, event)
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].SourceSessionID < pending[j].SourceSessionID })
	raw, err := json.Marshal(sessionCallbackIndex{SchemaVersion: sessionCallbackStoreSchemaVersion, Registrations: registrations, Pending: pending})
	if err != nil {
		return false, err
	}
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".session-callbacks-*")
	if err != nil {
		return false, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return false, err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return false, err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return false, err
	}
	if err := temp.Close(); err != nil {
		return false, err
	}
	if err := replaceAgentFile(tempPath, s.path); err != nil {
		return false, err
	}
	syncParent := syncAgentParentDirectory
	if s.syncParentOverride != nil {
		syncParent = s.syncParentOverride
	}
	if err := syncParent(s.path); err != nil {
		return true, err
	}
	return true, nil
}

func callbackStoreUnavailableError() error {
	return &sessionCallbackError{code: "AGENT_CALLBACK_STORE_UNAVAILABLE", message: "session callback state is unavailable; repair the persisted index before changing callbacks"}
}
