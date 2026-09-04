package node

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkingContextSetGetClearAndLiveGitFacts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(root, "app.txt")
	if err := os.WriteFile(tracked, []byte("v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runWorkingContextGit(t, root, "init")
	runWorkingContextGit(t, root, "config", "user.email", "test@example.invalid")
	runWorkingContextGit(t, root, "config", "user.name", "Fast Spider Test")
	runWorkingContextGit(t, root, "add", "app.txt")
	runWorkingContextGit(t, root, "commit", "-m", "baseline")

	client, err := New(Config{DataDir: filepath.Join(t.TempDir(), "node"), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	text := "# 目标\n实现轻量项目上下文\n\n## 进度\n- 正在修改 API\n\n## 下一步\n- 运行测试"
	set, err := client.workingContextControl(ctx, "set", map[string]any{"projectPath": root, "text": text})
	if err != nil {
		t.Fatal(err)
	}
	if !set.Exists || set.State == nil || set.State.Text != text || set.Revision == "" || !set.CurrentGit.IsRepository || set.CurrentGit.Head == "" {
		t.Fatalf("set result=%+v", set)
	}
	if pathWithin(root, client.workingContextPath(root)) {
		t.Fatal("working context was stored inside the project")
	}

	if err := os.WriteFile(tracked, []byte("v2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	get, err := client.workingContextControl(ctx, "get", map[string]any{"projectPath": root})
	if err != nil {
		t.Fatal(err)
	}
	if !get.Exists || get.State == nil || get.State.Text != text || !get.CurrentGit.Dirty {
		t.Fatalf("get result=%+v", get)
	}
	if _, err := client.workingContextControl(ctx, "set", map[string]any{"projectPath": root, "text": "stale", "expectedRevision": "sha256:stale"}); err != ErrRevisionConflict {
		t.Fatalf("stale set error=%v", err)
	}

	updated, err := client.workingContextControl(ctx, "set", map[string]any{"projectPath": root, "text": "已完成", "expectedRevision": get.Revision})
	if err != nil || updated.State == nil || updated.State.Text != "已完成" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	cleared, err := client.workingContextControl(ctx, "clear", map[string]any{"projectPath": root, "expectedRevision": updated.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if !cleared.Cleared || cleared.Exists {
		t.Fatalf("clear result=%+v", cleared)
	}
}

func TestWorkingContextBoundsAndOnlySimpleActions(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{DataDir: filepath.Join(t.TempDir(), "node"), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.workingContextControl(context.Background(), "set", map[string]any{"projectPath": project}); err == nil {
		t.Fatal("set without text was accepted")
	}
	if _, err := client.workingContextControl(context.Background(), "set", map[string]any{"projectPath": project, "text": strings.Repeat("x", maxWorkingContextTextBytes+1)}); err == nil {
		t.Fatal("oversized text was accepted")
	}
	if _, err := client.workingContextControl(context.Background(), "plan.init", map[string]any{"projectPath": project, "text": "legacy"}); err == nil {
		t.Fatal("legacy plan action was accepted")
	}
}

func TestWorkingContextReadsLegacyStateAsText(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project")
	dataDir := filepath.Join(t.TempDir(), "node")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	resolvedProject, err := resolveWorkingContextProject(project)
	if err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{
		"schemaVersion": 2,
		"projectPath":   resolvedProject,
		"planId":        "default",
		"goal":          "完成旧任务",
		"completed":     []string{"接口已完成"},
		"pending":       []string{"运行测试"},
		"updatedAt":     time.Now().UTC(),
	}
	raw, _ := json.MarshalIndent(legacy, "", "  ")
	raw = append(raw, '\n')
	path := client.workingContextPath(resolvedProject)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := client.workingContextControl(context.Background(), "get", map[string]any{"projectPath": project})
	if err != nil {
		t.Fatal(err)
	}
	if result.State == nil || !strings.Contains(result.State.Text, "完成旧任务") || !strings.Contains(result.State.Text, "运行测试") {
		t.Fatalf("legacy result=%+v", result)
	}
}

func runWorkingContextGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
