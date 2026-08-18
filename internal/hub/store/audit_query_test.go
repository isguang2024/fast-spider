package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestListAuditIsOwnerScopedAndFiltered(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	for _, owner := range []string{"owner-a", "owner-b"} {
		if _, err := st.db.ExecContext(ctx, "INSERT INTO owners(id, display_name, created_at) VALUES(?,?,?)", owner, owner, now.Add(-time.Hour).Unix()); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct{ id, owner string }{{"mach-a", "owner-a"}, {"mach-b", "owner-b"}} {
		if _, err := st.db.ExecContext(ctx, `INSERT INTO machines(id, owner_id, display_name, status, os, arch, node_version, created_at, updated_at)
			VALUES(?,?,?,'active','windows','amd64','test',?,?)`, row.id, row.owner, row.id, now.Add(-time.Hour).Unix(), now.Add(-time.Hour).Unix()); err != nil {
			t.Fatal(err)
		}
	}

	entries := []AuditEntry{
		{ID: "aud-a1", OwnerID: "owner-a", MachineID: "mach-a", ActorType: "owner", ActorID: "owner-a", Action: "git.repository.push", Result: "success", Detail: map[string]any{"ref": "main"}, CreatedAt: now.Add(-4 * time.Minute)},
		{ID: "aud-a2", OwnerID: "owner-a", MachineID: "mach-a", ActorType: "owner", ActorID: "owner-a", Action: "git.repository.pull", Result: "rejected", CreatedAt: now.Add(-3 * time.Minute)},
		{ID: "aud-a3", OwnerID: "owner-a", MachineID: "mach-a", ActorType: "owner", ActorID: "owner-a", Action: "file.write.edit", Result: "success", CreatedAt: now.Add(-2 * time.Minute)},
		{ID: "aud-b1", OwnerID: "owner-b", MachineID: "mach-b", ActorType: "owner", ActorID: "owner-b", Action: "git.repository.push", Result: "success", CreatedAt: now.Add(-time.Minute)},
	}
	for _, entry := range entries {
		if err := st.AppendAudit(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}

	got, err := st.ListAudit(ctx, AuditListQuery{OwnerID: "owner-a", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "aud-a3" || got[2].ID != "aud-a1" {
		t.Fatalf("owner scoped result=%+v", got)
	}
	if got[2].Detail == nil {
		t.Fatalf("detail was not decoded: %+v", got[2])
	}

	got, err = st.ListAudit(ctx, AuditListQuery{OwnerID: "owner-a", MachineID: "mach-a", ActionPrefix: "git.repository.", Result: "success", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "aud-a1" {
		t.Fatalf("filtered result=%+v", got)
	}

	got, err = st.ListAudit(ctx, AuditListQuery{OwnerID: "owner-a", Before: now.Add(-2 * time.Minute), Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "aud-a2" {
		t.Fatalf("before/limit result=%+v", got)
	}

	if _, err := st.ListAudit(ctx, AuditListQuery{}); err != ErrUnauthorized {
		t.Fatalf("empty owner err=%v want ErrUnauthorized", err)
	}
}
