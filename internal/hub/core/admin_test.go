package core

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/store"
)

func TestAdminAccountAndUserCreation(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := New(st, registry.New(), Config{DataDir: dataDir, Version: "admin-test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.EnsureAdminAccount(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.EnsureAdminAccount(ctx); err != nil {
		t.Fatal(err)
	}
	admin, err := service.LoginAdmin(ctx, DefaultAdminUsername, DefaultAdminPassword, "127.0.0.1")
	if err != nil || admin.Username != DefaultAdminUsername {
		t.Fatalf("admin login=%+v err=%v", admin, err)
	}
	if _, err := service.LoginAdmin(ctx, DefaultAdminUsername, "wrong-password", "127.0.0.1"); err != store.ErrUnauthorized {
		t.Fatalf("wrong admin password error=%v", err)
	}
	session, err := service.CreateAdminSession(ctx, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateAdminSession(ctx, session.Token); err != nil {
		t.Fatal(err)
	}
	user, err := service.CreateUser(ctx, admin.ID, "alice", "Alice", "alice-password-123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if user.Username != "alice" || user.OwnerID == "" {
		t.Fatalf("created user=%+v", user)
	}
	users, err := service.ListUsers(ctx)
	if err != nil || len(users) != 1 || users[0].Username != "alice" || users[0].DisplayName != "Alice" {
		t.Fatalf("listed users=%+v err=%v", users, err)
	}
	if _, err := service.LoginAccount(ctx, "alice", "alice-password-123", "127.0.0.1"); err != nil {
		t.Fatalf("created user cannot login: %v", err)
	}
	if _, err := service.CreateUser(ctx, admin.ID, "alice", "Alice 2", "alice-password-456", "127.0.0.1"); err != store.ErrConflict {
		t.Fatalf("duplicate user error=%v", err)
	}
}
