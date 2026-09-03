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

func TestCloudCompletionQueuePersistsBatchesDeduplicatesAndRecoversClaims(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC)
	dbPath := filepath.Join(t.TempDir(), "hub.db")
	open := func() *Store {
		st, err := Open(ctx, dbPath)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	st := open()
	if _, err := st.db.ExecContext(ctx, "INSERT INTO owners(id, display_name, created_at) VALUES(?,?,?)", "usr_completion", "Owner", now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO machines(id, owner_id, display_name, status, os, arch, node_version, created_at, updated_at)
		VALUES(?,?,?,'active','windows','amd64','test',?,?)`, "mach_completion", "usr_completion", "Node", now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO cloud_collaborations(collaboration_id,owner_id,machine_id,idempotency_key,request_hash,status,state_json,revision,created_at,updated_at)
		VALUES(?,?,?,?,?,'active','{}',1,?,?)`, "collab_completion", "usr_completion", "mach_completion", "completion-test-key", "hash", now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 70; i++ {
		rec := CloudCompletionNotificationRecord{
			NotificationID: "completion_" + strconv.Itoa(i), OwnerID: "usr_completion", CollaborationID: "collab_completion",
			TaskID: "task-" + strconv.Itoa(i), Generation: 1, NotificationKind: "completion", Outcome: "completed",
			SourceSessionID: "chat-" + strconv.Itoa(i), TargetSessionID: "dispatcher-1", DeliverablePath: `C:\results\` + strconv.Itoa(i) + ".md",
			CreatedAt: now.Add(time.Duration(i) * time.Second), UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}
		stored, replayed, err := st.EnqueueCloudCompletionNotification(ctx, rec)
		if err != nil || replayed || stored.NotificationID != rec.NotificationID {
			t.Fatalf("enqueue %d stored=%+v replayed=%v err=%v", i, stored, replayed, err)
		}
		if i == 0 {
			for retry := 0; retry < 10; retry++ {
				if _, replayed, err := st.EnqueueCloudCompletionNotification(ctx, rec); err != nil || !replayed {
					t.Fatalf("retry %d replayed=%v err=%v", retry, replayed, err)
				}
			}
		}
	}
	claimed, err := st.ClaimCloudCompletionNotifications(ctx, "usr_completion", "dispatcher-1", "claim-first", 64, now.Add(2*time.Minute), 5*time.Minute)
	if err != nil || len(claimed) != 64 {
		t.Fatalf("first claim count=%d err=%v", len(claimed), err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st = open()
	active, alreadyAcked, err := st.GetCloudCompletionClaim(ctx, "usr_completion", "dispatcher-1", "claim-first", now.Add(3*time.Minute), 5*time.Minute)
	if err != nil || alreadyAcked || len(active) != 64 {
		t.Fatalf("reloaded claim count=%d acked=%v err=%v", len(active), alreadyAcked, err)
	}
	if acked, err := st.AcknowledgeCloudCompletionClaim(ctx, "usr_completion", "dispatcher-1", "claim-first", now.Add(3*time.Minute), 5*time.Minute); err != nil || acked != 64 {
		t.Fatalf("ack count=%d err=%v", acked, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st = open()
	acknowledged, alreadyAcked, err := st.GetCloudCompletionClaim(ctx, "usr_completion", "dispatcher-1", "claim-first", now.Add(4*time.Minute), 5*time.Minute)
	if err != nil || !alreadyAcked || len(acknowledged) != 64 {
		t.Fatalf("reloaded acknowledged claim count=%d acked=%v err=%v", len(acknowledged), alreadyAcked, err)
	}
	remaining, err := st.ClaimCloudCompletionNotifications(ctx, "usr_completion", "dispatcher-1", "claim-remaining", 64, now.Add(4*time.Minute), 5*time.Minute)
	if err != nil || len(remaining) != 6 {
		t.Fatalf("remaining count=%d err=%v", len(remaining), err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st = open()
	defer st.Close()
	if _, alreadyAcked, err := st.GetCloudCompletionClaim(ctx, "usr_completion", "dispatcher-1", "claim-remaining", now.Add(10*time.Minute), 5*time.Minute); !errors.Is(err, ErrExpired) || alreadyAcked {
		t.Fatalf("expired claim lookup acked=%v err=%v", alreadyAcked, err)
	}
	if acked, err := st.AcknowledgeCloudCompletionClaim(ctx, "usr_completion", "dispatcher-1", "claim-remaining", now.Add(10*time.Minute), 5*time.Minute); !errors.Is(err, ErrExpired) || acked != 0 {
		t.Fatalf("expired claim ack count=%d err=%v", acked, err)
	}
	reclaimed, err := st.ClaimCloudCompletionNotifications(ctx, "usr_completion", "dispatcher-1", "claim-after-restart-expiry", 64, now.Add(10*time.Minute), 5*time.Minute)
	if err != nil || len(reclaimed) != 6 {
		t.Fatalf("reclaimed count=%d err=%v", len(reclaimed), err)
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
