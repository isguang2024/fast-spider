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
	sessionCallbackStoreSchemaVersion = 4
	maxSessionCallbacks               = 64
	maxRecentCallbackEventKeys        = 256
	maxRetiredCallbackClaims          = 256
	maxSessionCallbackClaimBatch      = 64
	sessionCallbackClaimLease         = 5 * time.Minute
)

const (
	callbackClaimTransportHub   = "hub"
	callbackClaimTransportLocal = "local"
)

func normalizeCallbackClaimTransport(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return callbackClaimTransportHub, nil
	}
	if value != callbackClaimTransportHub && value != callbackClaimTransportLocal {
		return "", fmt.Errorf("callbackClaimTransport must be hub or local")
	}
	return value, nil
}

func callbackTransportForRegistration(registration sessionCallbackRegistration) string {
	transport, err := normalizeCallbackClaimTransport(registration.CallbackClaimTransport)
	if err != nil {
		return callbackClaimTransportHub
	}
	return transport
}

func callbackTransportForEvent(event sessionCallbackEvent) string {
	transport, err := normalizeCallbackClaimTransport(event.CallbackClaimTransport)
	if err != nil {
		return callbackClaimTransportHub
	}
	return transport
}

func callbackCompletionSourceIsFormal(source string) bool {
	return source == "submission" || source == "local-submission"
}

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
	NativeRunner           bool   `json:"nativeRunner,omitempty"`
	SourceSessionID        string `json:"sourceSessionId"`
	TargetSessionID        string `json:"targetSessionId"`
	MissionID              string `json:"missionId"`
	TaskID                 string `json:"taskId"`
	Generation             int64  `json:"generation"`
	CallbackType           string `json:"callbackType"`
	CallbackClaimTransport string `json:"callbackClaimTransport,omitempty"`

	DeliverablePath        string    `json:"deliverablePath,omitempty"`
	BaselineIdentity       string    `json:"baselineIdentity,omitempty"`
	ImmediateWake          bool      `json:"immediateWake,omitempty"`
	Armed                  bool      `json:"armed"`
	ArmedAt                time.Time `json:"armedAt,omitempty"`
	LastEventSequence      int64     `json:"lastEventSequence,omitempty"`
	LastEventKey           string    `json:"lastEventKey,omitempty"`
	RecentEventKeys        []string  `json:"recentEventKeys,omitempty"`
	LastFallbackEventKey   string    `json:"lastFallbackEventKey,omitempty"`
	LastFallbackEventAt    time.Time `json:"lastFallbackEventAt,omitempty"`
	LastDeliveredAt        time.Time `json:"lastDeliveredAt,omitempty"`
	LastDeliveredEnvelope  string    `json:"lastDeliveredEnvelope,omitempty"`
	LastNudgeAt            time.Time `json:"lastNudgeAt,omitempty"`
	LastNudgeEnvelope      string    `json:"lastNudgeEnvelope,omitempty"`
	LastNudgeEventKey      string    `json:"lastNudgeEventKey,omitempty"`
	LastNudgeExecutionMode string    `json:"lastNudgeExecutionMode,omitempty"`
	LastNudgeOwner         string    `json:"lastNudgeOwner,omitempty"`
	LastNudgeTurnID        string    `json:"lastNudgeTurnId,omitempty"`
	NudgeFailureEnvelope   string    `json:"nudgeFailureEnvelope,omitempty"`
	NudgeFailureCount      int       `json:"nudgeFailureCount,omitempty"`
	NudgeRetryAt           time.Time `json:"nudgeRetryAt,omitempty"`
	NudgeErrorClass        string    `json:"nudgeErrorClass,omitempty"`
	LastResultID           string    `json:"lastResultId,omitempty"`
	LastResultStatus       string    `json:"lastResultStatus,omitempty"`
	LastResultBytes        int64     `json:"lastResultBytes,omitempty"`
	LastResultSHA256       string    `json:"lastResultSHA256,omitempty"`
	LastResultPageCount    int       `json:"lastResultPageCount,omitempty"`
	// CompletionAckedAt is retained only for loading schema-3 files written by
	// older Nodes. Current formal ACKs retire the active registration entirely.
	CompletionAckedAt time.Time `json:"completionAckedAt,omitempty"`
	RegisteredAt      time.Time `json:"registeredAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type sessionCallbackEvent struct {
	NativeRunner           bool      `json:"nativeRunner,omitempty"`
	SourceSessionID        string    `json:"sourceSessionId"`
	TargetSessionID        string    `json:"targetSessionId"`
	MissionID              string    `json:"missionId"`
	TaskID                 string    `json:"taskId"`
	Generation             int64     `json:"generation"`
	EventSequence          int64     `json:"eventSequence"`
	EventKey               string    `json:"eventKey"`
	EventType              string    `json:"eventType"`
	CompletionSource       string    `json:"completionSource,omitempty"`
	OccurredAt             time.Time `json:"occurredAt"`
	CallbackType           string    `json:"callbackType"`
	CallbackClaimTransport string    `json:"callbackClaimTransport,omitempty"`

	ResultText        string `json:"resultText,omitempty"`
	CallbackOutcome   string `json:"callbackOutcome"`
	CallbackErrorCode string `json:"callbackErrorCode,omitempty"`

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
	RetiredClaims map[string]time.Time          `json:"retiredClaims,omitempty"`
}

type sessionCallbackStore struct {
	mu                       sync.Mutex
	path                     string
	registrations            map[string]sessionCallbackRegistration
	pending                  map[string]sessionCallbackEvent
	retiredClaims            map[string]time.Time
	loadErr                  error
	confirming               map[string]int64
	beforeCommitSaveOverride func() error
	syncParentOverride       func(string) error
}

// currentProviderRegistration is a short critical section used on both sides
// of potentially blocking realtime subscription setup. Network/socket waits
// must never hold the callback store mutex: unregister/ACK remain locally
// available while the provider is slow or disconnected.
func (s *sessionCallbackStore) currentProviderRegistration(sourceSessionID string, generation int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return false, callbackStoreUnavailableError()
	}
	registration, exists := s.registrations[strings.TrimSpace(sourceSessionID)]
	return exists && registration.Generation == generation && callbackRegistrationProviderActive(registration), nil
}

// withCurrentRegistration remains for short, non-blocking state fences and
// compatibility with local lifecycle tests. Provider/socket work must use the
// two-phase currentProviderRegistration check instead.
func (s *sessionCallbackStore) withCurrentRegistration(sourceSessionID string, generation int64, fn func() error) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return false, callbackStoreUnavailableError()
	}
	registration, exists := s.registrations[strings.TrimSpace(sourceSessionID)]
	if !exists || registration.Generation != generation || !callbackRegistrationProviderActive(registration) {
		return false, nil
	}
	return true, fn()
}

func newSessionCallbackStore(dataDir string) *sessionCallbackStore {
	store := &sessionCallbackStore{
		path:          filepath.Join(dataDir, "agent", "session-callbacks.json"),
		registrations: map[string]sessionCallbackRegistration{},
		pending:       map[string]sessionCallbackEvent{},
		retiredClaims: map[string]time.Time{},
		confirming:    map[string]int64{},
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
	if err := json.Unmarshal(raw, &index); err != nil || (index.SchemaVersion != 1 && index.SchemaVersion != 2 && index.SchemaVersion != 3 && index.SchemaVersion != sessionCallbackStoreSchemaVersion) || len(index.Registrations) > maxSessionCallbacks || len(index.Pending) > maxSessionCallbacks || len(index.RetiredClaims) > maxRetiredCallbackClaims {
		return fmt.Errorf("invalid session callback index")
	}
	pendingSources := make(map[string]struct{}, len(index.Pending))
	for _, event := range index.Pending {
		pendingSources[event.SourceSessionID] = struct{}{}
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
		if registration.CallbackClaimTransport == "" {
			registration.CallbackClaimTransport = callbackClaimTransportHub
		}
		if err := validateSessionCallbackRegistration(registration); err != nil {
			return fmt.Errorf("invalid session callback index: %w", err)
		}
		// Schema-3 records written before CompletionAckedAt used the formal
		// submitted envelope and delivery timestamp as their durable completion
		// ACK marker. Restore only that exact evidence; ordinary nudge envelopes
		// and recovery claims must never finalize a route during load.
		if registration.CompletionAckedAt.IsZero() && !registration.LastDeliveredAt.IsZero() && registration.LastDeliveredEnvelope == callbackFormalCompletionKey(registration) {
			registration.CompletionAckedAt = registration.LastDeliveredAt.UTC()
		}
		// Schema-3 used to retain formally acknowledged generations forever. They
		// are inactive history owned by the Hub, not active Node routes. Retire them
		// during load so a restart cannot resurrect them or consume the 64-route
		// active capacity. A contradictory pending item fails closed below.
		if !registration.CompletionAckedAt.IsZero() {
			if _, pending := pendingSources[registration.SourceSessionID]; !pending {
				continue
			}
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
		if event.CallbackClaimTransport == "" {
			if registration, exists := s.registrations[event.SourceSessionID]; exists {
				event.CallbackClaimTransport = callbackTransportForRegistration(registration)
			}
		}
		if err := validateSessionCallbackEvent(event); err != nil {
			return fmt.Errorf("invalid session callback index: %w", err)
		}
		registration, exists := s.registrations[event.SourceSessionID]
		if !exists || registration.TargetSessionID != event.TargetSessionID || registration.MissionID != event.MissionID || registration.TaskID != event.TaskID || registration.Generation != event.Generation || registration.CallbackType != event.CallbackType || callbackTransportForRegistration(registration) != callbackTransportForEvent(event) || registration.DeliverablePath != event.DeliverablePath || registration.ImmediateWake != event.ImmediateWake {
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
	for key, acknowledgedAt := range index.RetiredClaims {
		if key == "" || len(key) > 512 || strings.ContainsAny(key, "\r\n") || acknowledgedAt.IsZero() {
			return fmt.Errorf("invalid session callback index: invalid retired claim")
		}
		s.retiredClaims[key] = acknowledgedAt.UTC()
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
	if _, err := normalizeCallbackClaimTransport(registration.CallbackClaimTransport); err != nil {
		return err
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
	if err := validateCallbackSafeToken(registration.LastNudgeExecutionMode, "callback nudge execution mode"); err != nil {
		return err
	}
	if err := validateCallbackSafeToken(registration.LastNudgeOwner, "callback nudge owner"); err != nil {
		return err
	}
	if err := validateCallbackSafeToken(registration.LastNudgeTurnID, "callback nudge turn ID"); err != nil {
		return err
	}
	if registration.NudgeFailureCount < 0 || registration.NudgeFailureCount > 32 {
		return fmt.Errorf("invalid callback retry count")
	}
	if err := validateCallbackSafeToken(registration.NudgeFailureEnvelope, "callback failure envelope"); err != nil {
		return err
	}
	if registration.NudgeFailureCount > 0 && (registration.NudgeFailureEnvelope == "" || registration.NudgeRetryAt.IsZero()) {
		return fmt.Errorf("callback failure requires an envelope and retry deadline")
	}
	if registration.NudgeErrorClass != "" && !validErrorClass(ErrorClass(registration.NudgeErrorClass)) {
		return fmt.Errorf("invalid callback error class")
	}
	if err := validateCallbackResultMetadata(callbackResultMetadata{registration.LastResultID, registration.LastResultStatus, registration.LastResultBytes, registration.LastResultSHA256, registration.LastResultPageCount}); err != nil {
		return err
	}
	return nil
}

func validateSessionCallbackEvent(event sessionCallbackEvent) error {
	registration := sessionCallbackRegistration{
		SourceSessionID:        event.SourceSessionID,
		TargetSessionID:        event.TargetSessionID,
		MissionID:              event.MissionID,
		TaskID:                 event.TaskID,
		Generation:             event.Generation,
		CallbackType:           event.CallbackType,
		CallbackClaimTransport: event.CallbackClaimTransport,
		DeliverablePath:        event.DeliverablePath,
		RegisteredAt:           time.Unix(1, 0),
		UpdatedAt:              time.Unix(1, 0),
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

func validateCallbackSafeToken(value, label string) error {
	if value == "" {
		return nil
	}
	if len(value) > 128 || strings.ContainsAny(value, "\x00\r\n\t ") {
		return fmt.Errorf("invalid %s", label)
	}
	return nil
}

func isPersistentCallbackEventKey(key string) bool {
	return strings.HasPrefix(key, "provider_evt_") || strings.HasPrefix(key, "completion_") || strings.HasPrefix(key, "submitted_") || strings.HasPrefix(key, "recovery_")
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

// callbackRegistrationProviderActive is the single route predicate used by
// watcher creation and provider recovery. CompletionAckedAt can only appear on
// a legacy record while it is being validated or compacted during load.
func callbackRegistrationProviderActive(registration sessionCallbackRegistration) bool {
	return registration.Armed && registration.CompletionAckedAt.IsZero()
}

func callbackFormalCompletionKey(registration sessionCallbackRegistration) string {
	return "submitted_" + strings.TrimPrefix(sessionCallbackCompletionEventKey(registration), "completion_")
}

func callbackRegistrationHasProviderObservationLocked(s *sessionCallbackStore, registration sessionCallbackRegistration) bool {
	if pending, ok := s.pending[registration.SourceSessionID]; ok && pending.Generation == registration.Generation {
		return true
	}
	formalKey := callbackFormalCompletionKey(registration)
	if registration.LastEventKey == formalKey || registration.LastDeliveredEnvelope == formalKey {
		return true
	}
	for _, key := range registration.RecentEventKeys {
		if key == formalKey || strings.HasPrefix(key, "recovery_") {
			return true
		}
	}
	return false
}

// providerRecoveryAllowed reports whether a status read may be issued for the
// current generation. A Hub-pushed formal submission is already authoritative;
// reading the provider after it only adds duplicate traffic and can race the
// local completion acknowledgement.
func (s *sessionCallbackStore) providerRecoveryAllowed(sourceSessionID string, generation int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return false, callbackStoreUnavailableError()
	}
	registration, ok := s.registrations[strings.TrimSpace(sourceSessionID)]
	if !ok || registration.Generation != generation || !callbackRegistrationProviderActive(registration) {
		return false, nil
	}
	return !callbackRegistrationHasProviderObservationLocked(s, registration), nil
}

// beginProviderConfirmation deduplicates realtime confirmation reads for a
// route generation while allowing a later realtime event after the prior
// bounded confirmation has settled.
func (s *sessionCallbackStore) beginProviderConfirmation(sourceSessionID string, generation int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return false, callbackStoreUnavailableError()
	}
	registration, ok := s.registrations[strings.TrimSpace(sourceSessionID)]
	if !ok || registration.Generation != generation || !callbackRegistrationProviderActive(registration) || callbackRegistrationHasProviderObservationLocked(s, registration) {
		return false, nil
	}
	if s.confirming == nil {
		s.confirming = map[string]int64{}
	}
	if activeGeneration, exists := s.confirming[registration.SourceSessionID]; exists && activeGeneration == generation {
		return false, nil
	}
	s.confirming[registration.SourceSessionID] = generation
	return true, nil
}

func (s *sessionCallbackStore) endProviderConfirmation(sourceSessionID string, generation int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.confirming != nil && s.confirming[strings.TrimSpace(sourceSessionID)] == generation {
		delete(s.confirming, strings.TrimSpace(sourceSessionID))
	}
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
	transport, transportErr := normalizeCallbackClaimTransport(request.CallbackClaimTransport)
	if transportErr != nil {
		return sessionCallbackRegistration{}, false, &sessionCallbackError{code: "INVALID_REQUEST", message: transportErr.Error()}
	}
	request.CallbackClaimTransport = transport
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
		if current.TargetSessionID != request.TargetSessionID || current.MissionID != request.MissionID || current.TaskID != request.TaskID || current.CallbackType != request.CallbackType || callbackTransportForRegistration(current) != request.CallbackClaimTransport || current.ImmediateWake != request.ImmediateWake || !callbackDeliverablePathEqual(current.DeliverablePath, request.DeliverablePath) {
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
	var reclaimed *sessionCallbackRegistration
	if !exists && len(s.registrations) >= maxSessionCallbacks {
		// Acknowledged generations no longer watch the provider and cannot have a
		// pending Node event. Reclaim the oldest one only under capacity pressure;
		// the Hub remains the durable task/result authority and a later CHAT reuse
		// can register a fresh generation. Active and recovery-only routes are never
		// displaced by a new mission.
		var candidate sessionCallbackRegistration
		for source, registration := range s.registrations {
			if registration.CompletionAckedAt.IsZero() {
				continue
			}
			if _, pending := s.pending[source]; pending {
				continue
			}
			if candidate.SourceSessionID == "" || registration.CompletionAckedAt.Before(candidate.CompletionAckedAt) ||
				registration.CompletionAckedAt.Equal(candidate.CompletionAckedAt) && registration.SourceSessionID < candidate.SourceSessionID {
				candidate = registration
			}
		}
		if candidate.SourceSessionID == "" {
			return sessionCallbackRegistration{}, false, &sessionCallbackError{code: "RESOURCE_LIMIT", message: "session callback registry is full"}
		}
		reclaimed = &candidate
		delete(s.registrations, candidate.SourceSessionID)
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
			if reclaimed != nil {
				s.registrations[reclaimed.SourceSessionID] = *reclaimed
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
	if !current.CompletionAckedAt.IsZero() {
		return sessionCallbackRegistration{}, false, &sessionCallbackError{code: "CALLBACK_ROUTE_FINALIZED", message: "callback route generation was already finalized"}
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
	}
	if event.Sequence <= registration.LastEventSequence {
		return false, nil
	}
	previousRegistration := registration
	previousPending, hadPending := s.pending[event.ConversationID]
	eventKey := sessionCallbackCompletionEventKey(registration)
	source := "recovery"
	if event.EventType == "hub-completion-notify" {
		source = "submission"
		eventKey = "submitted_" + strings.TrimPrefix(eventKey, "completion_")
	} else if callbackTransportForRegistration(registration) == callbackClaimTransportLocal {
		// A provider completion observed by the co-located Node is the local
		// execution result. It is formal for the local transport even when the
		// realtime hint was confirmed through the bounded recovery reader.
		source = "local-submission"
		eventKey = "submitted_" + strings.TrimPrefix(eventKey, "completion_")
	} else {
		eventKey = "recovery_" + strings.TrimPrefix(eventKey, "completion_")
	}
	// A formal result supersedes an observation, including a claimed legacy
	// observation. The old claim must not acknowledge the replacement.
	if hadPending && callbackCompletionSourceIsFormal(previousPending.CompletionSource) {
		if source == "recovery" {
			return false, nil
		}
		if previousPending.CallbackOutcome != event.CallbackOutcome || previousPending.ResultText != event.ResultText {
			return false, &sessionCallbackError{code: "TASK_RESULT_CONFLICT", message: "a different submitted callback already exists"}
		}
	}
	if source == "recovery" {
		formalKey := "submitted_" + strings.TrimPrefix(sessionCallbackCompletionEventKey(registration), "completion_")
		for _, key := range registration.RecentEventKeys {
			if key == formalKey {
				return false, nil
			}
		}
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
		SourceSessionID:        registration.SourceSessionID,
		TargetSessionID:        registration.TargetSessionID,
		MissionID:              registration.MissionID,
		TaskID:                 registration.TaskID,
		Generation:             registration.Generation,
		EventSequence:          event.Sequence,
		EventKey:               eventKey,
		EventType:              event.Type,
		CompletionSource:       source,
		NativeRunner:           registration.NativeRunner,
		OccurredAt:             event.Timestamp.UTC(),
		CallbackType:           event.CallbackType,
		CallbackClaimTransport: callbackTransportForRegistration(registration),
		ResultText:             event.ResultText,
		CallbackOutcome:        event.CallbackOutcome,
		CallbackErrorCode:      event.CallbackErrorCode,
		ResultID:               event.ResultID,
		ResultStatus:           event.ResultStatus,
		ResultBytes:            event.ResultBytes,
		ResultSHA256:           event.ResultSHA256,
		ResultPageCount:        event.ResultPageCount,
		DeliverablePath:        event.DeliverablePath,
		DeliverableStatus:      event.DeliverableStatus,
		ImmediateWake:          registration.ImmediateWake,
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
	return s.pendingByTargetMode(false)
}

func (s *sessionCallbackStore) pendingForNudge() (map[string][]sessionCallbackEvent, error) {
	return s.pendingByTargetMode(true)
}

type sessionCallbackNudgeGroup struct {
	TargetSessionID string
	Transport       string
}

func (s *sessionCallbackStore) pendingForNudgeByTransport() (map[sessionCallbackNudgeGroup][]sessionCallbackEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, callbackStoreUnavailableError()
	}
	grouped := map[sessionCallbackNudgeGroup][]sessionCallbackEvent{}
	for _, event := range s.pending {

		if event.CompletionSource == "recovery" && s.registrations[event.SourceSessionID].LastNudgeEventKey == event.EventKey {
			continue
		}
		key := sessionCallbackNudgeGroup{TargetSessionID: event.TargetSessionID, Transport: callbackTransportForEvent(event)}
		grouped[key] = append(grouped[key], event)
	}
	for key := range grouped {
		sortSessionCallbackEvents(grouped[key])
	}
	return grouped, nil
}

func (s *sessionCallbackStore) pendingByTargetMode(forNudge bool) (map[string][]sessionCallbackEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, callbackStoreUnavailableError()
	}
	grouped := map[string][]sessionCallbackEvent{}
	for _, event := range s.pending {
		if forNudge && event.CompletionSource == "recovery" && s.registrations[event.SourceSessionID].LastNudgeEventKey == event.EventKey {
			continue
		}
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
func (s *sessionCallbackStore) claim(targetSessionID, requestedClaimID string, limit int, now time.Time, transportArg ...string) (string, []sessionCallbackEvent, error) {
	return s.claimWithFilter(targetSessionID, requestedClaimID, limit, now, func(event sessionCallbackEvent) bool { return !event.NativeRunner }, transportArg...)
}

// claimExact reserves only the callback event bound to the supplied immutable
// source/task/generation identity. Native runner tasks share one controller
// queue, so a generic target claim could otherwise consume a sibling block's
// result before the runner has a chance to inspect it.
func (s *sessionCallbackStore) claimExact(targetSessionID, sourceSessionID, missionID, taskID string, generation int64, requestedClaimID string, limit int, now time.Time, transportArg ...string) (string, []sessionCallbackEvent, error) {
	return s.claimWithFilter(targetSessionID, requestedClaimID, limit, now, func(event sessionCallbackEvent) bool {
		return event.SourceSessionID == sourceSessionID && event.MissionID == missionID && event.TaskID == taskID && event.Generation == generation
	}, transportArg...)
}

func (s *sessionCallbackStore) claimWithFilter(targetSessionID, requestedClaimID string, limit int, now time.Time, filter func(sessionCallbackEvent) bool, transportArg ...string) (string, []sessionCallbackEvent, error) {
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
	transport := callbackClaimTransportHub
	if len(transportArg) > 0 {
		var transportErr error
		transport, transportErr = normalizeCallbackClaimTransport(transportArg[0])
		if transportErr != nil {
			return "", nil, &sessionCallbackError{code: "INVALID_REQUEST", message: transportErr.Error()}
		}
	}
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
		if event.ClaimID != claimID || callbackTransportForEvent(event) != transport {
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
		for i := range existing {
			event := s.pending[existing[i].SourceSessionID]
			registration := s.registrations[existing[i].SourceSessionID]
			if refreshLocalFileCallbackMetadata(&event, &registration) {
				released = true // also persists refreshed deliverable metadata
				registration.UpdatedAt = now
				s.registrations[existing[i].SourceSessionID] = registration
				s.pending[existing[i].SourceSessionID] = event
				existing[i] = event
			}
		}
		if err := persistReleased(); err != nil {
			return "", nil, err
		}
		sortSessionCallbackEvents(existing)
		return claimID, existing, nil
	}
	if requestedClaimID != "" {
		for _, registration := range s.registrations {
			if registration.TargetSessionID == targetSessionID && callbackTransportForRegistration(registration) == transport && registration.LastDeliveredEnvelope == claimID {
				if err := persistReleased(); err != nil {
					return "", nil, err
				}
				return claimID, nil, nil
			}
		}
	}

	available := make([]sessionCallbackEvent, 0, len(s.pending))
	for _, event := range s.pending {
		if event.TargetSessionID != targetSessionID || callbackTransportForEvent(event) != transport || event.ClaimID != "" {
			continue
		}
		if filter != nil && !filter(event) {
			continue
		}
		// A managed local event already owns a durable collaboration inbox
		// item. A fresh generic claim belongs only to the legacy callback
		// transport; an active explicit claim was handled above and remains
		// replayable as-is.

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
	for i, selected := range available {
		event := s.pending[selected.SourceSessionID]
		registration := s.registrations[selected.SourceSessionID]
		refreshLocalFileCallbackMetadata(&event, &registration)
		event.ClaimID = claimID
		event.ClaimedAt = now
		s.pending[selected.SourceSessionID] = event
		registration.UpdatedAt = now
		s.registrations[selected.SourceSessionID] = registration
		available[i] = event
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

func (s *sessionCallbackStore) pendingClaimSnapshot(targetSessionID, claimID, transport string) ([]sessionCallbackEvent, error) {
	targetSessionID = strings.TrimSpace(targetSessionID)
	claimID = strings.TrimSpace(claimID)
	transport, _ = normalizeCallbackClaimTransport(transport)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, callbackStoreUnavailableError()
	}
	events := make([]sessionCallbackEvent, 0)
	for _, event := range s.pending {
		if event.TargetSessionID == targetSessionID && event.ClaimID == claimID && callbackTransportForEvent(event) == transport {
			events = append(events, event)
		}
	}
	sortSessionCallbackEvents(events)
	return events, nil
}

func (s *sessionCallbackStore) releaseClaim(targetSessionID, claimID, transport string) error {
	targetSessionID = strings.TrimSpace(targetSessionID)
	claimID = strings.TrimSpace(claimID)
	transport, _ = normalizeCallbackClaimTransport(transport)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return callbackStoreUnavailableError()
	}
	previous := cloneSessionCallbackEvents(s.pending)
	released := false
	for source, event := range s.pending {
		if event.TargetSessionID != targetSessionID || event.ClaimID != claimID || callbackTransportForEvent(event) != transport {
			continue
		}
		event.ClaimID = ""
		event.ClaimedAt = time.Time{}
		s.pending[source] = event
		released = true
	}
	if !released {
		return nil
	}
	if _, err := s.saveLocked(); err != nil {
		s.pending = previous
		return err
	}
	return nil
}

// acknowledgeCompletion is called by Hub after accepting a formal result. It
// retires only the exact bound generation, regardless of a previous Node claim
// lease. Hub collaboration events retain the result/audit history; the Node
// registry contains only active or still-unacknowledged routes.
func (s *sessionCallbackStore) acknowledgeCompletion(expected sessionCallbackRegistration, _ time.Time) (int, error) {
	if expected.SourceSessionID == "" || expected.TargetSessionID == "" || expected.MissionID == "" || expected.TaskID == "" || expected.Generation < 1 {
		return 0, &sessionCallbackError{code: "INVALID_REQUEST", message: "completion acknowledgement requires source, target, mission, task and generation"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return 0, callbackStoreUnavailableError()
	}
	registration, exists := s.registrations[expected.SourceSessionID]
	if !exists || registration.Generation > expected.Generation {
		return 0, nil
	}
	if registration.TargetSessionID != expected.TargetSessionID || registration.MissionID != expected.MissionID || registration.TaskID != expected.TaskID || registration.Generation != expected.Generation {
		return 0, &sessionCallbackError{code: "CALLBACK_OWNER_CONFLICT", message: "completion acknowledgement does not match the registered task"}
	}
	event, pending := s.pending[expected.SourceSessionID]
	delete(s.registrations, expected.SourceSessionID)
	delete(s.pending, expected.SourceSessionID)
	if committed, err := s.saveLocked(); err != nil {
		if !committed {
			s.registrations[expected.SourceSessionID] = registration
			if pending {
				s.pending[expected.SourceSessionID] = event
			}
		}
		return 0, err
	}
	if pending {
		return 1, nil
	}
	return 0, nil
}

func (s *sessionCallbackStore) acknowledgeClaim(targetSessionID, claimID string, now time.Time, transportArg ...string) (int, error) {
	targetSessionID = strings.TrimSpace(targetSessionID)
	claimID = strings.TrimSpace(claimID)
	if err := validateCallbackOpaqueID(targetSessionID, "callback target session ID", 256); err != nil {
		return 0, &sessionCallbackError{code: "INVALID_REQUEST", message: err.Error()}
	}
	if err := validateSessionCallbackClaimID(claimID); err != nil {
		return 0, &sessionCallbackError{code: "INVALID_REQUEST", message: err.Error()}
	}
	now = now.UTC()
	transport := callbackClaimTransportHub
	if len(transportArg) > 0 {
		var transportErr error
		transport, transportErr = normalizeCallbackClaimTransport(transportArg[0])
		if transportErr != nil {
			return 0, &sessionCallbackError{code: "INVALID_REQUEST", message: transportErr.Error()}
		}
	}
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
		if event.TargetSessionID != targetSessionID || callbackTransportForEvent(event) != transport || event.ClaimID != claimID {
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
			if registration.TargetSessionID == targetSessionID && callbackTransportForRegistration(registration) == transport && registration.LastDeliveredEnvelope == claimID {
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

// acknowledgeClaimAndRetire atomically consumes a local callback claim and
// retires each exact source registration. Local callbacks have no Hub
// completion.ack phase, so retaining the route would leave a stale realtime
// watcher and make a completed task appear active after restart.
func (s *sessionCallbackStore) acknowledgeClaimAndRetire(targetSessionID, claimID string, now time.Time, transportArg ...string) (int, []sessionCallbackRegistration, error) {
	return s.acknowledgeClaimAndRetireWithEvidence(targetSessionID, claimID, now, "", transportArg...)
}

// acknowledgeClaimAndRetireWithEvidence permits a native runner to retire a
// local-file callback from a durable frozen snapshot when the original worker
// path disappeared or changed after the result was observed. The caller must
// validate the snapshot binding and digest; this store only verifies that the
// snapshot is readable and that the route is a native local callback.
func (s *sessionCallbackStore) acknowledgeClaimAndRetireWithEvidence(targetSessionID, claimID string, now time.Time, evidencePath string, transportArg ...string) (int, []sessionCallbackRegistration, error) {
	targetSessionID = strings.TrimSpace(targetSessionID)
	claimID = strings.TrimSpace(claimID)
	if err := validateCallbackOpaqueID(targetSessionID, "callback target session ID", 256); err != nil {
		return 0, nil, &sessionCallbackError{code: "INVALID_REQUEST", message: err.Error()}
	}
	if err := validateSessionCallbackClaimID(claimID); err != nil {
		return 0, nil, &sessionCallbackError{code: "INVALID_REQUEST", message: err.Error()}
	}
	transport := callbackClaimTransportLocal
	if len(transportArg) > 0 {
		var transportErr error
		transport, transportErr = normalizeCallbackClaimTransport(transportArg[0])
		if transportErr != nil {
			return 0, nil, &sessionCallbackError{code: "INVALID_REQUEST", message: transportErr.Error()}
		}
	}
	evidencePath = strings.TrimSpace(evidencePath)
	evidenceReady := false
	if evidencePath != "" {
		status, _, _ := inspectCallbackDeliverable(evidencePath)
		evidenceReady = status == "ready"
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return 0, nil, callbackStoreUnavailableError()
	}
	previousRegistrations := cloneSessionCallbackRegistrations(s.registrations)
	previousPending := cloneSessionCallbackEvents(s.pending)
	previousRetiredClaims := cloneRetiredCallbackClaims(s.retiredClaims)
	type localClaimedEvent struct {
		source       string
		event        sessionCallbackEvent
		registration sessionCallbackRegistration
	}
	var claimed []localClaimedEvent
	for source, event := range s.pending {
		if event.TargetSessionID != targetSessionID || callbackTransportForEvent(event) != transport || event.ClaimID != claimID {
			continue
		}
		if !callbackClaimActive(event, now) {
			return 0, nil, &sessionCallbackError{code: "CALLBACK_CLAIM_EXPIRED", message: "callback claim lease expired; claim the queue again before acknowledging"}
		}
		registration, exists := s.registrations[source]
		if !exists || registration.TargetSessionID != targetSessionID || callbackTransportForRegistration(registration) != transport || registration.Generation != event.Generation {
			return 0, nil, &sessionCallbackError{code: "CALLBACK_OWNER_CONFLICT", message: "callback claim does not match the registered local route"}
		}
		claimed = append(claimed, localClaimedEvent{source: source, event: event, registration: registration})
	}
	if len(claimed) == 0 {
		if _, acknowledged := s.retiredClaims[retiredCallbackClaimKey(targetSessionID, transport, claimID)]; acknowledged {
			return 0, nil, nil
		}
		for source, registration := range s.registrations {
			if registration.TargetSessionID == targetSessionID && callbackTransportForRegistration(registration) == transport && registration.LastDeliveredEnvelope == claimID {
				_ = source
				return 0, nil, nil
			}
		}
		return 0, nil, &sessionCallbackError{code: "CALLBACK_CLAIM_NOT_FOUND", message: "callback claim is not active for the target session"}
	}
	// All entries are validated before mutating either map. This keeps a
	// multi-event local ACK atomic when one event is stale or malformed.
	retired := make([]sessionCallbackRegistration, 0, len(claimed))
	metadataChanged := false
	for i := range claimed {
		item := &claimed[i]
		if refreshLocalFileCallbackMetadata(&item.event, &item.registration) {
			metadataChanged = true
		}
		s.pending[item.source] = item.event
		s.registrations[item.source] = item.registration
		if item.event.CallbackType == protocolv1.CloudCallbackTypeLocalFile && item.event.DeliverableStatus != "ready" {
			if evidenceReady && item.registration.NativeRunner {
				continue
			}
			if metadataChanged {
				if _, err := s.saveLocked(); err != nil {
					s.registrations = previousRegistrations
					s.pending = previousPending
					return 0, nil, err
				}
			}
			return 0, nil, &sessionCallbackError{code: "TASK_RESULT_FILE_INVALID", message: "the assigned local result must be a readable regular file no larger than 256 MiB"}
		}
	}
	for _, item := range claimed {
		delete(s.pending, item.source)
		delete(s.registrations, item.source)
		retired = append(retired, item.registration)
	}
	if s.retiredClaims == nil {
		s.retiredClaims = map[string]time.Time{}
	}
	s.retiredClaims[retiredCallbackClaimKey(targetSessionID, transport, claimID)] = now
	if len(s.retiredClaims) > maxRetiredCallbackClaims {
		oldestKey := ""
		var oldest time.Time
		for key, acknowledgedAt := range s.retiredClaims {
			if key == retiredCallbackClaimKey(targetSessionID, transport, claimID) {
				continue
			}
			if oldestKey == "" || acknowledgedAt.Before(oldest) {
				oldestKey, oldest = key, acknowledgedAt
			}
		}
		if oldestKey != "" {
			delete(s.retiredClaims, oldestKey)
		}
	}
	if _, err := s.saveLocked(); err != nil {
		s.registrations = previousRegistrations
		s.pending = previousPending
		s.retiredClaims = previousRetiredClaims
		return 0, nil, err
	}
	return len(retired), retired, nil
}

// refreshLocalFileCallbackMetadata revalidates a local_file deliverable at the
// point where it is exposed or retired. enqueue intentionally clears result
// metadata for a new event, and a file can also disappear after claim, so the
// durable queue must never report or consume stale file state.
func refreshLocalFileCallbackMetadata(event *sessionCallbackEvent, registration *sessionCallbackRegistration) bool {
	if event == nil || registration == nil || registration.CallbackType != protocolv1.CloudCallbackTypeLocalFile {
		return false
	}
	oldCallbackType := event.CallbackType
	oldDeliverablePath := event.DeliverablePath
	oldDeliverableStatus := event.DeliverableStatus
	oldResultStatus := event.ResultStatus
	oldResultBytes := event.ResultBytes
	oldResultSHA256 := event.ResultSHA256
	oldLastResultID := registration.LastResultID
	oldLastResultStatus := registration.LastResultStatus
	oldLastResultBytes := registration.LastResultBytes
	oldLastResultSHA256 := registration.LastResultSHA256
	oldLastResultPageCount := registration.LastResultPageCount
	path := registration.DeliverablePath
	status, bytes, digest := inspectCallbackDeliverable(path)
	event.CallbackType = registration.CallbackType
	event.DeliverablePath = path
	event.DeliverableStatus = status
	if status == "ready" {
		event.ResultStatus = "ready"
		event.ResultBytes = bytes
		event.ResultSHA256 = digest
	} else {
		event.ResultStatus = "failed"
		event.ResultBytes = 0
		event.ResultSHA256 = ""
	}
	registration.LastResultID = event.ResultID
	registration.LastResultStatus = event.ResultStatus
	registration.LastResultBytes = event.ResultBytes
	registration.LastResultSHA256 = event.ResultSHA256
	registration.LastResultPageCount = event.ResultPageCount
	return oldCallbackType != event.CallbackType ||
		oldDeliverablePath != event.DeliverablePath ||
		oldDeliverableStatus != event.DeliverableStatus ||
		oldResultStatus != event.ResultStatus ||
		oldResultBytes != event.ResultBytes ||
		oldResultSHA256 != event.ResultSHA256 ||
		oldLastResultID != registration.LastResultID ||
		oldLastResultStatus != registration.LastResultStatus ||
		oldLastResultBytes != registration.LastResultBytes ||
		oldLastResultSHA256 != registration.LastResultSHA256 ||
		oldLastResultPageCount != registration.LastResultPageCount
}

func retiredCallbackClaimKey(targetSessionID, transport, claimID string) string {
	return targetSessionID + "\x00" + transport + "\x00" + claimID
}

func cloneRetiredCallbackClaims(input map[string]time.Time) map[string]time.Time {
	output := make(map[string]time.Time, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
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

func (s *sessionCallbackStore) nudgeSchedule(targetSessionID string, now time.Time, interval time.Duration, transportArg ...string) (bool, time.Time, error) {
	targetSessionID = strings.TrimSpace(targetSessionID)
	transport := callbackClaimTransportHub
	if len(transportArg) > 0 {
		transport, _ = normalizeCallbackClaimTransport(transportArg[0])
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return false, time.Time{}, callbackStoreUnavailableError()
	}
	var latest time.Time
	found := false
	for _, registration := range s.registrations {
		if registration.TargetSessionID != targetSessionID || callbackTransportForRegistration(registration) != transport {
			continue
		}
		if event, ok := s.pending[registration.SourceSessionID]; ok && event.EventKey != registration.LastNudgeEventKey {
			return true, now, nil
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

func (s *sessionCallbackStore) nudgeRetryDeadline(targetSessionID, envelopeID string, transportArg ...string) (time.Time, error) {
	transport := callbackClaimTransportHub
	if len(transportArg) > 0 {
		transport, _ = normalizeCallbackClaimTransport(transportArg[0])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return time.Time{}, callbackStoreUnavailableError()
	}
	var next time.Time
	for _, registration := range s.registrations {
		if registration.TargetSessionID == targetSessionID && callbackTransportForRegistration(registration) == transport && registration.NudgeFailureEnvelope == envelopeID && registration.NudgeRetryAt.After(next) {
			next = registration.NudgeRetryAt
		}
	}
	return next, nil
}

func (s *sessionCallbackStore) recordNudgeFailure(targetSessionID, envelopeID string, class ErrorClass, now time.Time, interval time.Duration, transportArg ...string) (time.Time, error) {
	transport := callbackClaimTransportHub
	if len(transportArg) > 0 {
		transport, _ = normalizeCallbackClaimTransport(transportArg[0])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return time.Time{}, callbackStoreUnavailableError()
	}
	// A callback may be acknowledged, replaced or claimed while delivery is in
	// flight. Never attach its failure to a different queue generation.
	var events []sessionCallbackEvent
	for _, event := range s.pending {
		if event.TargetSessionID == targetSessionID && callbackTransportForEvent(event) == transport && !callbackClaimActive(event, now) {
			events = append(events, event)
		}
	}
	sortSessionCallbackEvents(events)
	if len(events) == 0 || sessionCallbackEnvelopeIDForTransport(targetSessionID, transport, events) != envelopeID {
		return time.Time{}, nil
	}
	failures := 0
	for _, registration := range s.registrations {
		if registration.TargetSessionID == targetSessionID && callbackTransportForRegistration(registration) == transport && registration.NudgeFailureEnvelope == envelopeID && registration.NudgeFailureCount > failures {
			failures = registration.NudgeFailureCount
		}
	}
	if failures < 32 {
		failures++
	}
	if interval <= 0 {
		interval = sessionCallbackDeliveryRetryInterval
	}
	for attempt := 1; attempt < failures && interval < sessionCallbackNudgeInterval; attempt++ {
		interval *= 2
	}
	if interval > sessionCallbackNudgeInterval {
		interval = sessionCallbackNudgeInterval
	}
	next := now.UTC().Add(interval)
	previous := cloneSessionCallbackRegistrations(s.registrations)
	for _, event := range events {
		registration := s.registrations[event.SourceSessionID]
		registration.NudgeFailureEnvelope = envelopeID
		registration.NudgeFailureCount = failures
		registration.NudgeRetryAt = next
		registration.NudgeErrorClass = string(class)
		registration.UpdatedAt = now.UTC()
		s.registrations[event.SourceSessionID] = registration
	}
	if committed, err := s.saveLocked(); err != nil {
		if !committed {
			s.registrations = previous
		}
		return time.Time{}, err
	}
	return next, nil
}

func (s *sessionCallbackStore) recordNudge(targetSessionID, envelopeID string, delivery sessionCallbackDeliveryResult, now time.Time, sent ...sessionCallbackEvent) error {
	return s.recordNudgeForTransport(targetSessionID, envelopeID, delivery, now, callbackClaimTransportHub, sent...)
}

func (s *sessionCallbackStore) recordNudgeForTransport(targetSessionID, envelopeID string, delivery sessionCallbackDeliveryResult, now time.Time, transport string, sent ...sessionCallbackEvent) error {
	targetSessionID = strings.TrimSpace(targetSessionID)
	transport, _ = normalizeCallbackClaimTransport(transport)
	if err := validateSessionCallbackClaimID(envelopeID); err != nil {
		return &sessionCallbackError{code: "INVALID_REQUEST", message: err.Error()}
	}
	if err := validateCallbackSafeToken(delivery.ExecutionMode, "callback nudge execution mode"); err != nil {
		return &sessionCallbackError{code: "INVALID_REQUEST", message: err.Error()}
	}
	if err := validateCallbackSafeToken(delivery.Owner, "callback nudge owner"); err != nil {
		return &sessionCallbackError{code: "INVALID_REQUEST", message: err.Error()}
	}
	if err := validateCallbackSafeToken(delivery.TurnID, "callback nudge turn ID"); err != nil {
		return &sessionCallbackError{code: "INVALID_REQUEST", message: err.Error()}
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return callbackStoreUnavailableError()
	}
	previous := cloneSessionCallbackRegistrations(s.registrations)
	var delivered []sessionCallbackEvent
	for _, event := range s.pending {
		if event.TargetSessionID == targetSessionID && callbackTransportForEvent(event) == transport && !callbackClaimActive(event, now) &&
			!(event.CompletionSource == "recovery" && s.registrations[event.SourceSessionID].LastNudgeEventKey == event.EventKey) {
			delivered = append(delivered, event)
		}
	}
	if len(sent) > 0 {
		delivered = append([]sessionCallbackEvent(nil), sent...)
	}
	sortSessionCallbackEvents(delivered)
	if len(delivered) == 0 || sessionCallbackEnvelopeIDForTransport(targetSessionID, transport, delivered) != envelopeID {
		return nil
	}
	deliveredKeys := map[string]string{}
	for _, event := range delivered {
		if current, ok := s.pending[event.SourceSessionID]; ok && current.EventKey == event.EventKey && current.Generation == event.Generation && current.EventSequence == event.EventSequence {
			deliveredKeys[event.SourceSessionID] = event.EventKey
		}
	}
	updated := 0
	for source, registration := range s.registrations {
		if registration.TargetSessionID != targetSessionID || callbackTransportForRegistration(registration) != transport {
			continue
		}
		if _, delivered := deliveredKeys[source]; !delivered {
			continue
		}
		registration.LastNudgeAt = now
		registration.LastNudgeEnvelope = envelopeID
		if key := deliveredKeys[source]; key != "" {
			registration.LastNudgeEventKey = key
		}
		registration.LastNudgeExecutionMode = delivery.ExecutionMode
		registration.LastNudgeOwner = delivery.Owner
		registration.LastNudgeTurnID = delivery.TurnID
		registration.NudgeFailureEnvelope = ""
		registration.NudgeFailureCount = 0
		registration.NudgeRetryAt = time.Time{}
		registration.NudgeErrorClass = ""
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
	return sessionCallbackEnvelopeIDForTransport(targetSessionID, callbackClaimTransportHub, events)
}

func sessionCallbackEnvelopeIDForTransport(targetSessionID, transport string, events []sessionCallbackEvent) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(targetSessionID))
	_, _ = hash.Write([]byte("\x00" + transport))
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
	retiredClaims := make(map[string]time.Time, len(s.retiredClaims))
	for key, acknowledgedAt := range s.retiredClaims {
		retiredClaims[key] = acknowledgedAt
	}
	raw, err := json.Marshal(sessionCallbackIndex{SchemaVersion: sessionCallbackStoreSchemaVersion, Registrations: registrations, Pending: pending, RetiredClaims: retiredClaims})
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
