package agent

// Restartable, bounded Git ownership for native runner task blocks.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode"

	"github.com/isguang2024/fast-spider/internal/node"
)

type NativeRunnerWorkspaceMode string

const (
	NativeRunnerWorkspaceShared   NativeRunnerWorkspaceMode = "shared"
	NativeRunnerWorkspaceWorktree NativeRunnerWorkspaceMode = "worktree"
	nativeRunnerWorkspaceShared                             = NativeRunnerWorkspaceShared
	nativeRunnerWorkspaceWorktree                           = NativeRunnerWorkspaceWorktree
)

type nativeRunnerWorkspaceMode = NativeRunnerWorkspaceMode

// nativeRunnerWorkspace is persisted by the controller. Path and Branch may
// be saved before creation so a retry can safely resume the same worktree.
type nativeRunnerWorkspace struct {
	Mode           NativeRunnerWorkspaceMode `json:"mode"`
	Path           string                    `json:"path"`
	Root           string                    `json:"root"`
	ProjectID      string                    `json:"projectId"`
	TaskID         string                    `json:"taskId"`
	Title          string                    `json:"title,omitempty"`
	Scope          string                    `json:"scope,omitempty"`
	Branch         string                    `json:"branch"`
	BaseCommit     string                    `json:"baseCommit"`
	HeadCommit     string                    `json:"headCommit,omitempty"`
	SealedCommit   string                    `json:"sealedCommit,omitempty"`
	DiffBaseCommit string                    `json:"diffBaseCommit,omitempty"`
	Integration    string                    `json:"integration,omitempty"`
	State          string                    `json:"state,omitempty"`
	Error          string                    `json:"error,omitempty"`
	ManagedRoot    string                    `json:"managedRoot,omitempty"`
	ChangedPaths   []string                  `json:"changedPaths,omitempty"`
}

type Workspace = nativeRunnerWorkspace

// Prepare creates or reuses a task-owned linked worktree. It returns the
// populated intent alongside an error so callers can persist Path and Branch.
func Prepare(ctx context.Context, root, projectID, taskID, title, scope string, existing *Workspace) (*Workspace, error) {
	return prepareNativeRunnerWorkspace(ctx, root, projectID, taskID, title, scope, existing)
}

func nativePrepareWorkspace(ctx context.Context, root, projectID, taskID, title, scope string, existing *nativeRunnerWorkspace) (*nativeRunnerWorkspace, error) {
	return prepareNativeRunnerWorkspace(ctx, root, projectID, taskID, title, scope, existing)
}

func nativeIntegrateWorkspace(ctx context.Context, root, scope string, workspace *nativeRunnerWorkspace) (*nativeRunnerWorkspace, error) {
	if workspace == nil {
		return nil, errors.New("workspace is nil")
	}
	if strings.TrimSpace(root) != "" {
		resolved, err := nativeRunnerGitRoot(ctx, root)
		if err != nil {
			workspace.Error = err.Error()
			workspace.State = "error"
			return workspace, err
		}
		workspace.Root = resolved
		if workspace.ManagedRoot == "" {
			workspace.ManagedRoot = nativeRunnerManagedRoot(resolved)
		}
	}
	if strings.TrimSpace(scope) != "" {
		workspace.Scope = strings.TrimSpace(scope)
	}
	return workspace, workspace.Integrate(ctx)
}

func nativeCleanupWorkspace(ctx context.Context, root string, workspace *nativeRunnerWorkspace) (*nativeRunnerWorkspace, error) {
	if workspace == nil {
		return nil, errors.New("workspace is nil")
	}
	if strings.TrimSpace(root) != "" {
		resolved, err := nativeRunnerGitRoot(ctx, root)
		if err != nil {
			workspace.Error = err.Error()
			workspace.State = "error"
			return workspace, err
		}
		workspace.Root = resolved
		if workspace.ManagedRoot == "" {
			workspace.ManagedRoot = nativeRunnerManagedRoot(resolved)
		}
	}
	if err := nativeRunnerNormalizeCleanupIntent(workspace); err != nil {
		workspace.Error = err.Error()
		workspace.State = "error"
		return workspace, err
	}
	return workspace, workspace.Cleanup(ctx)
}

