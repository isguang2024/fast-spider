package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxWorkingMarkdownFiles       = 64
	maxWorkingMarkdownFileBytes   = 512 << 10
	maxWorkingMarkdownTotalBytes  = 4 << 20
	maxWorkingMarkdownAppendBytes = 64 << 10
)

type workingMarkdownFile struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Revision string `json:"revision"`
}

var defaultWorkingMarkdownFiles = []struct{ Name, Title, Block string }{
	{"00-current-state.md", "Current State", "current-state"},
	{"01-roadmap-0.4.md", "Roadmap", "roadmap"},
	{"02-decisions.md", "Decisions", "decisions"},
	{"03-acceptance-log.md", "Acceptance Log", "acceptance"},
	{"04-open-issues.md", "Open Issues", "open-issues"},
	{"05-change-log.md", "Change Log", "change-log"},
}

func normalizeWorkingMarkdownRoot(projectPath, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		value = filepath.Join("docs", "progress")
	}
	clean, err := cleanWorkingRelativePath(value)
	if err != nil {
		return "", fmt.Errorf("markdownRoot: %w", err)
	}
	if err := verifyWorkingPathBoundary(projectPath, filepath.Join(projectPath, clean), true); err != nil {
		return "", fmt.Errorf("markdownRoot: %w", err)
	}
	return filepath.ToSlash(clean), nil
}

func (c *Client) workingMarkdownControl(ctx context.Context, action string, input workingContextParams, projectPath, planID string, git workingContextGitFacts) (workingContextResult, error) {
	planPath := c.workingContextPathForPlan(projectPath, planID)
	state, raw, exists, err := loadWorkingContext(planPath, projectPath)
	if err != nil {
		return workingContextResult{}, err
	}
	if !exists {
		return workingContextResult{}, os.ErrNotExist
	}
	if state.PlanID != planID {
		return workingContextResult{}, fmt.Errorf("stored working plan does not match planId")
	}
	if state.MarkdownRoot == "" {
		state.MarkdownRoot = filepath.ToSlash(filepath.Join("docs", "progress"))
	}
	planRevision := workingContextRevision(raw)
	switch action {
	case "markdown.list":
		files, _, err := listWorkingMarkdown(projectPath, state.MarkdownRoot)
		return workingContextResult{Action: action, Exists: true, State: &state, CurrentGit: git, Revision: planRevision, Markdown: files}, err
	case "markdown.read":
		path, err := resolveWorkingMarkdownFile(projectPath, state.MarkdownRoot, input.MarkdownPath, false)
		if err != nil {
			return workingContextResult{}, err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return workingContextResult{}, err
		}
		if len(content) > maxWorkingMarkdownFileBytes || !utf8.Valid(content) {
			return workingContextResult{}, fmt.Errorf("Markdown file is invalid or exceeds %d bytes", maxWorkingMarkdownFileBytes)
		}
		return workingContextResult{Action: action, Exists: true, State: &state, CurrentGit: git, Revision: planRevision, Content: string(content), FileRevision: workingContextRevision(content)}, nil
	case "markdown.append":
		return c.appendWorkingMarkdown(input, state, planRevision, projectPath, git)
	case "plan.sync":
		return c.syncWorkingPlan(ctx, input, state, planRevision, projectPath, git)
	default:
		return workingContextResult{}, fmt.Errorf("unsupported Markdown action %q", action)
	}
}

func initializeWorkingMarkdownWorkspace(projectPath, markdownRoot string) ([]string, error) {
	root, err := resolveWorkingMarkdownRoot(projectPath, markdownRoot, true)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	if err := verifyWorkingPathBoundary(projectPath, root, false); err != nil {
		return nil, err
	}
	existingFiles, total, err := listWorkingMarkdown(projectPath, markdownRoot)
	if err != nil {
		return nil, err
	}
	type missingMarkdown struct {
		path, relative string
		content        []byte
	}
	missing := make([]missingMarkdown, 0)
	for _, spec := range defaultWorkingMarkdownFiles {
		path := filepath.Join(root, spec.Name)
		if info, statErr := os.Lstat(path); statErr == nil {
			if workingPathIsLink(path, info) || !info.Mode().IsRegular() || info.Size() > maxWorkingMarkdownFileBytes {
				return nil, fmt.Errorf("existing Markdown workspace file %s is unsafe or oversized", spec.Name)
			}
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, statErr
		}
		content := []byte(fmt.Sprintf("# Fast Spider %s\n\n<!-- fast-spider:managed:%s:start -->\n<!-- fast-spider:managed:%s:end -->\n\n## Manual Notes\n\n", spec.Title, spec.Block, spec.Block))
		missing = append(missing, missingMarkdown{path: path, relative: filepath.ToSlash(filepath.Join(markdownRoot, spec.Name)), content: content})
		total += int64(len(content))
	}
	if len(existingFiles)+len(missing) > maxWorkingMarkdownFiles || total > maxWorkingMarkdownTotalBytes {
		return nil, fmt.Errorf("default Markdown files would exceed workspace limits")
	}
	created := make([]string, 0, len(missing))
	for _, item := range missing {
		if err := atomicWriteWorkingMarkdown(item.path, item.content, "", true); err != nil {
			return nil, err
		}
		created = append(created, item.relative)
	}
	return created, nil
}

