package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCallbackRetryPersistsAndDoesNotBlockAnotherTarget(t *testing.T) {
	dir := t.TempDir()
	store := newSessionCallbackStore(dir)
	for _, target := range []string{"a-failing", "b-healthy"} {
		if _, _, err := store.register(testCallbackRegistration(target, target, target, 1)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.enqueue(testCallbackEvent(target, 1)); err != nil {
			t.Fatal(err)
		}
	}
	sends := map[string]int{}
	send := func(_ context.Context, target, _ string) (sessionCallbackDeliveryResult, error) {
		sends[target]++
		if target == "a-failing" {
			return sessionCallbackDeliveryResult{}, errors.New("backend unavailable")
		}
		return testAppServerCallbackDelivery(), nil
	}
	d := newSessionCallbackDispatcher(store, nil, nil, send, nil)
	d.dispatchOnce()
	if sends["a-failing"] != 1 || sends["b-healthy"] != 1 {
		t.Fatalf("sends=%v", sends)
	}
	store = newSessionCallbackStore(dir)
	items, _, err := store.registrationsSnapshot("a-failing", "")
	if err != nil || len(items) != 1 || items[0].NudgeFailureCount != 1 || !items[0].NudgeRetryAt.After(time.Now()) {
		t.Fatalf("persisted=%v err=%v", items, err)
	}
	d = newSessionCallbackDispatcher(store, nil, nil, send, nil)
	for i := 0; i < 3; i++ {
		d.dispatchOnce()
	}
	if sends["a-failing"] != 1 || sends["b-healthy"] != 1 {
		t.Fatalf("signal bypassed deadline: %v", sends)
	}
	if _, _, err := store.register(testCallbackRegistration("a-failing", "a-failing", "a-failing", 2)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.enqueue(testCallbackEvent("a-failing", 2)); err != nil {
		t.Fatal(err)
	}
	d.dispatchOnce()
	items, _, err = store.registrationsSnapshot("a-failing", "")
	if err != nil || sends["a-failing"] != 2 || items[0].NudgeFailureCount != 1 {
		t.Fatalf("new generation inherited retry: sends=%v items=%v err=%v", sends, items, err)
	}
}

func TestCallbackRetryBackoffCapSuccessAndSaveFailure(t *testing.T) {
	dir := t.TempDir()
	store := newSessionCallbackStore(dir)
	if _, _, err := store.register(testCallbackRegistration("source", "target", "task", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.enqueue(testCallbackEvent("source", 1)); err != nil {
		t.Fatal(err)
	}
	grouped, err := store.pendingByTarget()
	if err != nil {
		t.Fatal(err)
	}
	envelope := sessionCallbackEnvelopeID("target", grouped["target"])
	now := time.Now().UTC()
	class := classifyExecutionError(errors.New("backend unavailable"))
	for i, delay := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 10 * time.Minute, 10 * time.Minute} {
		next, err := store.recordNudgeFailure("target", envelope, class, now, 30*time.Second)
		if err != nil || !next.Equal(now.Add(delay)) {
			t.Fatalf("attempt %d next=%s err=%v", i, next, err)
		}
	}
	old := store.registrations["source"]
	store.beforeCommitSaveOverride = func() error { return errors.New("disk failure") }
	if _, err := store.recordNudgeFailure("target", envelope, class, now, 30*time.Second); err == nil {
		t.Fatal("save unexpectedly succeeded")
	}
	if got := store.registrations["source"]; got.NudgeFailureCount != old.NudgeFailureCount || !got.NudgeRetryAt.Equal(old.NudgeRetryAt) {
		t.Fatal("failed save changed retry state")
	}
	store.beforeCommitSaveOverride = nil
	if next, err := store.recordNudgeFailure("target", "cb-stale", class, now, 30*time.Second); err != nil || !next.IsZero() {
		t.Fatalf("stale envelope recorded: %s %v", next, err)
	}
	if err := store.recordNudge("target", envelope, testAppServerCallbackDelivery(), now); err != nil {
		t.Fatal(err)
	}
	store = newSessionCallbackStore(dir)
	items, _, err := store.registrationsSnapshot("source", "")
	if err != nil || len(items) != 1 || items[0].NudgeFailureCount != 0 || !items[0].NudgeRetryAt.IsZero() || items[0].NudgeFailureEnvelope != "" {
		t.Fatalf("success did not clear retry: %v %v", items, err)
	}
}