func nativeRunnerNormalizeCleanupIntent(w *nativeRunnerWorkspace) error {
	if w == nil || w.Mode == NativeRunnerWorkspaceShared {
		return nil
	}
	if strings.TrimSpace(w.Root) == "" {
		return errors.New("cleanup workspace root is required")
	}
	if w.ManagedRoot == "" || nativeRunnerIsTibbsRoot(w.Root) {
		w.ManagedRoot = nativeRunnerManagedRoot(w.Root)
	}
	if w.Branch == "" {
		if w.ProjectID == "" || w.TaskID == "" {
			return errors.New("cleanup intent needs projectID and taskID when branch is absent")
		}
		w.Branch = nativeRunnerWorkspaceBranch(w.ProjectID, w.TaskID, w.Title)
	}
	if w.Path == "" {
		if w.ProjectID == "" || w.TaskID == "" {
			return errors.New("cleanup intent needs projectID and taskID when path is absent")
		}
		w.Path = filepath.Join(w.ManagedRoot, nativeRunnerWorkspaceID(w.ProjectID, w.TaskID))
	}
	w.Path = filepath.Clean(w.Path)
	if !nativeRunnerPathWithin(w.Path, w.ManagedRoot) || !nativeRunnerBranchOwned(w.Branch, w.ProjectID, w.TaskID) {
		return errors.New("cleanup workspace path or branch is outside its task ownership")
	}
	return nil
}

