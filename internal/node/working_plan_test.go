package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWorkingPlanInitGetListTaskCASAndEvidenceLimit(t *testing.T) {
	project := t.TempDir()
	client, err := New(Config{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	init, err := client.workingContextControl(ctx, "plan.init", map[string]any{
		"projectPath": project, "planId": "release-041", "goal": "finish backend", "targetVersion": "0.4.1",
		"tasks": []map[string]any{{"id": "FS-041-001", "title": "Plan model", "status": "in_progress", "completion": 25}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if init.State == nil || init.State.PlanID != "release-041" || len(init.State.Tasks) != 1 || init.Revision == "" {
		t.Fatalf("init=%+v", init)
	}
	if _, err := client.workingContextControl(ctx, "plan.init", map[string]any{"projectPath": project, "planId": "other-plan", "goal": "isolated"}); err != nil {
		t.Fatal(err)
	}
	get, err := client.workingContextControl(ctx, "plan.get", map[string]any{"projectPath": project, "planId": "release-041"})
	if err != nil || get.Revision != init.Revision {
		t.Fatalf("get=%+v err=%v", get, err)
	}
	list, err := client.workingContextControl(ctx, "plan.list", map[string]any{"projectPath": project})
	if err != nil || len(list.Plans) != 2 || list.Plans[0].PlanID != "other-plan" || list.Plans[1].PlanID != "release-041" {
		t.Fatalf("list=%+v err=%v", list, err)
	}

	completion := 100
	updated, err := client.workingContextControl(ctx, "task.update", map[string]any{
		"projectPath": project, "planId": "release-041", "expectedRevision": init.Revision,
		"taskId": "FS-041-001", "taskStatus": "done", "completion": completion,
		"evidence": map[string]any{"summary": "unit tests passed", "kind": "test", "reference": "go test ./internal/node"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.State.Tasks[0].Status != "done" || len(updated.State.Tasks[0].Evidences) != 1 {
		t.Fatalf("updated=%+v", updated.State.Tasks[0])
	}
	if _, err := client.workingContextControl(ctx, "task.update", map[string]any{"projectPath": project, "planId": "release-041", "expectedRevision": init.Revision, "taskId": "FS-041-001", "taskStatus": "pending"}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale CAS error=%v", err)
	}

	revision := updated.Revision
	for i := 1; i < maxWorkingTaskEvidences; i++ {
		result, err := client.workingContextControl(ctx, "task.update", map[string]any{"projectPath": project, "planId": "release-041", "expectedRevision": revision, "taskId": "FS-041-001", "evidence": map[string]any{"summary": fmt.Sprintf("evidence %d", i)}})
		if err != nil {
			t.Fatalf("evidence %d: %v", i, err)
		}
		revision = result.Revision
	}
	if _, err := client.workingContextControl(ctx, "task.update", map[string]any{"projectPath": project, "planId": "release-041", "expectedRevision": revision, "taskId": "FS-041-001", "evidence": map[string]any{"summary": "overflow"}}); err == nil || !strings.Contains(err.Error(), "32 evidences") {
		t.Fatalf("evidence overflow error=%v", err)
	}
}

func TestWorkingPlanTaskLimit(t *testing.T) {
	project := t.TempDir()
	client, _ := New(Config{DataDir: t.TempDir(), Version: "test"})
	tasks := make([]map[string]any, maxWorkingPlanTasks+1)
	for i := range tasks {
		tasks[i] = map[string]any{"id": fmt.Sprintf("T-%03d", i), "title": "task"}
	}
	if _, err := client.workingContextControl(context.Background(), "plan.init", map[string]any{"projectPath": project, "planId": "too-many", "goal": "test", "tasks": tasks}); err == nil {
		t.Fatal("plan.init accepted more than 500 tasks")
	}
}

func TestWorkingMarkdownInitSyncPreservesManualAndAppendCAS(t *testing.T) {
	project := t.TempDir()
	client, _ := New(Config{DataDir: t.TempDir(), Version: "test"})
	ctx := context.Background()
	init, err := client.workingContextControl(ctx, "plan.init", map[string]any{
		"projectPath": project, "planId": "workspace", "goal": "test workspace", "targetVersion": "0.4.1", "initializeMarkdown": true,
		"tasks": []map[string]any{{"id": "T-1", "title": "backend", "status": "blocked", "blockedReason": "waiting for test", "completion": 50}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manualPath := filepath.Join(project, "docs", "progress", "00-current-state.md")
	raw, err := os.ReadFile(manualPath)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("human manual note\n")...)
	if err := os.WriteFile(manualPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	synced, err := client.workingContextControl(ctx, "plan.sync", map[string]any{"projectPath": project, "planId": "workspace", "expectedRevision": init.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if len(synced.Synced) != 6 {
		t.Fatalf("synced=%v", synced.Synced)
	}
	after, _ := os.ReadFile(manualPath)
	if !strings.Contains(string(after), "human manual note") || !strings.Contains(string(after), "planId: `workspace`") {
		t.Fatalf("sync did not preserve manual content:\n%s", after)
	}

	read, err := client.workingContextControl(ctx, "markdown.read", map[string]any{"projectPath": project, "planId": "workspace", "markdownPath": "docs/progress/03-acceptance-log.md"})
	if err != nil {
		t.Fatal(err)
	}
	appended, err := client.workingContextControl(ctx, "markdown.append", map[string]any{"projectPath": project, "planId": "workspace", "markdownPath": "docs/progress/03-acceptance-log.md", "content": "\nmanual acceptance\n", "expectedFileRevision": read.FileRevision})
	if err != nil || appended.FileRevision == read.FileRevision {
		t.Fatalf("append=%+v err=%v", appended, err)
	}
	if _, err := client.workingContextControl(ctx, "markdown.append", map[string]any{"projectPath": project, "planId": "workspace", "markdownPath": "docs/progress/03-acceptance-log.md", "content": "stale", "expectedFileRevision": read.FileRevision}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale file CAS error=%v", err)
	}
	listed, err := client.workingContextControl(ctx, "markdown.list", map[string]any{"projectPath": project, "planId": "workspace"})
	if err != nil || len(listed.Markdown) != 6 {
		t.Fatalf("markdown.list=%+v err=%v", listed.Markdown, err)
	}
}

func TestWorkingPlanInitBindsExistingMarkdownWithoutReplacingIt(t *testing.T) {
	project := t.TempDir()
	root := filepath.Join(project, "docs", "progress")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := []byte("# Existing\n\n<!-- fast-spider:managed:current-state:start -->\nold managed\n<!-- fast-spider:managed:current-state:end -->\n\n## Manual Notes\nkeep this exactly\n")
	path := filepath.Join(root, "00-current-state.md")
	if err := os.WriteFile(path, existing, 0o644); err != nil {
		t.Fatal(err)
	}
	client, _ := New(Config{DataDir: t.TempDir(), Version: "test"})
	if _, err := client.workingContextControl(context.Background(), "plan.init", map[string]any{"projectPath": project, "planId": "bind", "goal": "bind", "initializeMarkdown": true}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(existing) {
		t.Fatalf("plan.init replaced existing Markdown:\n%s", after)
	}
}

func TestWorkingMarkdownRejectsTraversalLinksTypesAndLimits(t *testing.T) {
	project := t.TempDir()
	client, _ := New(Config{DataDir: t.TempDir(), Version: "test"})
	ctx := context.Background()
	_, err := client.workingContextControl(ctx, "plan.init", map[string]any{"projectPath": project, "planId": "safe", "goal": "safe markdown", "initializeMarkdown": true})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside.md", "docs/progress/not-markdown.txt"} {
		if _, err := client.workingContextControl(ctx, "markdown.read", map[string]any{"projectPath": project, "planId": "safe", "markdownPath": path}); err == nil {
			t.Fatalf("unsafe path %q was accepted", path)
		}
	}
	outside := t.TempDir()
	link := filepath.Join(project, "docs", "progress", "linked.md")
	if err := os.Symlink(filepath.Join(outside, "outside.md"), link); err == nil {
		if _, err := client.workingContextControl(ctx, "markdown.list", map[string]any{"projectPath": project, "planId": "safe"}); err == nil {
			t.Fatal("symlink was accepted")
		}
		_ = os.Remove(link)
	}
	if runtime.GOOS == "windows" {
		junction := filepath.Join(project, "docs", "progress", "junction")
		if err := createTestJunction(junction, outside); err == nil {
			if _, err := client.workingContextControl(ctx, "markdown.list", map[string]any{"projectPath": project, "planId": "safe"}); err == nil {
				t.Fatal("junction was accepted")
			}
		}
	}
	oversized := filepath.Join(project, "docs", "progress", "oversized.md")
	if err := os.WriteFile(oversized, []byte(strings.Repeat("x", maxWorkingMarkdownFileBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := client.workingContextControl(ctx, "markdown.list", map[string]any{"projectPath": project, "planId": "safe"}); err == nil {
		t.Fatal("oversized Markdown file was accepted")
	}
	_ = os.Remove(oversized)
	for i := 0; i < maxWorkingMarkdownFiles-5; i++ { // six defaults plus 59 files = 65
		if err := os.WriteFile(filepath.Join(project, "docs", "progress", fmt.Sprintf("extra-%02d.md", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.workingContextControl(ctx, "markdown.list", map[string]any{"projectPath": project, "planId": "safe"}); err == nil {
		t.Fatal("workspace with more than 64 Markdown files was accepted")
	}

	totalProject := t.TempDir()
	totalClient, _ := New(Config{DataDir: t.TempDir(), Version: "test"})
	if _, err := totalClient.workingContextControl(ctx, "plan.init", map[string]any{"projectPath": totalProject, "planId": "total", "goal": "total", "markdownRoot": "workspace"}); err != nil {
		t.Fatal(err)
	}
	totalRoot := filepath.Join(totalProject, "workspace")
	if err := os.MkdirAll(totalRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	chunk := []byte(strings.Repeat("x", 500<<10))
	for i := 0; i < 9; i++ {
		if err := os.WriteFile(filepath.Join(totalRoot, fmt.Sprintf("part-%d.md", i)), chunk, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := totalClient.workingContextControl(ctx, "markdown.list", map[string]any{"projectPath": totalProject, "planId": "total"}); err == nil {
		t.Fatal("workspace above total byte limit was accepted")
	}
}

func TestWorkingProgressWatchReturnsOnRevisionChange(t *testing.T) {
	project := t.TempDir()
	client, _ := New(Config{DataDir: t.TempDir(), Version: "test"})
	ctx := context.Background()
	init, err := client.workingContextControl(ctx, "plan.init", map[string]any{"projectPath": project, "planId": "watch", "goal": "watch", "tasks": []map[string]any{{"id": "T-1", "title": "task"}}})
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() {
		time.Sleep(150 * time.Millisecond)
		_, updateErr := client.workingContextControl(context.Background(), "task.update", map[string]any{"projectPath": project, "planId": "watch", "expectedRevision": init.Revision, "taskId": "T-1", "taskStatus": "done"})
		errCh <- updateErr
	}()
	watch, err := client.workingContextControl(ctx, "progress.watch", map[string]any{"projectPath": project, "planId": "watch", "sinceRevision": init.Revision, "waitSeconds": 2})
	if err != nil {
		t.Fatal(err)
	}
	if updateErr := <-errCh; updateErr != nil {
		t.Fatal(updateErr)
	}
	if !watch.Changed || watch.Revision == init.Revision || watch.State.Tasks[0].Status != "done" {
		t.Fatalf("watch=%+v", watch)
	}
}

func createTestJunction(link, target string) error {
	if runtime.GOOS != "windows" {
		return errors.New("not windows")
	}
	if err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).Run(); err == nil {
		return nil
	}
	return os.Symlink(target, link)
}
