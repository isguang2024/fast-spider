package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	visibilityContractVersion   = 1
	maxSessionVisibilityRecords = 4096

	sessionVisibilityVisible  = "visible"
	sessionVisibilityInternal = "internal"

	sessionBackendCodexLocal   = "codex_local"
	sessionBackendClaudeLocal  = "claude_local"
	sessionBackendChatGPTCloud = "chatgpt_cloud"

	sessionVisibilityTargetNone = "none"
)

type sessionVisibilitySpec struct {
	Visibility       string
	Backend          string
	VisibilityTarget string
	Ephemeral        bool
	ExternalIDType   string
	VisibilityState  string
	Guarantee        string
	Note             string
}

type sessionVisibilityError struct {
	code      string
	message   string
	retryable bool
}

func (e *sessionVisibilityError) Error() string { return e.message }

func (e *sessionVisibilityError) CapabilityError() (string, string, bool) {
	return e.code, e.message, e.retryable
}

func invalidSessionVisibility(message string) error {
	return &sessionVisibilityError{code: "AGENT_SESSION_VISIBILITY_INVALID", message: message}
}

func unsupportedSessionVisibility(message string) error {
	return &sessionVisibilityError{code: "AGENT_SESSION_VISIBILITY_UNSUPPORTED", message: message}
}

func unavailableSessionVisibilityState(_ error) error {
	message := "session visibility state is unavailable; refusing to claim or expose a session"
	return &sessionVisibilityError{code: "AGENT_SESSION_VISIBILITY_STATE_UNAVAILABLE", message: message}
}

func resolveSessionVisibility(providerID string, input agentControlParams) (sessionVisibilitySpec, error) {
	providerID = strings.TrimSpace(providerID)
	visibility := strings.ToLower(strings.TrimSpace(input.Visibility))
	if visibility == "" {
		visibility = sessionVisibilityVisible
	}
	if visibility != sessionVisibilityVisible && visibility != sessionVisibilityInternal {
		return sessionVisibilitySpec{}, invalidSessionVisibility("visibility must be visible or internal")
	}

	backend := strings.ToLower(strings.TrimSpace(input.Backend))
	if backend == "" {
		switch providerID {
		case "codex":
			backend = sessionBackendCodexLocal
		case "claude_code":
			backend = sessionBackendClaudeLocal
		default:
			return sessionVisibilitySpec{}, invalidSessionVisibility("backend cannot be inferred for the selected provider")
		}
	}
	if backend == sessionBackendChatGPTCloud {
		return sessionVisibilitySpec{}, unsupportedSessionVisibility("backend=chatgpt_cloud is not supported: Fast Spider has no stable official ChatGPT cloud conversation creation API and does not use private browser endpoints")
	}
	expectedBackend := map[string]string{"codex": sessionBackendCodexLocal, "claude_code": sessionBackendClaudeLocal}[providerID]
	if backend != expectedBackend {
		return sessionVisibilitySpec{}, invalidSessionVisibility(fmt.Sprintf("backend=%s is not compatible with providerId=%s", backend, providerID))
	}

	target := strings.ToLower(strings.TrimSpace(input.VisibilityTarget))
	if target == "" {
		if visibility == sessionVisibilityInternal {
			target = sessionVisibilityTargetNone
		} else {
			target = backend
		}
	}
	if target == sessionBackendChatGPTCloud {
		return sessionVisibilitySpec{}, unsupportedSessionVisibility("visibilityTarget=chatgpt_cloud is not supported: Fast Spider cannot create or attach a ChatGPT cloud conversation without a stable official API")
	}
	if visibility == sessionVisibilityInternal {
		if target != sessionVisibilityTargetNone {
			return sessionVisibilitySpec{}, invalidSessionVisibility("internal sessions must use visibilityTarget=none; they are not published to an external conversation surface")
		}
	} else {
		if target == sessionVisibilityTargetNone {
			return sessionVisibilitySpec{}, invalidSessionVisibility("visible sessions require an explicit external visibility target")
		}
		if target != backend {
			return sessionVisibilitySpec{}, invalidSessionVisibility(fmt.Sprintf("visible visibilityTarget=%s must match backend=%s", target, backend))
		}
	}

	ephemeral := false
	if input.Ephemeral != nil {
		ephemeral = *input.Ephemeral
	}
	if visibility == sessionVisibilityVisible && ephemeral {
		return sessionVisibilitySpec{}, invalidSessionVisibility("ephemeral=true is only supported for internal Codex sessions")
	}
	if backend == sessionBackendClaudeLocal && ephemeral {
		return sessionVisibilitySpec{}, unsupportedSessionVisibility("ephemeral=true is not supported by the Claude Code local session backend")
	}
	if visibility == sessionVisibilityInternal && backend == sessionBackendCodexLocal && input.Ephemeral == nil {
		// Codex app-server supports an ephemeral thread/start mode. Use it for
		// short internal work unless the caller explicitly asks for persistence.
		ephemeral = true
	}

	spec := sessionVisibilitySpec{
		Visibility:       visibility,
		Backend:          backend,
		VisibilityTarget: target,
		Ephemeral:        ephemeral,
	}
	if backend == sessionBackendCodexLocal {
		spec.ExternalIDType = "codex_thread"
	} else {
		spec.ExternalIDType = "claude_session"
	}
	if visibility == sessionVisibilityVisible {
		spec.VisibilityState = "external_id_returned"
		spec.Guarantee = "external_id"
		if backend == sessionBackendCodexLocal {
			spec.Note = "The Codex provider-native Thread identifier is returned. Codex Desktop/UI presentation is not independently guaranteed."
		} else {
			spec.Note = "The Claude Code provider-native session identifier is returned. Native history remains provider-owned and is not a ChatGPT cloud conversation."
		}
	} else if ephemeral {
		spec.VisibilityState = "ephemeral_requested"
		spec.Guarantee = "best_effort"
		spec.Note = "Codex ephemeral=true was requested and Fast Spider excludes this session from session.list; it may not survive an app-server restart, and other Codex clients or UI surfaces may still differ by runtime version."
	} else {
		spec.VisibilityState = "fast_spider_filtered"
		spec.Guarantee = "not_guaranteed"
		if backend == sessionBackendCodexLocal {
			spec.Note = "Fast Spider excludes this persistent local Thread from session.list, but the existing local Codex Thread may still be listed by other Codex clients."
		} else {
			spec.Note = "Fast Spider excludes this persistent local Claude session from session.list; Claude Code native session persistence is still provider-owned."
		}
	}
	return spec, nil
}

