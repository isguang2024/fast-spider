package core

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

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
