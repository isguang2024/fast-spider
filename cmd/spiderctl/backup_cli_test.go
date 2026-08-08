package main

import (
	"os"
	"path/filepath"
	"testing"
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