func sessionVisibilityUsesLegacyDefaults(input agentControlParams) bool {
	return strings.TrimSpace(input.Visibility) == "" &&
		strings.TrimSpace(input.Backend) == "" &&
		strings.TrimSpace(input.VisibilityTarget) == "" &&
		input.Ephemeral == nil
}

func (s sessionVisibilitySpec) hashFields() map[string]any {
	return map[string]any{
		"visibilityContractVersion": visibilityContractVersion,
		"visibility":                s.Visibility,
		"backend":                   s.Backend,
		"visibilityTarget":          s.VisibilityTarget,
		"ephemeral":                 s.Ephemeral,
	}
}

func (s sessionVisibilitySpec) applyToResult(out map[string]any, sessionID string) {
	if out == nil {
		return
	}
	out["visibility"] = s.Visibility
	out["backend"] = s.Backend
	out["visibilityTarget"] = s.VisibilityTarget
	out["ephemeral"] = s.Ephemeral
	out["visibilityState"] = s.VisibilityState
	out["visibilityGuarantee"] = s.Guarantee
	out["visibilityNote"] = s.Note
	out["externalId"] = sessionID
	out["externalIdType"] = s.ExternalIDType
	if s.Backend == sessionBackendCodexLocal {
		out["externalThreadId"] = sessionID
	} else if s.Backend == sessionBackendClaudeLocal {
		out["externalSessionId"] = sessionID
	}
	out["external"] = map[string]any{"id": sessionID, "type": s.ExternalIDType}
}

