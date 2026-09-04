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
	"sync"
	"time"
	"unicode/utf8"
)

const (
	workingContextSchemaVersion = 3
	maxWorkingContextBytes      = 1 << 20
	maxWorkingContextTextBytes  = 64 << 10
)

var workingContextWriteMu sync.Mutex

// working.context is intentionally small: one project owns one bounded text
// note. The calling AI decides how to express goals, progress, blockers and
// handoffs inside that text instead of asking Fast Spider to model a task tree.
type workingContextParams struct {
	ProjectPath      string `json:"projectPath"`
	ExpectedRevision string `json:"expectedRevision,omitempty"`
	Text             string `json:"text,omitempty"`
	Goal             string `json:"goal,omitempty"` // legacy set compatibility
}

type workingContextState struct {
	SchemaVersion int       `json:"schemaVersion"`
	ProjectPath   string    `json:"projectPath"`
	Text          string    `json:"text"`
	UpdatedAt     time.Time `json:"updatedAt"`
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

	switch strings.TrimSpace(action) {
	case "get":
		state, raw, exists, err := loadWorkingContext(filePath, projectPath)
		if err != nil {
			return workingContextResult{}, err
		}
		result := workingContextResult{Action: "get", Exists: exists, CurrentGit: currentGit}
		if exists {
			result.State = &state
			result.Revision = workingContextRevision(raw)
		}
		return result, nil
	case "set":
		text := input.Text
		if text == "" {
			text = input.Goal
		}
		text, err = normalizeWorkingContextText(text, true)
		if err != nil {
			return workingContextResult{}, fmt.Errorf("text: %w", err)
		}
		state := workingContextState{SchemaVersion: workingContextSchemaVersion, ProjectPath: projectPath, Text: text, UpdatedAt: time.Now().UTC()}
		raw, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			return workingContextResult{}, err
		}
		raw = append(raw, '\n')
		workingContextWriteMu.Lock()
		defer workingContextWriteMu.Unlock()
		if err := atomicWriteWorkingContextCAS(filePath, raw, input.ExpectedRevision); err != nil {
			return workingContextResult{}, err
		}
		return workingContextResult{Action: "set", Exists: true, State: &state, CurrentGit: currentGit, Revision: workingContextRevision(raw)}, nil
	case "clear":
		workingContextWriteMu.Lock()
		defer workingContextWriteMu.Unlock()
		current, statErr := os.ReadFile(filePath)
		existed := statErr == nil
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return workingContextResult{}, statErr
		}
		if input.ExpectedRevision != "" && (!existed || workingContextRevision(current) != input.ExpectedRevision) {
			return workingContextResult{}, ErrRevisionConflict
		}
		if err := os.Remove(filePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return workingContextResult{}, err
		}
		return workingContextResult{Action: "clear", Exists: false, Cleared: existed, CurrentGit: currentGit}, nil
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

func normalizeWorkingContextText(value string, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fmt.Errorf("value is required")
	}
	if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 || len([]byte(value)) > maxWorkingContextTextBytes {
		return "", fmt.Errorf("value is invalid or exceeds %d bytes", maxWorkingContextTextBytes)
	}
	if containsSensitiveWorkspaceMaterial(value) {
		return "", fmt.Errorf("value appears to contain sensitive or raw conversation material")
	}
	return value, nil
}

func containsSensitiveWorkspaceMaterial(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"bearer ", "full prompt:", "chat transcript:", "raw upstream error:", "<|user|>", "<|assistant|>"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

type legacyWorkingContextState struct {
	SchemaVersion int      `json:"schemaVersion"`
	ProjectPath   string   `json:"projectPath"`
	Goal          string   `json:"goal"`
	Completed     []string `json:"completed"`
	Constraints   []string `json:"constraints"`
	Pending       []string `json:"pending"`
	Facts         []string `json:"facts"`
	Tasks         []struct {
		ID            string `json:"id"`
		Title         string `json:"title"`
		Status        string `json:"status"`
		Completion    int    `json:"completion"`
		BlockedReason string `json:"blockedReason"`
	} `json:"tasks"`
	UpdatedAt time.Time `json:"updatedAt"`
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
	if state.SchemaVersion != 1 && state.SchemaVersion != 2 && state.SchemaVersion != workingContextSchemaVersion {
		return workingContextState{}, nil, false, fmt.Errorf("stored working context schema is unsupported")
	}
	if !samePath(state.ProjectPath, projectPath) {
		return workingContextState{}, nil, false, fmt.Errorf("stored working context does not match this project")
	}
	if state.Text == "" && state.SchemaVersion < workingContextSchemaVersion {
		var legacy legacyWorkingContextState
		if err := json.Unmarshal(raw, &legacy); err != nil {
			return workingContextState{}, nil, false, fmt.Errorf("decode legacy working context: %w", err)
		}
		state.Text = legacyWorkingContextText(legacy)
		state.UpdatedAt = legacy.UpdatedAt
	}
	state.SchemaVersion = workingContextSchemaVersion
	return state, raw, true, nil
}

func legacyWorkingContextText(state legacyWorkingContextState) string {
	var sections []string
	if strings.TrimSpace(state.Goal) != "" {
		sections = append(sections, "# 目标\n"+strings.TrimSpace(state.Goal))
	}
	appendItems := func(title string, values []string) {
		if len(values) == 0 {
			return
		}
		var block strings.Builder
		block.WriteString("## ")
		block.WriteString(title)
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				block.WriteString("\n- ")
				block.WriteString(value)
			}
		}
		sections = append(sections, block.String())
	}
	appendItems("已完成", state.Completed)
	appendItems("进行中 / 待办", state.Pending)
	appendItems("约束", state.Constraints)
	appendItems("事实", state.Facts)
	if len(state.Tasks) > 0 {
		var block strings.Builder
		block.WriteString("## 历史任务")
		for _, task := range state.Tasks {
			block.WriteString("\n- [")
			block.WriteString(task.Status)
			block.WriteString("] ")
			if task.ID != "" {
				block.WriteString(task.ID)
				block.WriteString(" · ")
			}
			block.WriteString(task.Title)
			if task.Completion > 0 {
				_, _ = fmt.Fprintf(&block, " · %d%%", task.Completion)
			}
			if task.BlockedReason != "" {
				block.WriteString(" · ")
				block.WriteString(task.BlockedReason)
			}
		}
		sections = append(sections, block.String())
	}
	text := strings.TrimSpace(strings.Join(sections, "\n\n"))
	if len([]byte(text)) <= maxWorkingContextTextBytes {
		return text
	}
	raw := []byte(text)
	raw = raw[:maxWorkingContextTextBytes]
	for !utf8.Valid(raw) {
		raw = raw[:len(raw)-1]
	}
	return strings.TrimSpace(string(raw))
}

func atomicWriteWorkingContextCAS(filePath string, raw []byte, expectedRevision string) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return err
	}
	if expectedRevision != "" {
		current, err := os.ReadFile(filePath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return ErrRevisionConflict
			}
			return err
		}
		if workingContextRevision(current) != expectedRevision {
			return ErrRevisionConflict
		}
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
	if expectedRevision != "" {
		current, err := os.ReadFile(filePath)
		if err != nil || workingContextRevision(current) != expectedRevision {
			return ErrRevisionConflict
		}
	}
	if err := replaceFile(tempPath, filePath); err != nil {
		return err
	}
	return syncParentDirectory(filepath.Dir(filePath))
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
