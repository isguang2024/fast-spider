package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
)

const (
	// The callback event itself is realtime. The local queue check only needs
	// to retry a nudge or release a lease; it must not turn into a busy poller.
	sessionCallbackDeliveryRetryInterval = 30 * time.Second
	// Provider status reads are a recovery path for a missed callback, not the
	// normal completion channel. Keep them deliberately infrequent.
	sessionCallbackRecoveryInterval = 10 * time.Minute
	sessionCallbackNudgeAfter       = 5 * time.Minute
	sessionCallbackNudgeInterval    = 10 * time.Minute
)

type sessionCallbackDispatcher struct {
	store  *sessionCallbackStore
	logger *slog.Logger
	active func(string) bool
	send   func(context.Context, string, string) error
	ensure func(context.Context, string, int64) error
	// recoverStatus performs one bounded provider status read for a registered
	// Cloud CHAT. It is intentionally separate from ensure so tests and callers
	// can keep callback subscription recovery free of provider polling.
	recoverStatus func(context.Context, string, int64) error

	notify    chan struct{}
	rootCtx   context.Context
	cancel    context.CancelFunc
	startOnce sync.Once
	closeOnce sync.Once
	wg        sync.WaitGroup
	interval  time.Duration
}

func newSessionCallbackDispatcher(
	store *sessionCallbackStore,
	logger *slog.Logger,
	active func(string) bool,
	send func(context.Context, string, string) error,
	ensure func(context.Context, string, int64) error,
) *sessionCallbackDispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	rootCtx, cancel := context.WithCancel(context.Background())
	return &sessionCallbackDispatcher{
		store: store, logger: logger, active: active, send: send, ensure: ensure,
		notify: make(chan struct{}, 1), rootCtx: rootCtx, cancel: cancel, interval: sessionCallbackDeliveryRetryInterval,
	}
}

func (d *sessionCallbackDispatcher) start() {
	if d == nil {
		return
	}
	d.startOnce.Do(func() {
		d.wg.Add(2)
		go d.runDelivery()
		go d.runRecovery()
	})
}

func (d *sessionCallbackDispatcher) signal() {
	if d == nil {
		return
	}
	select {
	case d.notify <- struct{}{}:
	default:
	}
}

func (d *sessionCallbackDispatcher) close(ctx context.Context) error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(d.cancel)
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *sessionCallbackDispatcher) runDelivery() {
	defer d.wg.Done()
	deliveryInterval := d.interval
	if deliveryInterval <= 0 {
		deliveryInterval = sessionCallbackDeliveryRetryInterval
	}
	deliveryTicker := time.NewTicker(deliveryInterval)
	defer deliveryTicker.Stop()
	d.dispatchOnce()
	for {
		select {
		case <-d.rootCtx.Done():
			return
		case <-d.notify:
			d.dispatchOnce()
		case <-deliveryTicker.C:
			d.dispatchOnce()
		}
	}
}

func (d *sessionCallbackDispatcher) runRecovery() {
	defer d.wg.Done()
	recoveryTicker := time.NewTicker(sessionCallbackRecoveryInterval)
	defer recoveryTicker.Stop()
	d.reconcileSubscriptions()
	for {
		select {
		case <-d.rootCtx.Done():
			return
		case <-recoveryTicker.C:
			d.reconcileSubscriptions()
			d.signal()
		}
	}
}

