package agent

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionCallbackRecoveryWakeRunsImmediately(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	if _, _, err := store.register(testCallbackRegistration("source-recovery-wake", "target-recovery-wake", "task-recovery-wake", 1)); err != nil {
		t.Fatal(err)
	}
	reads := make(chan struct{}, 4)
	dispatcher := newSessionCallbackDispatcher(store, nil, func(string) bool { return false }, nil, nil)
	dispatcher.recoverStatus = func(context.Context, string, int64) error {
		reads <- struct{}{}
		return nil
	}
	dispatcher.recoveryState = func() (bool, uint64) { return true, 0 }
	dispatcher.start()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := dispatcher.close(ctx); err != nil {
			t.Fatal(err)
		}
	}()

	select {
	case <-reads: // bounded startup catch-up
	case <-time.After(time.Second):
		t.Fatal("startup callback recovery did not run")
	}
	dispatcher.requestProviderRecovery()
	select {
	case <-reads:
	case <-time.After(time.Second):
		t.Fatal("explicit callback recovery wake waited for the 30-minute ticker")
	}
}

func TestSessionCallbackRecoveryBatchIsBoundedAndContinues(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	for i := 0; i < sessionCallbackRecoveryBatchLimit+4; i++ {
		registration := testCallbackRegistration(fmt.Sprintf("source-batch-%02d", i), "target-batch", fmt.Sprintf("task-batch-%02d", i), 1)
		if _, _, err := store.register(registration); err != nil {
			t.Fatal(err)
		}
	}
	var reads atomic.Int32
	dispatcher := newSessionCallbackDispatcher(store, nil, func(string) bool { return false }, nil, nil)
	dispatcher.recoverStatus = func(context.Context, string, int64) error {
		reads.Add(1)
		return nil
	}
	if !dispatcher.reconcileSubscriptionsWithRecovery(true) {
		t.Fatal("first bounded callback recovery batch failed")
	}
	if got := reads.Load(); got != sessionCallbackRecoveryBatchLimit {
		t.Fatalf("first callback recovery batch reads=%d want=%d", got, sessionCallbackRecoveryBatchLimit)
	}
	dispatcher.recoveryMu.Lock()
	more := dispatcher.recoveryBatchMore
	dispatcher.recoveryMu.Unlock()
	if !more {
		t.Fatal("bounded callback recovery did not advertise continuation")
	}
	if !dispatcher.reconcileSubscriptionsWithRecovery(true) {
		t.Fatal("second bounded callback recovery batch failed")
	}
	if got := reads.Load(); got != sessionCallbackRecoveryBatchLimit+4 {
		t.Fatalf("callback recovery total reads=%d want=%d", got, sessionCallbackRecoveryBatchLimit+4)
	}
	dispatcher.recoveryMu.Lock()
	more = dispatcher.recoveryBatchMore
	dispatcher.recoveryMu.Unlock()
	if more {
		t.Fatal("callback recovery continuation remained after final batch")
	}
}

func TestSessionCallbackSubscriptionSetupRaceReleasesStaleGeneration(t *testing.T) {
	store := newSessionCallbackStore(t.TempDir())
	if _, _, err := store.register(testCallbackRegistration("source-subscription-race", "target-subscription-race", "task-subscription-race", 3)); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	allowReady := make(chan struct{})
	released := make(chan string, 1)
	dispatcher := newSessionCallbackDispatcher(store, nil, func(string) bool { return false }, nil, func(context.Context, string, int64) error {
		close(started)
		<-allowReady
		return nil
	})
	dispatcher.release = func(source string, generation int64) {
		released <- fmt.Sprintf("%s/%d", source, generation)
	}
	done := make(chan bool, 1)
	go func() { done <- dispatcher.reconcileSubscriptionsWithRecovery(false) }()
	<-started

	unregisterDone := make(chan error, 1)
	go func() {
		removed, err := store.unregister("source-subscription-race", 3)
		if err == nil && !removed {
			err = fmt.Errorf("registration was not removed")
		}
		unregisterDone <- err
	}()
	select {
	case err := <-unregisterDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("unregister blocked behind realtime subscription readiness")
	}
	close(allowReady)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("subscription reconciliation failed")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription reconciliation did not finish")
	}
	select {
	case got := <-released:
		if got != "source-subscription-race/3" {
			t.Fatalf("released stale watcher=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("stale generation watcher was not released")
	}
}
