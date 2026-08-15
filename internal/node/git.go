package node

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/security"
)

const (
	maxGitOutputBytes = 128 << 10
	gitReadTimeout    = 30 * time.Second
	gitWriteTimeout   = 2 * time.Minute
	gitNetworkTimeout = 10 * time.Minute
)

var (
	ErrGitNotFound       = errors.New("git not found")
	ErrNotRepository     = errors.New("not a git repository")
	ErrGitHooksDenied    = errors.New("git hooks require local permission")
	ErrGitOutputTooLarge = errors.New("git output exceeds inline limit")
)

type gitControlParams struct {
	Action         string   `json:"action"`
	RepositoryPath string   `json:"repositoryPath"`
	Revision       string   `json:"revision,omitempty"`
	Paths          []string `json:"paths,omitempty"`
	Message        string   `json:"message,omitempty"`
	Remote         string   `json:"remote,omitempty"`
	Branch         string   `json:"branch,omitempty"`
	WorktreePath   string   `json:"worktreePath,omitempty"`
	IdempotencyKey string   `json:"idempotencyKey,omitempty"`
}

type gitControlResult struct {
	Action       string       `json:"action"`
	Output       string       `json:"output,omitempty"`
	Truncated    bool         `json:"truncated,omitempty"`
	ArtifactID   string       `json:"artifactId,omitempty"`
	Job          *JobSnapshot `json:"job,omitempty"`
	WorktreePath string       `json:"worktreePath,omitempty"`
}