func (d *sessionCallbackDispatcher) reconcileSubscriptions() {
	if d == nil || d.store == nil || (d.ensure == nil && d.recoverStatus == nil) {
		return
	}
	registrations, _, err := d.store.registrationsSnapshot("", "")
	if err != nil {
		d.logger.Warn("load session callbacks for realtime recovery", "error", err)
		return
	}
	for _, registration := range registrations {
		if d.ensure != nil {
			ctx, cancel := context.WithTimeout(d.rootCtx, 10*time.Second)
			_, ensureErr := d.store.withCurrentRegistration(registration.SourceSessionID, registration.Generation, func() error {
				return d.ensure(ctx, registration.SourceSessionID, registration.Generation)
			})
			cancel()
			if ensureErr != nil && !errors.Is(ensureErr, context.Canceled) {
				d.logger.Warn("restore ChatGPT Cloud callback subscription", "sourceSessionId", registration.SourceSessionID, "error", ensureErr)
			}
		}
		if d.recoverStatus != nil {
			statusCtx, statusCancel := context.WithTimeout(d.rootCtx, 20*time.Second)
			statusErr := d.recoverStatus(statusCtx, registration.SourceSessionID, registration.Generation)
			statusCancel()
			if statusErr != nil && !errors.Is(statusErr, context.Canceled) {
				d.logger.Warn("recover missed ChatGPT Cloud callback", "sourceSessionId", registration.SourceSessionID, "error", statusErr)
			}
		}
	}
}