func prepareNativeRunnerWorkspace(ctx context.Context, root, projectID, taskID, title, scope string, existing *nativeRunnerWorkspace) (*nativeRunnerWorkspace, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("workspace root is required")
	}
	actualRoot, err := nativeRunnerGitRoot(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("resolve git root: %w", err)
	}
	base, err := nativeRunnerGitHead(ctx, actualRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve git HEAD: %w", err)
	}
	w := &nativeRunnerWorkspace{Mode: NativeRunnerWorkspaceWorktree, Root: actualRoot, ProjectID: strings.TrimSpace(projectID), TaskID: strings.TrimSpace(taskID), Title: strings.TrimSpace(title), Scope: strings.TrimSpace(scope), BaseCommit: base, Integration: "pending", State: "preparing"}
	intentOnly := existing != nil && strings.TrimSpace(existing.Path) == "" && strings.TrimSpace(existing.Branch) == "" && strings.TrimSpace(existing.BaseCommit) == ""
	if existing != nil {
		*w = *existing
		w.Root = actualRoot
		if w.Mode == "" {
			w.Mode = NativeRunnerWorkspaceWorktree
		}
		if w.ProjectID == "" {
			w.ProjectID = strings.TrimSpace(projectID)
		}
		if w.TaskID == "" {
			w.TaskID = strings.TrimSpace(taskID)
		}
		if w.Title == "" {
			w.Title = strings.TrimSpace(title)
		}
		if w.Scope == "" {
			w.Scope = strings.TrimSpace(scope)
		}
		if w.BaseCommit == "" && existing == nil {
			w.BaseCommit = base
		}
		if w.Integration == "" {
			w.Integration = "pending"
		}
	}
	if w.Mode == NativeRunnerWorkspaceShared {
		w.Path = actualRoot
		if w.Branch == "" {
			w.Branch, _ = nativeRunnerGitBranch(ctx, actualRoot)
		}
		w.State, w.Error = "prepared", ""
		return w, nil
	}
	if w.Mode != NativeRunnerWorkspaceWorktree {
		return nativeRunnerWorkspaceFailure(w, fmt.Errorf("unsupported workspace mode %q", w.Mode))
	}
	if w.ProjectID == "" || w.TaskID == "" {
		return nativeRunnerWorkspaceFailure(w, errors.New("projectID and taskID are required for a worktree workspace"))
	}
	managedRoot := nativeRunnerManagedRoot(actualRoot)
	if w.ManagedRoot != "" && !nativeRunnerIsTibbsRoot(actualRoot) {
		managedRoot = filepath.Clean(w.ManagedRoot)
	}
	w.ManagedRoot = managedRoot
	if w.Path == "" {
		w.Path = filepath.Join(managedRoot, nativeRunnerWorkspaceID(w.ProjectID, w.TaskID))
	}
	w.Path = filepath.Clean(w.Path)
	if w.Branch == "" && w.Path != "" {
		if _, statErr := os.Stat(w.Path); statErr == nil {
			if branch, branchErr := nativeRunnerGitBranch(ctx, w.Path); branchErr == nil {
				w.Branch = branch
			}
		}
	}
	if w.Branch == "" {
		w.Branch = nativeRunnerWorkspaceBranch(w.ProjectID, w.TaskID, w.Title)
	}
	if !nativeRunnerPathWithin(w.Path, managedRoot) {
		return nativeRunnerWorkspaceFailure(w, errors.New("workspace path is outside its managed root"))
	}
	if !nativeRunnerBranchOwned(w.Branch, w.ProjectID, w.TaskID) {
		return nativeRunnerWorkspaceFailure(w, fmt.Errorf("workspace branch %q is not owned by project/task", w.Branch))
	}
	if w.BaseCommit == "" {
		if intentOnly && !nativeRunnerGitRefExists(ctx, actualRoot, "refs/heads/"+w.Branch) {
			w.BaseCommit = base
		} else {
			recovered, recoverErr := nativeRunnerRecoverWorkspaceBase(ctx, actualRoot, w)
			if recoverErr != nil {
				return nativeRunnerWorkspaceFailure(w, recoverErr)
			}
			w.BaseCommit = recovered
		}
	} else if _, checkErr := nativeRunnerGit(ctx, actualRoot, "cat-file", "-e", w.BaseCommit+"^{commit}"); checkErr != nil {
		return nativeRunnerWorkspaceFailure(w, fmt.Errorf("validate workspace base %s: %w", w.BaseCommit, checkErr))
	}
	if nativeRunnerGitRefExists(ctx, actualRoot, "refs/heads/"+w.Branch) {
		branchHead, headErr := nativeRunnerGit(ctx, actualRoot, "rev-parse", w.Branch)
		if headErr != nil {
			return nativeRunnerWorkspaceFailure(w, fmt.Errorf("read owned branch %s: %w", w.Branch, headErr))
		}
		based, ancestryErr := nativeRunnerIsAncestor(ctx, actualRoot, w.BaseCommit, strings.TrimSpace(branchHead))
		if ancestryErr != nil {
			return nativeRunnerWorkspaceFailure(w, fmt.Errorf("validate owned branch base: %w", ancestryErr))
		}
		if !based {
			return nativeRunnerWorkspaceFailure(w, fmt.Errorf("owned branch %s does not descend from workspace base %s", w.Branch, w.BaseCommit))
		}
	}
	if err := os.MkdirAll(managedRoot, 0o700); err != nil {
		return nativeRunnerWorkspaceFailure(w, fmt.Errorf("create worktree parent: %w", err))
	}
	if info, statErr := os.Stat(w.Path); statErr == nil {
		if !info.IsDir() {
			return nativeRunnerWorkspaceFailure(w, fmt.Errorf("workspace path exists but is not a directory: %s", w.Path))
		}
		if err := nativeRunnerWorkspaceVerifyRegistration(ctx, w); err != nil {
			return nativeRunnerWorkspaceFailure(w, err)
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		args := []string{"worktree", "add"}
		if nativeRunnerGitRefExists(ctx, actualRoot, "refs/heads/"+w.Branch) {
			args = append(args, w.Path, w.Branch)
		} else {
			args = append(args, "-b", w.Branch, w.Path, w.BaseCommit)
		}
		if _, runErr := nativeRunnerGit(ctx, actualRoot, args...); runErr != nil {
			return nativeRunnerWorkspaceFailure(w, fmt.Errorf("create worktree: %w", runErr))
		}
		if verifyErr := nativeRunnerWorkspaceVerifyRegistration(ctx, w); verifyErr != nil {
			return nativeRunnerWorkspaceFailure(w, verifyErr)
		}
	} else {
		return nativeRunnerWorkspaceFailure(w, fmt.Errorf("inspect workspace path: %w", statErr))
	}
	if err := nativeRunnerEnsureTibbsFrontendDeps(ctx, actualRoot, w.Path); err != nil {
		return nativeRunnerWorkspaceFailure(w, err)
	}
	w.Error, w.State = "", "prepared"
	w.HeadCommit, _ = nativeRunnerGitHead(ctx, w.Path)
	return w, nil
}

func (w *nativeRunnerWorkspace) Run(ctx context.Context, args ...string) (string, error) {
	return w.runGit(ctx, args...)
}
func (w *nativeRunnerWorkspace) run(ctx context.Context, args ...string) (string, error) {
	return w.runGit(ctx, args...)
}

func (w *nativeRunnerWorkspace) runGit(ctx context.Context, args ...string) (string, error) {
	if w == nil {
		return "", errors.New("workspace is nil")
	}
	directory := strings.TrimSpace(w.Path)
	if directory == "" {
		directory = strings.TrimSpace(w.Root)
	}
	return nativeRunnerGit(ctx, directory, args...)
}

