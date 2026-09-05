package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCompletionAckFinalizesGenerationButRetainsRouteMetadata(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	registration := testCallbackRegistration("source-final", "target-final", "task-final", 4)
	if _, _, err := store.register(registration); err != nil {
		t.Fatal(err)
	}
	event := testCallbackEvent("source-final", 1)
	event.EventType = "hub-completion-notify"
	if queued, err := store.enqueue(event); err != nil || !queued {
		t.Fatalf("formal completion queued=%v err=%v", queued, err)
	}
	if count, err := store.acknowledgeCompletion(registration, time.Now().UTC()); err != nil || count != 1 {
		t.Fatalf("completion ack count=%d err=%v", count, err)
	}
	current, ok, err := store.registrationFor("source-final")
	if err != nil || !ok || current.Generation != registration.Generation || current.CompletionAckedAt.IsZero() {
		t.Fatalf("finalized route=%#v ok=%v err=%v", current, ok, err)
	}
	if callbackRegistrationProviderActive(current) {
		t.Fatal("completion ACK left the provider route active")
	}
	if allowed, err := store.providerRecoveryAllowed("source-final", registration.Generation); err != nil || allowed {
		t.Fatalf("provider recovery allowed=%v err=%v after completion ACK", allowed, err)
	}
	if queued, err := store.enqueue(testCallbackEvent("source-final", 2)); err != nil || queued {
		t.Fatalf("post-ACK provider event queued=%v err=%v", queued, err)
	}
	if _, _, err := store.arm("source-final", registration.Generation, registration); err == nil || !strings.Contains(err.Error(), "finalized") {
		t.Fatalf("post-ACK arm err=%v", err)
	}
}

func TestCompletionAckFinalizesExactOwnerWhenNodePendingWasLost(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	registration := testCallbackRegistration("source-lost", "target-lost", "task-lost", 9)
	if _, _, err := store.register(registration); err != nil {
		t.Fatal(err)
	}
	// The Hub's formal completion acknowledgement is authoritative even when
	// the Node's durable enqueue was interrupted before a pending event existed.
	if count, err := store.acknowledgeCompletion(registration, time.Now().UTC()); err != nil || count != 0 {
		t.Fatalf("lost-pending completion ack count=%d err=%v", count, err)
	}
	current, ok, err := store.registrationFor(registration.SourceSessionID)
	if err != nil || !ok || current.CompletionAckedAt.IsZero() || callbackRegistrationProviderActive(current) {
		t.Fatalf("lost-pending ACK did not finalize exact owner: route=%#v ok=%v err=%v", current, ok, err)
	}
}

func TestLegacyFormalCompletionAckRestoresTerminalPredicateOnReload(t *testing.T) {
	dir := t.TempDir()
	store := newSessionCallbackStore(dir)
	registration := testCallbackRegistration("source-legacy-final", "target-legacy-final", "task-legacy-final", 6)
	if _, _, err := store.register(registration); err != nil {
		t.Fatal(err)
	}
	ackedAt := time.Date(2026, 9, 6, 4, 5, 6, 0, time.UTC)
	store.mu.Lock()
	legacy := store.registrations[registration.SourceSessionID]
	legacy.LastDeliveredEnvelope = callbackFormalCompletionKey(legacy)
	legacy.LastDeliveredAt = ackedAt
	legacy.CompletionAckedAt = time.Time{}
	store.registrations[registration.SourceSessionID] = legacy
	if _, err := store.saveLocked(); err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	store.mu.Unlock()

	reloaded := newSessionCallbackStore(dir)
	current, ok, err := reloaded.registrationFor(registration.SourceSessionID)
	if err != nil || !ok || !current.CompletionAckedAt.Equal(ackedAt) || callbackRegistrationProviderActive(current) {
		t.Fatalf("legacy formal ACK migration route=%#v ok=%v err=%v", current, ok, err)
	}
	called := false
	active, err := reloaded.withCurrentRegistration(registration.SourceSessionID, registration.Generation, func() error {
		called = true
		return nil
	})
	if err != nil || active || called {
		t.Fatalf("legacy finalized route allowed watcher ensure active=%v called=%v err=%v", active, called, err)
	}
}

