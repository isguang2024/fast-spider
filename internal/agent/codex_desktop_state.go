package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const codexDesktopStateFilename = ".codex-global-state.json"

type codexDesktopProject struct {
	ProjectID        string
	Name             string
	ProjectDirectory string
}

// codexDesktopSnapshot is deliberately limited to read-only project metadata.
// Local task existence, authorization, creation, and delivery are owned only by
// the Node's Codex app-server connection.
type codexDesktopSnapshot struct {
	Projects       []codexDesktopProject
	ProjectsByID   map[string]codexDesktopProject
	ProjectIDByKey map[string]string
}

func defaultCodexDesktopStatePath() string {
	if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
		return filepath.Join(codexHome, codexDesktopStateFilename)
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".codex", codexDesktopStateFilename)
}

func readCodexDesktopSnapshot(statePath string) (codexDesktopSnapshot, error) {
	snapshot := codexDesktopSnapshot{
		ProjectsByID:   map[string]codexDesktopProject{},
		ProjectIDByKey: map[string]string{},
	}
	if strings.TrimSpace(statePath) == "" {
		return snapshot, nil
	}
	raw, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot, nil
	}
	if err != nil {
		return snapshot, err
	}
	state, err := decodeCodexDesktopState(raw)
	if err != nil {
		return snapshot, err
	}
	projects, _ := state["local-projects"].(map[string]any)
	orderedIDs := stringSlice(state["project-order"])
	seen := map[string]bool{}
	addProject := func(projectID string) {
		if seen[projectID] {
			return
		}
		entry, _ := projects[projectID].(map[string]any)
		roots := stringSlice(entry["rootPaths"])
		if len(roots) == 0 || strings.TrimSpace(roots[0]) == "" {
			return
		}
		project := codexDesktopProject{
			ProjectID:        projectID,
			Name:             mapAnyString(entry, "name"),
			ProjectDirectory: filepath.Clean(roots[0]),
		}
		if project.Name == "" {
			project.Name = filepath.Base(project.ProjectDirectory)
		}
		seen[projectID] = true
		snapshot.Projects = append(snapshot.Projects, project)
		snapshot.ProjectsByID[projectID] = project
		snapshot.ProjectIDByKey[agentPathKey(project.ProjectDirectory)] = projectID
	}
	for _, projectID := range orderedIDs {
		addProject(projectID)
	}
	for projectID := range projects {
		addProject(projectID)
	}
	return snapshot, nil
}

func decodeCodexDesktopState(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var state map[string]any
	if err := decoder.Decode(&state); err != nil {
		return nil, fmt.Errorf("decode Codex Desktop state: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	return state, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("Codex Desktop state contains trailing JSON data")
}

func stringSlice(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			out = append(out, text)
		}
	}
	return out
}

func mapAnyString(record map[string]any, key string) string {
	value, _ := record[key].(string)
	return value
}

func agentPathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}