// Inspect verifies the task worktree is clean, on the expected branch, based
// on BaseCommit, and has changed only paths inside Scope.
func (w *nativeRunnerWorkspace) Inspect(ctx context.Context) error { return w.inspect(ctx) }

func (w *nativeRunnerWorkspace) inspect(ctx context.Context) error {
	if err := w.validateIdentity(ctx); err != nil {
		return w.workspaceError("inspect", err)
	}
	if w.Mode == NativeRunnerWorkspaceShared {
		w.HeadCommit, _ = nativeRunnerGitHead(ctx, w.Root)
		w.State, w.Error = "ready", ""
		return nil
	}
	status, err := nativeRunnerGit(ctx, w.Path, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return w.workspaceError("inspect status", err)
	}
	if strings.Trim(status, "\x00\r\n \t") != "" {
		return w.workspaceError("inspect", errors.New("source worktree is dirty; commit changes before integration"))
	}
	head, err := nativeRunnerGitHead(ctx, w.Path)
	if err != nil {
		return w.workspaceError("inspect HEAD", err)
	}
	w.HeadCommit = head
	if w.BaseCommit == "" {
		return w.workspaceError("inspect", errors.New("workspace has no base commit"))
	}
	ancestor, err := nativeRunnerIsAncestor(ctx, w.Path, w.BaseCommit, head)
	if err != nil {
		return w.workspaceError("inspect ancestry", err)
	}
	if !ancestor {
		return w.workspaceError("inspect", fmt.Errorf("workspace HEAD %s is not based on base commit %s", head, w.BaseCommit))
	}
	diffBase := w.BaseCommit
	if rootHead, rootErr := nativeRunnerGitHead(ctx, w.Root); rootErr == nil {
		if onCurrentRoot, ancestryErr := nativeRunnerIsAncestor(ctx, w.Path, rootHead, head); ancestryErr == nil && onCurrentRoot {
			diffBase = rootHead
		}
	}
	paths, err := nativeRunnerChangedPaths(ctx, w.Path, diffBase, head)
	if err != nil {
		return w.workspaceError("inspect changed paths", err)
	}
	w.ChangedPaths = paths
	w.DiffBaseCommit = diffBase
	if err := nativeRunnerCheckScopeAtRoot(paths, w.Scope, w.Root); err != nil {
		return w.workspaceError("inspect scope", err)
	}
	w.Integration, w.State, w.Error = "pending", "ready", ""
	return nil
}

// Integrate performs only a serial fast-forward of Root. Existing dirty Root
// paths are allowed when they do not overlap the candidate commit.
func (w *nativeRunnerWorkspace) Integrate(ctx context.Context) error { return w.integrate(ctx) }

func (w *nativeRunnerWorkspace) integrate(ctx context.Context) error {
	if w == nil {
		return errors.New("workspace is nil")
	}
	if w.Mode == NativeRunnerWorkspaceShared {
		w.Integration, w.State, w.Error = "integrated", "integrated", ""
		return nil
	}
	if err := w.Inspect(ctx); err != nil {
		return err
	}
	if w.SealedCommit != "" && w.HeadCommit != w.SealedCommit {
		return w.workspaceError("integrate", errors.New("task branch changed after result checks; resubmit the current commit"))
	}
	rootHead, err := nativeRunnerGitHead(ctx, w.Root)
	if err != nil {
		return w.workspaceError("integrate root HEAD", err)
	}
	candidateHead := w.HeadCommit
	if rootHead == candidateHead {
		w.Integration, w.State, w.Error = "integrated", "integrated", ""
		return nil
	}
	if already, checkErr := nativeRunnerIsAncestor(ctx, w.Root, candidateHead, rootHead); checkErr != nil {
		return w.workspaceError("integrate existing commit check", checkErr)
	} else if already {
		w.Integration, w.State, w.Error = "integrated", "integrated", ""
		return nil
	}
	if canFastForward, checkErr := nativeRunnerIsAncestor(ctx, w.Root, w.BaseCommit, rootHead); checkErr != nil {
		return w.workspaceError("integrate base check", checkErr)
	} else if !canFastForward {
		return w.workspaceRebaseNeeded(fmt.Errorf("repository HEAD %s diverged from workspace base %s; rebase-needed", rootHead, w.BaseCommit))
	}
	if canFastForward, checkErr := nativeRunnerIsAncestor(ctx, w.Root, rootHead, candidateHead); checkErr != nil {
		return w.workspaceError("integrate candidate ancestry", checkErr)
	} else if !canFastForward {
		return w.workspaceRebaseNeeded(fmt.Errorf("candidate HEAD %s diverged from repository HEAD %s; rebase-needed", candidateHead, rootHead))
	}
	dirty, err := nativeRunnerDirtyPaths(ctx, w.Root)
	if err != nil {
		return w.workspaceError("integrate dirty paths", err)
	}
	if overlap := nativeRunnerPathOverlap(dirty, w.ChangedPaths); len(overlap) > 0 {
		return w.workspaceError("integrate overlap", fmt.Errorf("repository has overlapping unsaved paths: %s", strings.Join(overlap, ", ")))
	}
	w.State = "integrating"
	if _, err := nativeRunnerGit(ctx, w.Root, "merge", "--ff-only", candidateHead); err != nil {
		return w.workspaceError("integrate fast-forward", err)
	}
	finalHead, err := nativeRunnerGitHead(ctx, w.Root)
	if err != nil {
		return w.workspaceError("integrate final HEAD", err)
	}
	if finalHead != candidateHead {
		return w.workspaceError("integrate", fmt.Errorf("fast-forward ended at %s, expected %s", finalHead, candidateHead))
	}
	w.Integration, w.State, w.Error = "integrated", "integrated", ""
	return nil
}