func TestLegacyRecoveryEnvelopeDoesNotFinalizeRouteOnReload(t *testing.T) {
	dir := t.TempDir()
	store := newSessionCallbackStore(dir)
	registration := testCallbackRegistration("source-legacy-recovery", "target-legacy-recovery", "task-legacy-recovery", 7)
	if _, _, err := store.register(registration); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	legacy := store.registrations[registration.SourceSessionID]
	legacy.LastDeliveredEnvelope = "cb_recovery_claim"
	legacy.LastDeliveredAt = time.Date(2026, 9, 6, 4, 5, 6, 0, time.UTC)
	store.registrations[registration.SourceSessionID] = legacy
	if _, err := store.saveLocked(); err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	store.mu.Unlock()

	reloaded := newSessionCallbackStore(dir)
	current, ok, err := reloaded.registrationFor(registration.SourceSessionID)
	if err != nil || !ok || !current.CompletionAckedAt.IsZero() || !callbackRegistrationProviderActive(current) {
		t.Fatalf("recovery envelope incorrectly finalized route=%#v ok=%v err=%v", current, ok, err)
	}
}

func TestClaimAckDoesNotFinalizeUnfinishedGeneration(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	registration := testCallbackRegistration("source-claim", "target-claim", "task-claim", 2)
	if _, _, err := store.register(registration); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.enqueue(testCallbackEvent("source-claim", 1)); err != nil || !queued {
		t.Fatalf("recovery event queued=%v err=%v", queued, err)
	}
	claimID, _, err := store.claim(registration.TargetSessionID, "claim-unfinished", 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.acknowledgeClaim(registration.TargetSessionID, claimID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	current, ok, err := store.registrationFor(registration.SourceSessionID)
	if err != nil || !ok || !current.CompletionAckedAt.IsZero() || !callbackRegistrationProviderActive(current) {
		t.Fatalf("claim ACK finalized route=%#v ok=%v err=%v", current, ok, err)
	}
}

func TestProviderConfirmationDeduplicatesRouteGeneration(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	registration := testCallbackRegistration("source-confirm", "target-confirm", "task-confirm", 8)
	if _, _, err := store.register(registration); err != nil {
		t.Fatal(err)
	}
	started, err := store.beginProviderConfirmation(registration.SourceSessionID, registration.Generation)
	if err != nil || !started {
		t.Fatalf("first confirmation started=%v err=%v", started, err)
	}
	started, err = store.beginProviderConfirmation(registration.SourceSessionID, registration.Generation)
	if err != nil || started {
		t.Fatalf("duplicate confirmation started=%v err=%v", started, err)
	}
	store.endProviderConfirmation(registration.SourceSessionID, registration.Generation)
	started, err = store.beginProviderConfirmation(registration.SourceSessionID, registration.Generation)
	if err != nil || !started {
		t.Fatalf("new confirmation after settle started=%v err=%v", started, err)
	}
}

func TestFormalSubmissionBlocksProviderRecoveryBeforeCompletionAck(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	registration := testCallbackRegistration("source-formal", "target-formal", "task-formal", 3)
	if _, _, err := store.register(registration); err != nil {
		t.Fatal(err)
	}
	event := testCallbackEvent("source-formal", 1)
	event.EventType = "hub-completion-notify"
	if queued, err := store.enqueue(event); err != nil || !queued {
		t.Fatalf("formal event queued=%v err=%v", queued, err)
	}
	if allowed, err := store.providerRecoveryAllowed(registration.SourceSessionID, registration.Generation); err != nil || allowed {
		t.Fatalf("provider recovery allowed=%v err=%v with formal submission pending", allowed, err)
	}
}

func TestCallbackRegistrationMapExposesBaselinePresenceOnly(t *testing.T) {
	registration := testCallbackRegistration("source-map", "target-map", "task-map", 1)
	registration.BaselineIdentity = "private-message-identity"
	mapped := callbackRegistrationMap(registration, 0)
	if mapped["baselineSet"] != true {
		t.Fatalf("baseline presence=%#v", mapped["baselineSet"])
	}
	if _, exposed := mapped["baselineIdentity"]; exposed {
		t.Fatalf("callback map exposed private baseline identity: %#v", mapped)
	}
}

func TestRecoveryBatchStopsAfterRateLimit(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	for _, source := range []string{"source-rate-a", "source-rate-b"} {
		registration := testCallbackRegistration(source, "target-rate", source, 1)
		if _, _, err := store.register(registration); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	dispatcher := newSessionCallbackDispatcher(store, nil, nil, nil, nil)
	dispatcher.recoverStatus = func(context.Context, string, int64) error {
		calls++
		return fmt.Errorf("ChatGPT Cloud read conversation returned HTTP 429")
	}
	if recovered := dispatcher.reconcileSubscriptionsWithRecovery(true); recovered {
		t.Fatal("rate-limited recovery batch was reported successful")
	}
	if calls != 1 {
		t.Fatalf("rate-limited recovery continued across routes: calls=%d", calls)
	}
}
