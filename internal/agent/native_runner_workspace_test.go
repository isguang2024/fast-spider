package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeRunnerWorkspacePrepareInspectIntegrateCleanup(t *testing.T) {
	root := nativeRunnerWorkspaceTestRepo(t)
	if err := os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := Prepare(context.Background(), root, "project", "task-one", "task one", "task.txt", nil)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if ws.Path == root || ws.Branch == "" || ws.BaseCommit == "" {
		t.Fatalf("invalid workspace: %+v", ws)
	}
	if !strings.HasPrefix(ws.Branch, "codex/任务-") {
		t.Fatalf("workspace branch lacks Chinese task namespace: %q", ws.Branch)
	}
	if err := os.WriteFile(filepath.Join(ws.Path, "task.txt"), []byte("done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), ws.Path, "add", "--", "task.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), ws.Path, "commit", "-m", "task"); err != nil {
		t.Fatal(err)
	}
	if err := ws.Inspect(context.Background()); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if err := ws.Integrate(context.Background()); err != nil {
		t.Fatalf("integrate with unrelated dirty root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "unrelated.txt")); err != nil {
		t.Fatalf("unrelated root file was lost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "task.txt")); err != nil {
		t.Fatalf("integrated task file missing: %v", err)
	}
	if err := ws.Cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(ws.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree still exists after cleanup: %v", err)
	}
}

func TestNativeRunnerWorkspaceRejectsDirtySourceAndScopeEscape(t *testing.T) {
	root := nativeRunnerWorkspaceTestRepo(t)
	ws, err := Prepare(context.Background(), root, "project", "task-dirty", "dirty", "allowed/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws.Path, "other"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Path, "other", "file.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ws.Inspect(context.Background()); err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("dirty source was accepted: %v", err)
	}
	if _, err := nativeRunnerGit(context.Background(), ws.Path, "add", "--", "other/file.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), ws.Path, "commit", "-m", "scope"); err != nil {
		t.Fatal(err)
	}
	if err := ws.Inspect(context.Background()); err == nil || !strings.Contains(err.Error(), "outside task scope") {
		t.Fatalf("scope escape was accepted: %v", err)
	}
	_, _ = nativeRunnerGit(context.Background(), root, "worktree", "remove", ws.Path)
}

func TestNativeRunnerWorkspaceRejectsOverlapAndDivergence(t *testing.T) {
	t.Run("overlap", func(t *testing.T) {
		root := nativeRunnerWorkspaceTestRepo(t)
		ws, err := Prepare(context.Background(), root, "project", "task-overlap", "overlap", "shared.txt", nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "shared.txt"), []byte("main dirty\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws.Path, "shared.txt"), []byte("task\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := nativeRunnerGit(context.Background(), ws.Path, "add", "--", "shared.txt"); err != nil {
			t.Fatal(err)
		}
		if _, err := nativeRunnerGit(context.Background(), ws.Path, "commit", "-m", "task"); err != nil {
			t.Fatal(err)
		}
		if err := ws.Integrate(context.Background()); err == nil || !strings.Contains(err.Error(), "overlapping") {
			t.Fatalf("overlap was accepted: %v", err)
		}
		_, _ = nativeRunnerGit(context.Background(), root, "worktree", "remove", ws.Path)
	})
	t.Run("divergence", func(t *testing.T) {
		root := nativeRunnerWorkspaceTestRepo(t)
		ws, err := Prepare(context.Background(), root, "project", "task-diverge", "diverge", "task.txt", nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws.Path, "task.txt"), []byte("task\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := nativeRunnerGit(context.Background(), ws.Path, "add", "--", "task.txt"); err != nil {
			t.Fatal(err)
		}
		if _, err := nativeRunnerGit(context.Background(), ws.Path, "commit", "-m", "task"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte("main\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := nativeRunnerGit(context.Background(), root, "add", "--", "main.txt"); err != nil {
			t.Fatal(err)
		}
		if _, err := nativeRunnerGit(context.Background(), root, "commit", "-m", "main"); err != nil {
			t.Fatal(err)
		}
		if err := ws.Integrate(context.Background()); err == nil || !strings.Contains(err.Error(), "rebase-needed") {
			t.Fatalf("divergence was accepted: %v", err)
		}
		if err := ws.Cleanup(context.Background()); err == nil || !strings.Contains(err.Error(), "unmerged") {
			t.Fatalf("unmerged workspace was cleaned: %v", err)
		}
		_, _ = nativeRunnerGit(context.Background(), root, "worktree", "remove", ws.Path)
	})
}

func TestNativeRunnerWorkspaceRetryReusesIntent(t *testing.T) {
	root := nativeRunnerWorkspaceTestRepo(t)
	first, err := Prepare(context.Background(), root, "project", "task-retry", "retry", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), root, "worktree", "remove", first.Path); err != nil {
		t.Fatal(err)
	}
	second, err := Prepare(context.Background(), root, "project", "task-retry", "retry", "", first)
	if err != nil {
		t.Fatalf("retry prepare: %v", err)
	}
	if second.Path != first.Path || second.Branch != first.Branch || second.BaseCommit != first.BaseCommit {
		t.Fatalf("retry did not reuse intent: first=%+v second=%+v", first, second)
	}
	_, _ = nativeRunnerGit(context.Background(), root, "worktree", "remove", second.Path)
}

func TestNativeRunnerWorkspaceModeOnlyFirstCreateUsesCurrentMain(t *testing.T) {
	root := nativeRunnerWorkspaceTestRepo(t)
	before, err := nativeRunnerGitHead(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	intent := &nativeRunnerWorkspace{Mode: NativeRunnerWorkspaceWorktree}
	ws, err := Prepare(context.Background(), root, "project", "task-mode-only", "isolate", "task.txt", intent)
	if err != nil {
		t.Fatalf("mode-only first create: %v", err)
	}
	if ws.BaseCommit != before {
		t.Fatalf("first create did not use current main: got %s want %s", ws.BaseCommit, before)
	}
	_, _ = nativeRunnerGit(context.Background(), root, "worktree", "remove", ws.Path)
}

func TestNativeRunnerWorkspaceModeOnlyExistingBranchRecoversOriginalBase(t *testing.T) {
	root := nativeRunnerWorkspaceTestRepo(t)
	first, err := Prepare(context.Background(), root, "project", "task-mode-restart", "isolate", "task.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	base := first.BaseCommit
	if _, err := nativeRunnerGit(context.Background(), root, "worktree", "remove", first.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte("advance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), root, "add", "--", "main.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), root, "commit", "-m", "advance"); err != nil {
		t.Fatal(err)
	}
	intent := &nativeRunnerWorkspace{Mode: NativeRunnerWorkspaceWorktree}
	second, err := Prepare(context.Background(), root, "project", "task-mode-restart", "isolate", "task.txt", intent)
	if err != nil {
		t.Fatalf("mode-only restart: %v", err)
	}
	if second.BaseCommit != base {
		t.Fatalf("restart refreshed base from main: got %s want %s", second.BaseCommit, base)
	}
	_, _ = nativeRunnerGit(context.Background(), root, "worktree", "remove", second.Path)
}

func TestNativeRunnerWorkspaceRecoversMissingBaseFromOwnedBranch(t *testing.T) {
	root := nativeRunnerWorkspaceTestRepo(t)
	first, err := Prepare(context.Background(), root, "project", "task-recover", "recover", "task.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	base := first.BaseCommit
	if _, err := nativeRunnerGit(context.Background(), root, "worktree", "remove", first.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte("main moved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), root, "add", "--", "main.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), root, "commit", "-m", "advance main"); err != nil {
		t.Fatal(err)
	}
	intent := *first
	intent.BaseCommit = ""
	second, err := Prepare(context.Background(), root, "project", "task-recover", "recover", "task.txt", &intent)
	if err != nil {
		t.Fatalf("recover prepare: %v", err)
	}
	if second.BaseCommit != base {
		t.Fatalf("base silently changed: got %s want %s", second.BaseCommit, base)
	}
	_, _ = nativeRunnerGit(context.Background(), root, "worktree", "remove", second.Path)
}

func TestNativeRunnerWorkspaceCleanupAllowsAlreadyMergedPreparedTree(t *testing.T) {
	root := nativeRunnerWorkspaceTestRepo(t)
	ws, err := Prepare(context.Background(), root, "project", "task-cancel", "cancel", "task.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Cleanup(context.Background()); err != nil {
		t.Fatalf("clean unmodified prepared worktree was not reclaimable: %v", err)
	}
	if _, err := os.Stat(ws.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepared worktree remains: %v", err)
	}
}

func TestNativeRunnerWorkspaceCleanupRechecksHeadAfterIntegration(t *testing.T) {
	root := nativeRunnerWorkspaceTestRepo(t)
	ws, err := Prepare(context.Background(), root, "project", "task-late", "late", "task.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Path, "task.txt"), []byte("task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), ws.Path, "add", "--", "task.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), ws.Path, "commit", "-m", "task"); err != nil {
		t.Fatal(err)
	}
	if err := ws.Integrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Path, "late.txt"), []byte("late\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), ws.Path, "add", "--", "late.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), ws.Path, "commit", "-m", "late"); err != nil {
		t.Fatal(err)
	}
	if err := ws.Cleanup(context.Background()); err == nil || !strings.Contains(err.Error(), "unmerged") {
		t.Fatalf("cleanup removed a post-integration commit: %v", err)
	}
	if _, err := os.Stat(ws.Path); err != nil {
		t.Fatalf("worktree disappeared after rejected cleanup: %v", err)
	}
	_, _ = nativeRunnerGit(context.Background(), root, "worktree", "remove", ws.Path)
}

func TestNativeRunnerWorkspaceCleanupIsIdempotentAfterRemove(t *testing.T) {
	root := nativeRunnerWorkspaceTestRepo(t)
	ws, err := Prepare(context.Background(), root, "project", "task-idempotent", "idempotent", "task.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Path, "task.txt"), []byte("task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), ws.Path, "add", "--", "task.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), ws.Path, "commit", "-m", "task"); err != nil {
		t.Fatal(err)
	}
	if err := ws.Integrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), root, "worktree", "remove", ws.Path); err != nil {
		t.Fatal(err)
	}
	if err := ws.Cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup after already completed remove: %v", err)
	}
	if ws.State != "cleaned" {
		t.Fatalf("state=%q want cleaned", ws.State)
	}
}

