package node

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectModePathGuard(t *testing.T) {
	projectRoot := t.TempDir()
	insideFile := filepath.Join(projectRoot, "README.md")
	if err := os.WriteFile(insideFile, []byte("project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideRoot := t.TempDir()
	outsideFile := filepath.Join(outsideRoot, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := newProjectPolicy(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	insideDir := filepath.Join(projectRoot, "src")
	if err := os.Mkdir(insideDir, 0o700); err != nil {
		t.Fatal(err)
	}

	insideCases := []struct {
		capability string
		action     string
		params     map[string]any
	}{
		{"file.read", "read", map[string]any{"path": insideFile}},
		{"file.write", "create", map[string]any{"path": filepath.Join(projectRoot, "new.txt")}},
		{"file.write", "preview", map[string]any{"path": filepath.Join(projectRoot, "preview.txt"), "previewOf": "create"}},
		{"code.search", "search", map[string]any{"path": projectRoot}},
		{"shell.exec", "run", map[string]any{"cwd": insideDir}},
		{"build.exec", "run", map[string]any{"cwd": projectRoot}},
		{"working.context", "get", map[string]any{"projectPath": projectRoot}},
	}
	for _, item := range insideCases {
		if err := policy.validate(item.capability, item.action, item.params); err != nil {
			t.Errorf("%s/%s rejected inside path: %v", item.capability, item.action, err)
		}
	}

	outsideCases := []struct {
		capability string
		action     string
		params     map[string]any
	}{
		{"file.read", "read", map[string]any{"path": outsideFile}},
		{"file.write", "create", map[string]any{"path": filepath.Join(outsideRoot, "new.txt")}},
		{"code.search", "search", map[string]any{"path": outsideRoot}},
		{"shell.exec", "run", map[string]any{"cwd": outsideRoot}},
		{"git.repository", "status", map[string]any{"repositoryPath": outsideRoot}},
		{"working.context", "get", map[string]any{"projectPath": outsideRoot}},
	}
	for _, item := range outsideCases {
		if err := policy.validate(item.capability, item.action, item.params); !errors.Is(err, ErrProjectPathForbidden) {
			t.Errorf("%s/%s error=%v, want ErrProjectPathForbidden", item.capability, item.action, err)
		}
	}
	if err := policy.validate("git.repository", "createWorktree", map[string]any{"repositoryPath": projectRoot}); !errors.Is(err, ErrProjectModeRequired) {
		t.Fatalf("missing worktreePath error=%v, want ErrProjectModeRequired", err)
	}
}

func TestProjectModeNativeScreenshotDenied(t *testing.T) {
	policy, err := newProjectPolicy(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.validate("screenshot.capture", "desktop", nil); !errors.Is(err, ErrProjectNativeCaptureDenied) {
		t.Fatalf("desktop screenshot error=%v", err)
	}
	if got := capabilityError(ErrProjectNativeCaptureDenied); got.Code != "PROJECT_CAPABILITY_DENIED" {
		t.Fatalf("protocol code=%q", got.Code)
	}
}

func TestProjectModeAgentPaths(t *testing.T) {
	projectRoot := t.TempDir()
	image := filepath.Join(projectRoot, "image.png")
	if err := os.WriteFile(image, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideImage := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outsideImage, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := newProjectPolicy(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	inside := map[string]any{
		"workingDirectory": projectRoot,
		"localImages":      []any{image},
		"skills":           []any{map[string]any{"name": "demo", "path": image}},
	}
	if err := policy.validate("agent.control", "session.create", inside); err != nil {
		t.Fatalf("agent paths inside root rejected: %v", err)
	}
	outside := map[string]any{"workingDirectory": projectRoot, "localImages": []any{outsideImage}}
	if err := policy.validate("agent.control", "session.create", outside); !errors.Is(err, ErrProjectPathForbidden) {
		t.Fatalf("outside local image error=%v", err)
	}
	if err := policy.validate("agent.control", "session.create", map[string]any{}); !errors.Is(err, ErrProjectModeRequired) {
		t.Fatalf("missing agent project directory error=%v", err)
	}
}

func TestProjectModeMachineModeUnchanged(t *testing.T) {
	var policy *projectPolicy
	outside := filepath.Join(t.TempDir(), "not-created-yet.txt")
	if err := policy.validate("file.write", "create", map[string]any{"path": outside}); err != nil {
		t.Fatalf("machine mode unexpectedly rejected path: %v", err)
	}
}
