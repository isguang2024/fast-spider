package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/isguang2024/fast-spider/internal/opsbackup"
)

func TestBackupVerifyRestoreCLIHelpers(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "hub.db"), []byte("cli-test-db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "secrets", "hub-ed25519.key"), []byte("cli-test-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	backupPath := filepath.Join(base, "backup.zip")
	backup([]string{"--data-dir", dataDir, "--out", backupPath})
	backupVerify([]string{"--file", backupPath})

	restored := filepath.Join(base, "restored")
	restore([]string{"--file", backupPath, "--data-dir", restored})
	for _, relative := range []string{"hub.db", filepath.Join("secrets", "hub-ed25519.key")} {
		want, err := os.ReadFile(filepath.Join(dataDir, relative))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(restored, relative))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("restored %s=%q want=%q", relative, got, want)
		}
	}
}

func TestBackupPruneCLIHelperUsesSafeDefaultsAndAbsoluteDirectory(t *testing.T) {
	t.Parallel()
	if defaultReleaseBackupKeep != 3 {
		t.Fatalf("default keep=%d want=3", defaultReleaseBackupKeep)
	}
	if _, err := runBackupPrune(context.Background(), "relative", 1); err == nil {
		t.Fatal("relative backup prune directory was accepted")
	}

	root := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "hub.db"), []byte("cli-prune-db"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pre-0.4.3-aaaaaaa.zip", "pre-0.4.4-bbbbbbb.zip"} {
		if _, err := opsbackup.Create(context.Background(), dataDir, filepath.Join(root, name), "test"); err != nil {
			t.Fatal(err)
		}
	}
	result, err := runBackupPrune(context.Background(), root, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.CandidateCount != 2 || result.KeptCount != 1 || result.DeletedCount != 1 {
		t.Fatalf("unexpected prune result: %+v", result)
	}
}