func (d *sessionCallbackDispatcher) dispatchOnce() {
	if d == nil || d.store == nil || d.send == nil {
		return
	}
	now := time.Now().UTC()
	if _, err := d.store.releaseExpiredClaims(now); err != nil {
		d.logger.Warn("release expired session callback claims", "error", err)
		return
	}
	grouped, err := d.store.pendingByTarget()
	if err != nil {
		d.logger.Warn("load pending session callbacks", "error", err)
		return
	}
	targets := make([]string, 0, len(grouped))
	for target := range grouped {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	for _, target := range targets {
		events := grouped[target]
		if len(events) == 0 || (d.active != nil && d.active(target)) {
			continue
		}
		claimable := make([]sessionCallbackEvent, 0, len(events))
		oldest := time.Time{}
		for _, event := range events {
			if event.ClaimID == "" || !callbackClaimActive(event, now) {
				claimable = append(claimable, event)
			}
			if oldest.IsZero() || event.OccurredAt.Before(oldest) {
				oldest = event.OccurredAt
			}
		}
		if len(claimable) == 0 || oldest.IsZero() || now.Sub(oldest) < sessionCallbackNudgeAfter {
			continue
		}
		due, err := d.store.nudgeDue(target, now, sessionCallbackNudgeInterval)
		if err != nil {
			d.logger.Warn("check session callback nudge", "targetSessionId", target, "error", err)
			continue
		}
		if !due {
			continue
		}
		envelopeID := sessionCallbackEnvelopeID(target, claimable)
		prompt := buildSessionCallbackNudge(target, len(claimable), envelopeID)
		ctx, cancel := context.WithTimeout(d.rootCtx, 2*time.Minute)
		sendErr := d.send(ctx, target, prompt)
		cancel()
		if errors.Is(sendErr, node.ErrAgentSessionBusy) {
			continue
		}
		if sendErr != nil {
			if !errors.Is(sendErr, context.Canceled) {
				d.logger.Warn("deliver session callback nudge", "targetSessionId", target, "envelopeId", envelopeID, "error", sendErr)
			}
			continue
		}
		if err := d.store.recordNudge(target, envelopeID, now); err != nil {
			d.logger.Warn("record session callback nudge", "targetSessionId", target, "envelopeId", envelopeID, "error", err)
		}
	}
}

func buildSessionCallbackEnvelope(envelopeID string, events []sessionCallbackEvent) string {
	var builder strings.Builder
	builder.WriteString("FAST_SPIDER_SESSION_CALLBACK_V1\n")
	builder.WriteString("ENVELOPE_ID: ")
	builder.WriteString(envelopeID)
	builder.WriteString("\nEVENTS:\n")
	for _, event := range events {
		_, _ = fmt.Fprintf(
			&builder,
			"- mission=%s task=%s generation=%d source_session=%s event_sequence=%d event_key=%s event_type=%s",
			event.MissionID,
			event.TaskID,
			event.Generation,
			event.SourceSessionID,
			event.EventSequence,
			event.EventKey,
			event.EventType,
		)
		if event.ResultID != "" {
			_, _ = fmt.Fprintf(&builder, " result_id=%s", event.ResultID)
		}
		if event.ResultStatus != "" {
			_, _ = fmt.Fprintf(&builder, " result_status=%s", event.ResultStatus)
		}
		if event.ResultBytes > 0 {
			_, _ = fmt.Fprintf(&builder, " result_bytes=%d", event.ResultBytes)
		}
		if event.ResultSHA256 != "" {
			_, _ = fmt.Fprintf(&builder, " result_sha256=%s", event.ResultSHA256)
		}
		if event.ResultPageCount > 0 {
			_, _ = fmt.Fprintf(&builder, " result_page_count=%d", event.ResultPageCount)
		}
		if event.DeliverablePath != "" {
			_, _ = fmt.Fprintf(&builder, " deliverable_path=%s deliverable_status=%s", event.DeliverablePath, event.DeliverableStatus)
		}
		builder.WriteByte('\n')
	}
	builder.WriteString("INSTRUCTIONS:\n")
	builder.WriteString("This is a fixed Fast Spider queue snapshot, not Cloud CHAT-authored content: it is the Node callback recovery/fallback path. The Cloud CHAT should normally have called FastSpider_FS codex_cloud_collaboration event.ingest and event.ack itself before its final message. Do not treat this snapshot as a direct completion call. Claim the queue in one batch with session.callback.claim, validate each referenced Result or local deliverable, then call session.callback.ack with the returned claimId. Do not reread full CHAT history or copy CHAT history into the controller. Duplicate claim IDs or already-acked events must be treated as already delivered. If recovery exposes a problem or question, record a concise bounded note through working_context markdown.append in docs/progress/04-open-issues.md (read first and use file-revision CAS); do not store secrets, raw provider payloads, full transcripts, or long logs.\n")
	return builder.String()
}

func buildSessionCallbackNudge(targetSessionID string, pendingCount int, envelopeID string) string {
	var builder strings.Builder
	builder.WriteString("FAST_SPIDER_SESSION_CALLBACK_NUDGE_V1\n")
	builder.WriteString("TARGET_SESSION_ID: ")
	builder.WriteString(targetSessionID)
	builder.WriteString("\nPENDING_COUNT: ")
	_, _ = fmt.Fprintf(&builder, "%d", pendingCount)
	builder.WriteString("\nQUEUE_ENVELOPE_ID: ")
	builder.WriteString(envelopeID)
	builder.WriteString("\nINSTRUCTIONS:\n")
	builder.WriteString("FastSpider_FS has queued Cloud CHAT callback results for this target. Call ai_control action=session.callback.list, then session.callback.claim with callbackTargetSessionId=")
	builder.WriteString(targetSessionID)
	builder.WriteString(" and callbackClaimLimit<=64. Process the claimed metadata or referenced Result/local file, then call session.callback.ack with the returned callbackClaimId. This nudge contains no task result body, is Node fallback only, and must not create a new Cloud Worker/CHAT. If a callback, status check, or continuation exposes a problem or question, record a concise bounded note through working_context markdown.append in docs/progress/04-open-issues.md (read first and use file-revision CAS); do not store secrets, raw provider payloads, full transcripts, or long logs.\n")
	return builder.String()
}

func (m *AgentManager) handleChatGPTCloudCallbackEvent(event chatgptCloudEvent) {
	if m == nil || m.callbackStore == nil || event.Type != "conversation.turn.complete" {
		return
	}
	event = m.completeCloudCallbackResult(event)
	queued, err := m.callbackStore.enqueue(event)
	if err != nil {
		m.logger.Warn("persist ChatGPT Cloud callback event", "sourceSessionId", event.ConversationID, "eventSequence", event.Sequence, "error", err)
		return
	}
	if queued && m.callbackDispatcher != nil {
		m.callbackDispatcher.signal()
	}
}

func (m *AgentManager) completeCloudCallbackResult(event chatgptCloudEvent) chatgptCloudEvent {
	registration, ok, err := m.callbackStore.registrationFor(event.ConversationID)
	if err != nil || !ok {
		return event
	}
	if registration.DeliverablePath != "" {
		event.DeliverablePath = registration.DeliverablePath
		event.DeliverableStatus, event.ResultBytes, event.ResultSHA256 = inspectCallbackDeliverable(registration.DeliverablePath)
		if event.DeliverableStatus == "ready" {
			event.ResultStatus = "ready"
		} else {
			event.ResultStatus = "failed"
		}
		return event
	}
	if m.resultPublisher == nil {
		event.ResultStatus = "unknown"
		return event
	}
	callbackCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	detail, err := m.chatgptCloud.Read(callbackCtx, event.ConversationID)
	if err != nil {
		event.ResultStatus = "failed"
		return event
	}
	status := chatgptCloudConversationStatus(detail)
	// The observer is invoked only for the provider's terminal
	// conversation-turn-complete signal. If the follow-up detail omits async
	// status, that terminal callback is the missing completion proof.
	if status == "unknown" {
		status = "completed"
	}
	event.ResultStatus = status
	if status != "completed" {
		return event
	}
	text, err := chatgptCloudLatestAssistantTextLimit(detail, 8<<20)
	if err != nil {
		event.ResultStatus = "failed"
		return event
	}
	keyHash := sha256.Sum256([]byte(event.ConversationID + "\x00" + event.EventKey + "\x00" + fmt.Sprintf("%d", registration.Generation)))
	idempotencyKey := "cloud_result_" + hex.EncodeToString(keyHash[:])[:48]
	metadata, err := m.resultPublisher.PublishCloudResult(callbackCtx, event.ConversationID, idempotencyKey, text)
	if err != nil {
		event.ResultStatus = "failed"
		return event
	}
	event.ResultID = mapString(metadata, "resultId")
	if event.ResultID == "" {
		event.ResultStatus = "failed"
		return event
	}
	event.ResultStatus = firstNonEmptyString(mapString(metadata, "status"), "ready")
	event.ResultBytes = mapInt64(metadata, "bytes")
	event.ResultSHA256 = mapString(metadata, "sha256")
	event.ResultPageCount = int(mapInt64(metadata, "pageCount"))
	return event
}

func inspectCallbackDeliverable(path string) (string, int64, string) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", 0, ""
	}
	if err != nil {
		return "unreadable", 0, ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "unreadable", 0, ""
	}
	if !info.Mode().IsRegular() {
		return "invalid", 0, ""
	}
	if info.Size() > 256<<20 {
		return "too_large", info.Size(), ""
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "unreadable", 0, ""
	}
	return "ready", info.Size(), "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func applyCallbackResultMetadata(out map[string]any, metadata callbackResultMetadata) {
	if metadata.ResultID != "" {
		out["resultId"] = metadata.ResultID
	}
	if metadata.Status != "" {
		out["resultStatus"] = metadata.Status
	}
	if metadata.Bytes > 0 {
		out["resultBytes"] = metadata.Bytes
	}
	if metadata.SHA256 != "" {
		out["resultSHA256"] = metadata.SHA256
	}
	if metadata.PageCount > 0 {
		out["resultPageCount"] = metadata.PageCount
	}
}

