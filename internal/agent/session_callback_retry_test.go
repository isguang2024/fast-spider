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

func TestCollaborationResultSinkMustSucceedBeforeCallbackWakeAndReplaysAfterRestart(t *testing.T) {
	dir := t.TempDir()
	store := newSessionCallbackStore(dir)
	registration := testCallbackRegistration("sink-source", "sink-target", "sink-task", 7)
	registration.CallbackType = "text"
	registration.ImmediateWake = true
	if _, _, err := store.register(registration); err != nil {
		t.Fatal(err)
	}
	event := testCallbackEvent(registration.SourceSessionID, 1)
	event.CallbackType = registration.CallbackType
	event.CallbackOutcome = "failed"
	event.CallbackErrorCode = "CALLBACK_TEXT_REQUIRED"
	event.ResultText = "must not cross sink"
	if queued, err := store.enqueue(event); err != nil || !queued {
		t.Fatalf("queued=%v err=%v", queued, err)
	}

	var sinkCalls int
	var sendCalls int
	dispatcher := newSessionCallbackDispatcher(store, nil, nil, func(context.Context, string, string) (sessionCallbackDeliveryResult, error) {
		sendCalls++
		return testAppServerCallbackDelivery(), nil
	}, nil)
	dispatcher.setCollaborationResultSink(func(_ context.Context, metadata map[string]any) error {
		sinkCalls++
		if _, leaked := metadata["resultText"]; leaked {
			t.Fatal("callback text crossed result sink")
		}
		if metadata["sourceSessionId"] != registration.SourceSessionID || metadata["targetSessionId"] != registration.TargetSessionID || metadata["taskId"] != registration.TaskID || metadata["missionId"] != registration.MissionID || metadata["generation"] != registration.Generation || metadata["callbackOutcome"] != "failed" || metadata["callbackErrorCode"] != event.CallbackErrorCode {
			t.Fatalf("metadata=%#v", metadata)
		}
		return errors.New("task database unavailable")
	})
	dispatcher.dispatchOnce()
	if sendCalls != 0 || sinkCalls != 1 {
		t.Fatalf("failed sink still woke target: sends=%d sinkCalls=%d", sendCalls, sinkCalls)
	}
	registrations, _, err := store.registrationsSnapshot(registration.SourceSessionID, "")
	if err != nil || len(registrations) != 1 || registrations[0].NudgeFailureCount != 1 {
		t.Fatalf("sink failure retry state=%#v err=%v", registrations, err)
	}
	if pending, err := store.pendingSnapshot(registration.SourceSessionID, registration.TargetSessionID); err != nil || len(pending) != 1 {
		t.Fatalf("sink failure removed pending=%#v err=%v", pending, err)
	}

	dispatcher.setCollaborationResultSink(func(_ context.Context, metadata map[string]any) error {
		sinkCalls++
		if metadata["resultTextAvailable"] != true {
			t.Fatalf("metadata omitted bounded text presence=%#v", metadata)
		}
		return nil
	})
	store.mu.Lock()
	registrationState := store.registrations[registration.SourceSessionID]
	registrationState.NudgeRetryAt = time.Time{}
	store.registrations[registration.SourceSessionID] = registrationState
	store.mu.Unlock()
	dispatcher.dispatchOnce()
	if sendCalls != 1 || sinkCalls != 2 {
		t.Fatalf("successful retry did not wake target: sends=%d sinkCalls=%d", sendCalls, sinkCalls)
	}

	restartRegistration := testCallbackRegistration("sink-source-restart", "sink-target", "sink-task-restart", 8)
	restartRegistration.CallbackType = "text"
	restartRegistration.ImmediateWake = true
	if _, _, err := store.register(restartRegistration); err != nil {
		t.Fatal(err)
	}
	restartEvent := testCallbackEvent(restartRegistration.SourceSessionID, 1)
	restartEvent.CallbackType = restartRegistration.CallbackType
	restartEvent.CallbackOutcome = "completed"
	restartEvent.ResultText = "restart body stays out of sink metadata"
	if queued, err := store.enqueue(restartEvent); err != nil || !queued {
		t.Fatalf("restart queued=%v err=%v", queued, err)
	}
	dispatcher.setCollaborationResultSink(func(_ context.Context, _ map[string]any) error {
		sinkCalls++
		return errors.New("task database unavailable before restart")
	})
	dispatcher.dispatchOnce()
	if sendCalls != 1 {
		t.Fatalf("failed restart sink woke target: sends=%d", sendCalls)
	}

	restarted := newSessionCallbackStore(dir)
	restartedSinkCalls := 0
	restartedSendCalls := 0
	restartedDispatcher := newSessionCallbackDispatcher(restarted, nil, nil, func(context.Context, string, string) (sessionCallbackDeliveryResult, error) {
		restartedSendCalls++
		return testAppServerCallbackDelivery(), nil
	}, nil)
	restarted.mu.Lock()
	restartedState := restarted.registrations[restartRegistration.SourceSessionID]
	restartedState.NudgeRetryAt = time.Time{}
	restarted.registrations[restartRegistration.SourceSessionID] = restartedState
	if _, err := restarted.saveLocked(); err != nil {
		restarted.mu.Unlock()
		t.Fatal(err)
	}
	restarted.mu.Unlock()
	restartedDispatcher.setCollaborationResultSink(func(_ context.Context, metadata map[string]any) error {
		restartedSinkCalls++
		if metadata["eventKey"] == "" || metadata["callbackOutcome"] != "completed" {
			t.Fatalf("replayed metadata=%#v", metadata)
		}
		if metadata["resultText"] != nil {
			t.Fatalf("replayed metadata leaked result text=%#v", metadata)
		}
		return nil
	})
	// The second pending event survives the failed nudge until the business
	// callback ACK. A fresh dispatcher must replay it for the restarted task DB.
	restartedDispatcher.dispatchOnce()
	if restartedSinkCalls != 1 || restartedSendCalls != 1 {
		t.Fatalf("durable pending did not replay sink/wake after restart: sink=%d sends=%d", restartedSinkCalls, restartedSendCalls)
	}
}
