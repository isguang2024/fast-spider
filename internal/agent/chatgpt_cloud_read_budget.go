package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	chatgptCloudReadMinInterval      = 1 * time.Second
	chatgptCloudProviderReadCacheTTL = 5 * time.Second
	chatgptCloudReadDefaultCooldown  = 30 * time.Second
	chatgptCloudReadRateLimitFloor   = 1 * time.Second
	chatgptCloudReadOperationTimeout = 20 * time.Second
)

type chatGPTCloudReadSourceContextKey struct{}

// withChatGPTCloudReadSource annotates a local read entry point for safe
// diagnostics. Only the short source label is logged; tokens and content are
// never carried in this context or emitted by the budget.
func withChatGPTCloudReadSource(ctx context.Context, source string) context.Context {
	source = strings.TrimSpace(source)
	if source == "" {
		source = "conversation_read"
	}
	return context.WithValue(ctx, chatGPTCloudReadSourceContextKey{}, source)
}

func chatGPTCloudReadSource(ctx context.Context) string {
	if source, ok := ctx.Value(chatGPTCloudReadSourceContextKey{}).(string); ok && strings.TrimSpace(source) != "" {
		return strings.TrimSpace(source)
	}
	return "conversation_read"
}

type chatGPTCloudProviderReadCacheEntry struct {
	detail map[string]any
	readAt time.Time
	epoch  uint64
}

type chatGPTCloudProviderReadCall struct {
	done   chan struct{}
	detail map[string]any
	err    error
	epoch  uint64
}

// chatGPTCloudProviderReadBudget owns all conversation-detail reads for one adapter.
// The pacing and cooldown are adapter-wide because the provider quota is
// account-wide, while cache and singleflight keys remain conversation-scoped.
type chatGPTCloudProviderReadBudget struct {
	mu               sync.Mutex
	logger           *slog.Logger
	cache            map[string]chatGPTCloudProviderReadCacheEntry
	active           map[string]*chatGPTCloudProviderReadCall
	epoch            map[string]uint64
	nextRequestAt    time.Time
	cooldownUntil    time.Time
	now              func() time.Time
	minInterval      time.Duration
	defaultCooldown  time.Duration
	operationTimeout time.Duration
}

func newChatGPTCloudProviderReadBudget(logger *slog.Logger) *chatGPTCloudProviderReadBudget {
	if logger == nil {
		logger = slog.Default()
	}
	return &chatGPTCloudProviderReadBudget{
		logger:           logger,
		cache:            make(map[string]chatGPTCloudProviderReadCacheEntry),
		active:           make(map[string]*chatGPTCloudProviderReadCall),
		epoch:            make(map[string]uint64),
		now:              time.Now,
		minInterval:      chatgptCloudReadMinInterval,
		defaultCooldown:  chatgptCloudReadDefaultCooldown,
		operationTimeout: chatgptCloudReadOperationTimeout,
	}
}

func (b *chatGPTCloudProviderReadBudget) read(ctx context.Context, conversationID string, maxAge time.Duration, direct func(context.Context, string) (map[string]any, error)) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, fmt.Errorf("conversationId is required")
	}

	b.mu.Lock()
	now := b.now()
	if maxAge > 0 {
		if cached, ok := b.cache[conversationID]; ok && now.Sub(cached.readAt) <= maxAge {
			detail := cloneChatGPTCloudDetail(cached.detail)
			b.logRead(chatGPTCloudReadSource(ctx), 200, 0, true, false)
			b.mu.Unlock()
			return detail, nil
		}
	}
	if active := b.active[conversationID]; active != nil {
		b.mu.Unlock()
		started := time.Now()
		detail, err := b.wait(ctx, active)
		b.logRead(chatGPTCloudReadSource(ctx), chatGPTCloudReadStatus(err), time.Since(started), false, true)
		return detail, err
	}
	call := &chatGPTCloudProviderReadCall{done: make(chan struct{}), epoch: b.epoch[conversationID]}
	b.active[conversationID] = call
	b.mu.Unlock()

	// Provider work is detached from the initiating caller. This lets a
	// cancelled waiter leave promptly without cancelling a shared read needed by
	// another waiter, while the bounded operation context guarantees cleanup.
	go b.execute(call, conversationID, chatGPTCloudReadSource(ctx), direct)
	return b.wait(ctx, call)
}

func (b *chatGPTCloudProviderReadBudget) wait(ctx context.Context, call *chatGPTCloudProviderReadCall) (map[string]any, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-call.done:
		if call.err != nil {
			return nil, call.err
		}
		return cloneChatGPTCloudDetail(call.detail), nil
	}
}

