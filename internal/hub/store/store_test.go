package store

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
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

func TestArtifactCleanupDeletionRetryPersistsAndIsBounded(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	dbPath := filepath.Join(t.TempDir(), "hub.db")
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, "INSERT INTO owners(id, display_name, created_at) VALUES(?,?,?)", "usr_cleanup", "Owner", now.Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO machines(id, owner_id, display_name, status, os, arch, node_version, created_at, updated_at)
		VALUES(?,?,?,'active','linux','amd64','test',?,?)`, "mach_cleanup", "usr_cleanup", "Node", now.Add(-time.Hour).Unix(), now.Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		artifactID := "art_cleanup_" + strconv.Itoa(i)
		uploadID := "upl_1234567890123456789012345678901" + strconv.Itoa(i)
		if _, err := st.db.ExecContext(ctx, `INSERT INTO artifacts(id, owner_id, machine_id, logical_name, content_type, size_bytes, sha256, status, created_at, expires_at)
			VALUES(?,?,?,?,?,0,?,'uploading',?,?)`, artifactID, "usr_cleanup", "mach_cleanup", "file.txt", "text/plain", strings.Repeat("0", 64), now.Add(-time.Hour).Unix(), now.Add(time.Hour).Unix()); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.ExecContext(ctx, `INSERT INTO artifact_uploads(id, artifact_id, machine_id, expected_size, expected_sha256, status, created_at, expires_at)
			VALUES(?,?,?,0,?,'active',?,?)`, uploadID, artifactID, "mach_cleanup", strings.Repeat("0", 64), now.Add(-time.Hour).Unix(), now.Add(-time.Minute).Unix()); err != nil {
			t.Fatal(err)
		}
	}

	deletions, err := st.CleanupArtifacts(ctx, now, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(deletions) != 1 {
		t.Fatalf("cleanup batch returned %d deletions, want 1", len(deletions))
	}
	first := deletions[0]
	if err := st.FailArtifactFileDeletion(ctx, first, errors.New("disk busy"), now); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	deletions, err = st.CleanupArtifacts(ctx, now.Add(time.Second), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(deletions) != 1 || deletions[0].PathKey == first.PathKey {
		t.Fatalf("retry delay or bounded next batch failed: first=%+v next=%+v", first, deletions)
	}
	if err := st.CompleteArtifactFileDeletions(ctx, deletions); err != nil {
		t.Fatal(err)
	}
	deletions, err = st.CleanupArtifacts(ctx, now.Add(31*time.Second), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(deletions) != 1 || deletions[0].PathKey != first.PathKey || deletions[0].Attempts != 1 {
		t.Fatalf("persisted retry=%+v, want first deletion with one attempt", deletions)
	}
	if err := st.CompleteArtifactFileDeletions(ctx, deletions); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := st.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM artifact_file_deletions").Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("completed artifact deletion remained queued: %d", pending)
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