func (m *AgentManager) handleCodexCallbackEvent(event AgentEvent) {
	if m == nil || m.callbackDispatcher == nil {
		return
	}
	if event.Type == "turn.completed" || event.Type == "turn.interrupted" || event.Type == "turn.failed" {
		m.callbackDispatcher.signal()
	}
}

func (m *AgentManager) sessionCallbackRegister(ctx context.Context, input agentControlParams) (map[string]any, error) {
	if m.callbackStore == nil || m.callbackDispatcher == nil {
		return nil, callbackStoreUnavailableError()
	}
	m.chatgptCloud.SetRealtimeObserver(m.handleChatGPTCloudCallbackEvent, m.callbackStore.maxEventSequence())
	sourceSessionID := strings.TrimSpace(input.SessionID)
	targetSessionID := strings.TrimSpace(input.CallbackTargetSessionID)
	if err := validateCallbackOpaqueID(sourceSessionID, "source session ID", 256); err != nil {
		return nil, &sessionCallbackError{code: "INVALID_REQUEST", message: err.Error()}
	}
	if err := validateCallbackOpaqueID(targetSessionID, "callback target session ID", 256); err != nil {
		return nil, &sessionCallbackError{code: "INVALID_REQUEST", message: err.Error()}
	}
	sourceIsManagedCloudCHAT := false
	if record, exists, err := m.storedSessionVisibilityRecord("codex", sourceSessionID); err != nil {
		return nil, err
	} else if exists && record.Backend == sessionBackendChatGPTCloud {
		sourceIsManagedCloudCHAT = true
	}
	if !sourceIsManagedCloudCHAT {
		if _, err := m.chatgptCloud.Read(ctx, sourceSessionID); err != nil {
			return nil, fmt.Errorf("validate ChatGPT Cloud callback source: %w", err)
		}
	}
	targetIsCloudCHAT := false
	if record, exists, err := m.storedSessionVisibilityRecord("codex", targetSessionID); err != nil {
		return nil, err
	} else if exists && record.Backend == sessionBackendChatGPTCloud {
		targetIsCloudCHAT = true
	}
	if !targetIsCloudCHAT {
		if _, err := m.authorizedThreadMetadata(ctx, targetSessionID); err != nil {
			return nil, fmt.Errorf("validate callback target session: %w", err)
		}
	}
	registration, replayed, err := m.callbackStore.register(sessionCallbackRegistration{
		SourceSessionID: sourceSessionID,
		TargetSessionID: targetSessionID,
		MissionID:       input.CallbackMissionID,
		TaskID:          input.CallbackTaskID,
		Generation:      input.CallbackGeneration,
		DeliverablePath: strings.TrimSpace(input.CallbackDeliverablePath),
	})
	if err != nil {
		return nil, err
	}
	// Persist ownership before subscribing. This closes the event visibility gap:
	// an event arriving while the watcher starts is now accepted by the store.
	current, err := m.callbackStore.withCurrentRegistration(sourceSessionID, registration.Generation, func() error {
		return m.chatgptCloud.EnsureCallbackRealtimeForGeneration(ctx, sourceSessionID, registration.Generation)
	})
	if err != nil {
		// Keep the durable registration. The recovery loop will retry the watcher;
		// returning the error makes the failed registration visible to the caller.
		return nil, err
	}
	if !current {
		return nil, &sessionCallbackError{code: "CALLBACK_GENERATION_STALE", message: "callback registration was replaced or unregistered before its watcher was established"}
	}
	// The Cloud turn may finish between session.create returning and the durable
	// callback registration being installed. Re-read once after subscription and
	// synthesize the same terminal event when that window was hit.
	go m.reconcileCompletedCloudCallback(sourceSessionID, registration.Generation)
	m.callbackDispatcher.signal()
	return map[string]any{
		"callback":                          callbackRegistrationMap(registration, 0),
		"replayed":                          replayed,
		"deliveryPolicy":                    "queued-batch-claim",
		"recoveryPolicy":                    "node-fallback-status-poll-and-nudge",
		"fallbackStatusPollIntervalSeconds": int64(sessionCallbackRecoveryInterval / time.Second),
		"fallbackNudgeAfterSeconds":         int64(sessionCallbackNudgeAfter / time.Second),
		"fallbackNudgeIntervalSeconds":      int64(sessionCallbackNudgeInterval / time.Second),
	}, nil
}