func (b *chatGPTCloudProviderReadBudget) execute(call *chatGPTCloudProviderReadCall, conversationID, source string, direct func(context.Context, string) (map[string]any, error)) {
	opCtx, cancel := context.WithTimeout(context.Background(), b.operationTimeout)
	defer cancel()
	started := time.Now()
	err := b.acquire(opCtx)
	var detail map[string]any
	if err == nil {
		detail, err = direct(opCtx, conversationID)
	}
	if err == nil {
		detail = cloneChatGPTCloudDetail(detail)
	}

	b.mu.Lock()
	call.detail, call.err = detail, err
	if err == nil && b.epoch[conversationID] == call.epoch {
		b.cache[conversationID] = chatGPTCloudProviderReadCacheEntry{detail: detail, readAt: b.now(), epoch: call.epoch}
		b.pruneCacheLocked(b.now())
	}
	if b.active[conversationID] == call {
		delete(b.active, conversationID)
	}
	close(call.done)
	b.mu.Unlock()
	b.logRead(source, chatGPTCloudReadStatus(err), time.Since(started), false, false)
}

func (b *chatGPTCloudProviderReadBudget) acquire(ctx context.Context) error {
	for {
		b.mu.Lock()
		now := b.now()
		waitFor := time.Duration(0)
		if b.cooldownUntil.After(now) {
			remaining := b.cooldownUntil.Sub(now)
			b.mu.Unlock()
			return chatGPTCloudReadRateLimitError(remaining)
		} else if b.nextRequestAt.After(now) {
			waitFor = b.nextRequestAt.Sub(now)
		}
		if waitFor == 0 {
			b.nextRequestAt = now.Add(b.minInterval)
			b.mu.Unlock()
			return nil
		}
		b.mu.Unlock()
		timer := time.NewTimer(waitFor)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (b *chatGPTCloudProviderReadBudget) noteRateLimit(retryAfter string) {
	b.mu.Lock()
	now := b.now()
	cooldown := chatGPTCloudRetryAfterDuration(retryAfter, now)
	trimmedRetryAfter := strings.TrimSpace(retryAfter)
	if cooldown <= 0 && trimmedRetryAfter != "0" {
		cooldown = b.defaultCooldown
	}
	if cooldown < chatgptCloudReadRateLimitFloor {
		cooldown = chatgptCloudReadRateLimitFloor
	}
	until := now.Add(cooldown)
	if until.After(b.cooldownUntil) {
		b.cooldownUntil = until
	}
	b.mu.Unlock()
}

func (b *chatGPTCloudProviderReadBudget) invalidate(conversationID string) {
	if conversationID == "" {
		return
	}
	b.mu.Lock()
	delete(b.cache, conversationID)
	b.epoch[conversationID]++
	// Existing waiters keep their call handle and can finish normally, while a
	// post-invalidation caller must be allowed to start a fresh provider read.
	// execute only removes the active entry when it still owns that entry.
	delete(b.active, conversationID)
	b.mu.Unlock()
}

func (b *chatGPTCloudProviderReadBudget) pruneCacheLocked(now time.Time) {
	for conversationID, cached := range b.cache {
		if now.Sub(cached.readAt) > chatgptCloudReadCacheTTL {
			delete(b.cache, conversationID)
		}
	}
}

func (b *chatGPTCloudProviderReadBudget) logRead(source string, status int, duration time.Duration, cacheHit, joined bool) {
	if b.logger == nil {
		return
	}
	level := slog.LevelDebug
	if !cacheHit && !joined {
		level = slog.LevelInfo
	}
	if status == http.StatusTooManyRequests {
		level = slog.LevelWarn
	}
	b.logger.Log(context.Background(), level, "chatgpt cloud conversation read", "source", source, "status", status, "duration_ms", duration.Milliseconds(), "cache_hit", cacheHit, "joined", joined)
}

func chatGPTCloudReadStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var cloudErr *chatGPTCloudHTTPError
	if errors.As(err, &cloudErr) {
		return cloudErr.status
	}
	return 0
}

func chatGPTCloudReadRateLimitError(remaining time.Duration) error {
	if remaining < time.Second {
		remaining = time.Second
	}
	seconds := remaining / time.Second
	if remaining%time.Second != 0 {
		seconds++
	}
	return &chatGPTCloudHTTPError{operation: "read conversation", status: http.StatusTooManyRequests, retryAfter: strconv.FormatInt(int64(seconds), 10) + " seconds"}
}

func chatGPTCloudRetryAfterDuration(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		if seconds > int64((time.Duration(1<<63-1))/time.Second) {
			return time.Duration(1<<63 - 1)
		}
		return time.Duration(seconds) * time.Second
	}
	if deadline, err := http.ParseTime(value); err == nil {
		if deadline.After(now) {
			return deadline.Sub(now)
		}
		return 0
	}
	return 0
}

func cloneChatGPTCloudDetail(detail map[string]any) map[string]any {
	if detail == nil {
		return nil
	}
	value := cloneChatGPTCloudValue(detail)
	cloned, _ := value.(map[string]any)
	return cloned
}

func cloneChatGPTCloudValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneChatGPTCloudValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneChatGPTCloudValue(item)
		}
		return out
	default:
		return value
	}
}
