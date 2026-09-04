package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
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

func TestReadCodexDesktopSnapshotReadsProjectsOnly(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), codexDesktopStateFilename)
	projectRoot := filepath.Join(t.TempDir(), "project")
	existingID := "existing-project-id"
	initial := map[string]any{
		"local-projects": map[string]any{
			existingID: map[string]any{
				"id": existingID, "name": "Custom Name", "rootPaths": []any{projectRoot},
				"createdAt": json.Number("123"), "updatedAt": json.Number("123"),
			},
		},
		"project-order":          []any{existingID},
		"projectless-thread-ids": []any{"thread-1"},
		"thread-project-assignments": map[string]any{
			"thread-2": map[string]any{"projectId": existingID, "cwd": projectRoot},
		},
	}
	raw, err := json.Marshal(initial)
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
	if len(snapshot.Projects) != 1 || snapshot.Projects[0].ProjectID != existingID || !sameAgentPath(snapshot.Projects[0].ProjectDirectory, projectRoot) {
		t.Fatalf("projects=%#v", snapshot.Projects)
	}
	if snapshot.ProjectIDByKey[agentPathKey(projectRoot)] != existingID {
		t.Fatalf("project lookup=%#v", snapshot.ProjectIDByKey)
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
