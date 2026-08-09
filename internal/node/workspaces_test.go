package node

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveMachinePathRequiresAbsoluteExistingPath(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(file, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveMachinePath(file)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(resolved, expected) {
		t.Fatalf("resolved=%q expected=%q", resolved, expected)
	}
	if _, err := ResolveMachinePath("relative/path.txt"); !errors.Is(err, ErrAbsolutePathRequired) {
		t.Fatalf("relative path error=%v", err)
	}
	if _, err := ResolveMachinePath(filepath.Join(root, "missing.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing path error=%v", err)
	}
}
