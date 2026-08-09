package node

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitControlUsesAbsoluteRepositoryPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	runTestGit(t, root, "init")
	runTestGit(t, root, "config", "user.email", "fast-spider@example.invalid")
	runTestGit(t, root, "config", "user.name", "Fast Spider Test")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "add", "a.txt")
	runTestGit(t, root, "commit", "-m", "initial")

	client, err := New(Config{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := client.gitControl(ctx, map[string]any{"action": "status", "repositoryPath": "."}); !errors.Is(err, ErrAbsolutePathRequired) {
		t.Fatalf("relative repositoryPath error=%v", err)
	}
	status, err := client.gitControl(ctx, map[string]any{"action": "status", "repositoryPath": root})
	if err != nil {
		t.Fatal(err)
	}
	if status.Action != "status" {
		t.Fatalf("status=%+v", status)
	}

	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.gitControl(ctx, map[string]any{"action": "add", "repositoryPath": root, "paths": []string{"a.txt"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.gitControl(ctx, map[string]any{"action": "commit", "repositoryPath": root, "message": "update"}); err != nil {
		t.Fatal(err)
	}

	runTestGit(t, root, "branch", "feature-worktree")
	created, err := client.gitControl(ctx, map[string]any{"action": "createWorktree", "repositoryPath": root, "branch": "feature-worktree"})
	if err != nil {
		t.Fatal(err)
	}
	if created.WorktreePath == "" || !filepath.IsAbs(created.WorktreePath) {
		t.Fatalf("worktree=%+v", created)
	}
	if _, err := os.Stat(created.WorktreePath); err != nil {
		t.Fatalf("created worktree stat=%v", err)
	}
	removed, err := client.gitControl(ctx, map[string]any{"action": "deleteWorktree", "repositoryPath": root, "worktreePath": created.WorktreePath})
	if err != nil {
		t.Fatal(err)
	}
	if removed.WorktreePath == "" {
		t.Fatalf("removed=%+v", removed)
	}
}

func TestGitControlRejectsNonRootRepositoryPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	runTestGit(t, root, "init")
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.gitControl(context.Background(), map[string]any{"action": "status", "repositoryPath": sub}); err == nil {
		t.Fatal("repository subdirectory unexpectedly accepted as repository root")
	}
}

func runTestGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
