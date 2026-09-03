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

const sessionCallbackRecoveryInterval = 5 * time.Minute

type sessionCallbackDispatcher struct {
	store  *sessionCallbackStore
	logger *slog.Logger
	active func(string) bool
	send   func(context.Context, string, string) error
	ensure func(context.Context, string, int64) error

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
		notify: make(chan struct{}, 1), rootCtx: rootCtx, cancel: cancel, interval: sessionCallbackRecoveryInterval,
	}
}

func (d *sessionCallbackDispatcher) start() {
	if d == nil {
		return
	}
	d.startOnce.Do(func() {
		d.wg.Add(1)
		go d.run()
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

func (d *sessionCallbackDispatcher) run() {
	defer d.wg.Done()
	interval := d.interval
	if interval <= 0 {
		interval = sessionCallbackRecoveryInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	d.reconcileSubscriptions()
	d.dispatchOnce()
	for {
		select {
		case <-d.rootCtx.Done():
			return
		case <-d.notify:
			d.dispatchOnce()
		case <-ticker.C:
			d.reconcileSubscriptions()
			d.dispatchOnce()
		}
	}
}

func (d *sessionCallbackDispatcher) reconcileSubscriptions() {
	if d == nil || d.store == nil || d.ensure == nil {
		return
	}
	registrations, _, err := d.store.registrationsSnapshot("", "")
	if err != nil {
		d.logger.Warn("load session callbacks for realtime recovery", "error", err)
		return
	}
	for _, registration := range registrations {
		ctx, cancel := context.WithTimeout(d.rootCtx, 10*time.Second)
		_, err := d.store.withCurrentRegistration(registration.SourceSessionID, registration.Generation, func() error {
			return d.ensure(ctx, registration.SourceSessionID, registration.Generation)
		})
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			d.logger.Warn("restore ChatGPT Cloud callback subscription", "sourceSessionId", registration.SourceSessionID, "error", err)
		}
	}
}

func (d *sessionCallbackDispatcher) dispatchOnce() {
	if d == nil || d.store == nil || d.send == nil {
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
		envelopeID := sessionCallbackEnvelopeID(target, events)
		prompt := buildSessionCallbackEnvelope(envelopeID, events)
		ctx, cancel := context.WithTimeout(d.rootCtx, 2*time.Minute)
		err := d.send(ctx, target, prompt)
		cancel()
		if errors.Is(err, node.ErrAgentSessionBusy) {
			continue
		}
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				d.logger.Warn("deliver session callback envelope", "targetSessionId", target, "envelopeId", envelopeID, "error", err)
			}
			continue
		}
		if err := d.store.acknowledge(target, envelopeID, events, time.Now().UTC()); err != nil {
			d.logger.Warn("acknowledge delivered session callback envelope", "targetSessionId", target, "envelopeId", envelopeID, "error", err)
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
	builder.WriteString("This is a fixed Fast Spider control envelope, not Cloud CHAT-authored content. Coalesce every listed event in this one dispatcher turn. When mission identifies a Codex cloud collaboration, call codex_cloud_collaboration action=event.ingest with the listed result or deliverable metadata, then action=event.ack after validating the referenced local file or Result; the acknowledgement advances completed tasks, goals, and collaboration closure automatically. Do not reread full CHAT history or repeat task.update, goal.update, or close for that completion. Otherwise use the bounded session result flow. Continue the original Cloud CHAT, integrate verified evidence, or record a true hard blocker. Do not copy CHAT history into the controller. Duplicate ENVELOPE_ID values must be treated as already delivered.\n")
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
		"callback":       callbackRegistrationMap(registration, 0),
		"replayed":       replayed,
		"deliveryPolicy": "callback-first",
		"recoveryPolicy": "heartbeat-fallback",
	}, nil
}

func (m *AgentManager) reconcileCompletedCloudCallback(sourceSessionID string, generation int64) {
	if m == nil || m.callbackStore == nil || m.chatgptCloud == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	detail, err := m.chatgptCloud.Read(ctx, sourceSessionID)
	if err != nil && !errors.Is(err, context.Canceled) {
		m.logger.Warn("reconcile completed ChatGPT Cloud callback after registration", "sourceSessionId", sourceSessionID, "error", err)
		return
	}
	if err != nil || chatgptCloudConversationStatus(detail) != "completed" {
		return
	}
	registration, current, storeErr := m.callbackStore.registrationFor(sourceSessionID)
	if storeErr != nil || !current || registration.Generation != generation {
		return
	}
	sum := sha256.Sum256([]byte(sourceSessionID + "\x00register-reconcile\x00" + fmt.Sprintf("%d", generation)))
	m.handleChatGPTCloudCallbackEvent(chatgptCloudEvent{
		Sequence:       m.callbackStore.maxEventSequence() + 1,
		EventKey:       "provider_evt_reconcile_" + hex.EncodeToString(sum[:])[:40],
		Type:           "conversation.turn.complete",
		ConversationID: sourceSessionID,
		EventType:      "conversation-turn-complete",
		Timestamp:      time.Now().UTC(),
	})
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
	out := make([]map[string]any, 0, len(items))
	for _, registration := range items {
		out = append(out, callbackRegistrationMap(registration, pendingCounts[registration.SourceSessionID]))
	}
	return map[string]any{
		"callbacks":      out,
		"deliveryPolicy": "callback-first",
		"recoveryPolicy": "heartbeat-fallback",
	}, nil
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
