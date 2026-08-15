package node

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCleanupOrphanBrowserSessionsIsStrictAndBounded(t *testing.T) {
	dataDir := t.TempDir()
	root := filepath.Join(dataDir, "browser", "sessions")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	old := now.Add(-browserSessionOrphanTTL - time.Minute)
	newTime := now.Add(-browserSessionOrphanTTL + time.Minute)
	makeSession := func(id string, modified time.Time) string {
		t.Helper()
		path := filepath.Join(root, id)
		if err := os.MkdirAll(filepath.Join(path, "screenshots"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
		return path
	}
	managedID := func(index int) string {
		return "brs_" + fmt.Sprintf("%032d", index)
	}
	activeID := managedID(1)
	activePath := makeSession(activeID, old)
	newPath := makeSession(managedID(2), newTime)
	manualPath := makeSession("brs_manual", old)
	oldPaths := make([]string, 0, maxBrowserSessionCleanup+2)
	for index := 3; index < 3+maxBrowserSessionCleanup+2; index++ {
		oldPaths = append(oldPaths, makeSession(managedID(index), old))
	}

	removed, err := cleanupOrphanBrowserSessions(dataDir, activeID, now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != maxBrowserSessionCleanup {
		t.Fatalf("removed=%d want=%d", removed, maxBrowserSessionCleanup)
	}
	for _, retained := range []string{activePath, newPath, manualPath} {
		if _, err := os.Stat(retained); err != nil {
			t.Fatalf("protected session %q was removed: %v", retained, err)
		}
	}
	remainingOld := 0
	for _, path := range oldPaths {
		if _, err := os.Stat(path); err == nil {
			remainingOld++
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if remainingOld != 2 {
		t.Fatalf("bounded cleanup left %d old managed sessions, want 2", remainingOld)
	}
}

func TestCleanupOrphanBrowserSessionsDoesNotFollowLinks(t *testing.T) {
	dataDir := t.TempDir()
	root := filepath.Join(dataDir, "browser", "sessions")
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "brs_"+strings.Repeat("a", browserOpaqueIDEncodedBytes))
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating a directory link requires Windows developer mode or elevation: %v", err)
		}
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(2 * browserSessionOrphanTTL)
	removed, err := cleanupOrphanBrowserSessions(dataDir, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("removed linked session count=%d", removed)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("cleanup followed managed-looking link: %v", err)
	}
}

func TestBrowserMaintenanceProtectsActiveSession(t *testing.T) {
	dataDir := t.TempDir()
	id := "brs_" + strings.Repeat("z", browserOpaqueIDEncodedBytes)
	path := filepath.Join(dataDir, "browser", "sessions", id)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-2 * browserSessionOrphanTTL)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	manager := &BrowserManager{
		dataDir: dataDir,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		session: &browserSessionRecord{BrowserSessionID: id, SessionDir: path},
	}
	manager.cleanupOrphanSessions(time.Now().UTC())
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("active browser session was removed: %v", err)
	}
}