func (m *AgentManager) reconcileCompletedCloudCallback(sourceSessionID string, generation int64) {
	if m == nil || m.callbackStore == nil || m.chatgptCloud == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for attempt := 0; attempt < 16; attempt++ {
		registration, current, storeErr := m.callbackStore.registrationFor(sourceSessionID)
		if storeErr != nil || !current || registration.Generation != generation {
			return
		}
		detail, err := m.chatgptCloud.Read(ctx, sourceSessionID)
		if err == nil && chatgptCloudConversationStatus(detail) == "completed" {
			sum := sha256.Sum256([]byte(sourceSessionID + "\x00register-reconcile\x00" + fmt.Sprintf("%d", generation)))
			m.handleChatGPTCloudCallbackEvent(chatgptCloudEvent{
				Sequence:       m.callbackStore.maxEventSequence() + 1,
				EventKey:       "provider_evt_reconcile_" + hex.EncodeToString(sum[:])[:40],
				Type:           "conversation.turn.complete",
				ConversationID: sourceSessionID,
				EventType:      "conversation-turn-complete",
				Timestamp:      time.Now().UTC(),
			})
			return
		}
		if err != nil && !errors.Is(err, context.Canceled) && attempt == 15 {
			m.logger.Warn("reconcile completed ChatGPT Cloud callback after registration", "sourceSessionId", sourceSessionID, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// recoverCompletedCloudCallback is the deliberately low-frequency Node
// fallback. Realtime callback delivery remains authoritative; this single
// status read only synthesizes a terminal event when a registered Cloud CHAT
// is already completed and the realtime event was missed.
func (m *AgentManager) recoverCompletedCloudCallback(ctx context.Context, sourceSessionID string, generation int64) error {
	if m == nil || m.callbackStore == nil || m.chatgptCloud == nil {
		return nil
	}
	registration, current, err := m.callbackStore.registrationFor(sourceSessionID)
	if err != nil {
		return err
	}
	if !current || registration.Generation != generation {
		return nil
	}
	detail, err := m.chatgptCloud.Read(ctx, sourceSessionID)
	if err != nil {
		return err
	}
	if chatgptCloudConversationStatus(detail) != "completed" {
		return nil
	}
	now := time.Now().UTC()
	assistantID := chatgptCloudLastAssistantID(detail)
	currentNode := chatgptCloudCurrentNodeID(detail)
	identity := firstNonEmptyString(assistantID, currentNode, fmt.Sprint(detail["updateTime"]))
	eventKey := ""
	if identity != "" {
		sum := sha256.Sum256([]byte(sourceSessionID + "\x00" + fmt.Sprintf("%d", generation) + "\x00" + identity))
		eventKey = "provider_evt_fallback_" + hex.EncodeToString(sum[:])[:40]
	} else {
		eventKey = chatgptRealtimeEventKeyAt(sourceSessionID, "conversation-turn-complete", map[string]any{"conversation_id": sourceSessionID}, now)
	}
	sequence := registration.LastEventSequence + 1
	if next := m.callbackStore.maxEventSequence() + 1; next > sequence {
		sequence = next
	}
	m.handleChatGPTCloudCallbackEvent(chatgptCloudEvent{
		Sequence:       sequence,
		EventKey:       eventKey,
		Type:           "conversation.turn.complete",
		ConversationID: sourceSessionID,
		EventType:      "conversation-turn-complete",
		Timestamp:      now,
	})
	return nil
}

func (m *AgentManager) sessionCallbackUnregister(input agentControlParams) (map[string]any, error) {
	if m.callbackStore == nil {
		return nil, callbackStoreUnavailableError()
	}
	sourceSessionID := strings.TrimSpace(input.SessionID)
	removed, err := m.callbackStore.unregister(sourceSessionID, input.CallbackGeneration)
	if err != nil {
		return nil, err
	}
	if removed {
		m.chatgptCloud.ReleaseCallbackRealtimeForGeneration(sourceSessionID, input.CallbackGeneration)
	}
	return map[string]any{"sourceSessionId": sourceSessionID, "unregistered": removed}, nil
}

func (m *AgentManager) sessionCallbackList(input agentControlParams) (map[string]any, error) {
	if m.callbackStore == nil {
		return nil, callbackStoreUnavailableError()
	}
	items, pendingCounts, err := m.callbackStore.registrationsSnapshot(strings.TrimSpace(input.SessionID), strings.TrimSpace(input.CallbackTargetSessionID))
	if err != nil {
		return nil, err
	}
	sourceSessionID := strings.TrimSpace(input.SessionID)
	targetSessionID := strings.TrimSpace(input.CallbackTargetSessionID)
	pending, err := m.callbackStore.pendingSnapshot(sourceSessionID, targetSessionID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, registration := range items {
		out = append(out, callbackRegistrationMap(registration, pendingCounts[registration.SourceSessionID]))
	}
	pendingOut := make([]map[string]any, 0, len(pending))
	now := time.Now().UTC()
	for _, event := range pending {
		pendingOut = append(pendingOut, sessionCallbackEventMap(event, now))
	}
	return map[string]any{
		"callbacks":                         out,
		"pending":                           pendingOut,
		"queueText":                         buildSessionCallbackQueueText(targetSessionID, pending, now),
		"maxClaimBatch":                     maxSessionCallbackClaimBatch,
		"claimLeaseSeconds":                 int64(sessionCallbackClaimLease / time.Second),
		"deliveryPolicy":                    "queued-batch-claim",
		"recoveryPolicy":                    "node-fallback-status-poll-and-nudge",
		"fallbackStatusPollIntervalSeconds": int64(sessionCallbackRecoveryInterval / time.Second),
		"fallbackNudgeAfterSeconds":         int64(sessionCallbackNudgeAfter / time.Second),
		"fallbackNudgeIntervalSeconds":      int64(sessionCallbackNudgeInterval / time.Second),
	}, nil
}

func (m *AgentManager) sessionCallbackClaim(input agentControlParams) (map[string]any, error) {
	if m.callbackStore == nil {
		return nil, callbackStoreUnavailableError()
	}
	targetSessionID := callbackTargetSessionID(input)
	if targetSessionID == "" {
		return nil, &sessionCallbackError{code: "INVALID_REQUEST", message: "callbackTargetSessionId is required for session.callback.claim"}
	}
	now := time.Now().UTC()
	claimID, events, err := m.callbackStore.claim(targetSessionID, input.CallbackClaimID, input.CallbackClaimLimit, now)
	if err != nil {
		return nil, err
	}
	claimed := make([]map[string]any, 0, len(events))
	for _, event := range events {
		claimed = append(claimed, sessionCallbackEventMap(event, now))
	}
	return map[string]any{
		"callbackTargetSessionId": targetSessionID,
		"claimId":                 claimID,
		"claimed":                 claimed,
		"claimedCount":            len(claimed),
		"claimLeaseSeconds":       int64(sessionCallbackClaimLease / time.Second),
		"deliveryPolicy":          "queued-batch-claim",
	}, nil
}

func (m *AgentManager) sessionCallbackAck(input agentControlParams) (map[string]any, error) {
	if m.callbackStore == nil {
		return nil, callbackStoreUnavailableError()
	}
	targetSessionID := callbackTargetSessionID(input)
	if targetSessionID == "" {
		return nil, &sessionCallbackError{code: "INVALID_REQUEST", message: "callbackTargetSessionId is required for session.callback.ack"}
	}
	acked, err := m.callbackStore.acknowledgeClaim(targetSessionID, input.CallbackClaimID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"callbackTargetSessionId": targetSessionID,
		"claimId":                 strings.TrimSpace(input.CallbackClaimID),
		"acked":                   true,
		"ackedCount":              acked,
		"deliveryPolicy":          "queued-batch-claim",
	}, nil
}

func callbackTargetSessionID(input agentControlParams) string {
	if target := strings.TrimSpace(input.CallbackTargetSessionID); target != "" {
		return target
	}
	return strings.TrimSpace(input.SessionID)
}

func sessionCallbackEventMap(event sessionCallbackEvent, now time.Time) map[string]any {
	out := map[string]any{
		"sourceSessionId": event.SourceSessionID,
		"targetSessionId": event.TargetSessionID,
		"missionId":       event.MissionID,
		"taskId":          event.TaskID,
		"generation":      event.Generation,
		"eventSequence":   event.EventSequence,
		"eventKey":        event.EventKey,
		"eventType":       event.EventType,
		"occurredAt":      event.OccurredAt.UTC().Format(time.RFC3339Nano),
		"claimState":      "claimable",
	}
	if event.ResultID != "" {
		out["resultId"] = event.ResultID
	}
	if event.ResultStatus != "" {
		out["resultStatus"] = event.ResultStatus
	}
	if event.ResultBytes > 0 {
		out["resultBytes"] = event.ResultBytes
	}
	if event.ResultSHA256 != "" {
		out["resultSHA256"] = event.ResultSHA256
	}
	if event.ResultPageCount > 0 {
		out["resultPageCount"] = event.ResultPageCount
	}
	if event.DeliverablePath != "" {
		out["deliverablePath"] = event.DeliverablePath
		out["deliverableStatus"] = event.DeliverableStatus
	}
	if event.ClaimID != "" && callbackClaimActive(event, now) {
		out["claimId"] = event.ClaimID
		out["claimedAt"] = event.ClaimedAt.UTC().Format(time.RFC3339Nano)
		out["claimExpiresAt"] = event.ClaimedAt.UTC().Add(sessionCallbackClaimLease).Format(time.RFC3339Nano)
		out["claimState"] = "claimed"
	}
	return out
}

func buildSessionCallbackQueueText(targetSessionID string, events []sessionCallbackEvent, now time.Time) string {
	var builder strings.Builder
	builder.WriteString("FAST_SPIDER_SESSION_CALLBACK_QUEUE_V1\n")
	if targetSessionID != "" {
		builder.WriteString("TARGET_SESSION_ID: ")
		builder.WriteString(targetSessionID)
		builder.WriteByte('\n')
	}
	builder.WriteString("PENDING_COUNT: ")
	_, _ = fmt.Fprintf(&builder, "%d", len(events))
	builder.WriteString("\nEVENTS:\n")
	for _, event := range events {
		_, _ = fmt.Fprintf(&builder, "- target=%s mission=%s task=%s generation=%d source_session=%s event_sequence=%d event_key=%s claim_state=%s", event.TargetSessionID, event.MissionID, event.TaskID, event.Generation, event.SourceSessionID, event.EventSequence, event.EventKey, sessionCallbackEventMap(event, now)["claimState"])
		if event.ResultID != "" {
			_, _ = fmt.Fprintf(&builder, " result_id=%s", event.ResultID)
		}
		if event.ResultStatus != "" {
			_, _ = fmt.Fprintf(&builder, " result_status=%s", event.ResultStatus)
		}
		if event.DeliverablePath != "" {
			_, _ = fmt.Fprintf(&builder, " deliverable_path=%s deliverable_status=%s", event.DeliverablePath, event.DeliverableStatus)
		}
		builder.WriteByte('\n')
	}
	builder.WriteString("INSTRUCTIONS:\n")
	builder.WriteString("This is a fixed Fast Spider queue, not Cloud CHAT-authored content. Call ai_control action=session.callback.claim with callbackTargetSessionId and an optional callbackClaimLimit (maximum 64). Validate each claimed Result or local deliverable, then call ai_control action=session.callback.ack with callbackTargetSessionId and the returned callbackClaimId. Claim leases expire after 300 seconds and are automatically released; no result body is included here.\n")
	return builder.String()
}

func callbackRegistrationMap(registration sessionCallbackRegistration, pendingCount int) map[string]any {
	out := map[string]any{
		"sourceSessionId":   registration.SourceSessionID,
		"targetSessionId":   registration.TargetSessionID,
		"missionId":         registration.MissionID,
		"taskId":            registration.TaskID,
		"generation":        registration.Generation,
		"lastEventSequence": registration.LastEventSequence,
		"pendingCount":      pendingCount,
		"registeredAt":      registration.RegisteredAt.UTC().Format(time.RFC3339Nano),
		"updatedAt":         registration.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if registration.DeliverablePath != "" {
		out["deliverablePath"] = registration.DeliverablePath
	}
	if !registration.LastDeliveredAt.IsZero() {
		out["lastDeliveredAt"] = registration.LastDeliveredAt.UTC().Format(time.RFC3339Nano)
		out["lastDeliveredEnvelopeId"] = registration.LastDeliveredEnvelope
	}
	if !registration.LastNudgeAt.IsZero() {
		out["lastNudgeAt"] = registration.LastNudgeAt.UTC().Format(time.RFC3339Nano)
		out["lastNudgeEnvelopeId"] = registration.LastNudgeEnvelope
	}
	return out
}

func mapInt64(values map[string]any, key string) int64 {
	switch value := values[key].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	}
	return 0
}
