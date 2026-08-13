package releaseinfo

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNodeUpdatePushMarker(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "node", "windows-amd64")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := []byte("fast-spider-node-0.4.14")
	if err := os.WriteFile(filepath.Join(dir, "fast-spider-node.exe"), artifact, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "version.txt"), []byte("0.4.14\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 1, 2, 3, 4, time.UTC)
	created, err := CreateNodeUpdatePush(root, "windows-amd64", now)
	if err != nil {
		t.Fatal(err)
	}
	wantSHA, _, err := FileSHA256(filepath.Join(dir, "fast-spider-node.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Format != NodeUpdatePushFormat || created.Version != "0.4.14" || created.Platform != "windows-amd64" || created.SHA256 != wantSHA || created.PushID == "" {
		t.Fatalf("created=%+v", created)
	}
	read, err := ReadNodeUpdatePush(root, "windows-amd64")
	if err != nil {
		t.Fatal(err)
	}
	if read != created {
		t.Fatalf("read=%+v want=%+v", read, created)
	}
}

func TestNodeUpdatePushRejectsMissingRelease(t *testing.T) {
	if _, err := CreateNodeUpdatePush(t.TempDir(), "windows-amd64", time.Now()); err == nil {
		t.Fatal("expected missing release error")
	}
}
