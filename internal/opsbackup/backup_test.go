package opsbackup

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCreateVerifyAndRestore(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	mustWrite(t, filepath.Join(dataDir, "hub.db"), []byte("database-content"), 0o600)
	mustWrite(t, filepath.Join(dataDir, "hub.db-shm"), []byte("transient-shm"), 0o600)
	mustWrite(t, filepath.Join(dataDir, "secrets", "hub-ed25519.key"), []byte("private-key"), 0o600)
	mustWrite(t, filepath.Join(dataDir, "artifacts", "blobs", "aa", "blob"), []byte("artifact-content"), 0o600)

	backupPath := filepath.Join(base, "backup.zip")
	manifest, err := Create(context.Background(), dataDir, backupPath, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Format != FormatV1 || manifest.FastSpiderVersion != "test-version" || len(manifest.Files) != 3 {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	verified, err := Verify(context.Background(), backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(verified.Files) != len(manifest.Files) {
		t.Fatalf("verified file count=%d want=%d", len(verified.Files), len(manifest.Files))
	}

	restored := filepath.Join(base, "restored")
	if _, err := Restore(context.Background(), backupPath, restored); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(restored, "hub.db-shm")); !os.IsNotExist(err) {
		t.Fatalf("transient hub.db-shm was restored: %v", err)
	}
	for _, rel := range []string{"hub.db", filepath.Join("secrets", "hub-ed25519.key"), filepath.Join("artifacts", "blobs", "aa", "blob")} {
		want, err := os.ReadFile(filepath.Join(dataDir, rel))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(restored, rel))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("restored %s=%q want=%q", rel, got, want)
		}
	}
}

func TestCreateRejectsOutputInsideDataDirectory(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	mustWrite(t, filepath.Join(dataDir, "hub.db"), []byte("db"), 0o600)
	nestedParent := filepath.Join(dataDir, "new", "nested")
	_, err := Create(context.Background(), dataDir, filepath.Join(nestedParent, "backup.zip"), "test")
	if err == nil {
		t.Fatal("Create accepted an output path inside the Hub data directory")
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "new")); !os.IsNotExist(statErr) {
		t.Fatalf("rejected output path created a directory inside Hub data: %v", statErr)
	}
}

func TestRestoreRejectsNonEmptyTarget(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	mustWrite(t, filepath.Join(dataDir, "hub.db"), []byte("db"), 0o600)
	backupPath := filepath.Join(base, "backup.zip")
	if _, err := Create(context.Background(), dataDir, backupPath, "test"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "target")
	mustWrite(t, filepath.Join(target, "existing.txt"), []byte("keep"), 0o600)
	if _, err := Restore(context.Background(), backupPath, target); err == nil {
		t.Fatal("Restore accepted a non-empty target directory")
	}
	got, err := os.ReadFile(filepath.Join(target, "existing.txt"))
	if err != nil || string(got) != "keep" {
		t.Fatalf("existing target changed: content=%q err=%v", got, err)
	}
}

func TestArchivePathsRejectNonCanonicalAndNonPortableNames(t *testing.T) {
	invalidUTF8 := string([]byte{'a', 0xff, 'b'})
	for _, value := range []string{"../hub.db", "foo/../hub.db", "/hub.db", "a:b", `a\b`, "CON", "con.txt", "file.", "file ", "a<1", invalidUTF8} {
		if _, err := validateArchivePath(value); err == nil {
			t.Fatalf("validateArchivePath(%q) accepted unsafe/non-portable path", value)
		}
	}
	if got, err := validateArchivePath("artifacts/blobs/aa/blob"); err != nil || got != "artifacts/blobs/aa/blob" {
		t.Fatalf("canonical path rejected: got=%q err=%v", got, err)
	}
	if runtime.GOOS != "windows" {
		if got, err := archivePathFromLocal(`a\b`); err == nil || got != "" {
			t.Fatalf("Linux-style local backslash filename was silently rewritten: got=%q err=%v", got, err)
		}
	}
}

func TestPortablePathRejectsCaseFoldCollision(t *testing.T) {
	seen := map[string]string{}
	if err := addPortablePath(seen, "artifacts/Foo"); err != nil {
		t.Fatal(err)
	}
	if err := addPortablePath(seen, "artifacts/foo"); err == nil {
		t.Fatal("case-folding path collision was accepted")
	}
}

func TestCreateRejectsCaseFoldCollision(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot create the case-only filename pair used by this test")
	}
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	mustWrite(t, filepath.Join(dataDir, "hub.db"), []byte("db"), 0o600)
	mustWrite(t, filepath.Join(dataDir, "Foo"), []byte("A"), 0o600)
	mustWrite(t, filepath.Join(dataDir, "foo"), []byte("B"), 0o600)
	if _, err := Create(context.Background(), dataDir, filepath.Join(base, "backup.zip"), "test"); err == nil {
		t.Fatal("Create accepted paths that collide on Windows")
	}
}