func (c *Client) appendWorkingMarkdown(input workingContextParams, state workingContextState, planRevision, projectPath string, git workingContextGitFacts) (workingContextResult, error) {
	if input.ExpectedFileRevision == "" {
		return workingContextResult{}, fmt.Errorf("expectedFileRevision is required for markdown.append")
	}
	if len(input.Content) == 0 || len(input.Content) > maxWorkingMarkdownAppendBytes || !utf8.ValidString(input.Content) || containsSensitiveWorkspaceMaterial(input.Content) {
		return workingContextResult{}, fmt.Errorf("Markdown append content is invalid, sensitive, or exceeds %d bytes", maxWorkingMarkdownAppendBytes)
	}
	workingContextWriteMu.Lock()
	defer workingContextWriteMu.Unlock()
	path, err := resolveWorkingMarkdownFile(projectPath, state.MarkdownRoot, input.MarkdownPath, false)
	if err != nil {
		return workingContextResult{}, err
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return workingContextResult{}, err
	}
	if workingContextRevision(current) != input.ExpectedFileRevision {
		return workingContextResult{}, ErrRevisionConflict
	}
	var next []byte
	if strings.TrimSpace(input.ManagedBlock) != "" {
		next, err = replaceWorkingManagedBlock(current, input.ManagedBlock, input.Content)
		if err != nil {
			return workingContextResult{}, err
		}
	} else {
		next = append(append([]byte(nil), current...), []byte(input.Content)...)
	}
	if len(next) > maxWorkingMarkdownFileBytes {
		return workingContextResult{}, fmt.Errorf("Markdown file exceeds %d bytes", maxWorkingMarkdownFileBytes)
	}
	_, total, err := listWorkingMarkdown(projectPath, state.MarkdownRoot)
	if err != nil {
		return workingContextResult{}, err
	}
	if total-int64(len(current))+int64(len(next)) > maxWorkingMarkdownTotalBytes {
		return workingContextResult{}, fmt.Errorf("Markdown workspace exceeds %d bytes", maxWorkingMarkdownTotalBytes)
	}
	if err := atomicWriteWorkingMarkdown(path, next, input.ExpectedFileRevision, false); err != nil {
		return workingContextResult{}, err
	}
	return workingContextResult{Action: "markdown.append", Exists: true, State: &state, CurrentGit: git, Revision: planRevision, FileRevision: workingContextRevision(next), Changed: true}, nil
}

func (c *Client) syncWorkingPlan(ctx context.Context, input workingContextParams, state workingContextState, planRevision, projectPath string, git workingContextGitFacts) (workingContextResult, error) {
	if input.ExpectedRevision == "" {
		return workingContextResult{}, fmt.Errorf("expectedRevision is required for plan.sync")
	}
	if input.ExpectedRevision != planRevision {
		return workingContextResult{}, ErrRevisionConflict
	}
	workingContextWriteMu.Lock()
	defer workingContextWriteMu.Unlock()
	currentPlan, err := os.ReadFile(c.workingContextPathForPlan(projectPath, state.PlanID))
	if err != nil || workingContextRevision(currentPlan) != input.ExpectedRevision {
		return workingContextResult{}, ErrRevisionConflict
	}
	if _, err := initializeWorkingMarkdownWorkspace(projectPath, state.MarkdownRoot); err != nil {
		return workingContextResult{}, err
	}
	blocks := renderWorkingPlanBlocks(state, git, planRevision)
	_, total, err := listWorkingMarkdown(projectPath, state.MarkdownRoot)
	if err != nil {
		return workingContextResult{}, err
	}
	type pendingSync struct {
		path, relative, expected string
		next                     []byte
	}
	pending := make([]pendingSync, 0, len(defaultWorkingMarkdownFiles))
	for _, spec := range defaultWorkingMarkdownFiles {
		path, err := resolveWorkingMarkdownFile(projectPath, state.MarkdownRoot, filepath.ToSlash(filepath.Join(state.MarkdownRoot, spec.Name)), false)
		if err != nil {
			return workingContextResult{}, err
		}
		current, err := os.ReadFile(path)
		if err != nil {
			return workingContextResult{}, err
		}
		next, err := replaceWorkingManagedBlock(current, spec.Block, blocks[spec.Block])
		if err != nil {
			return workingContextResult{}, err
		}
		if len(next) > maxWorkingMarkdownFileBytes {
			return workingContextResult{}, fmt.Errorf("managed Markdown exceeds file limit")
		}
		total += int64(len(next) - len(current))
		pending = append(pending, pendingSync{path: path, relative: filepath.ToSlash(filepath.Join(state.MarkdownRoot, spec.Name)), expected: workingContextRevision(current), next: next})
	}
	if total > maxWorkingMarkdownTotalBytes {
		return workingContextResult{}, fmt.Errorf("Markdown workspace exceeds %d bytes", maxWorkingMarkdownTotalBytes)
	}
	synced := make([]string, 0, len(pending))
	for _, item := range pending {
		if err := atomicWriteWorkingMarkdown(item.path, item.next, item.expected, false); err != nil {
			return workingContextResult{}, err
		}
		synced = append(synced, item.relative)
	}

	// Managed Markdown records the Git snapshot from before plan.sync. The
	// response refreshes currentGit after the writes so callers can distinguish
	// the persisted pre-sync snapshot from the live post-sync repository state.
	postGit := inspectWorkingContextGit(ctx, projectPath)
	return workingContextResult{Action: "plan.sync", Exists: true, State: &state, CurrentGit: postGit, Revision: planRevision, Synced: synced, Changed: true}, nil
}