// Cleanup uses the exact registered path and never force-removes a worktree.
func (w *nativeRunnerWorkspace) Cleanup(ctx context.Context) error { return w.cleanup(ctx) }

func (w *nativeRunnerWorkspace) cleanup(ctx context.Context) error {
	if w == nil {
		return errors.New("workspace is nil")
	}
	if w.State == "cleaned" {
		return nil
	}
	if w.Mode == NativeRunnerWorkspaceShared {
		w.State = "cleaned"
		return nil
	}
	if err := nativeRunnerNormalizeCleanupIntent(w); err != nil {
		return w.workspaceError("cleanup", err)
	}
	if _, statErr := os.Stat(w.Path); errors.Is(statErr, os.ErrNotExist) {
		registered, registrationErr := nativeRunnerWorkspaceIsRegistered(ctx, w)
		if registrationErr != nil {
			return w.workspaceError("cleanup registration", registrationErr)
		}
		if !registered {
			w.State, w.Error = "cleaned", ""
			return nil
		}
		return w.workspaceError("cleanup", errors.New("workspace path is missing but remains registered; refusing to recreate or remove it"))
	} else if statErr != nil {
		return w.workspaceError("cleanup path", statErr)
	}
	if err := w.validateIdentity(ctx); err != nil {
		return w.workspaceError("cleanup", err)
	}
	if err := nativeRunnerWorkspaceVerifyRegistration(ctx, w); err != nil {
		return w.workspaceError("cleanup", err)
	}
	status, err := nativeRunnerGit(ctx, w.Path, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return w.workspaceError("cleanup status", err)
	}
	if strings.Trim(status, "\x00\r\n \t") != "" {
		return w.workspaceError("cleanup", errors.New("worktree has unsaved files; preserving it until the task is integrated or repaired"))
	}
	candidate, headErr := nativeRunnerGitHead(ctx, w.Path)
	if headErr != nil {
		return w.workspaceError("cleanup workspace HEAD", headErr)
	}
	rootHead, rootErr := nativeRunnerGitHead(ctx, w.Root)
	if rootErr != nil {
		return w.workspaceError("cleanup main HEAD", rootErr)
	}
	merged, ancestryErr := nativeRunnerIsAncestor(ctx, w.Root, candidate, rootHead)
	if ancestryErr != nil {
		return w.workspaceError("cleanup ancestry", ancestryErr)
	}
	if !merged {
		return w.workspaceError("cleanup", errors.New("workspace has an unmerged commit; preserving it for integration or task repair"))
	}
	w.Integration = "integrated"
	if _, err := nativeRunnerGit(ctx, w.Root, "worktree", "remove", w.Path); err != nil {
		return w.workspaceError("cleanup remove", err)
	}
	w.State, w.Error = "cleaned", ""
	return nil
}

