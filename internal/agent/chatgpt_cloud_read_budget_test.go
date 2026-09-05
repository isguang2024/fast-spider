package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestChatGPTCloudProviderReadBudgetCoalescesAndClones(t *testing.T) {
	budget := newChatGPTCloudProviderReadBudget(nil)
	budget.minInterval = 0
	var calls atomic.Int32
	start := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	direct := func(context.Context, string) (map[string]any, error) {
		calls.Add(1)
		startedOnce.Do(func() { close(start) })
		<-release
		return map[string]any{"mapping": map[string]any{"node": map[string]any{"text": "value"}}}, nil
	}

	const readers = 8
	results := make(chan map[string]any, readers)
	errs := make(chan error, readers)
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			detail, err := budget.read(context.Background(), "conversation", 0, direct)
			results <- detail
			errs <- err
		}()
	}
	<-start
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("direct reads = %d, want 1", got)
	}
	first := <-results
	first["mutated"] = true
	second := <-results
	if _, ok := second["mutated"]; ok {
		t.Fatal("read results share the cached map")
	}
}

func TestChatGPTCloudProviderReadBudgetCacheAndInvalidation(t *testing.T) {
	budget := newChatGPTCloudProviderReadBudget(nil)
	budget.minInterval = 0
	var calls atomic.Int32
	direct := func(context.Context, string) (map[string]any, error) {
		return map[string]any{"call": calls.Add(1)}, nil
	}
	if _, err := budget.read(context.Background(), "conversation", time.Second, direct); err != nil {
		t.Fatal(err)
	}
	if _, err := budget.read(context.Background(), "conversation", time.Second, direct); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("cached direct reads = %d, want 1", got)
	}
	budget.invalidate("conversation")
	if _, err := budget.read(context.Background(), "conversation", time.Second, direct); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("post-invalidation direct reads = %d, want 2", got)
	}
}

func TestChatGPTCloudProviderReadBudgetCancelledWaiterDoesNotLeak(t *testing.T) {
	budget := newChatGPTCloudProviderReadBudget(nil)
	budget.minInterval = 0
	started := make(chan struct{})
	release := make(chan struct{})
	direct := func(context.Context, string) (map[string]any, error) {
		close(started)
		<-release
		return map[string]any{"ok": true}, nil
	}

	leaderDone := make(chan error, 1)
	go func() {
		_, err := budget.read(context.Background(), "conversation", 0, direct)
		leaderDone <- err
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if _, err := budget.read(ctx, "conversation", 0, direct); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter error = %v", err)
	}
	close(release)
	if err := <-leaderDone; err != nil {
		t.Fatal(err)
	}
	if _, err := budget.read(context.Background(), "conversation", time.Second, direct); err != nil {
		t.Fatal(err)
	}
}

func TestChatGPTCloudRetryAfterDuration(t *testing.T) {
	now := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	if got := chatGPTCloudRetryAfterDuration("120", now); got != 120*time.Second {
		t.Fatalf("seconds retry-after = %s", got)
	}
	if got := chatGPTCloudRetryAfterDuration("", now); got != 0 {
		t.Fatalf("empty retry-after = %s", got)
	}
	if got := chatGPTCloudRetryAfterDuration("invalid", now); got != 0 {
		t.Fatalf("invalid retry-after = %s", got)
	}
}

func TestChatGPTCloudAdapterReadCoalescesConcurrentFreshReads(t *testing.T) {
	var requests atomic.Int32
	firstStarted := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(firstStarted)
			<-release
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"conversation_id": "public-read",
			"mapping":         map[string]any{},
		})
	}))
	defer server.Close()

	adapter := NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
	defer adapter.Close(context.Background())
	adapter.baseURL, adapter.http = server.URL, server.Client()

	const readers = 8
	results := make(chan error, readers)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := adapter.Read(context.Background(), "public-read")
		results <- err
	}()
	<-firstStarted
	for i := 1; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := adapter.Read(context.Background(), "public-read")
			results <- err
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("provider reads = %d, want 1", got)
	}
}

func TestChatGPTCloudAdapterReadCooldownFailsImmediately(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	adapter := NewChatGPTCloudAdapter(nil, func(context.Context) (string, error) { return "token", nil })
	defer adapter.Close(context.Background())
	adapter.baseURL, adapter.http = server.URL, server.Client()
	if _, err := adapter.Read(context.Background(), "cooldown-read"); err == nil {
		t.Fatal("first rate-limited read unexpectedly succeeded")
	}

	started := time.Now()
	_, err := adapter.Read(context.Background(), "cooldown-read")
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("cooldown read waited %s", elapsed)
	}
	var capability interface {
		CapabilityError() (string, string, bool)
	}
	if !errors.As(err, &capability) {
		t.Fatalf("cooldown error does not preserve capability classification: %v", err)
	}
	code, message, retryable := capability.CapabilityError()
	if code != "CHATGPT_CLOUD_RATE_LIMITED" || !retryable || !strings.Contains(message, "Retry-After") {
		t.Fatalf("unexpected cooldown error: %s %s %v", code, message, retryable)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("provider reads during cooldown = %d, want 1", got)
	}
}
