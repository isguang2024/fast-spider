package node

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWorkspaceRegistryAndPathGuard(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewWorkspaceStore(dataDir)
	record, err := store.Add(root, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(record.WorkspaceID, "ws_") || strings.Contains(record.WorkspaceID, root) {
		t.Fatalf("workspace id is not opaque: %q", record.WorkspaceID)
	}
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].WorkspaceId != record.WorkspaceID || !list[0].Enabled {
		t.Fatalf("unexpected workspace list: %#v", list)
	}

	resolved, err := resolveWorkspacePath(root, "inside.txt")
	if err != nil {
		t.Fatal(err)
	}
	expectedResolved, err := filepath.EvalSymlinks(filepath.Join(root, "inside.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(resolved, expectedResolved) {
		t.Fatalf("resolved path=%q expected=%q", resolved, expectedResolved)
	}
	for _, candidate := range []string{"../secret.txt", filepath.Join(outside, "secret.txt")} {
		if _, err := resolveWorkspacePath(root, candidate); !errors.Is(err, ErrPathOutsideWorkspace) {
			t.Fatalf("path %q error=%v, want ErrPathOutsideWorkspace", candidate, err)
		}
	}

	link := filepath.Join(root, "escape-link")
	if err := os.Symlink(outsideFile, link); err == nil {
		if _, err := resolveWorkspacePath(root, "escape-link"); !errors.Is(err, ErrPathOutsideWorkspace) {
			t.Fatalf("symlink escape error=%v, want ErrPathOutsideWorkspace", err)
		}
	} else if runtime.GOOS != "windows" {
		t.Fatalf("create symlink: %v", err)
	}

	if err := store.SetEnabled(record.WorkspaceID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(record.WorkspaceID); !errors.Is(err, ErrWorkspaceDisabled) {
		t.Fatalf("Resolve disabled error=%v", err)
	}
	if err := store.Remove(record.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(record.WorkspaceID); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("Resolve removed error=%v", err)
	}
}