func (w *nativeRunnerWorkspace) validateIdentity(ctx context.Context) error {
	if w == nil {
		return errors.New("workspace is nil")
	}
	if strings.TrimSpace(w.Root) == "" || strings.TrimSpace(w.Path) == "" {
		return errors.New("workspace root and path are required")
	}
	actualRoot, err := nativeRunnerGitRoot(ctx, w.Path)
	if err != nil {
		return fmt.Errorf("resolve workspace root: %w", err)
	}
	if w.Mode == NativeRunnerWorkspaceWorktree {
		if nativeRunnerIsTibbsRoot(w.Root) {
			w.ManagedRoot = nativeRunnerManagedRoot(w.Root)
		} else if w.ManagedRoot == "" {
			w.ManagedRoot = filepath.Join(filepath.Dir(w.Root), ".fast-spider-worktrees")
		}
		// rev-parse --show-toplevel returns the linked worktree path for a
		// linked checkout. The primary-root relationship is proven by the
		// repository's registered worktree record below.
		if !nativeRunnerSamePath(actualRoot, w.Path) {
			return fmt.Errorf("workspace root does not match its path: %s", actualRoot)
		}
		if err := nativeRunnerWorkspaceVerifyRegistration(ctx, w); err != nil {
			return err
		}
		branch, err := nativeRunnerGitBranch(ctx, w.Path)
		if err != nil {
			return fmt.Errorf("resolve workspace branch: %w", err)
		}
		if branch != w.Branch {
			return fmt.Errorf("workspace branch %q does not match task branch %q", branch, w.Branch)
		}
		if !nativeRunnerPathWithin(w.Path, w.ManagedRoot) {
			return errors.New("workspace path is outside its managed root")
		}
	} else if !nativeRunnerSamePath(actualRoot, w.Root) {
		return fmt.Errorf("workspace belongs to %s, expected %s", actualRoot, w.Root)
	}
	return nil
}

func (w *nativeRunnerWorkspace) workspaceError(operation string, err error) error {
	if w == nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	w.Error = fmt.Sprintf("%s: %v", operation, err)
	if strings.Contains(strings.ToLower(w.Error), "rebase-needed") {
		w.State, w.Integration = "rebase-needed", "rebase-needed"
	} else if w.State != "cleaned" {
		w.State = "error"
	}
	return errors.New(w.Error)
}

func (w *nativeRunnerWorkspace) workspaceRebaseNeeded(err error) error {
	if w == nil {
		return err
	}
	w.Error, w.State, w.Integration = err.Error(), "rebase-needed", "rebase-needed"
	return err
}

func nativeRunnerWorkspaceFailure(w *nativeRunnerWorkspace, err error) (*nativeRunnerWorkspace, error) {
	if w != nil {
		w.Error, w.State = err.Error(), "error"
	}
	return w, err
}

func nativeRunnerGit(ctx context.Context, directory string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	directory = filepath.Clean(strings.TrimSpace(directory))
	if directory == "." || directory == "" {
		return "", errors.New("git directory is required")
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, args...)...)
	node.ConfigureBackgroundCommand(cmd)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if message != "" {
			return stdout.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, message)
		}
		return stdout.String(), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

func nativeRunnerGitRoot(ctx context.Context, directory string) (string, error) {
	root, err := nativeRunnerGit(ctx, directory, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Clean(strings.TrimSpace(root)))
}

func nativeRunnerGitHead(ctx context.Context, directory string) (string, error) {
	head, err := nativeRunnerGit(ctx, directory, "rev-parse", "HEAD")
	return strings.TrimSpace(head), err
}

func nativeRunnerGitBranch(ctx context.Context, directory string) (string, error) {
	branch, err := nativeRunnerGit(ctx, directory, "symbolic-ref", "--quiet", "--short", "HEAD")
	return strings.TrimSpace(branch), err
}

