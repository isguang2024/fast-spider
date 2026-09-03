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
	"strconv"
	"strings"
	"time"
)

const codexDesktopStateFilename = ".codex-global-state.json"

var errCodexDesktopStateChanged = errors.New("Codex Desktop state changed concurrently")

type codexDesktopProject struct {
	ProjectID        string
	Name             string
	ProjectDirectory string
}

type codexDesktopAssignment struct {
	ProjectID        string
	ProjectDirectory string
	WorkingDirectory string
}

type codexDesktopThread struct {
	ProjectID        string
	ProjectDirectory string
	WorkingDirectory string
	OutputDirectory  string
	Projectless      bool
}

type codexDesktopSnapshot struct {
	Projects       []codexDesktopProject
	ProjectsByID   map[string]codexDesktopProject
	Assignments    map[string]codexDesktopAssignment
	Threads        map[string]codexDesktopThread
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

func syncCodexDesktopProject(statePath, sessionID string, project agentProjectContext, now time.Time) (string, bool, error) {
	if strings.TrimSpace(statePath) == "" || strings.TrimSpace(sessionID) == "" || !project.IsGitRepository {
		return "", false, nil
	}
	projectID := ""
	changed, err := updateCodexDesktopState(statePath, func(state map[string]any) (bool, error) {
		projects := ensureStringMap(state, "local-projects")
		projectID = findCodexDesktopProjectID(projects, project.ProjectDirectory)
		if projectID == "" {
			projectID = project.ProjectID
			if projectID == "" {
				projectID = codexLocalProjectID(project.ProjectDirectory)
			}
		}
		nowMillis := json.Number(strconv.FormatInt(now.UnixMilli(), 10))
		entry, _ := projects[projectID].(map[string]any)
		if entry == nil {
			entry = map[string]any{
				"id":        projectID,
				"name":      filepath.Base(project.ProjectDirectory),
				"rootPaths": []any{project.ProjectDirectory},
				"createdAt": nowMillis,
				"updatedAt": nowMillis,
			}
			projects[projectID] = entry
		} else {
			entry["id"] = projectID
			if strings.TrimSpace(mapAnyString(entry, "name")) == "" {
				entry["name"] = filepath.Base(project.ProjectDirectory)
			}
			entry["rootPaths"] = []any{project.ProjectDirectory}
			entry["updatedAt"] = nowMillis
		}
		state["project-order"] = appendUniqueString(state["project-order"], projectID)
		assignments := ensureStringMap(state, "thread-project-assignments")
		assignments[sessionID] = map[string]any{
			"projectKind":       "local",
			"projectId":         projectID,
			"cwd":               project.WorkingDirectory,
			"pendingCoreUpdate": false,
		}
		sidebarOrders := ensureStringMap(state, "sidebar-project-thread-orders")
		removeThreadFromSidebarOrders(sidebarOrders, sessionID)
		projectOrder, _ := sidebarOrders[projectID].(map[string]any)
		if projectOrder == nil {
			projectOrder = map[string]any{}
			sidebarOrders[projectID] = projectOrder
		}
		projectOrder["threadIds"] = prependUniqueString(projectOrder["threadIds"], sessionID)
		state["projectless-thread-ids"] = removeStringValue(state["projectless-thread-ids"], sessionID)
		return true, nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	return projectID, changed, err
}

func removeCodexDesktopThreadAssignment(statePath, sessionID string) error {
	if strings.TrimSpace(statePath) == "" || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	_, err := updateCodexDesktopState(statePath, func(state map[string]any) (bool, error) {
		assignments, _ := state["thread-project-assignments"].(map[string]any)
		if _, ok := assignments[sessionID]; !ok {
			return false, nil
		}
		delete(assignments, sessionID)
		if sidebarOrders, _ := state["sidebar-project-thread-orders"].(map[string]any); sidebarOrders != nil {
			removeThreadFromSidebarOrders(sidebarOrders, sessionID)
		}
		return true, nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func readCodexDesktopSnapshot(statePath string) (codexDesktopSnapshot, error) {
	snapshot := codexDesktopSnapshot{
		ProjectsByID:   map[string]codexDesktopProject{},
		Assignments:    map[string]codexDesktopAssignment{},
		Threads:        map[string]codexDesktopThread{},
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
	assignments, _ := state["thread-project-assignments"].(map[string]any)
	for sessionID, rawAssignment := range assignments {
		entry, _ := rawAssignment.(map[string]any)
		projectID := mapAnyString(entry, "projectId")
		project := snapshot.ProjectsByID[projectID]
		assignment := codexDesktopAssignment{
			ProjectID:        projectID,
			ProjectDirectory: project.ProjectDirectory,
			WorkingDirectory: cleanOptionalAgentPath(mapAnyString(entry, "cwd")),
		}
		snapshot.Assignments[sessionID] = assignment
		snapshot.Threads[sessionID] = codexDesktopThread{
			ProjectID:        assignment.ProjectID,
			ProjectDirectory: assignment.ProjectDirectory,
			WorkingDirectory: assignment.WorkingDirectory,
		}
	}
	rootHints, _ := state["thread-workspace-root-hints"].(map[string]any)
	outputDirectories, _ := state["thread-projectless-output-directories"].(map[string]any)
	for _, sessionID := range stringSlice(state["projectless-thread-ids"]) {
		if strings.TrimSpace(sessionID) == "" {
			continue
		}
		if _, assigned := snapshot.Threads[sessionID]; assigned {
			continue
		}
		snapshot.Threads[sessionID] = codexDesktopThread{
			WorkingDirectory: cleanOptionalAgentPath(mapAnyString(rootHints, sessionID)),
			OutputDirectory:  cleanOptionalAgentPath(mapAnyString(outputDirectories, sessionID)),
			Projectless:      true,
		}
	}
	return snapshot, nil
}

func updateCodexDesktopState(statePath string, mutate func(map[string]any) (bool, error)) (bool, error) {
	for attempt := 0; attempt < 3; attempt++ {
		raw, err := os.ReadFile(statePath)
		if err != nil {
			return false, err
		}
		state, err := decodeCodexDesktopState(raw)
		if err != nil {
			return false, err
		}
		changed, err := mutate(state)
		if err != nil || !changed {
			return changed, err
		}
		updated, err := json.Marshal(state)
		if err != nil {
			return false, err
		}
		if bytes.Equal(raw, updated) {
			return false, nil
		}
		err = replaceCodexDesktopState(statePath, raw, updated)
		if errors.Is(err, errCodexDesktopStateChanged) {
			continue
		}
		return err == nil, err
	}
	return false, errCodexDesktopStateChanged
}

func replaceCodexDesktopState(statePath string, expected, updated []byte) error {
	directory := filepath.Dir(statePath)
	temp, err := os.CreateTemp(directory, ".fast-spider-codex-state-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(updated); err != nil {
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
	current, err := os.ReadFile(statePath)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, expected) {
		return errCodexDesktopStateChanged
	}
	if err := replaceAgentFile(tempPath, statePath); err != nil {
		return err
	}
	return syncAgentParentDirectory(statePath)
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

func findCodexDesktopProjectID(projects map[string]any, projectDirectory string) string {
	for projectID, raw := range projects {
		entry, _ := raw.(map[string]any)
		roots := stringSlice(entry["rootPaths"])
		for _, root := range roots {
			if sameAgentPath(root, projectDirectory) {
				return projectID
			}
		}
	}
	return ""
}

func ensureStringMap(state map[string]any, key string) map[string]any {
	value, _ := state[key].(map[string]any)
	if value == nil {
		value = map[string]any{}
		state[key] = value
	}
	return value
}

func appendUniqueString(value any, item string) []any {
	items, _ := value.([]any)
	for _, existing := range items {
		if existingString, _ := existing.(string); existingString == item {
			return items
		}
	}
	return append(items, item)
}

func prependUniqueString(value any, item string) []any {
	items, _ := value.([]any)
	out := []any{item}
	for _, existing := range items {
		if existingString, _ := existing.(string); existingString == item {
			continue
		}
		out = append(out, existing)
	}
	return out
}

func removeThreadFromSidebarOrders(sidebarOrders map[string]any, sessionID string) {
	for projectID, raw := range sidebarOrders {
		entry, _ := raw.(map[string]any)
		if entry == nil {
			continue
		}
		entry["threadIds"] = removeStringValue(entry["threadIds"], sessionID)
		if len(stringSlice(entry["threadIds"])) == 0 {
			delete(sidebarOrders, projectID)
		}
	}
}

func removeStringValue(value any, item string) []any {
	items, _ := value.([]any)
	out := make([]any, 0, len(items))
	for _, existing := range items {
		if existingString, _ := existing.(string); existingString == item {
			continue
		}
		out = append(out, existing)
	}
	return out
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

func cleanOptionalAgentPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return filepath.Clean(path)
}
