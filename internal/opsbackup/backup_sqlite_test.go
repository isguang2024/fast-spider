package opsbackup

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/isguang2024/fast-spider/internal/hub/store"
)

func TestLiveIdleSQLiteBackupRestores(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	backupPath := filepath.Join(base, "backup.zip")
	manifest, err := Create(ctx, dataDir, backupPath, "sqlite-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range manifest.Files {
		if entry.Path == "hub.db-shm" {
			t.Fatal("backup manifest contains transient hub.db-shm")
		}
	}

	restored := filepath.Join(base, "restored")
	if _, err := Restore(ctx, backupPath, restored); err != nil {
		t.Fatal(err)
	}
	restoredStore, err := store.Open(ctx, filepath.Join(restored, "hub.db"))
	if err != nil {
		t.Fatalf("open restored SQLite store: %v", err)
	}
	defer restoredStore.Close()
	if err := restoredStore.Ping(ctx); err != nil {
		t.Fatalf("ping restored SQLite store: %v", err)
	}
}