func TestCreateRejectsOversizedManifestAndPublishesNothing(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	mustWrite(t, filepath.Join(dataDir, "hub.db"), []byte("db"), 0o600)
	backupPath := filepath.Join(base, "backup.zip")
	version := strings.Repeat("v", int(maxManifestBytes)+1)
	if _, err := Create(context.Background(), dataDir, backupPath, version); err == nil {
		t.Fatal("Create accepted an oversized manifest")
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Fatalf("failed oversized backup published an output file: %v", err)
	}
}

func TestCopyWithContextLimitDoesNotWritePastBudget(t *testing.T) {
	var output bytes.Buffer
	written, err := copyWithContextLimit(context.Background(), &output, strings.NewReader("12345"), 4)
	if err == nil {
		t.Fatal("copyWithContextLimit accepted data beyond the remaining budget")
	}
	if written != 4 || output.String() != "1234" {
		t.Fatalf("limited copy wrote=%d data=%q, want 4 bytes", written, output.String())
	}
	output.Reset()
	written, err = copyWithContextLimit(context.Background(), &output, strings.NewReader("1234"), 4)
	if err != nil || written != 4 || output.String() != "1234" {
		t.Fatalf("exact-budget copy wrote=%d data=%q err=%v", written, output.String(), err)
	}
}

func TestCreateCancellationPublishesNothing(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	mustWrite(t, filepath.Join(dataDir, "hub.db"), []byte("db"), 0o600)
	backupPath := filepath.Join(base, "backup.zip")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Create(ctx, dataDir, backupPath, "test"); err == nil {
		t.Fatal("Create ignored a canceled context")
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Fatalf("canceled backup published an output file: %v", err)
	}
}

func TestVerifyRejectsEntryCountAndSizeBombs(t *testing.T) {
	tooMany := &zip.Reader{File: make([]*zip.File, maxBackupFiles+2)}
	if _, err := verifyArchive(context.Background(), tooMany); err == nil {
		t.Fatal("verifyArchive accepted too many ZIP entries")
	}
	oversized := &zip.Reader{File: []*zip.File{{FileHeader: zip.FileHeader{
		Name:               "data/hub.db",
		UncompressedSize64: uint64(maxBackupBytes) + 1,
	}}}}
	if _, err := verifyArchive(context.Background(), oversized); err == nil {
		t.Fatal("verifyArchive accepted oversized ZIP data")
	}
}

func TestCreateRejectsOutputParentSymlinkIntoDataDirectory(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	mustWrite(t, filepath.Join(dataDir, "hub.db"), []byte("db"), 0o600)
	inside := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "backup-link")
	if err := os.Symlink(inside, link); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	if _, err := Create(context.Background(), dataDir, filepath.Join(link, "backup.zip"), "test"); err == nil {
		t.Fatal("Create accepted an output path resolving inside the Hub data directory")
	}
}

func TestRestoreRejectsSymlinkTarget(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	mustWrite(t, filepath.Join(dataDir, "hub.db"), []byte("db"), 0o600)
	backupPath := filepath.Join(base, "backup.zip")
	if _, err := Create(context.Background(), dataDir, backupPath, "test"); err != nil {
		t.Fatal(err)
	}
	realTarget := filepath.Join(base, "real-target")
	if err := os.MkdirAll(realTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	linkTarget := filepath.Join(base, "linked-target")
	if err := os.Symlink(realTarget, linkTarget); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	if _, err := Restore(context.Background(), backupPath, linkTarget); err == nil {
		t.Fatal("Restore accepted a symlink target directory")
	}
}

func TestVerifyRejectsBackupWithoutDatabase(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	mustWrite(t, filepath.Join(dataDir, "secrets", "hub-ed25519.key"), []byte("key"), 0o600)
	backupPath := filepath.Join(base, "backup.zip")
	if _, err := Create(context.Background(), dataDir, backupPath, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), backupPath); err == nil {
		t.Fatal("Verify accepted a backup without hub.db")
	}
}

func TestVerifyRejectsCorruptedData(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	payload := []byte("unique-database-payload-for-corruption")
	mustWrite(t, filepath.Join(dataDir, "hub.db"), payload, 0o600)
	backupPath := filepath.Join(base, "backup.zip")
	if _, err := Create(context.Background(), dataDir, backupPath, "test"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	index := bytes.Index(raw, payload)
	if index < 0 {
		t.Fatal("stored payload was not found in backup archive")
	}
	raw[index] ^= 0xff
	corruptPath := filepath.Join(base, "corrupt.zip")
	if err := os.WriteFile(corruptPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), corruptPath); err == nil {
		t.Fatal("Verify accepted corrupted backup data")
	}
}

func mustWrite(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}