func renderWorkingPlanBlocks(state workingContextState, git workingContextGitFacts, revision string) map[string]string {
	current := fmt.Sprintf("## Managed Current State\n\n- planId: `%s`\n- targetVersion: `%s`\n- Git branch: `%s`\n- Git HEAD: `%s`\n- dirtyBeforeSync: `%t`\n- completion: `%d%%`\n- workingContextRevision: `%s`", state.PlanID, state.TargetVersion, git.Branch, git.Head, git.Dirty, workingPlanCompletion(state.Tasks), revision)
	var roadmap strings.Builder
	roadmap.WriteString("## Managed Tasks\n\n| Task | 状态 | 完成度 |\n|---|---|---|\n")
	var acceptance strings.Builder
	acceptance.WriteString("## Managed Acceptance\n")
	var issues strings.Builder
	issues.WriteString("## Managed Open Issues\n")
	for _, task := range state.Tasks {
		fmt.Fprintf(&roadmap, "| %s %s | %s | %d%% |\n", task.ID, task.Title, task.Status, task.Completion)
		if task.Status == "blocked" {
			fmt.Fprintf(&issues, "\n- `%s`: %s\n", task.ID, task.BlockedReason)
		}
		for _, evidence := range task.Evidences {
			fmt.Fprintf(&acceptance, "\n- `%s`: %s\n", task.ID, evidence.Summary)
		}
	}
	if acceptance.String() == "## Managed Acceptance\n" {
		acceptance.WriteString("\n- 暂无验收证据。\n")
	}
	if issues.String() == "## Managed Open Issues\n" {
		issues.WriteString("\n- 当前无阻塞。\n")
	}
	return map[string]string{
		"current-state": current, "roadmap": strings.TrimSpace(roadmap.String()),
		"decisions":  "## Managed Decisions\n\n- Plan/Task 结构化状态以 Working Context revision 为准；Git 与文件内容仍是最终事实源。",
		"acceptance": strings.TrimSpace(acceptance.String()), "open-issues": strings.TrimSpace(issues.String()),
		"change-log": fmt.Sprintf("## Managed Change Log\n\n- %s：同步 plan `%s` 的受管区块。", time.Now().UTC().Format("2006-01-02"), state.PlanID),
	}
}

func replaceWorkingManagedBlock(raw []byte, name, content string) ([]byte, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "<>:\r\n") {
		return nil, fmt.Errorf("managedBlock is invalid")
	}
	start := "<!-- fast-spider:managed:" + name + ":start -->"
	end := "<!-- fast-spider:managed:" + name + ":end -->"
	text := string(raw)
	first := strings.Index(text, start)
	last := strings.Index(text, end)
	if first < 0 || last < 0 || last < first || strings.Count(text, start) != 1 || strings.Count(text, end) != 1 {
		return nil, fmt.Errorf("managed block %q is missing or ambiguous", name)
	}
	replacement := start + "\n" + strings.TrimSpace(content) + "\n" + end
	return []byte(text[:first] + replacement + text[last+len(end):]), nil
}

