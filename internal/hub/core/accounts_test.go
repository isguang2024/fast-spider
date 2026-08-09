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

func TestOwnerPersonalAccessTokenLifecycleAndSecretRedaction(t *testing.T) {
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
	account, err := service.BootstrapAccount(ctx, bootstrapToken, "pat-owner", "PAT Owner", "correct horse battery staple", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	created, err := service.CreateOwnerAPIToken(ctx, account.OwnerID, "maintenance", 0, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Token, "own_") {
		t.Fatalf("PAT has unexpected prefix: %q", created.Token)
	}
	listed, err := service.ListOwnerAPITokens(ctx, account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != created.Record.ID || listed[0].LastUsedAt != nil {
		t.Fatalf("new PAT listing=%+v", listed)
	}
	listedJSON, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(listedJSON), created.Token) {
		t.Fatalf("PAT plaintext was persisted in list response: %s", listedJSON)
	}

	if ownerID, err := service.AuthenticateOwner(ctx, created.Token); err != nil || ownerID != account.OwnerID {
		t.Fatalf("PAT authentication owner=%q err=%v", ownerID, err)
	}
	listed, err = service.ListOwnerAPITokens(ctx, account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].LastUsedAt == nil {
		t.Fatalf("PAT last_used_at was not recorded: %+v", listed)
	}

	if err := service.RevokeOwnerAPIToken(ctx, account.OwnerID, created.Record.ID, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateOwner(ctx, created.Token); !errors.Is(err, store.ErrRevoked) {
		t.Fatalf("revoked PAT authentication error=%v, want ErrRevoked", err)
	}
}
