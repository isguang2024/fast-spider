package nodeupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyInstallArtifactNamesAreStrict(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		temp   bool
		backup bool
	}{
		{name: ".fast-spider-node.new-0123456789abcdef0123456789abcdef.tmp", temp: true},
		{name: ".FAST-SPIDER-NODE.NEW-0123456789ABCDEF0123456789ABCDEF.TMP", temp: true},
		{name: ".fast-spider-node.new-01234567-89ab-cdef-0123-456789abcdef.tmp"},
		{name: ".fast-spider-node.new-0123456789abcdef0123456789abcdef.tmp.extra"},
		{name: "fast-spider-node-20260811T120102Z.exe", backup: true},
		{name: "fast-spider-node-pre-0.3.14-20260811T120102Z.exe", backup: true},
		{name: "fast-spider-node-20261340T996099Z.exe"},
		{name: "fast-spider-node-pre-latest-20260811T120102Z.exe"},
		{name: "fast-spider-node.exe"},
		{name: "fast-spider-node.exe.previous"},
		{name: "other-fast-spider-node-20260811T120102Z.exe"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isLegacyNodeTempName(test.name); got != test.temp {
				t.Fatalf("temp match=%v want=%v", got, test.temp)
			}
			if got := isLegacyNodeBackupName(test.name); got != test.backup {
				t.Fatalf("backup match=%v want=%v", got, test.backup)
			}
		})
	}
}

func TestLegacyCleanupLogicSkipsInjectedReparseCandidates(t *testing.T) {
	t.Parallel()
	binDir := t.TempDir()
	executable := filepath.Join(binDir, legacyNodeExecutableName)
	temp := filepath.Join(binDir, ".fast-spider-node.new-0123456789abcdef0123456789abcdef.tmp")
	marker := filepath.Join(binDir, ".node-update-backup-path")
	backups := filepath.Join(binDir, "backups")
	backup := filepath.Join(backups, "fast-spider-node-20260811T120102Z.exe")
	for path, content := range map[string]string{executable: "current", temp: "temp", marker: "marker", backup: "backup"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reparse := map[string]bool{temp: true, marker: true, backups: true}
	if err := cleanupLegacyWindowsInstallArtifacts(executable, func(path string) (bool, error) {
		return reparse[path], nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{temp, marker, backup} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("injected reparse candidate %q was removed: %v", filepath.Base(path), err)
		}
	}
}

func TestLegacyCleanupRequiresCanonicalExecutableName(t *testing.T) {
	t.Parallel()
	binDir := t.TempDir()
	executable := filepath.Join(binDir, "renamed-node.exe")
	marker := filepath.Join(binDir, ".node-update-backup-path")
	if err := os.WriteFile(executable, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupLegacyWindowsInstallArtifacts(executable, func(string) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("cleanup ran for a renamed executable: %v", err)
	}
}