func TestNativeRunnerWorkspaceCleanupPathlessIntentDoesNotCreate(t *testing.T) {
	root := nativeRunnerWorkspaceTestRepo(t)
	intent := &nativeRunnerWorkspace{Mode: NativeRunnerWorkspaceWorktree, ProjectID: "project", TaskID: "task-never-created", Title: "取消"}
	cleaned, err := nativeCleanupWorkspace(context.Background(), root, intent)
	if err != nil {
		t.Fatalf("pathless never-created cleanup: %v", err)
	}
	if cleaned.State != "cleaned" {
		t.Fatalf("state=%q want cleaned", cleaned.State)
	}
	if _, err := os.Stat(cleaned.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pathless cleanup created a directory: %v", err)
	}

	prepared, err := Prepare(context.Background(), root, "project", "task-existing", "取消", "task.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	pathlessExisting := &nativeRunnerWorkspace{Mode: NativeRunnerWorkspaceWorktree, ProjectID: "project", TaskID: "task-existing", Title: "取消"}
	cleaned, err = nativeCleanupWorkspace(context.Background(), root, pathlessExisting)
	if err != nil {
		t.Fatalf("pathless existing cleanup: %v", err)
	}
	if _, err := os.Stat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pathless existing worktree remains: %v", err)
	}
}

