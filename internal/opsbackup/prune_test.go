package opsbackup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestReleaseBackupNameIsStrict(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		want bool
	}{
		{name: "pre-0.4.5-abcdef0.zip", want: true},
		{name: "pre-12.34.56-ABCDEF0123456789abcdef0123456789ABCDEF01.zip", want: true},
		{name: "pre-01.4.5-abcdef0.zip"},
		{name: "pre-0.4-abcdef0.zip"},
		{name: "pre-0.4.5-abcdef.zip"},
		{name: "pre-0.4.5-abcdefg.zip"},
		{name: "pre-0.4.5-abcdef0.zip.tmp"},
		{name: "fast-spider-pre-0.4.5-abcdef0.zip"},
		{name: "fast-spider-hub-0.4.5"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := releaseBackupNamePattern.MatchString(test.name); got != test.want {
				t.Fatalf("match=%v want=%v", got, test.want)
			}
		})
	}
}

func TestPruneReleaseBackupsKeepsNewestAndPreservesUnknown(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	archives := []struct {
		name    string
		created string
	}{
		{name: "pre-0.4.1-aaaaaaa.zip", created: "2026-08-08T01:00:00Z"},
		{name: "pre-0.4.2-bbbbbbb.zip", created: "2026-08-09T01:00:00Z"},
		{name: "pre-0.4.3-ccccccc.zip", created: "2026-08-10T01:00:00Z"},
		{name: "pre-0.4.4-ddddddd.zip", created: "2026-08-11T01:00:00Z"},
	}
	for _, archive := range archives {
		writePruneTestBackup(t, filepath.Join(root, archive.name), archive.created)
	}
	unknown := []string{"fast-spider-pre-0.4.0.zip", "fast-spider-hub-previous", "notes.txt"}
	for _, name := range unknown {
		if err := os.WriteFile(filepath.Join(root, name), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	nested := filepath.Join(root, "history", "pre-0.1.0-abcdef0.zip")
	if err := os.MkdirAll(filepath.Dir(nested), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("not inspected"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := PruneReleaseBackups(context.Background(), root, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.CandidateCount != 4 || result.KeptCount != 2 || result.DeletedCount != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	wantKept := []string{"pre-0.4.4-ddddddd.zip", "pre-0.4.3-ccccccc.zip"}
	wantDeleted := []string{"pre-0.4.2-bbbbbbb.zip", "pre-0.4.1-aaaaaaa.zip"}
	if !slices.Equal(result.Kept, wantKept) || !slices.Equal(result.Deleted, wantDeleted) {
		t.Fatalf("result=%+v want kept=%v deleted=%v", result, wantKept, wantDeleted)
	}
	for _, name := range append(append([]string{}, wantKept...), unknown...) {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("kept file %q missing: %v", name, err)
		}
	}
	for _, name := range wantDeleted {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("pruned file %q remains: %v", name, err)
		}
	}
	if _, err := os.Stat(nested); err != nil {
		t.Fatalf("unknown subdirectory was traversed: %v", err)
	}
}

func TestPruneReleaseBackupsDefaultsToAReadOnlyPlan(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	newest := "pre-0.4.5-eeeeeee.zip"
	oldest := "pre-0.4.4-ddddddd.zip"
	writePruneTestBackup(t, filepath.Join(root, newest), "2026-08-12T02:00:00Z")
	writePruneTestBackup(t, filepath.Join(root, oldest), "2026-08-11T02:00:00Z")

	result, err := PruneReleaseBackups(context.Background(), root, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.CandidateCount != 2 || result.KeptCount != 1 || result.PlannedCount != 1 || result.DeletedCount != 0 {
		t.Fatalf("unexpected plan result: %+v", result)
	}
	if !slices.Equal(result.Kept, []string{newest}) || !slices.Equal(result.Planned, []string{oldest}) || len(result.Deleted) != 0 {
		t.Fatalf("unexpected plan entries: %+v", result)
	}
	for _, name := range []string{newest, oldest} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("plan changed %q: %v", name, err)
		}
	}
}

func TestPruneReleaseBackupsUsesFilenameTieBreak(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	created := "2026-08-11T01:00:00Z"
	for _, name := range []string{"pre-0.4.3-ccccccc.zip", "pre-0.4.5-eeeeeee.zip", "pre-0.4.4-ddddddd.zip"} {
		writePruneTestBackup(t, filepath.Join(root, name), created)
	}
	result, err := PruneReleaseBackups(context.Background(), root, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Kept, []string{"pre-0.4.3-ccccccc.zip"}) {
		t.Fatalf("tie-break result=%+v", result)
	}
}

func TestPruneReleaseBackupsCorruptCandidateCausesZeroDeletion(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	valid := "pre-0.4.4-ddddddd.zip"
	corrupt := "pre-0.4.3-ccccccc.zip"
	writePruneTestBackup(t, filepath.Join(root, valid), "2026-08-11T01:00:00Z")
	if err := os.WriteFile(filepath.Join(root, corrupt), []byte("not a backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := PruneReleaseBackups(context.Background(), root, 1, true)
	if err == nil || result.DeletedCount != 0 || len(result.Deleted) != 0 {
		t.Fatalf("corrupt prune result=%+v err=%v", result, err)
	}
	for _, name := range []string{valid, corrupt} {
		if _, statErr := os.Stat(filepath.Join(root, name)); statErr != nil {
			t.Fatalf("candidate %q was deleted after verify failure: %v", name, statErr)
		}
	}
}

func TestPruneReleaseBackupsMatchingDirectoryCausesZeroDeletion(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	valid := "pre-0.4.5-eeeeeee.zip"
	matchingDirectory := "pre-0.4.4-ddddddd.zip"
	writePruneTestBackup(t, filepath.Join(root, valid), "2026-08-11T02:00:00Z")
	if err := os.Mkdir(filepath.Join(root, matchingDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := PruneReleaseBackups(context.Background(), root, 1, true)
	if err == nil || result.DeletedCount != 0 {
		t.Fatalf("matching directory result=%+v err=%v", result, err)
	}
	for _, name := range []string{valid, matchingDirectory} {
		if _, statErr := os.Stat(filepath.Join(root, name)); statErr != nil {
			t.Fatalf("candidate %q changed after planning failure: %v", name, statErr)
		}
	}
}

func TestPruneReleaseBackupsMatchingSymlinkCausesZeroDeletion(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	valid := "pre-0.4.5-eeeeeee.zip"
	writePruneTestBackup(t, filepath.Join(root, valid), "2026-08-11T02:00:00Z")
	target := filepath.Join(t.TempDir(), "outside.zip")
	writePruneTestBackup(t, target, "2026-08-10T02:00:00Z")
	link := filepath.Join(root, "pre-0.4.4-ddddddd.zip")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
	result, err := PruneReleaseBackups(context.Background(), root, 1, true)
	if err == nil || result.DeletedCount != 0 {
		t.Fatalf("symlink prune result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, valid)); err != nil {
		t.Fatalf("valid candidate was deleted: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("matching symlink was removed: %v", err)
	}
}

func TestPruneReleaseBackupsRejectsRootSymlink(t *testing.T) {
	t.Parallel()
	realRoot := t.TempDir()
	link := filepath.Join(t.TempDir(), "backup-link")
	if err := os.Symlink(realRoot, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := PruneReleaseBackups(context.Background(), link, 1, true); err == nil {
		t.Fatal("root symlink was accepted")
	}
}

func TestPruneReleaseBackupsInjectedReparseCandidateIsFailClosed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	first := filepath.Join(root, "pre-0.4.4-ddddddd.zip")
	second := filepath.Join(root, "pre-0.4.3-ccccccc.zip")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	verifyCalls := 0
	result, err := pruneReleaseBackups(context.Background(), root, 1, true, releaseBackupPruneDependencies{
		verify: func(context.Context, string) (Manifest, error) {
			verifyCalls++
			return Manifest{CreatedAt: "2026-08-11T01:00:00Z"}, nil
		},
		isReparse: func(path string, _ os.FileInfo) (bool, error) {
			return path == second, nil
		},
		removeFile: os.Remove,
	})
	if err == nil || result.DeletedCount != 0 {
		t.Fatalf("injected reparse result=%+v err=%v", result, err)
	}
	if verifyCalls > 1 {
		t.Fatalf("verification continued past reparse candidate: calls=%d", verifyCalls)
	}
	for _, path := range []string{first, second} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("candidate was deleted: %v", statErr)
		}
	}
}

func TestPruneReleaseBackupsInjectedRootReparseIsRejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	verifyCalled := false
	_, err := pruneReleaseBackups(context.Background(), root, 1, true, releaseBackupPruneDependencies{
		verify: func(context.Context, string) (Manifest, error) {
			verifyCalled = true
			return Manifest{}, nil
		},
		isReparse: func(path string, _ os.FileInfo) (bool, error) {
			return path == root, nil
		},
		removeFile: os.Remove,
	})
	if err == nil {
		t.Fatal("injected root reparse point was accepted")
	}
	if verifyCalled {
		t.Fatal("candidate verification ran for a rejected root")
	}
}

func TestPruneReleaseBackupsKeepBoundsAndNoCandidates(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, keep := range []int{0, MaxReleaseBackupKeep + 1} {
		if _, err := PruneReleaseBackups(context.Background(), root, keep, true); err == nil {
			t.Fatalf("keep=%d was accepted", keep)
		}
	}
	if _, err := PruneReleaseBackups(context.Background(), "relative", 1, true); err == nil {
		t.Fatal("relative root was accepted")
	}
	result, err := PruneReleaseBackups(context.Background(), root, 3, true)
	if err != nil || result.CandidateCount != 0 || result.KeptCount != 0 || result.DeletedCount != 0 || result.Kept == nil || result.Deleted == nil {
		t.Fatalf("empty prune result=%+v err=%v", result, err)
	}
}

func TestPruneReleaseBackupsCandidateLimitIsFailClosed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for index := 0; index <= maxReleaseBackupCandidates; index++ {
		name := fmt.Sprintf("pre-0.4.5-%07x.zip", index)
		if err := os.WriteFile(filepath.Join(root, name), []byte("candidate"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := PruneReleaseBackups(context.Background(), root, 1, true)
	if err == nil || result.CandidateCount != maxReleaseBackupCandidates+1 || result.DeletedCount != 0 {
		t.Fatalf("candidate limit result=%+v err=%v", result, err)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != maxReleaseBackupCandidates+1 {
		t.Fatalf("candidate limit changed files: got=%d want=%d", len(entries), maxReleaseBackupCandidates+1)
	}
}

func TestPruneReleaseBackupsRemoveErrorReportsPartialFacts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	names := []string{"pre-0.4.5-eeeeeee.zip", "pre-0.4.4-ddddddd.zip", "pre-0.4.3-ccccccc.zip"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removeErr := errors.New("remove denied")
	result, err := pruneReleaseBackups(context.Background(), root, 1, true, releaseBackupPruneDependencies{
		verify: func(_ context.Context, path string) (Manifest, error) {
			return Manifest{CreatedAt: map[string]string{
				names[0]: "2026-08-11T03:00:00Z",
				names[1]: "2026-08-11T02:00:00Z",
				names[2]: "2026-08-11T01:00:00Z",
			}[filepath.Base(path)]}, nil
		},
		isReparse: func(string, os.FileInfo) (bool, error) { return false, nil },
		removeFile: func(path string) error {
			if filepath.Base(path) == names[2] {
				return removeErr
			}
			return os.Remove(path)
		},
	})
	if !errors.Is(err, removeErr) || result.DeletedCount != 1 || result.KeptCount != 2 {
		t.Fatalf("partial result=%+v err=%v", result, err)
	}
	if !slices.Contains(result.Deleted, names[1]) || !slices.Contains(result.Kept, names[2]) {
		t.Fatalf("partial facts are inaccurate: %+v", result)
	}
}

func TestPruneReleaseBackupsStopsWhenLaterCandidateChangesDuringApply(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	names := []string{"pre-0.4.6-fffffff.zip", "pre-0.4.5-eeeeeee.zip", "pre-0.4.4-ddddddd.zip", "pre-0.4.3-ccccccc.zip"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	replacedPath := filepath.Join(root, names[2])
	originalData, err := os.ReadFile(replacedPath)
	if err != nil {
		t.Fatal(err)
	}
	originalInfo, err := os.Stat(replacedPath)
	if err != nil {
		t.Fatal(err)
	}
	replacementData := bytes.Repeat([]byte("x"), len(originalData))
	removeCalls := 0
	result, err := pruneReleaseBackups(context.Background(), root, 1, true, releaseBackupPruneDependencies{
		verify: func(_ context.Context, path string) (Manifest, error) {
			return Manifest{CreatedAt: map[string]string{
				names[0]: "2026-08-11T04:00:00Z",
				names[1]: "2026-08-11T03:00:00Z",
				names[2]: "2026-08-11T02:00:00Z",
				names[3]: "2026-08-11T01:00:00Z",
			}[filepath.Base(path)]}, nil
		},
		isReparse: func(string, os.FileInfo) (bool, error) { return false, nil },
		removeFile: func(path string) error {
			removeCalls++
			if filepath.Base(path) == names[1] {
				temporary := filepath.Join(root, ".replacement.tmp")
				if err := os.WriteFile(temporary, replacementData, 0o600); err != nil {
					return err
				}
				if err := os.Rename(temporary, replacedPath); err != nil {
					return err
				}
				if err := os.Chtimes(replacedPath, originalInfo.ModTime(), originalInfo.ModTime()); err != nil {
					return err
				}
			}
			return os.Remove(path)
		},
	})
	if err == nil || removeCalls != 1 || result.CandidateCount != 4 || result.PlannedCount != 3 || result.DeletedCount != 1 || result.KeptCount != 3 {
		t.Fatalf("changed candidate result=%+v err=%v", result, err)
	}
	if !slices.Equal(result.Deleted, []string{names[1]}) || !slices.Equal(result.Kept, []string{names[0], names[2], names[3]}) {
		t.Fatalf("partial facts are inaccurate: %+v", result)
	}
	gotReplacement, readErr := os.ReadFile(replacedPath)
	if readErr != nil {
		t.Fatalf("replacement backup was deleted: %v", readErr)
	}
	replacementInfo, statErr := os.Stat(replacedPath)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if !bytes.Equal(gotReplacement, replacementData) || len(gotReplacement) != len(originalData) || !replacementInfo.ModTime().Equal(originalInfo.ModTime()) {
		t.Fatalf("replacement fixture changed unexpectedly: size=%d want=%d mtime=%v want=%v", len(gotReplacement), len(originalData), replacementInfo.ModTime(), originalInfo.ModTime())
	}
	if _, statErr := os.Stat(filepath.Join(root, names[3])); statErr != nil {
		t.Fatalf("deletion continued after changed candidate: %v", statErr)
	}
}

func writePruneTestBackup(t *testing.T, path, createdAt string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	data := []byte("prune-test-hub-db")
	entry, err := zw.Create(archivePrefix + "hub.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(data); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	manifest := Manifest{
		Format:            FormatV1,
		CreatedAt:         createdAt,
		FastSpiderVersion: "test",
		Files: []FileEntry{{
			Path: "hub.db", Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Mode: "0600",
		}},
	}
	if err := writeManifest(zw, manifest); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPruneReleaseBackupsOrdersCreatedAtInUTC(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	older := "pre-0.4.4-ddddddd.zip"
	newer := "pre-0.4.5-eeeeeee.zip"
	writePruneTestBackup(t, filepath.Join(root, older), "2026-08-11T09:00:00+08:00")
	writePruneTestBackup(t, filepath.Join(root, newer), "2026-08-11T02:00:00Z")
	result, err := PruneReleaseBackups(context.Background(), root, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Kept, []string{newer}) || !slices.Equal(result.Deleted, []string{older}) {
		t.Fatalf("UTC ordering result=%+v", result)
	}
}
