package node

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGitControlPermissionsHooksAndNetwork(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dataDir := t.TempDir()
	root := t.TempDir()
	runTestGit(t, root, "init")
	runTestGit(t, root, "config", "user.name", "Fast Spider Test")
	runTestGit(t, root, "config", "user.email", "fast-spider@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "add", "a.txt")
	runTestGit(t, root, "commit", "-m", "initial")

	store := NewWorkspaceStore(dataDir)
	workspace, err := store.Add(root, "git-fixture")
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	status, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "status"})
	if err != nil || !strings.Contains(status.Output, "##") {
		t.Fatalf("git status result=%+v err=%v", status, err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "add", "paths": []string{"a.txt"}}); err != ErrPermissionDenied {
		t.Fatalf("git add without permission error=%v", err)
	}
	if err := store.SetPermissions(workspace.WorkspaceID, []string{"read", "git-write"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "add", "paths": []string{"a.txt"}}); err != nil {
		t.Fatal(err)
	}

	hookPath := filepath.Join(root, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "commit", "message": "blocked by hook"}); err != ErrGitHooksDenied {
		t.Fatalf("commit with hook error=%v", err)
	}
	if err := store.SetPermissions(workspace.WorkspaceID, []string{"read", "git-write", "git-hooks"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "commit", "message": "allowed hook"}); err != nil {
		t.Fatal(err)
	}

	runTestGit(t, root, "branch", "feature-worktree")
	createdWorktree, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "createWorktree", "branch": "feature-worktree"})
	if err != nil || createdWorktree.ManagedWorkspace == nil || createdWorktree.ManagedWorkspace.WorkspaceId == "" {
		t.Fatalf("create managed worktree=%+v err=%v", createdWorktree, err)
	}
	managed, err := store.Resolve(createdWorktree.ManagedWorkspace.WorkspaceId)
	if err != nil {
		t.Fatal(err)
	}
	if !pathWithin(filepath.Join(dataDir, "worktrees", workspace.WorkspaceID), managed.Root) {
		t.Fatalf("managed worktree escaped node data dir: %q", managed.Root)
	}
	if err := store.SetEnabled(managed.WorkspaceID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "deleteWorktree", "worktreeWorkspaceId": managed.WorkspaceID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(managed.WorkspaceID); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("deleted managed worktree still registered: %v", err)
	}

	remoteDir := filepath.Join(dataDir, "remotes")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(remoteDir, "remote.git")
	runTestGit(t, remoteDir, "init", "--bare", bare)
	runTestGit(t, root, "remote", "add", "origin", bare)
	if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "push", "remote": "origin", "branch": "HEAD", "idempotencyKey": "idem_git_push_0001"}); err != ErrPermissionDenied {
		t.Fatalf("git push without network permission error=%v", err)
	}
	if err := store.SetPermissions(workspace.WorkspaceID, []string{"read", "git-write", "git-hooks", "git-network"}); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "remote", "add", "evil", "ext::sh -c echo-unsafe")
	if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "fetch", "remote": "evil", "idempotencyKey": "idem_git_evil_0001"}); err == nil {
		t.Fatal("git ext:: remote unexpectedly allowed")
	}
	push, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "push", "remote": "origin", "branch": "HEAD", "idempotencyKey": "idem_git_push_0001"})
	if err != nil || push.Job == nil || push.Job.JobID == "" {
		t.Fatalf("git push result=%+v err=%v", push, err)
	}
	final := waitJobTerminal(t, client.jobs, push.Job.JobID, 15*time.Second)
	if final.State != "completed" {
		t.Fatalf("git push job=%+v", final)
	}
}

