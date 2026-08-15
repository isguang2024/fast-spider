package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDeleteOAuthClientCascadesAuthorizationsAndTokens(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	st, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedOAuthOwnerAndClient(t, ctx, st, now)

	if err := st.CreateOAuthAuthorization(ctx, OAuthAuthorizationRecord{
		AuthorizationID: "authz_delete",
		OwnerID:         "usr_oauth",
		ClientID:        "mcpcli_test",
		Scopes:          []string{"fast-spider"},
		Resource:        "https://hub.example/mcp",
		CreatedAt:       now,
		ExpiresAt:       now.Add(30 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveOAuthTokenPair(
		ctx,
		"delete-access",
		"delete-refresh",
		OAuthTokenRecord{
			AuthorizationID: "authz_delete",
			OwnerID:         "usr_oauth",
			ClientID:        "mcpcli_test",
			Scopes:          []string{"fast-spider"},
			Resource:        "https://hub.example/mcp",
		},
		now.Add(time.Hour),
		now.Add(30*24*time.Hour),
		"",
		now,
	); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteOAuthClient(ctx, "mcpcli_test"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"oauth_clients", "oauth_authorizations", "oauth_access_tokens", "oauth_refresh_tokens"} {
		var count int
		if err := st.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d rows after client deletion", table, count)
		}
	}
}

func TestOAuthClientsAreOwnerScopedAndOrphansAreNotDeletable(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	st, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, owner := range []string{"usr_owner_a", "usr_owner_b"} {
		if _, err := st.db.ExecContext(ctx,
			"INSERT INTO owners(id, display_name, username, password_hash, created_at) VALUES(?,?,?,?,?)",
			owner, owner, owner, "password-hash", now.Unix(),
		); err != nil {
			t.Fatal(err)
		}
	}
	register := func(id string) {
		t.Helper()
		if err := st.RegisterOAuthClient(ctx, OAuthClientRecord{
			ClientID: id, ClientName: id, RedirectURIs: []string{"https://chatgpt.com/callback"},
			GrantTypes: []string{"authorization_code"}, ResponseTypes: []string{"code"},
			Scope: "fast-spider", CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	register("mcpcli_owner_a")
	register("mcpcli_owner_b")
	register("mcpcli_orphan")
	for _, auth := range []OAuthAuthorizationRecord{
		{AuthorizationID: "authz_owner_a", OwnerID: "usr_owner_a", ClientID: "mcpcli_owner_a", Scopes: []string{"fast-spider"}, Resource: "https://hub.example/mcp", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		{AuthorizationID: "authz_owner_b", OwnerID: "usr_owner_b", ClientID: "mcpcli_owner_b", Scopes: []string{"fast-spider"}, Resource: "https://hub.example/mcp", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
	} {
		if err := st.CreateOAuthAuthorization(ctx, auth); err != nil {
			t.Fatal(err)
		}
	}

	clients, err := st.ListOAuthClientsForOwner(ctx, "usr_owner_a")
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 1 || clients[0].ClientID != "mcpcli_owner_a" {
		t.Fatalf("owner A saw wrong OAuth clients: %+v", clients)
	}
	clients, err = st.ListOAuthClientsForOwner(ctx, "usr_owner_b")
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 1 || clients[0].ClientID != "mcpcli_owner_b" {
		t.Fatalf("owner B saw wrong OAuth clients: %+v", clients)
	}
	if err := st.DeleteOAuthClientForOwner(ctx, "usr_owner_a", "mcpcli_owner_b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner OAuth client deletion error=%v, want ErrNotFound", err)
	}
	if _, err := st.GetOAuthClient(ctx, "mcpcli_orphan"); err != nil {
		t.Fatalf("orphan DCR client disappeared before owner cleanup: %v", err)
	}
	if err := st.DeleteOAuthClientForOwner(ctx, "usr_owner_a", "mcpcli_owner_a"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetOAuthClient(ctx, "mcpcli_owner_a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("owner A OAuth client remains after deletion: %v", err)
	}
}

func TestDeleteOAuthClientForOwnerPreservesSharedClientAndOtherOwnerTokens(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	st, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, ownerID := range []string{"usr_shared_a", "usr_shared_b"} {
		if _, err := st.db.ExecContext(ctx,
			"INSERT INTO owners(id, display_name, username, password_hash, created_at) VALUES(?,?,?,?,?)",
			ownerID, ownerID, ownerID, "password-hash", now.Unix(),
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RegisterOAuthClient(ctx, oauthClientForQuota("mcpcli_shared", "source", now)); err != nil {
		t.Fatal(err)
	}
	for _, ownerID := range []string{"usr_shared_a", "usr_shared_b"} {
		authorizationID := "authz_" + ownerID
		if err := st.CreateOAuthAuthorization(ctx, OAuthAuthorizationRecord{
			AuthorizationID: authorizationID, OwnerID: ownerID, ClientID: "mcpcli_shared",
			Scopes: []string{"fast-spider"}, Resource: "https://hub.example/mcp",
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveOAuthTokenPair(ctx, "access_"+ownerID, "refresh_"+ownerID, OAuthTokenRecord{
			AuthorizationID: authorizationID, OwnerID: ownerID, ClientID: "mcpcli_shared",
			Scopes: []string{"fast-spider"}, Resource: "https://hub.example/mcp",
		}, now.Add(time.Hour), now.Add(time.Hour), "", now); err != nil {
			t.Fatal(err)
		}
	}

	if err := st.DeleteOAuthClientForOwner(ctx, "usr_shared_a", "mcpcli_shared"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetOAuthClient(ctx, "mcpcli_shared"); err != nil {
		t.Fatalf("shared client was deleted with its first owner: %v", err)
	}
	if _, err := st.AuthenticateOAuthAccessToken(ctx, "access_usr_shared_a", now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("detached owner's token remained valid: %v", err)
	}
	if _, err := st.AuthenticateOAuthAccessToken(ctx, "access_usr_shared_b", now); err != nil {
		t.Fatalf("other owner's token was removed: %v", err)
	}
	authorizations, err := st.ListOAuthAuthorizations(ctx, "usr_shared_b")
	if err != nil || len(authorizations) != 1 {
		t.Fatalf("other owner's authorization count=%d error=%v", len(authorizations), err)
	}
}

func TestDeleteOAuthClientForOwnersSerializesConcurrentSharedDetach(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	st, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.RegisterOAuthClient(ctx, oauthClientForQuota("mcpcli_concurrent_shared", "source", now)); err != nil {
		t.Fatal(err)
	}
	const owners = 8
	for i := 0; i < owners; i++ {
		ownerID := fmt.Sprintf("usr_shared_%d", i)
		if _, err := st.db.ExecContext(ctx,
			"INSERT INTO owners(id, display_name, username, password_hash, created_at) VALUES(?,?,?,?,?)",
			ownerID, ownerID, ownerID, "password-hash", now.Unix(),
		); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateOAuthAuthorization(ctx, OAuthAuthorizationRecord{
			AuthorizationID: "authz_" + ownerID, OwnerID: ownerID, ClientID: "mcpcli_concurrent_shared",
			Scopes: []string{"fast-spider"}, Resource: "https://hub.example/mcp",
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	errs := make(chan error, owners)
	var wait sync.WaitGroup
	for i := 0; i < owners; i++ {
		ownerID := fmt.Sprintf("usr_shared_%d", i)
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errs <- st.DeleteOAuthClientForOwner(ctx, ownerID, "mcpcli_concurrent_shared")
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent shared-client detach: %v", err)
		}
	}
	if _, err := st.GetOAuthClient(ctx, "mcpcli_concurrent_shared"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("last owner did not delete the shared client: %v", err)
	}
}

func TestRegisterOAuthClientWithinLimitsEnforcesPersistentSourceAndGlobalQuota(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	st, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	limits := OAuthClientRegistrationLimits{
		MaxClients: 10, MaxOrphans: 2, MaxSourceOrphans: 1,
		OrphanCutoff: now.Add(-30 * time.Minute),
	}
	if err := st.RegisterOAuthClientWithinLimits(ctx, oauthClientForQuota("mcpcli_a1", "source-a", now), limits); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterOAuthClientWithinLimits(ctx, oauthClientForQuota("mcpcli_a2", "source-a", now), limits); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("same-source orphan quota error=%v, want ErrResourceLimit", err)
	}
	if err := st.RegisterOAuthClientWithinLimits(ctx, oauthClientForQuota("mcpcli_b1", "source-b", now), limits); err != nil {
		t.Fatalf("independent source was rejected: %v", err)
	}
	if err := st.RegisterOAuthClientWithinLimits(ctx, oauthClientForQuota("mcpcli_c1", "source-c", now), limits); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("global orphan quota error=%v, want ErrResourceLimit", err)
	}
}

func TestRegisterOAuthClientWithinLimitsReclaimsExpiredOrphans(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	st, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.RegisterOAuthClient(ctx, oauthClientForQuota("mcpcli_stale", "source-a", now.Add(-31*time.Minute))); err != nil {
		t.Fatal(err)
	}
	limits := OAuthClientRegistrationLimits{
		MaxClients: 1, MaxOrphans: 1, MaxSourceOrphans: 1,
		OrphanCutoff: now.Add(-30 * time.Minute),
	}
	if err := st.RegisterOAuthClientWithinLimits(ctx, oauthClientForQuota("mcpcli_new", "source-a", now), limits); err != nil {
		t.Fatalf("stale orphan did not release registration capacity: %v", err)
	}
	if _, err := st.GetOAuthClient(ctx, "mcpcli_stale"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale orphan remained after atomic cleanup: %v", err)
	}
}

func TestRegisterOAuthClientWithinLimitsCountsAuthorizedClientsTowardTotal(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	st, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.db.ExecContext(ctx,
		"INSERT INTO owners(id, display_name, username, password_hash, created_at) VALUES(?,?,?,?,?)",
		"usr_total", "Owner", "owner-total", "password-hash", now.Unix(),
	); err != nil {
		t.Fatal(err)
	}
	limits := OAuthClientRegistrationLimits{
		MaxClients: 1, MaxOrphans: 1, MaxSourceOrphans: 1,
		OrphanCutoff: now.Add(-30 * time.Minute),
	}
	if err := st.RegisterOAuthClientWithinLimits(ctx, oauthClientForQuota("mcpcli_owned", "source-a", now), limits); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateOAuthAuthorization(ctx, OAuthAuthorizationRecord{
		AuthorizationID: "authz_total", OwnerID: "usr_total", ClientID: "mcpcli_owned",
		Scopes: []string{"fast-spider"}, Resource: "https://hub.example/mcp",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterOAuthClientWithinLimits(ctx, oauthClientForQuota("mcpcli_over_total", "source-b", now), limits); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("authorized client did not count toward total quota: %v", err)
	}
}

func TestRegisterOAuthClientWithinLimitsCountsRevokedClientsAsOrphans(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	st, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.db.ExecContext(ctx,
		"INSERT INTO owners(id, display_name, username, password_hash, created_at) VALUES(?,?,?,?,?)",
		"usr_revoke_loop", "Owner", "revoke-loop", "password-hash", now.Unix(),
	); err != nil {
		t.Fatal(err)
	}
	limits := OAuthClientRegistrationLimits{
		MaxClients: 10, MaxOrphans: 2, MaxSourceOrphans: 2,
		OrphanCutoff: now.Add(-30 * time.Minute),
	}
	for i := 0; i < 2; i++ {
		clientID := fmt.Sprintf("mcpcli_revoke_%d", i)
		authorizationID := fmt.Sprintf("authz_revoke_%d", i)
		if err := st.RegisterOAuthClientWithinLimits(ctx, oauthClientForQuota(clientID, "source-loop", now), limits); err != nil {
			t.Fatalf("register loop client %d: %v", i, err)
		}
		if err := st.CreateOAuthAuthorization(ctx, OAuthAuthorizationRecord{
			AuthorizationID: authorizationID, OwnerID: "usr_revoke_loop", ClientID: clientID,
			Scopes: []string{"fast-spider"}, Resource: "https://hub.example/mcp",
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.RevokeOAuthAuthorization(ctx, "usr_revoke_loop", authorizationID, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RegisterOAuthClientWithinLimits(ctx, oauthClientForQuota("mcpcli_revoke_over", "source-loop", now), limits); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("revocation loop bypassed orphan quota: %v", err)
	}
	if count, err := st.CountOrphanOAuthClients(ctx); err != nil || count != 2 {
		t.Fatalf("revoked clients counted as orphans=%d error=%v", count, err)
	}
}

func TestRegisterOAuthClientWithinLimitsPreservesRevokedAuthorizationHistoryUntilRetentionCleanup(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	st, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.db.ExecContext(ctx,
		"INSERT INTO owners(id, display_name, username, password_hash, created_at) VALUES(?,?,?,?,?)",
		"usr_history_retention", "Owner", "history-retention", "password-hash", now.Unix(),
	); err != nil {
		t.Fatal(err)
	}
	clientID := "mcpcli_history_retention"
	authorizationID := "authz_history_retention"
	createdAt := now.Add(-31 * time.Minute)
	if err := st.RegisterOAuthClient(ctx, oauthClientForQuota(clientID, "source-history", createdAt)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateOAuthAuthorization(ctx, OAuthAuthorizationRecord{
		AuthorizationID: authorizationID, OwnerID: "usr_history_retention", ClientID: clientID,
		Scopes: []string{"fast-spider"}, Resource: "https://hub.example/mcp",
		CreatedAt: createdAt, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeOAuthAuthorization(ctx, "usr_history_retention", authorizationID, now); err != nil {
		t.Fatal(err)
	}
	registrationTime := now.Add(31 * time.Minute)
	limits := OAuthClientRegistrationLimits{
		MaxClients: 10, MaxOrphans: 2, MaxSourceOrphans: 2,
		OrphanCutoff: registrationTime.Add(-30 * time.Minute),
	}
	if err := st.RegisterOAuthClientWithinLimits(ctx, oauthClientForQuota("mcpcli_history_next", "source-other", registrationTime), limits); err != nil {
		t.Fatalf("registration after orphan retention: %v", err)
	}
	if _, err := st.GetOAuthClient(ctx, clientID); err != nil {
		t.Fatalf("registration cleanup deleted client with retained authorization history: %v", err)
	}
	var authorizationCount int
	if err := st.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM oauth_authorizations WHERE id = ?", authorizationID).Scan(&authorizationCount); err != nil || authorizationCount != 1 {
		t.Fatalf("authorization history count=%d error=%v", authorizationCount, err)
	}
	if err := st.CleanupExpired(ctx, now.Add(oauthAuthorizationHistoryRetention-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetOAuthClient(ctx, clientID); err != nil {
		t.Fatalf("client was reclaimed before authorization history retention elapsed: %v", err)
	}
	if err := st.CleanupExpired(ctx, now.Add(oauthAuthorizationHistoryRetention)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetOAuthClient(ctx, clientID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("client remained after authorization history cleanup: %v", err)
	}
	if err := st.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM oauth_authorizations WHERE id = ?", authorizationID).Scan(&authorizationCount); err != nil || authorizationCount != 0 {
		t.Fatalf("authorization history remained after retention count=%d error=%v", authorizationCount, err)
	}
}

func TestCreateOAuthAuthorizationAtomicallyLimitsHistoricalClientsPerOwner(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	st, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.db.ExecContext(ctx,
		"INSERT INTO owners(id, display_name, username, password_hash, created_at) VALUES(?,?,?,?,?)",
		"usr_owner_limit", "Owner", "owner-limit", "password-hash", now.Unix(),
	); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < oauthMaxClientsPerOwner+1; i++ {
		if err := st.RegisterOAuthClient(ctx, oauthClientForQuota(fmt.Sprintf("mcpcli_owner_%03d", i), "source", now)); err != nil {
			t.Fatal(err)
		}
	}
	createAuthorization := func(i int) error {
		return st.CreateOAuthAuthorization(ctx, OAuthAuthorizationRecord{
			AuthorizationID: fmt.Sprintf("authz_owner_%03d", i), OwnerID: "usr_owner_limit",
			ClientID: fmt.Sprintf("mcpcli_owner_%03d", i), Scopes: []string{"fast-spider"},
			Resource: "https://hub.example/mcp", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		})
	}
	for i := 0; i < oauthMaxClientsPerOwner-1; i++ {
		if err := createAuthorization(i); err != nil {
			t.Fatalf("seed authorization %d: %v", i, err)
		}
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for _, i := range []int{oauthMaxClientsPerOwner - 1, oauthMaxClientsPerOwner} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errs <- createAuthorization(i)
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	succeeded, limited := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrResourceLimit):
			limited++
		default:
			t.Fatalf("unexpected authorization error: %v", err)
		}
	}
	if succeeded != 1 || limited != 1 {
		t.Fatalf("authorization results succeeded=%d limited=%d", succeeded, limited)
	}
}

func TestCreateOAuthAuthorizationCountsRevokedAndDeletedHistoryUntilClientDeletion(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	st, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.db.ExecContext(ctx,
		"INSERT INTO owners(id, display_name, username, password_hash, created_at) VALUES(?,?,?,?,?)",
		"usr_owner_history", "Owner", "owner-history", "password-hash", now.Unix(),
	); err != nil {
		t.Fatal(err)
	}
	limits := OAuthClientRegistrationLimits{
		MaxClients: 4096, MaxOrphans: 1024, MaxSourceOrphans: 256,
		OrphanCutoff: now.Add(-30 * time.Minute),
	}
	for i := 0; i < oauthMaxClientsPerOwner; i++ {
		clientID := fmt.Sprintf("mcpcli_history_%03d", i)
		authorizationID := fmt.Sprintf("authz_history_%03d", i)
		if err := st.RegisterOAuthClientWithinLimits(ctx, oauthClientForQuota(clientID, "source-loop", now), limits); err != nil {
			t.Fatalf("register client %d: %v", i, err)
		}
		if err := st.CreateOAuthAuthorization(ctx, OAuthAuthorizationRecord{
			AuthorizationID: authorizationID, OwnerID: "usr_owner_history", ClientID: clientID,
			Scopes: []string{"fast-spider"}, Resource: "https://hub.example/mcp",
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		}); err != nil {
			t.Fatalf("authorize client %d: %v", i, err)
		}
		if err := st.RevokeOAuthAuthorization(ctx, "usr_owner_history", authorizationID, now); err != nil {
			t.Fatalf("revoke authorization %d: %v", i, err)
		}
		if i%2 == 0 {
			if err := st.DeleteOAuthAuthorization(ctx, "usr_owner_history", authorizationID, now); err != nil {
				t.Fatalf("soft-delete authorization %d: %v", i, err)
			}
		}
	}

	const nextClientID = "mcpcli_history_next"
	if err := st.RegisterOAuthClientWithinLimits(ctx, oauthClientForQuota(nextClientID, "source-other", now), limits); err != nil {
		t.Fatalf("owner association quota incorrectly blocked DCR from another source: %v", err)
	}
	nextAuthorization := OAuthAuthorizationRecord{
		AuthorizationID: "authz_history_next", OwnerID: "usr_owner_history", ClientID: nextClientID,
		Scopes: []string{"fast-spider"}, Resource: "https://hub.example/mcp",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := st.CreateOAuthAuthorization(ctx, nextAuthorization); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("revoked/deleted history did not enforce owner client quota: %v", err)
	}
	if err := st.CreateOAuthAuthorization(ctx, OAuthAuthorizationRecord{
		AuthorizationID: "authz_history_reauthorize", OwnerID: "usr_owner_history", ClientID: "mcpcli_history_000",
		Scopes: []string{"fast-spider"}, Resource: "https://hub.example/mcp",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("reauthorizing an already-associated client consumed another slot: %v", err)
	}
	if err := st.DeleteOAuthClient(ctx, "mcpcli_history_000"); err != nil {
		t.Fatalf("delete associated client: %v", err)
	}
	if err := st.CreateOAuthAuthorization(ctx, nextAuthorization); err != nil {
		t.Fatalf("client deletion did not release the owner's historical slot: %v", err)
	}
}

func oauthClientForQuota(id, sourceHash string, createdAt time.Time) OAuthClientRecord {
	return OAuthClientRecord{
		ClientID: id, ClientName: id, RedirectURIs: []string{"https://chatgpt.com/callback"},
		GrantTypes: []string{"authorization_code"}, ResponseTypes: []string{"code"}, Scope: "fast-spider",
		RegistrationSourceHash: sourceHash, CreatedAt: createdAt,
	}
}

func seedOAuthOwnerAndClient(t *testing.T, ctx context.Context, st *Store, now time.Time) {
	t.Helper()
	if _, err := st.db.ExecContext(ctx,
		"INSERT INTO owners(id, display_name, username, password_hash, created_at) VALUES(?,?,?,?,?)",
		"usr_oauth", "Owner", "owner", "password-hash", now.Unix(),
	); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterOAuthClient(ctx, OAuthClientRecord{
		ClientID:      "mcpcli_test",
		ClientName:    "Test Client",
		RedirectURIs:  []string{"https://chatgpt.com/callback"},
		GrantTypes:    []string{"authorization_code", "refresh_token"},
		ResponseTypes: []string{"code"},
		Scope:         "fast-spider",
		CreatedAt:     now,
	}); err != nil {
		t.Fatal(err)
	}
}
