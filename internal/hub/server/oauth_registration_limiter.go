package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/store"
)

const (
	oauthRegistrationWindow     = time.Minute
	oauthRegistrationsPerWindow = 30
	oauthRegistrationMaxSources = 4096
	oauthMaxRegisteredClients   = 4096
	oauthMaxOrphanClients       = 1024
	oauthMaxSourceOrphanClients = 32
	oauthOrphanClientRetention  = 30 * time.Minute
)

var (
	errOAuthRegistrationLimited = errors.New("OAuth client registration rate limit exceeded")
	errOAuthClientQuota         = errors.New("OAuth client registration capacity is temporarily exhausted")
)

type oauthRegistrationWindowEntry struct {
	count       int
	windowStart time.Time
}

type oauthRegistrationGuard struct {
	mu               sync.Mutex
	entries          map[string]oauthRegistrationWindowEntry
	window           time.Duration
	perWindow        int
	maxSources       int
	maxClients       int
	maxOrphans       int
	maxSourceOrphans int
	orphanRetention  time.Duration
	nextSweep        time.Time
}

func newOAuthRegistrationGuard() *oauthRegistrationGuard {
	return &oauthRegistrationGuard{
		entries:          make(map[string]oauthRegistrationWindowEntry),
		window:           oauthRegistrationWindow,
		perWindow:        oauthRegistrationsPerWindow,
		maxSources:       oauthRegistrationMaxSources,
		maxClients:       oauthMaxRegisteredClients,
		maxOrphans:       oauthMaxOrphanClients,
		maxSourceOrphans: oauthMaxSourceOrphanClients,
		orphanRetention:  oauthOrphanClientRetention,
	}
}

func (g *oauthRegistrationGuard) allow(source string, now time.Time) bool {
	if g == nil {
		return false
	}
	if source == "" {
		source = "unknown"
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.nextSweep.IsZero() || !now.Before(g.nextSweep) {
		for key, entry := range g.entries {
			if now.Sub(entry.windowStart) >= g.window {
				delete(g.entries, key)
			}
		}
		g.nextSweep = now.Add(g.window)
	}
	entry, ok := g.entries[source]
	if ok && now.Sub(entry.windowStart) < g.window {
		if entry.count >= g.perWindow {
			return false
		}
		entry.count++
		g.entries[source] = entry
		return true
	}
	if len(g.entries) >= g.maxSources {
		return false
	}
	g.entries[source] = oauthRegistrationWindowEntry{count: 1, windowStart: now}
	return true
}

func (g *oauthRegistrationGuard) register(ctx context.Context, st *store.Store, record store.OAuthClientRecord, now time.Time) error {
	if g == nil || st == nil {
		return errOAuthClientQuota
	}
	err := st.RegisterOAuthClientWithinLimits(ctx, record, store.OAuthClientRegistrationLimits{
		MaxClients:       g.maxClients,
		MaxOrphans:       g.maxOrphans,
		MaxSourceOrphans: g.maxSourceOrphans,
		OrphanCutoff:     now.Add(-g.orphanRetention),
	})
	if errors.Is(err, store.ErrResourceLimit) {
		return errOAuthClientQuota
	}
	return err
}

func oauthRegistrationSourceHash(r *http.Request) string {
	peer := "unknown"
	if r != nil {
		peer = remoteIP(r)
		if peer == "" {
			peer = "unknown"
		}
	}
	sum := sha256.Sum256([]byte(peer))
	return hex.EncodeToString(sum[:])
}
