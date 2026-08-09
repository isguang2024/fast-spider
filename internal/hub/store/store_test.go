package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

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
	if err := st.CreateEnrollmentToken(ctx, "enr_expired", "usr_test", "hash_enrollment", "", "", now.Add(-2*time.Hour), now.Add(-time.Hour), 5); err != nil {
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
	if got := assertCount("SELECT COUNT(*) FROM enrollment_tokens WHERE id = ?", "enr_expired"); got != 0 {
		t.Fatalf("expired enrollment token retained: %d", got)
	}
	if got := assertCount("SELECT COUNT(*) FROM audit_entries WHERE id = ?", "aud_old"); got != 0 {
		t.Fatalf("old audit retained: %d", got)
	}
	if got := assertCount("SELECT COUNT(*) FROM audit_entries WHERE id = ?", "aud_recent"); got != 1 {
		t.Fatalf("recent audit removed: %d", got)
	}
}

func TestBootstrapOwnerAccountRepairsHalfMigratedOwner(t *testing.T) {
	for _, test := range []struct {
		name     string
		username any
		password any
	}{
		{name: "username-only", username: "legacy-owner", password: nil},
		{name: "password-only", username: nil, password: "legacy-password-hash"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Second)
			st, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if _, err := st.db.ExecContext(ctx,
				"INSERT INTO owners(id, display_name, username, password_hash, created_at) VALUES(?,?,?,?,?)",
				"usr_legacy", "Legacy Owner", test.username, test.password, now.Unix(),
			); err != nil {
				t.Fatal(err)
			}
			if _, err := st.db.ExecContext(ctx,
				"INSERT INTO bootstrap_tokens(id, token_hash, created_at, expires_at) VALUES(?,?,?,?)",
				"bst_setup", "bootstrap-hash", now.Unix(), now.Add(time.Hour).Unix(),
			); err != nil {
				t.Fatal(err)
			}

			ownerID, err := st.BootstrapOwnerAccount(ctx, "bootstrap-hash", "usr_proposed", "new-owner", "New Owner", "new-password-hash", now)
			if err != nil {
				t.Fatal(err)
			}
			if ownerID != "usr_legacy" {
				t.Fatalf("owner ID changed during half-migration repair: %q", ownerID)
			}
			var username, password sql.NullString
			var displayName string
			if err := st.db.QueryRowContext(ctx, "SELECT username, display_name, password_hash FROM owners WHERE id = ?", ownerID).Scan(&username, &displayName, &password); err != nil {
				t.Fatal(err)
			}
			if !username.Valid || username.String != "new-owner" || displayName != "New Owner" || !password.Valid || password.String != "new-password-hash" {
				t.Fatalf("half-migrated owner was not repaired: username=%+v display=%q password=%+v", username, displayName, password)
			}
		})
	}
}
