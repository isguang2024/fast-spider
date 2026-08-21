package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/security"
)

func TestRotateLegacyAdminPasswordRevokesSessions(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC().Truncate(time.Second)
	legacyHash, err := security.HashPassword("legacy-admin-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO admin_accounts(id, username, password_hash, created_at, updated_at)
		VALUES(?,?,?,?,?)`, "adm_legacy", "admin", legacyHash, now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	session := AdminSessionRecord{ID: "ads_legacy", AdminID: "adm_legacy", CSRFToken: "csrf", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := st.CreateAdminSession(ctx, session, security.HashToken("session-token")); err != nil {
		t.Fatal(err)
	}
	newHash, err := security.HashPassword("rotated-admin-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RotateAdminPassword(ctx, "adm_legacy", newHash, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthenticateAdminSession(ctx, security.HashToken("session-token"), now.Add(2*time.Second)); !errors.Is(err, ErrRevoked) {
		t.Fatalf("legacy admin session after rotation error=%v", err)
	}
	record, err := st.AdminAccountByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if record.PasswordVersion != 1 || !security.VerifyPassword(record.PasswordHash, "rotated-admin-password-123") {
		t.Fatalf("rotated admin record=%+v", record)
	}
}
