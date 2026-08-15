package node

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestGitControlRejectsCommitHookBeforeMarkerExecutes(t *testing.T) {
	root := initTestGitRepository(t)
	client, err := New(Config{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "add", "a.txt")
	marker := filepath.Join(root, "hook-ran.marker")
	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf executed > \"$PWD/hook-ran.marker\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = client.gitControl(context.Background(), map[string]any{"action": "commit", "repositoryPath": root, "message": "blocked"})
	if !errors.Is(err, ErrGitHooksDenied) {
		t.Fatalf("commit hook error=%v", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("commit hook marker exists or stat failed: %v", statErr)
	}
}

func TestGitControlRejectsExecutableFilterForAddedPath(t *testing.T) {
	root := initTestGitRepository(t)
	client, err := New(Config{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("a.txt filter=danger\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "config", "filter.danger.clean", "printf executed > filter-ran.marker")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = client.gitControl(context.Background(), map[string]any{"action": "add", "repositoryPath": root, "paths": []string{"a.txt"}})
	if !errors.Is(err, ErrGitHooksDenied) {
		t.Fatalf("executable filter error=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "filter-ran.marker")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("filter marker exists or stat failed: %v", statErr)
	}
}

func TestGitControlRejectsExecutableNetworkConfigForEveryNetworkAction(t *testing.T) {
	dataDir := t.TempDir()
	root := initTestGitRepository(t)
	remote := filepath.Join(dataDir, "remotes", "origin.git")
	if err := os.MkdirAll(filepath.Dir(remote), 0o700); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, filepath.Dir(remote), "init", "--bare", filepath.Base(remote))
	runTestGit(t, root, "remote", "add", "origin", remote)
	runTestGit(t, root, "config", "core.sshCommand", "printf executed > network-ran.marker")
	client, err := New(Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"fetch", "pull", "push"} {
		result, err := client.gitControl(context.Background(), map[string]any{
			"action": action, "repositoryPath": root, "remote": "origin", "idempotencyKey": "idem_network_config_" + action,
		})
		if !errors.Is(err, ErrGitHooksDenied) || result.Job != nil {
			t.Fatalf("%s result=%+v error=%v", action, result, err)
		}
	}
	if _, statErr := os.Stat(filepath.Join(root, "network-ran.marker")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("network marker exists or stat failed: %v", statErr)
	}
}

func TestGitControlRejectsRemoteSpecificExecutableCommands(t *testing.T) {
	for _, test := range []struct {
		name   string
		action string
		key    string
	}{
		{name: "proxy", action: "fetch", key: "remote.origin.proxy"},
		{name: "upload pack", action: "fetch", key: "remote.origin.uploadpack"},
		{name: "receive pack", action: "push", key: "remote.origin.receivepack"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			root := initTestGitRepository(t)
			remote := filepath.Join(dataDir, "remotes", "origin.git")
			if err := os.MkdirAll(filepath.Dir(remote), 0o700); err != nil {
				t.Fatal(err)
			}
			runTestGit(t, filepath.Dir(remote), "init", "--bare", filepath.Base(remote))
			runTestGit(t, root, "remote", "add", "origin", remote)
			runTestGit(t, root, "config", test.key, "printf executed")
			client, err := New(Config{DataDir: dataDir, Version: "test"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.gitControl(context.Background(), map[string]any{
				"action": test.action, "repositoryPath": root, "remote": "origin", "idempotencyKey": "idem_remote_command_001",
			})
			if !errors.Is(err, ErrGitHooksDenied) || result.Job != nil {
				t.Fatalf("%s result=%+v error=%v", test.action, result, err)
			}
		})
	}
}

func TestGitControlRejectsRepositoryCredentialHelpersAndAskPass(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*testing.T, string)
	}{
		{name: "local helper", configure: func(t *testing.T, root string) {
			runTestGit(t, root, "config", "credential.helper", `!printf executed > credential-ran.marker`)
		}},
		{name: "worktree URL helper", configure: func(t *testing.T, root string) {
			runTestGit(t, root, "config", "extensions.worktreeConfig", "true")
			runTestGit(t, root, "config", "--worktree", "credential.https://example.invalid.helper", `!printf executed > credential-ran.marker`)
		}},
		{name: "askpass", configure: func(t *testing.T, root string) {
			runTestGit(t, root, "config", "core.askPass", `printf executed > credential-ran.marker`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := initTestGitRepository(t)
			test.configure(t, root)
			runTestGit(t, root, "remote", "add", "origin", "https://example.invalid/repo.git")
			client, err := New(Config{DataDir: t.TempDir(), Version: "test"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.gitControl(context.Background(), map[string]any{
				"action": "fetch", "repositoryPath": root, "remote": "origin", "idempotencyKey": "idem_credential_config_001",
			})
			if !errors.Is(err, ErrGitHooksDenied) || result.Job != nil {
				t.Fatalf("fetch result=%+v error=%v", result, err)
			}
			if _, statErr := os.Stat(filepath.Join(root, "credential-ran.marker")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("credential marker exists or stat failed: %v", statErr)
			}
		})
	}
}

func TestGitControlRejectsCreateWorktreeHookBeforeMarkerExecutes(t *testing.T) {
	root := initTestGitRepository(t)
	runTestGit(t, root, "branch", "hook-worktree")
	marker := filepath.ToSlash(filepath.Join(root, "worktree-hook-ran.marker"))
	hook := filepath.Join(root, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf executed > \""+marker+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.gitControl(context.Background(), map[string]any{"action": "createWorktree", "repositoryPath": root, "branch": "hook-worktree"})
	if !errors.Is(err, ErrGitHooksDenied) || result.WorktreePath != "" {
		t.Fatalf("createWorktree result=%+v error=%v", result, err)
	}
	if _, statErr := os.Stat(filepath.FromSlash(marker)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("worktree hook marker exists or stat failed: %v", statErr)
	}
}

func TestGitControlRejectsCreateWorktreeCheckoutFilter(t *testing.T) {
	root := initTestGitRepository(t)
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("a.txt filter=danger\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "add", ".gitattributes")
	runTestGit(t, root, "commit", "-m", "add attributes")
	runTestGit(t, root, "branch", "filter-worktree")
	marker := filepath.ToSlash(filepath.Join(root, "filter-ran.marker"))
	runTestGit(t, root, "config", "filter.danger.smudge", `printf executed > "`+marker+`"`)
	client, err := New(Config{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.gitControl(context.Background(), map[string]any{"action": "createWorktree", "repositoryPath": root, "branch": "filter-worktree"})
	if !errors.Is(err, ErrGitHooksDenied) || result.WorktreePath != "" {
		t.Fatalf("createWorktree result=%+v error=%v", result, err)
	}
	if _, statErr := os.Stat(filepath.FromSlash(marker)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("checkout filter marker exists or stat failed: %v", statErr)
	}
}

func TestRunGitWriteDisablesHooksAtCommandLayer(t *testing.T) {
	root := initTestGitRepository(t)
	runTestGit(t, root, "branch", "command-layer-worktree")
	hook := filepath.Join(root, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf executed > \"$PWD/command-layer-hook.marker\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	client, err := New(Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dataDir, "command-layer-worktree")
	if _, err := client.runGitWrite(context.Background(), root, "createWorktree", []string{"worktree", "add", target, "command-layer-worktree"}); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(target, "command-layer-hook.marker")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("command-layer hook marker exists or stat failed: %v", statErr)
	}
}

func initTestGitRepository(t *testing.T) string {
	t.Helper()
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
	return strings.TrimSpace(root)
}

func runTestGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
