package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/store"
)

func TestAuditLogToolIsHubLocalAndOwnerScoped(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := core.New(st, registry.New(), core.Config{DataDir: dataDir, Version: "audit-test"})
	if err != nil {
		t.Fatal(err)
	}

	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ownerA, err := service.BootstrapAccount(ctx, bootstrapToken, "owner-a", "Owner A", "password-a-123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	// No Node is connected in this test. Hub-local audit reads must still work.
	if err := st.AppendAudit(ctx, store.AuditEntry{ID: "aud-owner-a", OwnerID: ownerA.OwnerID, ActorType: "owner", ActorID: ownerA.OwnerID, Action: "git.repository.push", Result: "success", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendAudit(ctx, store.AuditEntry{ID: "aud-owner-b", OwnerID: "owner-b", ActorType: "owner", ActorID: "owner-b", Action: "git.repository.push", Result: "success", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	out, err := executeTypedTool[auditLogOutput](newToolExecutor(service), ctx, ownerA.OwnerID, "audit_log", auditLogInput{ActionPrefix: "git.repository.", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range out.Entries {
		if entry.ID == "aud-owner-b" {
			t.Fatalf("cross-owner audit leaked: %+v", out.Entries)
		}
		if entry.ID == "aud-owner-a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("owner audit missing: %+v", out.Entries)
	}

	if _, err := executeTypedTool[auditLogOutput](newToolExecutor(service), ctx, ownerA.OwnerID, "audit_log", auditLogInput{Before: "not-a-time"}); err == nil {
		t.Fatal("invalid before timestamp was accepted")
	}
}