func listWorkingMarkdown(projectPath, markdownRoot string) ([]workingMarkdownFile, int64, error) {
	root, err := resolveWorkingMarkdownRoot(projectPath, markdownRoot, false)
	if err != nil {
		return nil, 0, err
	}
	files := make([]workingMarkdownFile, 0)
	var total int64
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if workingPathIsLink(path, info) {
			return fmt.Errorf("Markdown workspace contains a symlink or junction")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return nil
		}
		if len(files) >= maxWorkingMarkdownFiles {
			return fmt.Errorf("Markdown workspace exceeds %d files", maxWorkingMarkdownFiles)
		}
		if info.Size() > maxWorkingMarkdownFileBytes {
			return fmt.Errorf("Markdown file exceeds %d bytes", maxWorkingMarkdownFileBytes)
		}
		total += info.Size()
		if total > maxWorkingMarkdownTotalBytes {
			return fmt.Errorf("Markdown workspace exceeds %d bytes", maxWorkingMarkdownTotalBytes)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !utf8.Valid(raw) {
			return fmt.Errorf("Markdown file is not UTF-8")
		}
		rel, _ := filepath.Rel(projectPath, path)
		files = append(files, workingMarkdownFile{Path: filepath.ToSlash(rel), Size: info.Size(), Revision: workingContextRevision(raw)})
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, total, nil
}

func resolveWorkingMarkdownRoot(projectPath, markdownRoot string, allowMissing bool) (string, error) {
	clean, err := cleanWorkingRelativePath(markdownRoot)
	if err != nil {
		return "", err
	}
	target := filepath.Join(projectPath, clean)
	if err := verifyWorkingPathBoundary(projectPath, target, allowMissing); err != nil {
		return "", err
	}
	if !allowMissing {
		info, err := os.Lstat(target)
		if err != nil {
			return "", err
		}
		if workingPathIsLink(target, info) || !info.IsDir() {
			return "", fmt.Errorf("Markdown root must be a normal directory")
		}
	}
	return target, nil
}

func resolveWorkingMarkdownFile(projectPath, markdownRoot, raw string, allowMissing bool) (string, error) {
	clean, err := cleanWorkingRelativePath(raw)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(filepath.Ext(clean), ".md") {
		return "", fmt.Errorf("Markdown path must end in .md")
	}
	root, err := resolveWorkingMarkdownRoot(projectPath, markdownRoot, allowMissing)
	if err != nil {
		return "", err
	}
	target := filepath.Join(projectPath, clean)
	if !lexicalWorkingPathWithin(root, target) {
		return "", fmt.Errorf("Markdown path is outside the bound workspace")
	}
	if err := verifyWorkingPathBoundary(projectPath, target, allowMissing); err != nil {
		return "", err
	}
	if !allowMissing {
		info, err := os.Lstat(target)
		if err != nil {
			return "", err
		}
		if workingPathIsLink(target, info) || !info.Mode().IsRegular() {
			return "", fmt.Errorf("Markdown path must identify a regular file")
		}
	}
	return target, nil
}

func cleanWorkingRelativePath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("relative path is required")
	}
	clean := filepath.Clean(filepath.FromSlash(value))
	if filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path must stay inside projectPath")
	}
	return clean, nil
}

func verifyWorkingPathBoundary(projectPath, target string, allowMissing bool) error {
	if !lexicalWorkingPathWithin(projectPath, target) {
		return fmt.Errorf("path is outside projectPath")
	}
	rel, err := filepath.Rel(projectPath, target)
	if err != nil {
		return err
	}
	current := projectPath
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) && allowMissing {
			break
		}
		if statErr != nil {
			return statErr
		}
		if workingPathIsLink(current, info) {
			return fmt.Errorf("symlink or junction paths are not allowed")
		}
	}
	realProject, err := filepath.EvalSymlinks(projectPath)
	if err != nil {
		return err
	}
	existing := target
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return os.ErrNotExist
		}
		existing = parent
	}
	realExisting, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return err
	}
	if !pathWithin(realProject, realExisting) {
		return fmt.Errorf("resolved path escapes projectPath")
	}
	return nil
}

func lexicalWorkingPathWithin(root, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func atomicWriteWorkingMarkdown(path string, raw []byte, expectedRevision string, createOnly bool) error {
	if len(raw) > maxWorkingMarkdownFileBytes || !utf8.Valid(raw) {
		return fmt.Errorf("Markdown content is invalid or oversized")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	current, err := os.ReadFile(path)
	if err == nil {
		if createOnly {
			return ErrRevisionConflict
		}
		if expectedRevision == "" || workingContextRevision(current) != expectedRevision {
			return ErrRevisionConflict
		}
		if info, statErr := os.Stat(path); statErr == nil {
			mode = info.Mode().Perm()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if !createOnly {
		return ErrRevisionConflict
	}
	temp, err := os.CreateTemp(dir, ".fast-spider-markdown-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if !createOnly {
		latest, err := os.ReadFile(path)
		if err != nil || workingContextRevision(latest) != expectedRevision {
			return ErrRevisionConflict
		}
	}
	if err := replaceFile(tempPath, path); err != nil {
		return err
	}
	return syncParentDirectory(dir)
}
