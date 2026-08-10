package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type agentProjectContext struct {
	WorkingDirectory string
	ProjectDirectory string
	ProjectID        string
	IsGitRepository  bool
}

func resolveAgentProjectContext(ctx context.Context, workingDirectory string) agentProjectContext {
	workingDirectory = filepath.Clean(workingDirectory)
	project := agentProjectContext{
		WorkingDirectory: workingDirectory,
		ProjectDirectory: workingDirectory,
	}
	gitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	inside, err := runAgentGit(gitCtx, workingDirectory, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(inside) != "true" {
		return project
	}
	project.IsGitRepository = true
	if worktrees, worktreeErr := runAgentGit(gitCtx, workingDirectory, "worktree", "list", "--porcelain"); worktreeErr == nil {
		for _, line := range strings.Split(worktrees, "\n") {
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree "); ok && strings.TrimSpace(value) != "" {
				project.ProjectDirectory = filepath.Clean(strings.TrimSpace(value))
				break
			}
		}
	}
	if sameAgentPath(project.ProjectDirectory, workingDirectory) {
		if top, topErr := runAgentGit(gitCtx, workingDirectory, "rev-parse", "--show-toplevel"); topErr == nil && strings.TrimSpace(top) != "" {
			project.ProjectDirectory = filepath.Clean(strings.TrimSpace(top))
		}
	}
	project.ProjectID = codexLocalProjectID(project.ProjectDirectory)
	return project
}

func runAgentGit(ctx context.Context, directory string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", directory}, args...)
	output, err := exec.CommandContext(ctx, "git", commandArgs...).Output()
	return string(output), err
}

func codexLocalProjectID(projectDirectory string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(projectDirectory)))
	return "local-" + hex.EncodeToString(sum[:16])
}

func sameAgentPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		if strings.EqualFold(left, right) {
			return true
		}
	} else if left == right {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}
