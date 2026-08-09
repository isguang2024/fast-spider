package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyOAuthTokensRemainReadableAndRefreshable(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	st, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedOAuthOwnerAndClient(t, ctx, st, now)

	if _, err := st.db.ExecContext(ctx, `INSERT INTO oauth_access_tokens(
		token_hash, owner_id, client_id, scopes_json, resource, created_at, expires_at
	) VALUES(?,?,?,?,?,?,?)`,
		"legacy-access", "usr_oauth", "mcpcli_test", `["fast-spider"]`, "https://hub.example/mcp", now.Unix(), now.Add(time.Hour).Unix(),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO oauth_refresh_tokens(
		token_hash, owner_id, client_id, scopes_json, resource, created_at, expires_at
	) VALUES(?,?,?,?,?,?,?)`,
		"legacy-refresh", "usr_oauth", "mcpcli_test", `["fast-spider"]`, "https://hub.example/mcp", now.Unix(), now.Add(24*time.Hour).Unix(),
	); err != nil {
		t.Fatal(err)
	}

	access, err := st.AuthenticateOAuthAccessToken(ctx, "legacy-access", now)
	if err != nil {
		t.Fatal(err)
	}
	if access.AuthorizationID != "" || access.OwnerID != "usr_oauth" {
		t.Fatalf("unexpected legacy access record: %+v", access)
	}
	refresh, err := st.GetOAuthRefreshToken(ctx, "legacy-refresh", now)
	if err != nil {
		t.Fatal(err)
	}
	if refresh.AuthorizationID != "" {
		t.Fatalf("legacy refresh unexpectedly had authorization ID: %+v", refresh)
	}

	if err := st.CreateOAuthAuthorization(ctx, OAuthAuthorizationRecord{
		AuthorizationID: "authz_migrated",
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
		"new-access",
		"new-refresh",
		OAuthTokenRecord{
			AuthorizationID: "authz_migrated",
			OwnerID:         "usr_oauth",
			ClientID:        "mcpcli_test",
			Scopes:          []string{"fast-spider"},
			Resource:        "https://hub.example/mcp",
		},
		now.Add(time.Hour),
		now.Add(30*24*time.Hour),
		"legacy-refresh",
		now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetOAuthRefreshToken(ctx, "legacy-refresh", now); err == nil {
		t.Fatal("legacy refresh token was not consumed")
	}
	rotated, err := st.GetOAuthRefreshToken(ctx, "new-refresh", now)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.AuthorizationID != "authz_migrated" {
		t.Fatalf("rotated refresh was not attached to authorization: %+v", rotated)
	}
}

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