func nativeRunnerGitRefExists(ctx context.Context, directory, ref string) bool {
	_, err := nativeRunnerGit(ctx, directory, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

func nativeRunnerIsAncestor(ctx context.Context, directory, ancestor, descendant string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", directory, "merge-base", "--is-ancestor", ancestor, descendant)
	node.ConfigureBackgroundCommand(cmd)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func nativeRunnerChangedPaths(ctx context.Context, directory, base, head string) ([]string, error) {
	raw, err := nativeRunnerGit(ctx, directory, "diff", "--name-only", "--no-renames", "-z", base, head, "--")
	return nativeRunnerNULPaths(raw), err
}

func nativeRunnerDirtyPaths(ctx context.Context, directory string) ([]string, error) {
	raw, err := nativeRunnerGit(ctx, directory, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	return nativeRunnerStatusPaths(raw), err
}

func nativeRunnerNULPaths(raw string) []string {
	var paths []string
	for _, value := range strings.Split(raw, "\x00") {
		value = filepath.ToSlash(strings.TrimSpace(value))
		if value != "" {
			paths = append(paths, value)
		}
	}
	return nativeRunnerUniquePaths(paths)
}

func nativeRunnerStatusPaths(raw string) []string {
	var paths []string
	parts := strings.Split(raw, "\x00")
	for i := 0; i < len(parts); i++ {
		value := parts[i]
		if len(value) < 3 {
			continue
		}
		status, path := value[:2], strings.TrimSpace(value[3:])
		if path != "" {
			paths = append(paths, filepath.ToSlash(path))
		}
		if strings.Contains(status, "R") || strings.Contains(status, "C") {
			if i+1 < len(parts) && parts[i+1] != "" {
				paths = append(paths, filepath.ToSlash(parts[i+1]))
				i++
			}
		}
	}
	return nativeRunnerUniquePaths(paths)
}

func nativeRunnerUniquePaths(paths []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		path = filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(path), "./"))
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func nativeRunnerCheckScope(paths []string, scope string) error {
	return nativeRunnerCheckScopeAtRoot(paths, scope, "")
}

func nativeRunnerCheckScopeAtRoot(paths []string, scope, root string) error {
	scopes := nativeRunnerScopePaths(scope)
	if root != "" {
		for i, prefix := range scopes {
			if filepath.IsAbs(filepath.FromSlash(prefix)) {
				rel, err := filepath.Rel(root, filepath.FromSlash(prefix))
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return fmt.Errorf("scope path %q is outside repository", prefix)
				}
				scopes[i] = filepath.ToSlash(rel)
			}
		}
	}
	for _, path := range paths {
		allowed := len(scopes) == 0
		for _, prefix := range scopes {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("changed path %q is outside task scope", path)
		}
	}
	return nil
}

func nativeRunnerScopePaths(scope string) []string {
	var result []string
	for _, value := range strings.FieldsFunc(scope, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' }) {
		value = filepath.ToSlash(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(value), "./"), "/"))
		if value != "" {
			result = append(result, value)
		}
	}
	return nativeRunnerUniquePaths(result)
}

func nativeRunnerPathOverlap(left, right []string) []string {
	var overlap []string
	for _, a := range left {
		for _, b := range right {
			if a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") {
				overlap = append(overlap, a)
				break
			}
		}
	}
	return nativeRunnerUniquePaths(overlap)
}

func nativeRunnerWorkspaceVerifyRegistration(ctx context.Context, w *nativeRunnerWorkspace) error {
	branch, registered, err := nativeRunnerWorkspaceRegistration(ctx, w)
	if err != nil {
		return err
	}
	if !registered {
		return fmt.Errorf("workspace path is not a registered linked worktree: %s", filepath.Clean(w.Path))
	}
	if branch != "refs/heads/"+w.Branch {
		return fmt.Errorf("registered worktree branch %q does not match %q", branch, "refs/heads/"+w.Branch)
	}
	return nil
}

func nativeRunnerWorkspaceIsRegistered(ctx context.Context, w *nativeRunnerWorkspace) (bool, error) {
	_, registered, err := nativeRunnerWorkspaceRegistration(ctx, w)
	return registered, err
}

func nativeRunnerWorkspaceRegistration(ctx context.Context, w *nativeRunnerWorkspace) (string, bool, error) {
	if w == nil {
		return "", false, errors.New("workspace is nil")
	}
	worktrees, err := nativeRunnerGit(ctx, w.Root, "worktree", "list", "--porcelain")
	if err != nil {
		return "", false, fmt.Errorf("list worktrees: %w", err)
	}
	path, registered, branch := filepath.Clean(w.Path), false, ""
	for _, line := range strings.Split(worktrees, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "worktree ") {
			registered = nativeRunnerSamePath(strings.TrimSpace(strings.TrimPrefix(line, "worktree ")), path)
			branch = ""
			continue
		}
		if registered && strings.HasPrefix(line, "branch ") {
			branch = strings.TrimPrefix(line, "branch ")
			break
		}
	}
	return branch, registered, nil
}