func (c *Client) gitControl(ctx context.Context, params map[string]any) (gitControlResult, error) {
	var input gitControlParams
	if err := decodeParams(params, &input); err != nil {
		return gitControlResult{}, fmt.Errorf("invalid params: %w", err)
	}
	input.Action = strings.TrimSpace(input.Action)
	if input.Action == "" {
		return gitControlResult{}, fmt.Errorf("action is required")
	}
	if input.RepositoryPath == "" {
		return gitControlResult{}, fmt.Errorf("absolute repositoryPath is required")
	}
	repositoryPath, err := ResolveMachinePath(input.RepositoryPath)
	if err != nil {
		return gitControlResult{}, err
	}
	if err := ensureGitRepository(ctx, repositoryPath); err != nil {
		return gitControlResult{}, err
	}

	switch input.Action {
	case "status":
		return c.runGitRead(ctx, repositoryPath, input.Action, []string{"status", "--short", "--branch", "--untracked-files=normal"})
	case "diff":
		return c.runGitRead(ctx, repositoryPath, input.Action, []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--"})
	case "stagedDiff":
		return c.runGitRead(ctx, repositoryPath, input.Action, []string{"diff", "--cached", "--no-ext-diff", "--no-textconv", "--no-color", "--"})
	case "log":
		return c.runGitRead(ctx, repositoryPath, input.Action, []string{"log", "-n", "50", "--date=iso-strict", "--pretty=format:%H%x09%aI%x09%an%x09%s"})
	case "show":
		revision := input.Revision
		if revision == "" {
			revision = "HEAD"
		}
		if err := validateGitRef(revision); err != nil {
			return gitControlResult{}, err
		}
		return c.runGitRead(ctx, repositoryPath, input.Action, []string{"show", "--no-ext-diff", "--no-textconv", "--no-color", "--format=fuller", "--stat", "--patch", revision, "--"})
	case "branches":
		return c.runGitRead(ctx, repositoryPath, input.Action, []string{"branch", "--format=%(refname:short)%09%(objectname:short)%09%(upstream:short)"})
	case "currentBranch":
		return c.runGitRead(ctx, repositoryPath, input.Action, []string{"branch", "--show-current"})
	case "worktrees":
		return c.runGitRead(ctx, repositoryPath, input.Action, []string{"worktree", "list", "--porcelain"})
	case "add":
		paths, err := validateGitPaths(input.Paths)
		if err != nil {
			return gitControlResult{}, err
		}
		if len(paths) == 0 {
			return gitControlResult{}, fmt.Errorf("paths are required for git add")
		}
		if err := validateGitSideEffects(ctx, repositoryPath, input.Action, paths, ""); err != nil {
			return gitControlResult{}, err
		}
		return c.runGitWrite(ctx, repositoryPath, input.Action, append([]string{"add", "--"}, paths...))
	case "commit":
		message := strings.TrimSpace(input.Message)
		if message == "" || len(message) > 4096 {
			return gitControlResult{}, fmt.Errorf("commit message is required and must be at most 4096 bytes")
		}
		if err := validateGitSideEffects(ctx, repositoryPath, input.Action, nil, ""); err != nil {
			return gitControlResult{}, err
		}
		return c.runGitWrite(ctx, repositoryPath, input.Action, []string{"commit", "--no-gpg-sign", "-m", message})
	case "fetch", "pull", "push":
		if input.IdempotencyKey == "" || input.Remote == "" {
			return gitControlResult{}, fmt.Errorf("remote and idempotencyKey are required for git network actions")
		}
		if err := validateConfiguredRemote(ctx, repositoryPath, input.Remote, c.cfg.DataDir); err != nil {
			return gitControlResult{}, err
		}
		if err := validateGitSideEffects(ctx, repositoryPath, input.Action, nil, input.Remote); err != nil {
			return gitControlResult{}, err
		}
		args := []string{input.Action, input.Remote}
		if input.Branch != "" {
			if err := validateGitRef(input.Branch); err != nil {
				return gitControlResult{}, err
			}
			args = append(args, input.Branch)
		}
		gitArgv := append([]string{"git", "-c", "color.ui=false", "-c", "core.pager=cat", "-c", "core.fsmonitor=false", "-c", "diff.external=", "-c", "interactive.diffFilter="}, gitSideEffectCommandConfig(input.Remote)...)
		gitArgv = append(gitArgv, args...)
		job, err := c.jobs.StartShell(repositoryPath, gitArgv, gitNetworkTimeout, input.IdempotencyKey)
		if err != nil {
			return gitControlResult{}, err
		}
		return gitControlResult{Action: input.Action, Job: &job}, nil
	case "createWorktree":
		if input.Branch == "" {
			return gitControlResult{}, fmt.Errorf("branch is required")
		}
		if err := validateGitRef(input.Branch); err != nil {
			return gitControlResult{}, err
		}
		if err := validateGitSideEffects(ctx, repositoryPath, input.Action, nil, ""); err != nil {
			return gitControlResult{}, err
		}
		target := strings.TrimSpace(input.WorktreePath)
		if target == "" {
			folderID, err := security.RandomOpaque("wt_")
			if err != nil {
				return gitControlResult{}, err
			}
			managedRoot := filepath.Join(c.cfg.DataDir, "worktrees")
			if err := os.MkdirAll(managedRoot, 0o700); err != nil {
				return gitControlResult{}, err
			}
			target = filepath.Join(managedRoot, folderID)
		} else {
			if !filepath.IsAbs(target) || strings.IndexByte(target, 0) >= 0 {
				return gitControlResult{}, ErrAbsolutePathRequired
			}
			target = filepath.Clean(target)
			parent, err := ResolveMachinePath(filepath.Dir(target))
			if err != nil {
				return gitControlResult{}, err
			}
			target = filepath.Join(parent, filepath.Base(target))
		}
		created, err := c.runGitWrite(ctx, repositoryPath, input.Action, []string{"worktree", "add", target, input.Branch})
		if err != nil {
			return gitControlResult{}, err
		}
		created.WorktreePath = target
		return created, nil
	case "deleteWorktree":
		if strings.TrimSpace(input.WorktreePath) == "" {
			return gitControlResult{}, fmt.Errorf("absolute worktreePath is required")
		}
		target, err := ResolveMachinePath(input.WorktreePath)
		if err != nil {
			return gitControlResult{}, err
		}
		removed, err := c.runGitWrite(ctx, repositoryPath, input.Action, []string{"worktree", "remove", target})
		if err != nil {
			return gitControlResult{}, err
		}
		removed.WorktreePath = target
		return removed, nil
	default:
		return gitControlResult{}, fmt.Errorf("unsupported git action %q", input.Action)
	}
}

func (c *Client) runGitRead(ctx context.Context, root, action string, args []string) (gitControlResult, error) {
	commandCtx, cancel := context.WithTimeout(ctx, gitReadTimeout)
	defer cancel()
	captureFull := action == "diff" || action == "stagedDiff" || action == "show"
	var spool *os.File
	var spoolPath string
	if captureFull {
		if err := os.MkdirAll(filepath.Join(c.cfg.DataDir, "git-output"), 0o700); err != nil {
			return gitControlResult{}, err
		}
		var err error
		spool, err = os.CreateTemp(filepath.Join(c.cfg.DataDir, "git-output"), "git-*.txt")
		if err != nil {
			return gitControlResult{}, err
		}
		spoolPath = spool.Name()
		defer os.Remove(spoolPath)
	}
	output, truncated, tooLarge, err := runGitCommandCapture(commandCtx, root, args, spool)
	cancel()
	if spool != nil {
		if syncErr := spool.Sync(); err == nil && syncErr != nil {
			err = syncErr
		}
		if closeErr := spool.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}
	if err != nil {
		return gitControlResult{}, err
	}
	if tooLarge {
		return gitControlResult{}, ErrGitOutputTooLarge
	}
	result := gitControlResult{Action: action, Output: output, Truncated: truncated}
	if truncated && captureFull {
		logicalName := "git-" + strings.ToLower(action) + "-" + time.Now().UTC().Format("20060102T150405Z") + ".txt"
		artifact, err := c.uploadArtifactPath(ctx, "", logicalName, "text/plain; charset=utf-8", spoolPath)
		if err != nil {
			return gitControlResult{}, err
		}
		result.ArtifactID = artifact.ArtifactID
	}
	return result, nil
}

func (c *Client) runGitWrite(ctx context.Context, root, action string, args []string) (gitControlResult, error) {
	ctx, cancel := context.WithTimeout(ctx, gitWriteTimeout)
	defer cancel()
	safeArgs := append(gitSideEffectCommandConfig(""), args...)
	output, truncated, err := runGitCommand(ctx, root, safeArgs)
	if err != nil {
		return gitControlResult{}, err
	}
	return gitControlResult{Action: action, Output: output, Truncated: truncated}, nil
}

func runGitCommand(ctx context.Context, root string, args []string) (string, bool, error) {
	output, truncated, tooLarge, err := runGitCommandCapture(ctx, root, args, nil)
	if tooLarge && err == nil {
		err = ErrGitOutputTooLarge
	}
	return output, truncated, err
}

func runGitCommandCapture(ctx context.Context, root string, args []string, spool *os.File) (string, bool, bool, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", false, false, ErrGitNotFound
	}
	base := []string{"-C", root, "-c", "color.ui=false", "-c", "core.pager=cat", "-c", "core.fsmonitor=false", "-c", "diff.external=", "-c", "interactive.diffFilter="}
	cmd := exec.CommandContext(ctx, gitPath, append(base, args...)...)
	cmd.Env = append(safeShellEnvironment(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	stdoutWriter := &gitOutputWriter{preview: &limitedBuffer{buf: &stdout, limit: maxGitOutputBytes}, spool: spool, fullLimit: maxArtifactUploadBytes}
	stderrWriter := &limitedBuffer{buf: &stderr, limit: 32 << 10}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	err = cmd.Run()
	truncated := stdoutWriter.preview.truncated
	if err != nil {
		if ctx.Err() != nil {
			return "", truncated, stdoutWriter.tooLarge, ctx.Err()
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", truncated, stdoutWriter.tooLarge, fmt.Errorf("git command failed: %s", redactGitText(message))
	}
	return redactGitText(stdout.String()), truncated, stdoutWriter.tooLarge, nil
}

func ensureGitRepository(ctx context.Context, root string) error {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return ErrGitNotFound
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, gitPath, "-C", root, "rev-parse", "--show-toplevel")
	cmd.Env = append(safeShellEnvironment(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return ErrNotRepository
	}
	top := strings.TrimSpace(string(out))
	realTop, err := filepath.EvalSymlinks(top)
	if err != nil {
		return ErrNotRepository
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if !samePath(realTop, realRoot) {
		return fmt.Errorf("repositoryPath must point to the Git repository root")
	}
	return nil
}

func activeGitHooks(ctx context.Context, root string) (bool, error) {
	for _, hook := range []string{"pre-commit", "prepare-commit-msg", "commit-msg", "post-commit", "pre-push", "pre-merge-commit", "post-merge", "post-checkout"} {
		result, _, err := runGitCommand(ctx, root, []string{"rev-parse", "--git-path", "hooks/" + hook})
		if err != nil {
			return false, err
		}
		path := strings.TrimSpace(result)
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return true, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func validateGitSideEffects(ctx context.Context, root, action string, paths []string, remote string) error {
	hooks, err := activeGitHooks(ctx, root)
	if err != nil {
		return fmt.Errorf("inspect Git hooks: %w", err)
	}
	if hooks {
		return fmt.Errorf("%w: active Git hook", ErrGitHooksDenied)
	}

	switch action {
	case "add":
		usesFilters, err := gitPathsUseFilters(ctx, root, paths)
		if err != nil {
			return fmt.Errorf("inspect Git path filters: %w", err)
		}
		if !usesFilters {
			return nil
		}
		executable, err := hasExecutableGitFilters(ctx, root)
		if err != nil {
			return fmt.Errorf("inspect executable Git filters: %w", err)
		}
		if executable {
			return fmt.Errorf("%w: executable Git filter", ErrGitHooksDenied)
		}
	case "pull":
		executable, err := hasExecutableGitFilters(ctx, root)
		if err != nil {
			return fmt.Errorf("inspect executable Git filters: %w", err)
		}
		if executable {
			return fmt.Errorf("%w: executable Git filter or merge driver", ErrGitHooksDenied)
		}
		fallthrough
	case "fetch", "push":
		executable, err := hasExecutableGitNetworkConfig(ctx, root, remote)
		if err != nil {
			return fmt.Errorf("inspect executable Git network configuration: %w", err)
		}
		if executable {
			return fmt.Errorf("%w: executable Git network configuration", ErrGitHooksDenied)
		}
	case "createWorktree":
		executable, err := hasExecutableGitFilters(ctx, root)
		if err != nil {
			return fmt.Errorf("inspect executable Git filters: %w", err)
		}
		if executable {
			return fmt.Errorf("%w: executable Git checkout filter", ErrGitHooksDenied)
		}
	}
	return nil
}

func gitSideEffectCommandConfig(remote string) []string {
	args := []string{"-c", "core.hooksPath="}
	if remote == "" {
		return args
	}
	return append(args,
		"-c", "core.askPass=",
		"-c", "core.sshCommand=",
		"-c", "core.gitProxy=",
		"-c", "remote."+remote+".proxy=",
		"-c", "remote."+remote+".uploadpack=git-upload-pack",
		"-c", "remote."+remote+".receivepack=git-receive-pack",
		"-c", "remote."+remote+".vcs=",
	)
}

func validateGitRef(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || strings.HasPrefix(value, "-") || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("invalid git ref")
	}
	return nil
}

func validateConfiguredRemote(ctx context.Context, root, remote, nodeDataDir string) error {
	remote = strings.TrimSpace(remote)
	if remote == "" || len(remote) > 128 || strings.HasPrefix(remote, "-") || strings.ContainsAny(remote, "\x00\r\n/\\:") {
		return fmt.Errorf("invalid git remote")
	}
	result, _, err := runGitCommand(ctx, root, []string{"remote", "get-url", "--push", remote})
	if err != nil {
		return fmt.Errorf("git remote is not configured")
	}
	remoteURL := strings.TrimSpace(result)
	if remoteURL == "" || strings.ContainsAny(remoteURL, "\x00\r\n") {
		return fmt.Errorf("git remote URL is invalid")
	}
	lower := strings.ToLower(remoteURL)
	if strings.HasPrefix(lower, "ext::") || strings.Contains(lower, "::") {
		return fmt.Errorf("git remote helper transport is not allowed")
	}
	if strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "ssh://") {
		return nil
	}
	if strings.Contains(remoteURL, "@") && strings.Contains(remoteURL, ":") && !strings.Contains(remoteURL, "://") {
		return nil
	}
	localPath := remoteURL
	if strings.HasPrefix(lower, "file://") {
		localPath = strings.TrimPrefix(remoteURL, "file://")
	}
	localPath = filepath.FromSlash(localPath)
	if !filepath.IsAbs(localPath) {
		localPath = filepath.Join(root, localPath)
	}
	if !pathWithin(nodeDataDir, localPath) {
		return fmt.Errorf("local git remote must stay inside the Node data directory")
	}
	return nil
}

func hasExecutableGitNetworkConfig(ctx context.Context, root, remote string) (bool, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return false, ErrGitNotFound
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, key := range []string{
		"core.sshCommand",
		"core.gitProxy",
		"core.askPass",
		"remote." + remote + ".proxy",
		"remote." + remote + ".uploadpack",
		"remote." + remote + ".receivepack",
		"remote." + remote + ".vcs",
	} {
		cmd := exec.CommandContext(checkCtx, gitPath, "-C", root, "config", "--get", key)
		cmd.Env = safeShellEnvironment()
		out, err := cmd.Output()
		if err == nil {
			if strings.TrimSpace(string(out)) != "" {
				return true, nil
			}
			continue
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			continue
		}
		if checkCtx.Err() != nil {
			return false, checkCtx.Err()
		}
		return false, err
	}
	hasHelper, err := hasRepositoryCredentialHelper(checkCtx, gitPath, root)
	if err != nil {
		return false, err
	}
	if hasHelper {
		return true, nil
	}
	return false, nil
}

func hasRepositoryCredentialHelper(ctx context.Context, gitPath, root string) (bool, error) {
	cmd := exec.CommandContext(ctx, gitPath, "-C", root, "config", "--show-scope", "--get-regexp", `^credential(\..*)?\.helper$`)
	cmd.Env = safeShellEnvironment()
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 2 {
			return false, fmt.Errorf("unexpected scoped Git configuration output")
		}
		if fields[0] != "local" && fields[0] != "worktree" {
			continue
		}
		if len(fields) >= 3 {
			return true, nil
		}
	}
	return false, nil
}

func gitPathsUseFilters(ctx context.Context, root string, paths []string) (bool, error) {
	if len(paths) == 0 {
		return false, nil
	}
	args := append([]string{"check-attr", "filter", "--"}, paths...)
	result, _, err := runGitCommand(ctx, root, args)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(result, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasSuffix(line, ": unspecified") || strings.HasSuffix(line, ": unset") {
			continue
		}
		parts := strings.SplitN(line, ": ", 3)
		if len(parts) == 3 && parts[2] != "unspecified" && parts[2] != "unset" && parts[2] != "" {
			return true, nil
		}
	}
	return false, nil
}

func hasExecutableGitFilters(ctx context.Context, root string) (bool, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return false, ErrGitNotFound
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, gitPath, "-C", root, "config", "--show-scope", "--get-regexp", `^(filter\..*\.(clean|smudge|process)|merge\..*\.driver)$`)
	cmd.Env = safeShellEnvironment()
	out, err := cmd.Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if len(fields) < 2 {
				return false, fmt.Errorf("unexpected scoped Git configuration output")
			}
			if (fields[0] == "local" || fields[0] == "worktree") && len(fields) >= 3 {
				return true, nil
			}
		}
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	if checkCtx.Err() != nil {
		return false, checkCtx.Err()
	}
	return false, err
}

func validateGitPaths(paths []string) ([]string, error) {
	if len(paths) > 128 {
		return nil, fmt.Errorf("too many git paths")
	}
	out := make([]string, 0, len(paths))
	for _, value := range paths {
		value = filepath.Clean(strings.TrimSpace(value))
		if value == "" || value == "." || filepath.IsAbs(value) || filepath.VolumeName(value) != "" || value == ".." || strings.HasPrefix(value, ".."+string(filepath.Separator)) || strings.HasPrefix(value, ":(") || strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("invalid git path")
		}
		out = append(out, filepath.ToSlash(value))
	}
	return out, nil
}

