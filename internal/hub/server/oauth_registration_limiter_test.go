package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/store"
)

func TestOAuthRegistrationGuardIsBoundedAndFailsClosed(t *testing.T) {
	now := time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)
	guard := newOAuthRegistrationGuard()
	guard.perWindow = 2
	guard.maxSources = 1
	if !guard.allow("source-a", now) || !guard.allow("source-a", now) {
		t.Fatal("valid registrations were rejected before the per-source limit")
	}
	if guard.allow("source-a", now) {
		t.Fatal("per-source registration limit was not enforced")
	}
	if guard.allow("source-b", now) {
		t.Fatal("new source bypassed the bounded limiter map")
	}
	if !guard.allow("source-b", now.Add(guard.window)) {
		t.Fatal("expired limiter entries did not release capacity")
	}
}

func TestOAuthRegistrationGuardSerializesOrphanClientQuota(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	guard := newOAuthRegistrationGuard()
	guard.maxOrphans = 1
	guard.maxSourceOrphans = 1
	now := time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)
	record := func(id string) store.OAuthClientRecord {
		return store.OAuthClientRecord{
			ClientID: id, ClientName: "test", RedirectURIs: []string{"https://chatgpt.com/oauth/callback"},
			GrantTypes: []string{"authorization_code"}, ResponseTypes: []string{"code"}, Scope: oauthScope,
			RegistrationSourceHash: "source-hash", CreatedAt: now,
		}
	}
	var wait sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []string{"mcpcli_first", "mcpcli_second"} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- guard.register(ctx, st, record(id), now)
		}()
	}
	wait.Wait()
	close(errs)
	succeeded, limited := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, errOAuthClientQuota):
			limited++
		default:
			t.Fatalf("unexpected registration error: %v", err)
		}
	}
	if succeeded != 1 || limited != 1 {
		t.Fatalf("registrations succeeded=%d limited=%d", succeeded, limited)
	}
	if count, err := st.CountOrphanOAuthClients(ctx); err != nil || count != 1 {
		t.Fatalf("orphan clients=%d error=%v", count, err)
	}
}

func TestOAuthRegistrationSourceHashIgnoresForwardedHeaders(t *testing.T) {
	req := httptest.NewRequest("POST", "https://hub.example/oauth/register", nil)
	req.RemoteAddr = "198.51.100.10:4321"
	req.Header.Set("CF-Connecting-IP", "203.0.113.1")
	req.Header.Set("X-Real-IP", "203.0.113.2")
	req.Header.Set("X-Forwarded-For", "203.0.113.3, 127.0.0.1")
	first := oauthRegistrationSourceHash(req)
	if first == "" || first == "198.51.100.10" {
		t.Fatalf("registration source was not hashed: %q", first)
	}
	req.Header.Set("CF-Connecting-IP", "192.0.2.1")
	req.Header.Set("X-Real-IP", "192.0.2.2")
	req.Header.Set("X-Forwarded-For", "192.0.2.3")
	if got := oauthRegistrationSourceHash(req); got != first {
		t.Fatalf("forwarded headers changed DCR source hash: got=%q want=%q", got, first)
	}
	req.RemoteAddr = "198.51.100.11:9876"
	if got := oauthRegistrationSourceHash(req); got == first {
		t.Fatal("different direct peer reused the DCR source hash")
	}
}

func TestOAuthRegistrationSourceHashIsolatesClientsBehindLoopbackProxy(t *testing.T) {
	req := httptest.NewRequest("POST", "https://hub.example/oauth/register", nil)
	req.RemoteAddr = "127.0.0.1:4321"
	req.Header.Set("X-Forwarded-For", "203.0.113.10, 127.0.0.1")
	first := oauthRegistrationSourceHash(req)
	req.Header.Set("X-Forwarded-For", "203.0.113.11, 127.0.0.1")
	second := oauthRegistrationSourceHash(req)
	if first == second {
		t.Fatal("distinct clients behind a trusted loopback proxy shared one DCR source")
	}

	req.RemoteAddr = "198.51.100.20:4321"
	direct := oauthRegistrationSourceHash(req)
	req.Header.Set("X-Forwarded-For", "203.0.113.12")
	if got := oauthRegistrationSourceHash(req); got != direct {
		t.Fatal("a public peer spoofed the trusted proxy source header")
	}
}
