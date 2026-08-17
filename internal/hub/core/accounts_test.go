package core

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/store"
)

func TestConnectionTokenLifecycleAndSecretRedaction(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := New(st, registry.New(), Config{DataDir: dataDir, Version: "accounts-test"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	account, err := service.BootstrapAccount(ctx, bootstrapToken, "token-owner", "Token Owner", "correct horse battery staple", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	created, err := service.CreateConnectionToken(ctx, account.OwnerID, "office-windows", 0, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Token, "ctk_") {
		t.Fatalf("connection token has unexpected prefix: %q", created.Token)
	}
	listed, err := service.ListConnectionTokens(ctx, account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != created.Record.ID || listed[0].LastUsedAt != nil {
		t.Fatalf("new connection token listing=%+v", listed)
	}
	listedJSON, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(listedJSON), created.Token) {
		t.Fatalf("connection token plaintext was persisted in list response: %s", listedJSON)
	}

	if ownerID, err := service.AuthenticateConnectionToken(ctx, created.Token); err != nil || ownerID != account.OwnerID {
		t.Fatalf("connection token authentication owner=%q err=%v", ownerID, err)
	}
	listed, err = service.ListConnectionTokens(ctx, account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].LastUsedAt == nil {
		t.Fatalf("connection token last_used_at was not recorded: %+v", listed)
	}

	if err := service.RevokeConnectionToken(ctx, account.OwnerID, created.Record.ID, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateConnectionToken(ctx, created.Token); !errors.Is(err, store.ErrRevoked) {
		t.Fatalf("revoked connection token authentication error=%v, want ErrRevoked", err)
	}
}

func TestDirectAccessKeyLifecyclePolicyAndSecretRedaction(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := New(st, registry.New(), Config{DataDir: dataDir, Version: "direct-key-test"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	account, err := service.BootstrapAccount(ctx, bootstrapToken, "direct-owner", "Direct Owner", "correct horse battery staple", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	readonly, err := service.CreateDirectAccessKey(ctx, account.OwnerID, "readonly-ai", "", nil, 7*24*time.Hour, 120, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(readonly.Token, "fsp_tmp_") {
		t.Fatalf("direct key has unexpected prefix: %q", readonly.Token)
	}
	if len(readonly.Record.Scopes) != 0 || readonly.Record.RateLimitPerMinute != 120 {
		t.Fatalf("unexpected readonly record: %+v", readonly.Record)
	}
	listed, err := service.ListDirectAccessKeys(ctx, account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	listedJSON, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(listedJSON), readonly.Token) {
		t.Fatalf("direct key plaintext was persisted in list response: %s", listedJSON)
	}
	authenticated, err := service.AuthenticateDirectAccessKey(ctx, readonly.Token)
	if err != nil || authenticated.OwnerID != account.OwnerID {
		t.Fatalf("direct key authentication=%+v err=%v", authenticated, err)
	}

	if _, err := service.CreateDirectAccessKey(ctx, account.OwnerID, "too-long-write", "", []string{DirectScopeFilesWrite}, 7*24*time.Hour, 120, "127.0.0.1"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("elevated seven-day key error=%v, want ErrConflict", err)
	}
	if _, err := service.CreateDirectAccessKey(ctx, account.OwnerID, "unknown-scope", "", []string{"root.everything"}, time.Hour, 120, "127.0.0.1"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("unknown scope error=%v, want ErrConflict", err)
	}
	elevated, err := service.CreateDirectAccessKey(ctx, account.OwnerID, "write-ai", "", []string{DirectScopeShell, DirectScopeFilesWrite, DirectScopeShell}, 24*time.Hour, 300, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(elevated.Record.Scopes, ",") != DirectScopeFilesWrite+","+DirectScopeShell {
		t.Fatalf("normalized scopes=%v", elevated.Record.Scopes)
	}
	if err := service.RevokeDirectAccessKey(ctx, account.OwnerID, elevated.Record.ID, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateDirectAccessKey(ctx, elevated.Token); !errors.Is(err, store.ErrRevoked) {
		t.Fatalf("revoked direct key authentication error=%v, want ErrRevoked", err)
	}

	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return base }
	expiring, err := service.CreateDirectAccessKey(ctx, account.OwnerID, "ten-minute-key", "", nil, 10*time.Minute, 120, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateDirectAccessKey(ctx, expiring.Token); err != nil {
		t.Fatalf("fresh direct key authentication error=%v", err)
	}
	service.now = func() time.Time { return base.Add(11 * time.Minute) }
	if _, err := service.AuthenticateDirectAccessKey(ctx, expiring.Token); !errors.Is(err, store.ErrExpired) {
		t.Fatalf("expired direct key authentication error=%v, want ErrExpired", err)
	}
}