func nativeRunnerRecoverWorkspaceBase(ctx context.Context, root string, w *nativeRunnerWorkspace) (string, error) {
	if w == nil || strings.TrimSpace(w.Branch) == "" {
		return "", errors.New("workspace base commit is missing and no owned branch is available for recovery")
	}
	ref := "refs/heads/" + w.Branch
	raw, err := nativeRunnerGit(ctx, root, "reflog", "show", "--format=%H%x09%gs", ref)
	if err != nil {
		return "", fmt.Errorf("workspace base commit is missing; recover owned branch %s: %w", w.Branch, err)
	}
	var recovered string
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), "\t", 2)
		if len(fields) != 2 || !strings.Contains(strings.ToLower(fields[1]), "created from") {
			continue
		}
		recovered = strings.TrimSpace(fields[0])
	}
	if recovered == "" {
		return "", fmt.Errorf("workspace base commit is missing; owned branch %s has no creation reflog evidence", w.Branch)
	}
	if _, err := nativeRunnerGit(ctx, root, "cat-file", "-e", recovered+"^{commit}"); err != nil {
		return "", fmt.Errorf("recovered workspace base commit %s is unavailable: %w", recovered, err)
	}
	return recovered, nil
}

func nativeRunnerWorkspaceID(projectID, taskID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(projectID) + "\x00" + strings.TrimSpace(taskID)))
	return "task-" + hex.EncodeToString(sum[:])[:12]
}

func nativeRunnerWorkspaceBranch(projectID, taskID, title string) string {
	label := strings.TrimSpace(title)
	if label == "" {
		label = strings.TrimSpace(taskID)
	}
	var labelBuilder strings.Builder
	for _, r := range label {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.':
			labelBuilder.WriteRune(r)
		default:
			labelBuilder.WriteByte('-')
		}
	}
	label = labelBuilder.String()
	label = strings.Trim(label, "-._")
	if label == "" {
		label = "执行"
	}
	if runes := []rune(label); len(runes) > 28 {
		label = string(runes[:28])
	}
	return "codex/任务-" + label + "-" + nativeRunnerWorkspaceID(projectID, taskID)[5:]
}

func nativeRunnerBranchOwned(branch, projectID, taskID string) bool {
	return strings.HasPrefix(branch, "codex/任务-") && strings.HasSuffix(branch, "-"+nativeRunnerWorkspaceID(projectID, taskID)[5:])
}

func nativeRunnerManagedRoot(root string) string {
	if nativeRunnerIsTibbsRoot(root) {
		return `V:\temp\Tibbs\worktrees`
	}
	return filepath.Join(filepath.Dir(root), ".fast-spider-worktrees")
}

func nativeRunnerIsTibbsRoot(root string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	root, _ = filepath.Abs(filepath.Clean(root))
	expected, _ := filepath.Abs(filepath.Clean(`V:\repos\GitHub\Tibbs`))
	return strings.EqualFold(root, expected) && filepath.IsAbs(filepath.Join(root, "project", "tools", "frontend-deps.ps1"))
}

func nativeRunnerEnsureTibbsFrontendDeps(ctx context.Context, root, worktree string) error {
	if !nativeRunnerIsTibbsRoot(root) {
		return nil
	}
	if runtime.GOOS != "windows" {
		return errors.New("Tibbs frontend dependency Ensure requires Windows PowerShell")
	}
	script := filepath.Join(root, "project", "tools", "frontend-deps.ps1")
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("Tibbs frontend dependency script unavailable: %w", err)
	}
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-File", script, "-Action", "Ensure", "-RepoRoot", worktree, "-StoreRoot", `V:\temp\Tibbs\dependencies`)
	node.ConfigureBackgroundCommand(cmd)
	cmd.Dir = worktree
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if message != "" {
			return fmt.Errorf("Tibbs frontend dependency Ensure failed: %w: %s", err, message)
		}
		return fmt.Errorf("Tibbs frontend dependency Ensure failed: %w", err)
	}
	return nil
}

func nativeRunnerPathWithin(path, parent string) bool {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(parent) == "" {
		return false
	}
	path, parent = filepath.Clean(path), filepath.Clean(parent)
	if nativeRunnerSamePath(path, parent) {
		return false
	}
	rel, err := filepath.Rel(parent, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func nativeRunnerSamePath(left, right string) bool {
	left, _ = filepath.Abs(filepath.Clean(left))
	right, _ = filepath.Abs(filepath.Clean(right))
	if strings.EqualFold(left, right) {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}