func pathWithin(root, target string) bool {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	targetReal, err := filepath.EvalSymlinks(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootReal, targetReal)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func redactGitText(value string) string {
	for _, prefix := range []string{"https://", "http://"} {
		start := 0
		for {
			idx := strings.Index(value[start:], prefix)
			if idx < 0 {
				break
			}
			idx += start
			at := strings.Index(value[idx+len(prefix):], "@")
			if at < 0 {
				break
			}
			at += idx + len(prefix)
			segment := value[idx+len(prefix) : at]
			if strings.Contains(segment, ":") || len(segment) > 0 {
				value = value[:idx+len(prefix)] + "***@" + value[at+1:]
				start = idx + len(prefix) + 4
				continue
			}
			start = at + 1
		}
	}
	return value
}

type gitOutputWriter struct {
	preview   *limitedBuffer
	spool     *os.File
	fullBytes int64
	fullLimit int64
	tooLarge  bool
}

func (w *gitOutputWriter) Write(p []byte) (int, error) {
	original := len(p)
	_, _ = w.preview.Write(p)
	if w.spool == nil {
		return original, nil
	}
	remaining := w.fullLimit - w.fullBytes
	if remaining <= 0 {
		w.tooLarge = true
		return original, nil
	}
	toWrite := p
	if int64(len(toWrite)) > remaining {
		toWrite = toWrite[:remaining]
		w.tooLarge = true
	}
	n, err := w.spool.Write(toWrite)
	w.fullBytes += int64(n)
	if err != nil {
		return 0, err
	}
	return original, nil
}

type limitedBuffer struct {
	buf       *bytes.Buffer
	limit     int
	truncated bool
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := w.limit - w.buf.Len()
	if remaining <= 0 {
		w.truncated = true
		return original, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		w.truncated = true
	}
	_, _ = w.buf.Write(p)
	return original, nil
}
