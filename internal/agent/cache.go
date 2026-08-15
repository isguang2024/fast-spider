package agent

import (
	"sync"
	"time"
)

const (
	cliProbeTTL = 45 * time.Second
	modelsTTL   = 20 * time.Second
)

type versionProbe struct {
	version string
	err     error
}

type cacheEntry[T any] struct {
	value   T
	expires time.Time
}
type ttlCache[T any] struct {
	mu      sync.Mutex
	entries map[string]cacheEntry[T]
	ttl     time.Duration
	maxKeys int
	now     func() time.Time
	clone   func(T) T
}

func newTTLCache[T any](ttl time.Duration, maxKeys int, clone func(T) T) *ttlCache[T] {
	if maxKeys <= 0 {
		maxKeys = 1
	}
	return &ttlCache[T]{entries: make(map[string]cacheEntry[T]), ttl: ttl, maxKeys: maxKeys, now: time.Now, clone: clone}
}

func (c *ttlCache[T]) get(key string) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !c.now().Before(entry.expires) {
		delete(c.entries, key)
		var zero T
		return zero, false
	}
	if c.clone != nil {
		return c.clone(entry.value), true
	}
	return entry.value, true
}

func (c *ttlCache[T]) set(key string, value T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.maxKeys {
		var oldestKey string
		var oldest time.Time
		for itemKey, entry := range c.entries {
			if oldestKey == "" || entry.expires.Before(oldest) {
				oldestKey, oldest = itemKey, entry.expires
			}
		}
		delete(c.entries, oldestKey)
	}
	if c.clone != nil {
		value = c.clone(value)
	}
	c.entries[key] = cacheEntry[T]{value: value, expires: c.now().Add(c.ttl)}
}

func (c *ttlCache[T]) clear() { c.mu.Lock(); c.entries = make(map[string]cacheEntry[T]); c.mu.Unlock() }

func cloneAgentMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = cloneAgentValue(item)
	}
	return out
}

func cloneAgentValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAgentMap(typed)
	case []map[string]any:
		items := make([]map[string]any, len(typed))
		for index := range typed {
			items[index] = cloneAgentMap(typed[index])
		}
		return items
	case []any:
		items := make([]any, len(typed))
		for index := range typed {
			items[index] = cloneAgentValue(typed[index])
		}
		return items
	default:
		return value
	}
}
