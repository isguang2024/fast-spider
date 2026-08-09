package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestConnectionTokenSchemaRetiresLegacyTables(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var connectionTokens, legacyTokens, enrollmentTokens int
	if err := st.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='connection_tokens'").Scan(&connectionTokens); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='owner_api_tokens'").Scan(&legacyTokens); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='enrollment_tokens'").Scan(&enrollmentTokens); err != nil {
		t.Fatal(err)
	}
	if connectionTokens != 1 || legacyTokens != 0 || enrollmentTokens != 0 {
		t.Fatalf("unexpected final token schema: connection=%d legacy=%d enrollment=%d", connectionTokens, legacyTokens, enrollmentTokens)
	}
}

func TestCleanupExpiredRemovesTokensAndOldAudit(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	st, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if _, err := st.db.ExecContext(ctx, "INSERT INTO owners(id, display_name, created_at) VALUES(?,?,?)", "usr_test", "Owner", now.Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, "INSERT INTO bootstrap_tokens(id, token_hash, created_at, expires_at) VALUES(?,?,?,?)", "bst_expired", "hash_bootstrap", now.Add(-2*time.Hour).Unix(), now.Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendAudit(ctx, AuditEntry{ID: "aud_old", OwnerID: "usr_test", ActorType: "owner", ActorID: "usr_test", Action: "test.old", Result: "success", CreatedAt: now.Add(-31 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendAudit(ctx, AuditEntry{ID: "aud_recent", OwnerID: "usr_test", ActorType: "owner", ActorID: "usr_test", Action: "test.recent", Result: "success", CreatedAt: now.Add(-24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	if err := st.CleanupExpired(ctx, now); err != nil {
		t.Fatal(err)
	}

	assertCount := func(query string, args ...any) int {
		t.Helper()
		var count int
		if err := st.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if got := assertCount("SELECT COUNT(*) FROM bootstrap_tokens WHERE id = ?", "bst_expired"); got != 0 {
		t.Fatalf("expired bootstrap token retained: %d", got)
	}
	if got := assertCount("SELECT COUNT(*) FROM audit_entries WHERE id = ?", "aud_old"); got != 0 {
		t.Fatalf("old audit retained: %d", got)
	}
	if got := assertCount("SELECT COUNT(*) FROM audit_entries WHERE id = ?", "aud_recent"); got != 1 {
		t.Fatalf("recent audit removed: %d", got)
	}
}
