package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	workingContextSchemaVersion = 1
	maxWorkingContextBytes      = 64 << 10
	maxWorkingContextItems      = 64
	maxWorkingContextGoalBytes  = 4 << 10
	maxWorkingContextItemBytes  = 1 << 10
)

type workingContextParams struct {
	ProjectPath    string   `json:"projectPath"`
	Goal           string   `json:"goal,omitempty"`
	BaselineBranch string   `json:"baselineBranch,omitempty"`
	BaselineCommit string   `json:"baselineCommit,omitempty"`
	Completed      []string `json:"completed,omitempty"`
	Constraints    []string `json:"constraints,omitempty"`
	Pending        []string `json:"pending,omitempty"`
	KeyFiles       []string `json:"keyFiles,omitempty"`
	Facts          []string `json:"facts,omitempty"`
}

type workingContextBaseline struct {
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit,omitempty"`
}

type workingContextState struct {
	SchemaVersion int                    `json:"schemaVersion"`
	ProjectPath   string                 `json:"projectPath"`
	Goal          string                 `json:"goal"`
	Baseline      workingContextBaseline `json:"baseline"`
	Completed     []string               `json:"completed,omitempty"`
	Constraints   []string               `json:"constraints,omitempty"`
	Pending       []string               `json:"pending,omitempty"`
	KeyFiles      []string               `json:"keyFiles,omitempty"`
	Facts         []string               `json:"facts,omitempty"`
	UpdatedAt     time.Time              `json:"updatedAt"`
}

type workingContextGitFacts struct {
	IsRepository bool   `json:"isRepository"`
	Branch       string `json:"branch,omitempty"`
	Head         string `json:"head,omitempty"`
	Dirty        bool   `json:"dirty,omitempty"`
}

type workingContextResult struct {
	Action     string                 `json:"action"`
	Exists     bool                   `json:"exists"`
	State      *workingContextState   `json:"state,omitempty"`
	CurrentGit workingContextGitFacts `json:"currentGit"`
	Revision   string                 `json:"revision,omitempty"`
	Cleared    bool                   `json:"cleared,omitempty"`
}

func (c *Client) workingContextControl(ctx context.Context, action string, params map[string]any) (workingContextResult, error) {
	if err := ctx.Err(); err != nil {
		return workingContextResult{}, err
	}
	var input workingContextParams
	if err := decodeParams(params, &input); err != nil {
		return workingContextResult{}, fmt.Errorf("invalid params: %w", err)
	}
	projectPath, err := resolveWorkingContextProject(input.ProjectPath)
	if err != nil {
		return workingContextResult{}, err
	}
	currentGit := inspectWorkingContextGit(ctx, projectPath)
	filePath := c.workingContextPath(projectPath)

	switch action {
	case "get":
		state, raw, exists, err := loadWorkingContext(filePath, projectPath)
		if err != nil {
			return workingContextResult{}, err
		}
		result := workingContextResult{Action: action, Exists: exists, CurrentGit: currentGit}
		if exists {
			result.State = &state
			result.Revision = workingContextRevision(raw)
		}
		return result, nil
	case "set":
		state, err := buildWorkingContextState(projectPath, input, currentGit)
		if err != nil {
			return workingContextResult{}, err
		}
		raw, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			return workingContextResult{}, err
		}
		raw = append(raw, '\n')
		if len(raw) > maxWorkingContextBytes {
			return workingContextResult{}, fmt.Errorf("working context exceeds %d bytes", maxWorkingContextBytes)
		}
		if err := atomicWriteWorkingContext(filePath, raw); err != nil {
			return workingContextResult{}, err
		}
		return workingContextResult{
			Action: action, Exists: true, State: &state, CurrentGit: currentGit, Revision: workingContextRevision(raw),
		}, nil
	case "clear":
		_, statErr := os.Stat(filePath)
		existed := statErr == nil
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return workingContextResult{}, statErr
		}
		if err := os.Remove(filePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return workingContextResult{}, err
		}
		return workingContextResult{Action: action, Exists: false, Cleared: existed, CurrentGit: currentGit}, nil
	default:
		return workingContextResult{}, fmt.Errorf("unsupported working context action %q", action)
	}
}

func resolveWorkingContextProject(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("projectPath is required")
	}
	projectPath, err := ResolveMachinePath(value)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(projectPath)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("projectPath must be a directory")
	}
	return projectPath, nil
}

func (c *Client) workingContextPath(projectPath string) string {
	key := filepath.Clean(projectPath)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(c.cfg.DataDir, "working-contexts", hex.EncodeToString(sum[:16])+".json")
}

