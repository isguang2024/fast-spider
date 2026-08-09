package server

import (
	"sync"
	"time"
)

const (
	loginFailureWindow     = 15 * time.Minute
	loginFailureLimit      = 10
	loginFailureMaxEntries = 4096
)

type loginFailureEntry struct {
	count       int
	windowStart time.Time
}

type loginFailureLimiter struct {
	mu      sync.Mutex
	entries map[string]loginFailureEntry
}

func newLoginFailureLimiter() *loginFailureLimiter {
	return &loginFailureLimiter{entries: make(map[string]loginFailureEntry)}
}

func (l *loginFailureLimiter) blocked(key string, now time.Time) bool {
	if key == "" {
		key = "unknown"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneExpiredLocked(now)
	entry, ok := l.entries[key]
	if !ok {
		return false
	}
	if now.Sub(entry.windowStart) >= loginFailureWindow {
		delete(l.entries, key)
		return false
	}
	return entry.count >= loginFailureLimit
}

func (l *loginFailureLimiter) failure(key string, now time.Time) {
	if key == "" {
		key = "unknown"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneExpiredLocked(now)
	entry, ok := l.entries[key]
	if !ok {
		if len(l.entries) >= loginFailureMaxEntries {
			return
		}
		l.entries[key] = loginFailureEntry{count: 1, windowStart: now}
		return
	}
	entry.count++
	l.entries[key] = entry
}

func (l *loginFailureLimiter) pruneExpiredLocked(now time.Time) {
	for key, entry := range l.entries {
		if now.Sub(entry.windowStart) >= loginFailureWindow {
			delete(l.entries, key)
		}
	}
}

func (l *loginFailureLimiter) success(key string) {
	if key == "" {
		key = "unknown"
	}
	l.mu.Lock()
	delete(l.entries, key)
	l.mu.Unlock()
}