func TestGitReadDisablesTextconvExternalDiffAndFSMonitor(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dataDir := t.TempDir()
	root := t.TempDir()
	runTestGit(t, root, "init")
	runTestGit(t, root, "config", "user.name", "Fast Spider Test")
	runTestGit(t, root, "config", "user.email", "fast-spider@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("a.txt diff=marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "side-effect.marker")
	textconv := filepath.Join(root, "textconv"+scriptExtension())
	external := filepath.Join(root, "external"+scriptExtension())
	fsmonitor := filepath.Join(root, "fsmonitor"+scriptExtension())
	writeGitMarkerScript(t, textconv, marker, true)
	writeGitMarkerScript(t, external, marker, false)
	writeGitMarkerScript(t, fsmonitor, marker, false)
	runTestGit(t, root, "config", "diff.marker.textconv", textconv)
	runTestGit(t, root, "config", "diff.external", external)
	runTestGit(t, root, "config", "core.fsmonitor", fsmonitor)
	runTestGit(t, root, "add", "a.txt", ".gitattributes")
	runTestGit(t, root, "commit", "-m", "initial")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewWorkspaceStore(dataDir)
	workspace, err := store.Add(root, "git-read-fixture")
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, action := range []string{"status", "diff", "show"} {
		if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": action}); err != nil {
			t.Fatalf("git %s error=%v", action, err)
		}
	}
	runTestGit(t, root, "add", "a.txt")
	if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "stagedDiff"}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(marker); err == nil && len(raw) != 0 {
		t.Fatalf("git read executed textconv/external diff/fsmonitor: %q", raw)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestGitHooksRemoteValidationAndNetworkRevocation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dataDir := t.TempDir()
	root := t.TempDir()
	runTestGit(t, root, "init")
	runTestGit(t, root, "config", "user.name", "Fast Spider Test")
	runTestGit(t, root, "config", "user.email", "fast-spider@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "add", "a.txt")
	runTestGit(t, root, "commit", "-m", "initial")
	runTestGit(t, root, "branch", "feature")
	hooks := filepath.Join(root, ".git", "hooks")
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	remote := filepath.Join(dataDir, "remote.git")
	runTestGit(t, filepath.Dir(remote), "init", "--bare", remote)
	runTestGit(t, root, "remote", "add", "origin", remote)
	store := NewWorkspaceStore(dataDir)
	workspace, err := store.Add(root, "git-security-fixture")
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.SetPermissions(workspace.WorkspaceID, []string{"read", "git-write"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "commit", "message": "blocked"}); !errors.Is(err, ErrGitHooksDenied) {
		t.Fatalf("commit without git-hooks error=%v", err)
	}
	if err := store.SetPermissions(workspace.WorkspaceID, []string{"read", "git-network"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "pull", "remote": "origin", "branch": "HEAD", "idempotencyKey": "idem_git_pull_write_001"}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("pull without git-write error=%v", err)
	}
	if err := store.SetPermissions(workspace.WorkspaceID, []string{"read", "git-write", "git-network"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "push", "remote": "origin", "branch": "HEAD", "idempotencyKey": "idem_git_push_hooks_001"}); !errors.Is(err, ErrGitHooksDenied) {
		t.Fatalf("push without git-hooks error=%v", err)
	}
	if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "pull", "remote": "origin", "branch": "HEAD", "idempotencyKey": "idem_git_pull_hooks_001"}); !errors.Is(err, ErrGitHooksDenied) {
		t.Fatalf("pull without git-hooks error=%v", err)
	}
	if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "createWorktree", "branch": "feature"}); !errors.Is(err, ErrGitHooksDenied) {
		t.Fatalf("worktree without git-hooks error=%v", err)
	}
	if err := os.Remove(filepath.Join(hooks, "pre-commit")); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "config", "filter.risky.clean", "cat")
	if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "commit", "message": "blocked by transform"}); !errors.Is(err, ErrGitHooksDenied) {
		t.Fatalf("commit with executable transform and no git-hooks error=%v", err)
	}
	if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "createWorktree", "branch": "feature"}); !errors.Is(err, ErrGitHooksDenied) {
		t.Fatalf("worktree with executable transform and no git-hooks error=%v", err)
	}
	if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "pull", "remote": "origin", "branch": "HEAD", "idempotencyKey": "idem_git_pull_filter_001"}); !errors.Is(err, ErrGitHooksDenied) {
		t.Fatalf("pull with executable filter and no git-hooks error=%v", err)
	}
	runTestGit(t, root, "config", "core.gitProxy", "sh -c blocked-proxy")
	if err := store.SetPermissions(workspace.WorkspaceID, []string{"read", "git-network"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "fetch", "remote": "origin", "idempotencyKey": "idem_git_proxy_001"}); !errors.Is(err, ErrGitHooksDenied) {
		t.Fatalf("fetch with core.gitProxy and no git-hooks error=%v", err)
	}
	if _, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "push", "remote": "missing", "branch": "HEAD", "idempotencyKey": "idem_git_missing_remote_001"}); err == nil || !strings.Contains(err.Error(), "git remote is not configured") {
		t.Fatalf("unconfigured remote error=%v", err)
	}

	preReceive := filepath.Join(remote, "hooks", "pre-receive")
	if err := os.WriteFile(preReceive, []byte("#!/bin/sh\nsleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPermissions(workspace.WorkspaceID, []string{"read", "git-write", "git-network", "git-hooks"}); err != nil {
		t.Fatal(err)
	}
	push, err := client.gitControl(ctx, workspace.WorkspaceID, map[string]any{"action": "push", "remote": "origin", "branch": "HEAD", "idempotencyKey": "idem_git_revoke_network_001"})
	if err != nil || push.Job == nil {
		t.Fatalf("network push start=%+v err=%v", push, err)
	}
	time.Sleep(2500 * time.Millisecond)
	if err := store.SetPermissions(workspace.WorkspaceID, []string{"read", "git-write", "git-hooks"}); err != nil {
		t.Fatal(err)
	}
	final := waitJobTerminal(t, client.jobs, push.Job.JobID, 8*time.Second)
	if final.State != "canceled" || !strings.Contains(final.Error, "permission revoked") {
		t.Fatalf("network job after permission revocation=%+v", final)
	}
}

func scriptExtension() string {
	if runtime.GOOS == "windows" {
		return ".cmd"
	}
	return ".sh"
}

func writeGitMarkerScript(t *testing.T, path, marker string, catArgument bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		body := "@echo off\r\necho hit>>\"" + marker + "\"\r\n"
		if catArgument {
			body += "type \"%1\"\r\n"
		}
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
		return
	}
	body := "#!/bin/sh\nprintf hit >> '" + marker + "'\n"
	if catArgument {
		body += "cat \"$1\"\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func runTestGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}
