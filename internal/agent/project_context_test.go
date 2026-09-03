package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestResolveAgentProjectContextGroupsLinkedWorktreeUnderPrimaryRoot(t *testing.T) {
	mainRoot := filepath.Join(t.TempDir(), "main")
	worktreeRoot := filepath.Join(t.TempDir(), "feature")
	if err := os.MkdirAll(mainRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, mainRoot, "init")
	runGitForTest(t, mainRoot, "config", "user.name", "Fast Spider Test")
	runGitForTest(t, mainRoot, "config", "user.email", "fast-spider@example.invalid")
	if err := os.WriteFile(filepath.Join(mainRoot, "README.md"), []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, mainRoot, "add", "README.md")
	runGitForTest(t, mainRoot, "commit", "-m", "initial")
	runGitForTest(t, mainRoot, "worktree", "add", "-b", "feature", worktreeRoot)
	workingDirectory := filepath.Join(worktreeRoot, "nested")
	if err := os.MkdirAll(workingDirectory, 0o755); err != nil {
		t.Fatal(err)
	}

	project := resolveAgentProjectContext(context.Background(), workingDirectory)
	if !project.IsGitRepository {
		t.Fatal("linked worktree was not recognized as Git")
	}
	if !sameAgentPath(project.ProjectDirectory, mainRoot) {
		t.Fatalf("projectDirectory=%q want %q", project.ProjectDirectory, mainRoot)
	}
	if !sameAgentPath(project.WorkingDirectory, workingDirectory) {
		t.Fatalf("workingDirectory=%q want %q", project.WorkingDirectory, workingDirectory)
	}
	if project.ProjectID == "" {
		t.Fatal("projectId is empty")
	}
}

func TestResolveAgentProjectContextDoesNotRegisterNonGitDirectory(t *testing.T) {
	root := t.TempDir()
	project := resolveAgentProjectContext(context.Background(), root)
	if project.IsGitRepository || project.ProjectID != "" {
		t.Fatalf("non-Git directory became a project: %+v", project)
	}
	if !sameAgentPath(project.ProjectDirectory, root) {
		t.Fatalf("projectDirectory=%q want %q", project.ProjectDirectory, root)
	}
}

func TestCodexLocalProjectIDMatchesDesktopConventionOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Codex Desktop path convention is Windows-specific")
	}
	if got, want := codexLocalProjectID(`C:\projects\example`), "local-8c0a8134bf0dd972eb3a471cc08cbe53"; got != want {
		t.Fatalf("projectId=%q want %q", got, want)
	}
}

func TestSyncCodexDesktopProjectReusesProjectAndAssignsWorktree(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), codexDesktopStateFilename)
	projectRoot := filepath.Join(t.TempDir(), "project")
	worktreeRoot := filepath.Join(t.TempDir(), "worktree")
	existingID := "existing-project-id"
	initial := map[string]any{
		"local-projects": map[string]any{
			existingID: map[string]any{
				"id": existingID, "name": "Custom Name", "rootPaths": []any{projectRoot},
				"createdAt": json.Number("123"), "updatedAt": json.Number("123"),
			},
		},
		"project-order":              []any{existingID},
		"projectless-thread-ids":     []any{"thread-1", "other-thread"},
		"thread-project-assignments": map[string]any{},
		"unrelated":                  map[string]any{"kept": true},
	}
	raw, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	project := agentProjectContext{
		WorkingDirectory: worktreeRoot,
		ProjectDirectory: projectRoot,
		ProjectID:        codexLocalProjectID(projectRoot),
		IsGitRepository:  true,
	}
	projectID, changed, err := syncCodexDesktopProject(statePath, "thread-1", project, time.UnixMilli(456))
	if err != nil {
		t.Fatal(err)
	}
	if !changed || projectID != existingID {
		t.Fatalf("projectId=%q changed=%v", projectID, changed)
	}
	snapshot, err := readCodexDesktopSnapshot(statePath)
	if err != nil {
		t.Fatal(err)
	}
	assignment := snapshot.Assignments["thread-1"]
	if assignment.ProjectID != existingID || !sameAgentPath(assignment.ProjectDirectory, projectRoot) || !sameAgentPath(assignment.WorkingDirectory, worktreeRoot) {
		t.Fatalf("assignment=%+v", assignment)
	}
	updatedRaw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := decodeCodexDesktopState(updatedRaw)
	if err != nil {
		t.Fatal(err)
	}
	projectless := stringSlice(updated["projectless-thread-ids"])
	if len(projectless) != 1 || projectless[0] != "other-thread" {
		t.Fatalf("projectless-thread-ids=%#v", projectless)
	}
	sidebarOrders, _ := updated["sidebar-project-thread-orders"].(map[string]any)
	sidebarProject, _ := sidebarOrders[existingID].(map[string]any)
	if threadIDs := stringSlice(sidebarProject["threadIds"]); len(threadIDs) != 1 || threadIDs[0] != "thread-1" {
		t.Fatalf("sidebar threadIds=%#v", threadIDs)
	}
	unrelated, _ := updated["unrelated"].(map[string]any)
	if kept, _ := unrelated["kept"].(bool); !kept {
		t.Fatal("unrelated state was not preserved")
	}
}

func TestSyncCodexDesktopProjectSkipsNonGitDirectory(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), codexDesktopStateFilename)
	if err := os.WriteFile(statePath, []byte(`{"local-projects":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	projectID, changed, err := syncCodexDesktopProject(statePath, "thread", agentProjectContext{WorkingDirectory: t.TempDir()}, time.Now())
	if err != nil || changed || projectID != "" {
		t.Fatalf("projectId=%q changed=%v err=%v", projectID, changed, err)
	}
}

func TestReadCodexDesktopSnapshotIncludesProjectlessThreads(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), codexDesktopStateFilename)
	rootHint := filepath.Join(t.TempDir(), "projectless-root")
	outputDirectory := filepath.Join(rootHint, "task", "outputs")
	state := map[string]any{
		"local-projects":                        map[string]any{},
		"thread-project-assignments":            map[string]any{},
		"projectless-thread-ids":                []any{"projectless-thread"},
		"thread-workspace-root-hints":           map[string]any{"projectless-thread": rootHint},
		"thread-projectless-output-directories": map[string]any{"projectless-thread": outputDirectory},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := readCodexDesktopSnapshot(statePath)
	if err != nil {
		t.Fatal(err)
	}
	thread, ok := snapshot.Threads["projectless-thread"]
	if !ok || !thread.Projectless || !sameAgentPath(thread.WorkingDirectory, rootHint) || !sameAgentPath(thread.OutputDirectory, outputDirectory) {
		t.Fatalf("projectless thread=%+v exists=%v", thread, ok)
	}
	if _, assigned := snapshot.Assignments["projectless-thread"]; assigned {
		t.Fatal("projectless thread was treated as a project assignment")
	}
}

func runGitForTest(t *testing.T, directory string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", directory}, args...)
	command := exec.Command("git", commandArgs...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
