package node

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomicFileRejectsLastMomentRevisionChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	staleHash := sha256String([]byte("older"))
	if err := writeAtomicFile(path, []byte("replacement"), 0o600, staleHash); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("writeAtomicFile error=%v, want ErrRevisionConflict", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "current" {
		t.Fatalf("stale write changed file: %q", content)
	}
}
