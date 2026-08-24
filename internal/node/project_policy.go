package node

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrProjectPathForbidden       = errors.New("project path is outside the project root")
	ErrProjectModeRequired        = errors.New("project mode requires an explicit project path")
	ErrProjectNativeCaptureDenied = errors.New("native capture is disabled in project mode")
)

// projectPolicy is enforced at the Node capability boundary. MCP, Direct API,
// Local Bridge and any future Hub caller therefore share the same path policy
// without copying authorization logic into each client surface.
type projectPolicy struct {
	root string
}

func newProjectPolicy(raw string) (*projectPolicy, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.IndexByte(raw, 0) >= 0 {
		return nil, fmt.Errorf("project root: %w", ErrProjectPathForbidden)
	}
	root, err := filepath.Abs(raw)
	if err != nil {
		return nil, fmt.Errorf("resolve project root: %w", err)
	}
	root, err = ResolveMachinePath(root)
	if err != nil {
		return nil, fmt.Errorf("resolve project root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat project root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("project root must be a directory")
	}
	return &projectPolicy{root: root}, nil
}

func (p *projectPolicy) validate(capability, action string, params map[string]any) error {
	if p == nil {
		return nil
	}
	if params == nil {
		params = map[string]any{}
	}

	switch capability {
	case "file.read":
		return p.validatePathParam(params, "path", false)
	case "file.write":
		allowCreate := action == "create"
		if action == "preview" {
			previewOf, _ := params["previewOf"].(string)
			allowCreate = strings.EqualFold(strings.TrimSpace(previewOf), "create")
		}
		return p.validatePathParam(params, "path", allowCreate)
	case "code.search":
		return p.validatePathParam(params, "path", false)
	case "shell.exec", "build.exec":
		return p.validatePathParam(params, "cwd", false)
	case "git.repository":
		if err := p.validatePathParam(params, "repositoryPath", false); err != nil {
			return err
		}
		switch action {
		case "createWorktree":
			// The machine-mode default stores worktrees under Node data-dir.
			// Requiring an explicit in-project target prevents that escape in
			// project mode.
			return p.validatePathParam(params, "worktreePath", true)
		case "deleteWorktree":
			return p.validatePathParam(params, "worktreePath", false)
		default:
			return nil
		}
	case "artifact.store":
		if action == "uploadFile" || action == "publishFile" {
			return p.validatePathParam(params, "path", false)
		}
		return nil
	case "working.context":
		return p.validatePathParam(params, "projectPath", false)
	case "screenshot.capture":
		return ErrProjectNativeCaptureDenied
	case "agent.control":
		return p.validateAgentParams(action, params)
	default:
		return nil
	}
}

func (p *projectPolicy) validatePathParam(params map[string]any, name string, allowCreate bool) error {
	raw, ok := params[name]
	if !ok {
		return fmt.Errorf("%w: %s is required", ErrProjectModeRequired, name)
	}
	value, ok := raw.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrProjectModeRequired, name)
	}
	value = strings.TrimSpace(value)
	if strings.IndexByte(value, 0) >= 0 || strings.ContainsAny(value, "\r\n") || !filepath.IsAbs(value) {
		return fmt.Errorf("%w: invalid %s", ErrProjectPathForbidden, name)
	}
	if filepath.Separator == '\\' {
		volume := filepath.VolumeName(value)
		if strings.Contains(strings.TrimPrefix(value, volume), ":") {
			return fmt.Errorf("%w: invalid %s", ErrProjectPathForbidden, name)
		}
	}
	clean, err := filepath.Abs(value)
	if err != nil || !filepath.IsAbs(clean) {
		return fmt.Errorf("%w: %s must be an absolute path", ErrProjectPathForbidden, name)
	}
	clean = filepath.Clean(clean)
	if _, err := ResolveMachinePath(clean); err == nil {
		if !pathWithin(p.root, clean) {
			return fmt.Errorf("%w: %s", ErrProjectPathForbidden, name)
		}
		return nil
	} else if !allowCreate {
		return fmt.Errorf("%w: %s", ErrProjectPathForbidden, name)
	}

	parent, err := ResolveMachinePath(filepath.Dir(clean))
	if err != nil || !pathWithin(p.root, parent) {
		return fmt.Errorf("%w: %s", ErrProjectPathForbidden, name)
	}
	if !lexicalPathWithin(p.root, filepath.Join(parent, filepath.Base(clean))) {
		return fmt.Errorf("%w: %s", ErrProjectPathForbidden, name)
	}
	return nil
}

func lexicalPathWithin(root, target string) bool {
	root, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (p *projectPolicy) validateAgentParams(action string, params map[string]any) error {
	workingDirectory, hasWorkingDirectory := projectStringParam(params, "workingDirectory")
	if hasWorkingDirectory && strings.TrimSpace(workingDirectory) != "" {
		if err := p.validatePathParam(params, "workingDirectory", false); err != nil {
			return err
		}
	} else if agentActionRequiresProjectDirectory(action) {
		return fmt.Errorf("%w: workingDirectory is required for agent action", ErrProjectModeRequired)
	}
	if marketplacePath, ok := projectStringParam(params, "marketplacePath"); ok && strings.TrimSpace(marketplacePath) != "" {
		if err := p.validatePathParam(params, "marketplacePath", false); err != nil {
			return err
		}
	}
	if err := p.validateStringPathArray(params, "localImages"); err != nil {
		return err
	}
	for _, field := range []string{"skills", "mentions"} {
		if err := p.validateNestedPathArray(params, field); err != nil {
			return err
		}
	}
	return nil
}

func agentActionRequiresProjectDirectory(action string) bool {
	if strings.HasPrefix(action, "session.") {
		return true
	}
	switch action {
	case "projects.list", "skills.list", "hooks.list", "permissions.list", "plugins.list", "plugins.installed":
		return true
	default:
		return false
	}
}

func projectStringParam(params map[string]any, name string) (string, bool) {
	value, ok := params[name].(string)
	return value, ok
}

func (p *projectPolicy) validateStringPathArray(params map[string]any, name string) error {
	value, ok := params[name]
	if !ok {
		return nil
	}
	switch values := value.(type) {
	case []string:
		for _, item := range values {
			if strings.TrimSpace(item) == "" {
				continue
			}
			if err := p.validatePathParam(map[string]any{"path": item}, "path", false); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	case []any:
		for _, item := range values {
			text, ok := item.(string)
			if !ok || strings.TrimSpace(text) == "" {
				continue
			}
			if err := p.validatePathParam(map[string]any{"path": text}, "path", false); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	return nil
}

func (p *projectPolicy) validateNestedPathArray(params map[string]any, name string) error {
	value, ok := params[name]
	if !ok {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		path, ok := entry["path"].(string)
		if !ok || strings.TrimSpace(path) == "" {
			continue
		}
		if err := p.validatePathParam(map[string]any{"path": path}, "path", false); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}
