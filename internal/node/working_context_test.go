package node

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	set, err := client.workingContextControl(ctx, "set", map[string]any{
		"projectPath": root,
		"goal":        "实现轻量 Working Context",
		"completed":   []string{"Presentation Relay 已完成"},
		"constraints": []string{"不保存聊天原文", "Git/文件是最终事实源"},
		"pending":     []string{"大图自动压缩"},
		"keyFiles":    []string{"app.txt"},
		"facts":       []string{"公开仓库需要 clean export/squash history"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !set.Exists || set.State == nil || set.Revision == "" || !set.CurrentGit.IsRepository || set.CurrentGit.Head == "" {
		t.Fatalf("set result=%+v", set)
	}
	baselineHead := set.CurrentGit.Head
	if set.State.Baseline.Commit != baselineHead || set.State.Baseline.Branch != set.CurrentGit.Branch {
		t.Fatalf("baseline=%+v currentGit=%+v", set.State.Baseline, set.CurrentGit)
	}
	if len(set.State.KeyFiles) != 1 || set.State.KeyFiles[0] != "app.txt" {
		t.Fatalf("keyFiles=%v", set.State.KeyFiles)
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
	if !get.Exists || get.State == nil || !get.CurrentGit.Dirty {
		t.Fatalf("get result=%+v", get)
	}
	if get.State.Baseline.Commit != baselineHead {
		t.Fatalf("saved baseline changed: %q want %q", get.State.Baseline.Commit, baselineHead)
	}

	cleared, err := client.workingContextControl(ctx, "clear", map[string]any{"projectPath": root})
	if err != nil {
		t.Fatal(err)
	}
	if !cleared.Cleared || cleared.Exists {
		t.Fatalf("clear result=%+v", cleared)
	}
	empty, err := client.workingContextControl(ctx, "get", map[string]any{"projectPath": root})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Exists || !empty.CurrentGit.Dirty {
		t.Fatalf("empty result=%+v", empty)
	}
}

func TestWorkingContextBoundsAndProjectBoundary(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{DataDir: filepath.Join(t.TempDir(), "node"), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.workingContextControl(context.Background(), "set", map[string]any{"projectPath": project}); err == nil {
		t.Fatal("set without goal was accepted")
	}
	items := make([]string, maxWorkingContextItems+1)
	for index := range items {
		items[index] = "item"
	}
	if _, err := client.workingContextControl(context.Background(), "set", map[string]any{
		"projectPath": project, "goal": "test", "completed": items,
	}); err == nil {
		t.Fatal("oversized item list was accepted")
	}
	if _, err := client.workingContextControl(context.Background(), "set", map[string]any{
		"projectPath": project, "goal": "test", "keyFiles": []string{outside},
	}); err == nil || !strings.Contains(err.Error(), "outside projectPath") {
		t.Fatalf("outside key file error=%v", err)
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