func (s sessionVisibilitySpec) record(providerID, sessionID string, now time.Time) sessionVisibilityRecord {
	return sessionVisibilityRecord{
		SessionID:           sessionID,
		ProviderID:          providerID,
		Visibility:          s.Visibility,
		Backend:             s.Backend,
		VisibilityTarget:    s.VisibilityTarget,
		Ephemeral:           s.Ephemeral,
		ExternalID:          sessionID,
		ExternalIDType:      s.ExternalIDType,
		VisibilityState:     s.VisibilityState,
		VisibilityGuarantee: s.Guarantee,
		VisibilityNote:      s.Note,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
}

type sessionVisibilityRecord struct {
	SessionID           string    `json:"sessionId"`
	ProviderID          string    `json:"providerId"`
	Visibility          string    `json:"visibility"`
	Backend             string    `json:"backend"`
	VisibilityTarget    string    `json:"visibilityTarget"`
	Ephemeral           bool      `json:"ephemeral"`
	ExternalID          string    `json:"externalId"`
	ExternalIDType      string    `json:"externalIdType"`
	VisibilityState     string    `json:"visibilityState"`
	VisibilityGuarantee string    `json:"visibilityGuarantee"`
	VisibilityNote      string    `json:"visibilityNote"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

func (r sessionVisibilityRecord) applyToResult(out map[string]any) {
	if out == nil {
		return
	}
	out["visibility"] = r.Visibility
	out["backend"] = r.Backend
	out["visibilityTarget"] = r.VisibilityTarget
	out["ephemeral"] = r.Ephemeral
	out["visibilityState"] = r.VisibilityState
	out["visibilityGuarantee"] = r.VisibilityGuarantee
	out["visibilityNote"] = r.VisibilityNote
	out["externalId"] = r.ExternalID
	out["externalIdType"] = r.ExternalIDType
	if r.ExternalIDType == "codex_thread" {
		out["externalThreadId"] = r.ExternalID
	} else if r.ExternalIDType == "claude_session" {
		out["externalSessionId"] = r.ExternalID
	}
	out["external"] = map[string]any{"id": r.ExternalID, "type": r.ExternalIDType}
}

func defaultSessionVisibilityRecord(providerID, sessionID string) sessionVisibilityRecord {
	backend := sessionBackendCodexLocal
	externalType := "codex_thread"
	if providerID == "claude_code" {
		backend = sessionBackendClaudeLocal
		externalType = "claude_session"
	}
	return sessionVisibilityRecord{
		SessionID:           sessionID,
		ProviderID:          providerID,
		Visibility:          sessionVisibilityVisible,
		Backend:             backend,
		VisibilityTarget:    backend,
		ExternalID:          sessionID,
		ExternalIDType:      externalType,
		VisibilityState:     "unmanaged_existing",
		VisibilityGuarantee: "unmanaged_existing",
		VisibilityNote:      "This session predates the visibility contract; Fast Spider has no stored visibility metadata. The provider session ID is returned without a stronger UI visibility claim.",
	}
}

type sessionVisibilityIndex struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Records       []sessionVisibilityRecord `json:"records"`
}

type sessionVisibilityStore struct {
	mu                       sync.Mutex
	path                     string
	records                  map[string]sessionVisibilityRecord
	loadErr                  error
	beforeCommitSaveOverride func() error
	syncParentOverride       func(string) error
}

func newSessionVisibilityStore(dataDir string) *sessionVisibilityStore {
	store := &sessionVisibilityStore{
		path:    filepath.Join(dataDir, "agent", "session-visibility.json"),
		records: map[string]sessionVisibilityRecord{},
	}
	store.loadErr = store.load()
	return store
}

func sessionVisibilityKey(providerID, sessionID string) string {
	return strings.TrimSpace(providerID) + ":" + strings.TrimSpace(sessionID)
}

func (s *sessionVisibilityStore) load() error {
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var index sessionVisibilityIndex
	if err := json.Unmarshal(raw, &index); err != nil || index.SchemaVersion != visibilityContractVersion || len(index.Records) > maxSessionVisibilityRecords {
		return fmt.Errorf("invalid session visibility index")
	}
	seen := make(map[string]struct{}, len(index.Records))
	for _, record := range index.Records {
		if err := validateSessionVisibilityRecord(record); err != nil {
			return fmt.Errorf("invalid session visibility index: %w", err)
		}
		key := sessionVisibilityKey(record.ProviderID, record.SessionID)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("invalid session visibility index: duplicate session")
		}
		seen[key] = struct{}{}
		s.records[key] = record
	}
	return nil
}

func validateSessionVisibilityRecord(record sessionVisibilityRecord) error {
	if record.SessionID == "" || len(record.SessionID) > 256 || strings.ContainsAny(record.SessionID, "\x00\r\n") {
		return fmt.Errorf("invalid session ID")
	}
	if record.ProviderID != "codex" && record.ProviderID != "claude_code" {
		return fmt.Errorf("invalid provider ID")
	}
	if record.Visibility != sessionVisibilityVisible && record.Visibility != sessionVisibilityInternal {
		return fmt.Errorf("invalid visibility")
	}
	if record.Backend != sessionBackendCodexLocal && record.Backend != sessionBackendClaudeLocal {
		return fmt.Errorf("invalid backend")
	}
	if record.VisibilityTarget != sessionVisibilityTargetNone && record.VisibilityTarget != record.Backend {
		return fmt.Errorf("invalid visibility target")
	}
	if record.Visibility == sessionVisibilityVisible && record.VisibilityTarget != record.Backend {
		return fmt.Errorf("visible record has no matching target")
	}
	if record.Visibility == sessionVisibilityInternal && record.VisibilityTarget != sessionVisibilityTargetNone {
		return fmt.Errorf("internal record has an external target")
	}
	if record.ProviderID == "codex" && record.Backend != sessionBackendCodexLocal {
		return fmt.Errorf("Codex record has an incompatible backend")
	}
	if record.ProviderID == "claude_code" && record.Backend != sessionBackendClaudeLocal {
		return fmt.Errorf("Claude record has an incompatible backend")
	}
	if record.ProviderID == "claude_code" && record.Ephemeral {
		return fmt.Errorf("Claude record cannot be ephemeral")
	}
	expectedExternalType := "codex_thread"
	if record.Backend == sessionBackendClaudeLocal {
		expectedExternalType = "claude_session"
	}
	if record.ExternalIDType != expectedExternalType {
		return fmt.Errorf("invalid external ID type")
	}
	if record.Visibility == sessionVisibilityVisible && record.Ephemeral {
		return fmt.Errorf("visible record cannot be ephemeral")
	}
	if record.ExternalID == "" || record.ExternalID != record.SessionID || record.ExternalIDType == "" || record.VisibilityState == "" || record.VisibilityGuarantee == "" || record.VisibilityNote == "" {
		return fmt.Errorf("incomplete visibility metadata")
	}
	if record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() {
		return fmt.Errorf("missing visibility timestamp")
	}
	return nil
}

func (s *sessionVisibilityStore) snapshot() (map[string]sessionVisibilityRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	out := make(map[string]sessionVisibilityRecord, len(s.records))
	for key, record := range s.records {
		out[key] = record
	}
	return out, nil
}

func (s *sessionVisibilityStore) put(record sessionVisibilityRecord) error {
	if err := validateSessionVisibilityRecord(record); err != nil {
		s.mu.Lock()
		if s.loadErr == nil {
			s.loadErr = err
		}
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return s.loadErr
	}
	key := sessionVisibilityKey(record.ProviderID, record.SessionID)
	previous, existed := s.records[key]
	if !existed && len(s.records) >= maxSessionVisibilityRecords {
		return fmt.Errorf("session visibility store is full")
	}
	s.records[key] = record
	if committed, err := s.saveLocked(); err != nil {
		s.loadErr = err
		if !committed {
			if existed {
				s.records[key] = previous
			} else {
				delete(s.records, key)
			}
		}
		return err
	}
	return nil
}

func (s *sessionVisibilityStore) delete(providerID, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return s.loadErr
	}
	key := sessionVisibilityKey(providerID, sessionID)
	previous, existed := s.records[key]
	if !existed {
		return nil
	}
	delete(s.records, key)
	if committed, err := s.saveLocked(); err != nil {
		s.loadErr = err
		if !committed {
			s.records[key] = previous
		}
		return err
	}
	return nil
}

func (s *sessionVisibilityStore) saveLocked() (bool, error) {
	if s.beforeCommitSaveOverride != nil {
		return false, s.beforeCommitSaveOverride()
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return false, err
	}
	records := make([]sessionVisibilityRecord, 0, len(s.records))
	for _, record := range s.records {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].UpdatedAt.Before(records[j].UpdatedAt) })
	raw, err := json.Marshal(sessionVisibilityIndex{SchemaVersion: visibilityContractVersion, Records: records})
	if err != nil {
		return false, err
	}
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".session-visibility-*")
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
		s.loadErr = err
		return true, err
	}
	return true, nil
}

func sessionVisibilityCapabilityMatrix() map[string]any {
	return map[string]any{
		"contractVersion":        visibilityContractVersion,
		"visibilityValues":       []string{sessionVisibilityVisible, sessionVisibilityInternal},
		"backendValues":          []string{sessionBackendCodexLocal, sessionBackendClaudeLocal, sessionBackendChatGPTCloud},
		"visibilityTargetValues": []string{sessionVisibilityTargetNone, sessionBackendCodexLocal, sessionBackendClaudeLocal, sessionBackendChatGPTCloud},
		"defaults": map[string]any{
			"codex":       map[string]any{"visibility": sessionVisibilityVisible, "backend": sessionBackendCodexLocal, "visibilityTarget": sessionBackendCodexLocal, "ephemeral": false},
			"claude_code": map[string]any{"visibility": sessionVisibilityVisible, "backend": sessionBackendClaudeLocal, "visibilityTarget": sessionBackendClaudeLocal, "ephemeral": false},
		},
		"chatgptCloud": map[string]any{
			"state":      "unsupported",
			"create":     false,
			"reasonCode": "CHATGPT_CLOUD_CREATE_UNSUPPORTED",
			"reason":     "Fast Spider does not have a stable official ChatGPT cloud conversation creation API and will not call private browser endpoints",
		},
		"targets": map[string]any{
			"none": map[string]any{
				"internal": map[string]any{"state": "supported", "publishesExternalSession": false},
			},
			"codex_local": map[string]any{
				"visible":  map[string]any{"state": "supported", "externalIdType": "codex_thread"},
				"internal": map[string]any{"state": "best_effort", "ephemeralSupported": true, "persistentVisibility": "not_guaranteed"},
			},
			"claude_local": map[string]any{
				"visible":  map[string]any{"state": "supported", "externalIdType": "claude_session"},
				"internal": map[string]any{"state": "best_effort", "ephemeralSupported": false, "persistentVisibility": "fast_spider_filtered_only"},
			},
			"chatgpt_cloud": map[string]any{
				"visible":  map[string]any{"state": "unsupported", "reasonCode": "CHATGPT_CLOUD_CREATE_UNSUPPORTED"},
				"internal": map[string]any{"state": "unsupported", "reasonCode": "CHATGPT_CLOUD_CREATE_UNSUPPORTED"},
			},
		},
	}
}

func (m *AgentManager) visibilitySnapshot() (map[string]sessionVisibilityRecord, error) {
	if m.visibilityStore == nil {
		return map[string]sessionVisibilityRecord{}, nil
	}
	snapshot, err := m.visibilityStore.snapshot()
	if err != nil {
		return nil, unavailableSessionVisibilityState(err)
	}
	return snapshot, nil
}

func (m *AgentManager) persistSessionVisibility(record sessionVisibilityRecord) error {
	if m.visibilityStore == nil {
		return nil
	}
	if err := m.visibilityStore.put(record); err != nil {
		return unavailableSessionVisibilityState(err)
	}
	return nil
}

func (m *AgentManager) forgetSessionVisibility(providerID, sessionID string) error {
	if m.visibilityStore == nil {
		return nil
	}
	if err := m.visibilityStore.delete(providerID, sessionID); err != nil {
		return unavailableSessionVisibilityState(err)
	}
	return nil
}

func visibilityRecordFor(snapshot map[string]sessionVisibilityRecord, providerID, sessionID string) sessionVisibilityRecord {
	if record, ok := snapshot[sessionVisibilityKey(providerID, sessionID)]; ok {
		return record
	}
	return defaultSessionVisibilityRecord(providerID, sessionID)
}

func decorateSessionWithVisibility(session map[string]any, providerID string, snapshot map[string]sessionVisibilityRecord) (sessionVisibilityRecord, error) {
	if session == nil {
		return sessionVisibilityRecord{}, nil
	}
	sessionID := mapString(session, "sessionId")
	if sessionID == "" {
		sessionID = mapString(session, "id")
	}
	if sessionID == "" {
		return sessionVisibilityRecord{}, nil
	}
	record := visibilityRecordFor(snapshot, providerID, sessionID)
	session["providerId"] = providerID
	record.applyToResult(session)
	return record, nil
}

func (m *AgentManager) claudeSessionList(workingDirectory string, limit int) (map[string]any, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	snapshot, err := m.visibilitySnapshot()
	if err != nil {
		return nil, err
	}
	// Fetch the provider maximum before filtering. Otherwise a page containing
	// internal sessions could return fewer visible sessions than requested.
	result := m.claude.List(workingDirectory, 100)
	raw, ok := result["sessions"].([]map[string]any)
	if !ok {
		return map[string]any{"sessions": []map[string]any{}}, nil
	}
	items := make([]map[string]any, 0, len(raw))
	for _, session := range raw {
		record, _ := decorateSessionWithVisibility(session, "claude_code", snapshot)
		if record.Visibility == sessionVisibilityInternal {
			continue
		}
		items = append(items, session)
		if len(items) >= limit {
			break
		}
	}
	result["sessions"] = items
	return result, nil
}
