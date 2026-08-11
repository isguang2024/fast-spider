//go:build windows

package nodeupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupLegacyInstallArtifactsRemovesOnlyStrictWindowsArtifacts(t *testing.T) {
	t.Parallel()
	binDir := t.TempDir()
	executable := filepath.Join(binDir, legacyNodeExecutableName)
	previous := executable + ".previous"
	marker := filepath.Join(binDir, ".node-update-backup-path")
	temp := filepath.Join(binDir, ".fast-spider-node.new-0123456789abcdef0123456789abcdef.tmp")
	unknownTemp := filepath.Join(binDir, ".fast-spider-node.new-not-a-guid.tmp")
	backups := filepath.Join(binDir, "backups")
	legacyBackup := filepath.Join(backups, "fast-spider-node-20260811T120102Z.exe")
	preBackup := filepath.Join(backups, "fast-spider-node-pre-0.3.14-20260811T120103Z.exe")
	unknownBackup := filepath.Join(backups, "keep.exe")
	nestedBackup := filepath.Join(backups, "nested", "fast-spider-node-20260811T120104Z.exe")

	files := map[string]string{
		executable:    "current-0.4.4",
		previous:      "previous-0.4.3",
		marker:        "legacy-marker",
		temp:          "legacy-temp",
		unknownTemp:   "keep-temp",
		legacyBackup:  "legacy-backup",
		preBackup:     "legacy-pre-backup",
		unknownBackup: "keep-backup",
		nestedBackup:  "keep-nested",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := CleanupLegacyInstallArtifacts(executable); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{marker, temp, legacyBackup, preBackup} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("legacy artifact %q remains: %v", filepath.Base(path), err)
		}
	}
	for path, want := range map[string]string{
		executable:    "current-0.4.4",
		previous:      "previous-0.4.3",
		unknownTemp:   "keep-temp",
		unknownBackup: "keep-backup",
		nestedBackup:  "keep-nested",
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("kept artifact %q missing: %v", filepath.Base(path), err)
		}
		if string(got) != want {
			t.Fatalf("kept artifact %q changed to %q", filepath.Base(path), got)
		}
	}
	if err := CleanupLegacyInstallArtifacts(executable); err != nil {
		t.Fatalf("idempotent cleanup failed: %v", err)
	}
}

func TestCleanupLegacyInstallArtifactsRemovesEmptyBackupsDirectory(t *testing.T) {
	t.Parallel()
	binDir := t.TempDir()
	executable := filepath.Join(binDir, legacyNodeExecutableName)
	backup := filepath.Join(binDir, "backups", "fast-spider-node-20260811T120102Z.exe")
	if err := os.WriteFile(executable, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CleanupLegacyInstallArtifacts(executable); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(binDir, "backups")); !os.IsNotExist(err) {
		t.Fatalf("empty backups directory remains: %v", err)
	}
}

func TestCleanupLegacyInstallArtifactsSkipsWindowsReparsePoints(t *testing.T) {
	t.Parallel()
	binDir := t.TempDir()
	executable := filepath.Join(binDir, legacyNodeExecutableName)
	if err := os.WriteFile(executable, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	target := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	markerLink := filepath.Join(binDir, ".node-update-backup-path")
	if err := os.Symlink(target, markerLink); err != nil {
		t.Skipf("Windows symlink privilege unavailable: %v", err)
	}
	backupsLink := filepath.Join(binDir, "backups")
	if err := os.Symlink(outside, backupsLink); err != nil {
		t.Skipf("Windows directory symlink privilege unavailable: %v", err)
	}
	if err := CleanupLegacyInstallArtifacts(executable); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(markerLink); err != nil {
		t.Fatalf("marker reparse point was removed: %v", err)
	}
	if _, err := os.Lstat(backupsLink); err != nil {
		t.Fatalf("backups reparse point was removed: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "outside" {
		t.Fatalf("reparse target changed: %q err=%v", got, err)
	}
}
