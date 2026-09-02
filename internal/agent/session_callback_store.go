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

const (
	sessionCallbackStoreSchemaVersion = 1
	maxSessionCallbacks               = 64
	maxRecentCallbackEventKeys        = 256
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
	LastEventSequence     int64     `json:"lastEventSequence,omitempty"`
	LastEventKey          string    `json:"lastEventKey,omitempty"`
	RecentEventKeys       []string  `json:"recentEventKeys,omitempty"`
	LastFallbackEventKey  string    `json:"lastFallbackEventKey,omitempty"`
	LastFallbackEventAt   time.Time `json:"lastFallbackEventAt,omitempty"`
	LastDeliveredAt       time.Time `json:"lastDeliveredAt,omitempty"`
	LastDeliveredEnvelope string    `json:"lastDeliveredEnvelope,omitempty"`
	RegisteredAt          time.Time `json:"registeredAt"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

type sessionCallbackEvent struct {
	SourceSessionID string    `json:"sourceSessionId"`
	TargetSessionID string    `json:"targetSessionId"`
	MissionID       string    `json:"missionId"`
	TaskID          string    `json:"taskId"`
	Generation      int64     `json:"generation"`
	EventSequence   int64     `json:"eventSequence"`
	EventKey        string    `json:"eventKey"`
	EventType       string    `json:"eventType"`
	OccurredAt      time.Time `json:"occurredAt"`
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
	if err := json.Unmarshal(raw, &index); err != nil || index.SchemaVersion != sessionCallbackStoreSchemaVersion || len(index.Registrations) > maxSessionCallbacks || len(index.Pending) > maxSessionCallbacks {
		return fmt.Errorf("invalid session callback index")
	}
	for _, registration := range index.Registrations {
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
		if err := validateSessionCallbackEvent(event); err != nil {
			return fmt.Errorf("invalid session callback index: %w", err)
		}
		registration, exists := s.registrations[event.SourceSessionID]
		if !exists || registration.TargetSessionID != event.TargetSessionID || registration.MissionID != event.MissionID || registration.TaskID != event.TaskID || registration.Generation != event.Generation {
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
	return nil
}

func validateSessionCallbackEvent(event sessionCallbackEvent) error {
	registration := sessionCallbackRegistration{
		SourceSessionID: event.SourceSessionID,
		TargetSessionID: event.TargetSessionID,
		MissionID:       event.MissionID,
		TaskID:          event.TaskID,
		Generation:      event.Generation,
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

func isPersistentCallbackEventKey(key string) bool {
	return strings.HasPrefix(key, "provider_evt_")
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

func (s *sessionCallbackStore) register(request sessionCallbackRegistration) (sessionCallbackRegistration, bool, error) {
	now := time.Now().UTC()
	request.SourceSessionID = strings.TrimSpace(request.SourceSessionID)
	request.TargetSessionID = strings.TrimSpace(request.TargetSessionID)
	request.MissionID = strings.TrimSpace(request.MissionID)
	request.TaskID = strings.TrimSpace(request.TaskID)
	request.RegisteredAt = now
	request.UpdatedAt = now
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
		if current.TargetSessionID != request.TargetSessionID || current.MissionID != request.MissionID || current.TaskID != request.TaskID {
			return sessionCallbackRegistration{}, false, &sessionCallbackError{code: "CALLBACK_OWNER_CONFLICT", message: "source session already has a different callback owner"}
		}
		if request.Generation < current.Generation {
			return sessionCallbackRegistration{}, false, &sessionCallbackError{code: "CALLBACK_GENERATION_STALE", message: "callback generation is older than the registered owner"}
		}
		if request.Generation == current.Generation {
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

func (s *sessionCallbackStore) unregister(sourceSessionID string, generation int64) (bool, error) {
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
	if event.Sequence <= registration.LastEventSequence {
		return false, nil
	}
	previousRegistration := registration
	previousPending, hadPending := s.pending[event.ConversationID]
	eventKey := event.EventKey
	if eventKey == "" {
		eventKey = chatgptRealtimeEventKey(event.ConversationID, event.EventType, nil)
	}
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
		SourceSessionID: registration.SourceSessionID,
		TargetSessionID: registration.TargetSessionID,
		MissionID:       registration.MissionID,
		TaskID:          registration.TaskID,
		Generation:      registration.Generation,
		EventSequence:   event.Sequence,
		EventKey:        eventKey,
		EventType:       event.Type,
		OccurredAt:      event.Timestamp.UTC(),
	}
	if err := validateSessionCallbackEvent(pending); err != nil {
		return false, err
	}
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