func buildWorkingContextState(projectPath string, input workingContextParams, currentGit workingContextGitFacts) (workingContextState, error) {
	goal, err := normalizeWorkingContextText(input.Goal, maxWorkingContextGoalBytes, true)
	if err != nil {
		return workingContextState{}, fmt.Errorf("goal: %w", err)
	}
	branch, err := normalizeWorkingContextText(input.BaselineBranch, 256, false)
	if err != nil {
		return workingContextState{}, fmt.Errorf("baselineBranch: %w", err)
	}
	commit, err := normalizeWorkingContextText(input.BaselineCommit, 128, false)
	if err != nil {
		return workingContextState{}, fmt.Errorf("baselineCommit: %w", err)
	}
	if branch == "" && commit == "" && currentGit.IsRepository {
		branch = currentGit.Branch
		commit = currentGit.Head
	}
	completed, err := normalizeWorkingContextItems(input.Completed, "completed")
	if err != nil {
		return workingContextState{}, err
	}
	constraints, err := normalizeWorkingContextItems(input.Constraints, "constraints")
	if err != nil {
		return workingContextState{}, err
	}
	pending, err := normalizeWorkingContextItems(input.Pending, "pending")
	if err != nil {
		return workingContextState{}, err
	}
	facts, err := normalizeWorkingContextItems(input.Facts, "facts")
	if err != nil {
		return workingContextState{}, err
	}
	keyFiles, err := normalizeWorkingContextKeyFiles(projectPath, input.KeyFiles)
	if err != nil {
		return workingContextState{}, err
	}
	return workingContextState{
		SchemaVersion: workingContextSchemaVersion,
		ProjectPath:   projectPath,
		Goal:          goal,
		Baseline:      workingContextBaseline{Branch: branch, Commit: commit},
		Completed:     completed,
		Constraints:   constraints,
		Pending:       pending,
		KeyFiles:      keyFiles,
		Facts:         facts,
		UpdatedAt:     time.Now().UTC(),
	}, nil
}

func normalizeWorkingContextText(value string, maxBytes int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fmt.Errorf("value is required")
	}
	if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 || len([]byte(value)) > maxBytes {
		return "", fmt.Errorf("value is invalid or exceeds %d bytes", maxBytes)
	}
	return value, nil
}

func normalizeWorkingContextItems(values []string, field string) ([]string, error) {
	if len(values) > maxWorkingContextItems {
		return nil, fmt.Errorf("%s has too many items", field)
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		item, err := normalizeWorkingContextText(value, maxWorkingContextItemBytes, false)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out, nil
}

func normalizeWorkingContextKeyFiles(projectPath string, values []string) ([]string, error) {
	if len(values) > maxWorkingContextItems {
		return nil, fmt.Errorf("keyFiles has too many items")
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 || len([]byte(value)) > maxWorkingContextItemBytes {
			return nil, fmt.Errorf("keyFiles contains an invalid path")
		}
		var relative string
		if filepath.IsAbs(value) {
			target, err := ResolveMachinePath(value)
			if err != nil {
				return nil, fmt.Errorf("keyFiles: %w", err)
			}
			if !pathWithin(projectPath, target) {
				return nil, fmt.Errorf("keyFiles path is outside projectPath")
			}
			relative, err = filepath.Rel(projectPath, target)
			if err != nil {
				return nil, err
			}
		} else {
			clean := filepath.Clean(value)
			if clean == "." || clean == ".." || filepath.VolumeName(clean) != "" || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("keyFiles contains a path outside projectPath")
			}
			relative = clean
		}
		relative = filepath.ToSlash(relative)
		if _, ok := seen[relative]; ok {
			continue
		}
		seen[relative] = struct{}{}
		out = append(out, relative)
	}
	return out, nil
}

func loadWorkingContext(filePath, projectPath string) (workingContextState, []byte, bool, error) {
	raw, err := os.ReadFile(filePath)
	if errors.Is(err, os.ErrNotExist) {
		return workingContextState{}, nil, false, nil
	}
	if err != nil {
		return workingContextState{}, nil, false, err
	}
	if len(raw) == 0 || len(raw) > maxWorkingContextBytes || !utf8.Valid(raw) {
		return workingContextState{}, nil, false, fmt.Errorf("stored working context is invalid")
	}
	var state workingContextState
	if err := json.Unmarshal(raw, &state); err != nil {
		return workingContextState{}, nil, false, fmt.Errorf("decode working context: %w", err)
	}
	if state.SchemaVersion != workingContextSchemaVersion || !samePath(state.ProjectPath, projectPath) {
		return workingContextState{}, nil, false, fmt.Errorf("stored working context does not match this project")
	}
	return state, raw, true, nil
}

func atomicWriteWorkingContext(filePath string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(filePath), ".working-context-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, filePath); err == nil {
		return nil
	}
	if err := os.Remove(filePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tempPath, filePath)
}

func workingContextRevision(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func inspectWorkingContextGit(ctx context.Context, projectPath string) workingContextGitFacts {
	facts := workingContextGitFacts{}
	gitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	inside, _, err := runGitCommand(gitCtx, projectPath, []string{"rev-parse", "--is-inside-work-tree"})
	if err != nil || strings.TrimSpace(inside) != "true" {
		return facts
	}
	facts.IsRepository = true
	if branch, _, err := runGitCommand(gitCtx, projectPath, []string{"branch", "--show-current"}); err == nil {
		facts.Branch = strings.TrimSpace(branch)
	}
	if head, _, err := runGitCommand(gitCtx, projectPath, []string{"rev-parse", "HEAD"}); err == nil {
		facts.Head = strings.TrimSpace(head)
	}
	status, _, err := runGitCommand(gitCtx, projectPath, []string{"status", "--porcelain=v1", "--untracked-files=normal"})
	if err == nil {
		facts.Dirty = strings.TrimSpace(status) != ""
	} else if errors.Is(err, ErrGitOutputTooLarge) {
		facts.Dirty = true
	}
	return facts
}