func TestNativeRunnerWorkspaceRebaseUsesCurrentRootDiffForScope(t *testing.T) {
	root := nativeRunnerWorkspaceTestRepo(t)
	ws, err := Prepare(context.Background(), root, "project", "task-rebase", "rebase", "task.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Path, "task.txt"), []byte("task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), ws.Path, "add", "--", "task.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), ws.Path, "commit", "-m", "task"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), root, "add", "--", "main.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), root, "commit", "-m", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), ws.Path, "rebase", "main"); err != nil {
		t.Fatal(err)
	}
	if err := ws.Integrate(context.Background()); err != nil {
		t.Fatalf("rebased task rejected: %v", err)
	}
	if len(ws.ChangedPaths) != 1 || ws.ChangedPaths[0] != "task.txt" {
		t.Fatalf("rebase diff included main changes: base=%s paths=%v", ws.DiffBaseCommit, ws.ChangedPaths)
	}
	if err := ws.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNativeRunnerWorkspaceTibbsManagedRootIsFixed(t *testing.T) {
	if got := nativeRunnerManagedRoot(`V:\repos\GitHub\Tibbs`); got != `V:\temp\Tibbs\worktrees` {
		t.Fatalf("Tibbs managed root=%q", got)
	}
}

func nativeRunnerWorkspaceTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if _, err := nativeRunnerGit(context.Background(), root, "init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"user.name", "test"}, {"user.email", "test@example.invalid"}} {
		if _, err := nativeRunnerGit(context.Background(), root, "config", pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), root, "add", "--", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeRunnerGit(context.Background(), root, "commit", "-m", "base"); err != nil {
		t.Fatal(err)
	}
	return root
}
