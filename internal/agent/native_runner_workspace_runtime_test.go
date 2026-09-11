package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeWorkspaceRuntimeIntegratesChecksThenCleansWithoutCloudSlot(t *testing.T) {
	ctx := context.Background()
	root := nativeRunnerWorkspaceTestRepo(t)
	r, _, p := newNativeRunnerForTest(t, "isolate and integrate", map[string]nativeRunnerCheck{"unit": {Argv: []string{"test"}}})
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	p.Root = canonical
	tx, err := r.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = nativeSave(tx, "runner_projects", p.ID, "", p); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	task := addNativeTask(t, r, p.ID, "isolate", "task.txt", "unit")
	task.Workspace = &nativeRunnerWorkspace{Mode: "worktree"}
	task.State = "workspace_preparing"
	if err = r.saveTask(ctx, task, ""); err != nil {
		t.Fatal(err)
	}
	r.startWorkspace(ctx, p, task, "prepare")
	r.lifecycleWG.Wait()
	if err = r.drainWorkspace(ctx); err != nil {
		t.Fatal(err)
	}
	task = loadNativeTask(t, r, p.ID, task.ID)
	if task.State != "queued" || task.Workspace.Path == "" {
		t.Fatalf("prepare failed: %+v", task)
	}
	req, err := r.compile(p, task, []nativeRunnerTask{task})
	if err != nil {
		t.Fatal(err)
	}
	if req.WorkingDirectory != task.Workspace.Path || !nativeScopeOverlap(task.Workspace.Path, req.WriteScope) {
		t.Fatalf("dispatch escaped isolated root: %+v", req)
	}
	if err = os.WriteFile(filepath.Join(task.Workspace.Path, "task.txt"), []byte("finished"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = nativeRunnerGit(ctx, task.Workspace.Path, "add", "--", "task.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err = nativeRunnerGit(ctx, task.Workspace.Path, "commit", "-m", "task"); err != nil {
		t.Fatal(err)
	}
	task.Result = &nativeRunnerResult{Terminal: true, Outcome: "completed"}
	task.State = "returned"
	if err = r.saveTask(ctx, task, ""); err != nil {
		t.Fatal(err)
	}
	if err = r.tickWorkspaces(ctx, p, []nativeRunnerTask{task}, nil); err != nil {
		t.Fatal(err)
	}
	r.lifecycleWG.Wait()
	if err = r.drainWorkspace(ctx); err != nil {
		t.Fatal(err)
	}
	task = loadNativeTask(t, r, p.ID, task.ID)
	if task.Workspace.SealedCommit == "" {
		t.Fatal("result was not bound to the exact source commit")
	}
	task.State = "awaiting_integration"
	task.Workspace.State = "pending_integration"
	task.Validations = map[string]nativeRunnerValidation{"unit": {State: "passed"}}
	if err = r.saveTask(ctx, task, ""); err != nil {
		t.Fatal(err)
	}
	if nativeHolds(task) {
		t.Fatal("integration queue occupied Cloud capacity")
	}
	shared := nativeRunnerTask{ID: "shared", State: "active", Scope: filepath.Join(p.Root, "unrelated"), Request: &nativeRunnerDispatch{}}
	if err = r.tickWorkspaces(ctx, p, []nativeRunnerTask{task}, []nativeRunnerTask{task, shared}); err != nil {
		t.Fatal(err)
	}
	if r.workspaceInFlight[task.ID] {
		t.Fatal("changed main HEAD under a shared writer")
	}
	if err = r.tickWorkspaces(ctx, p, []nativeRunnerTask{task}, []nativeRunnerTask{task}); err != nil {
		t.Fatal(err)
	}
	r.lifecycleWG.Wait()
	if err = r.drainWorkspace(ctx); err != nil {
		t.Fatal(err)
	}
	task = loadNativeTask(t, r, p.ID, task.ID)
	if task.State != "integrating" || task.Workspace.State != "integrated" || len(task.Validations) != 0 {
		t.Fatalf("integration bypassed affected checks: %+v", task)
	}
	if err = r.tickWorkspaces(ctx, p, []nativeRunnerTask{task}, nil); err != nil {
		t.Fatal(err)
	}
	if loadNativeTask(t, r, p.ID, task.ID).State == "accepted" {
		t.Fatal("accepted without integrated check")
	}
	task.Validations = map[string]nativeRunnerValidation{"unit": {State: "passed"}}
	if err = r.saveTask(ctx, task, ""); err != nil {
		t.Fatal(err)
	}
	if err = r.tickWorkspaces(ctx, p, []nativeRunnerTask{task}, nil); err != nil {
		t.Fatal(err)
	}
	task = loadNativeTask(t, r, p.ID, task.ID)
	if task.State != "accepted" {
		t.Fatal("passed integration was not adopted")
	}
	if err = r.tickWorkspaces(ctx, p, []nativeRunnerTask{task}, nil); err != nil {
		t.Fatal(err)
	}
	r.lifecycleWG.Wait()
	if err = r.drainWorkspace(ctx); err != nil {
		t.Fatal(err)
	}
	task = loadNativeTask(t, r, p.ID, task.ID)
	if task.Workspace.State != "cleaned" {
		t.Fatalf("workspace not reclaimed: %+v", task.Workspace)
	}
}

func TestNativeWorkspaceIntegrationBarrierLeavesOtherWorktreesReady(t *testing.T) {
	p := nativeRunnerProject{ID: "p", GoalVersion: "v"}
	integrate := nativeRunnerTask{ID: "merge", ProjectID: "p", Kind: "work", State: "awaiting_integration", Scope: "/repo/backend", Result: &nativeRunnerResult{Outcome: "completed"}, Workspace: &nativeRunnerWorkspace{Mode: "worktree", Root: "/repo", Path: "/temp/task"}}
	shared := nativeRunnerTask{ID: "shared", ProjectID: "p", Kind: "work", State: "queued", GoalVersion: "v", Scope: "/repo/frontend"}
	isolated := shared
	isolated.ID = "isolated"
	isolated.Workspace = &nativeRunnerWorkspace{Mode: "worktree"}
	s := nativeBuildSchedulingSnapshot([]nativeRunnerTask{integrate, shared, isolated}, []nativeRunnerProject{p}, 8)
	if s.Selected[shared.ID] || !s.Selected[isolated.ID] || s.GlobalActive != 0 {
		t.Fatalf("integration barrier/capacity wrong: %+v", s)
	}
}

func TestNativeWorkspaceSealedCommitRejectsLaterBranchChange(t *testing.T) {
	ctx := context.Background()
	root := nativeRunnerWorkspaceTestRepo(t)
	ws, err := nativePrepareWorkspace(ctx, root, "p", "sealed", "提交校验", "task.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	original, err := nativeRunnerGitHead(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"first", "second"} {
		if err = os.WriteFile(filepath.Join(ws.Path, "task.txt"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = nativeRunnerGit(ctx, ws.Path, "add", "--", "task.txt"); err != nil {
			t.Fatal(err)
		}
		if _, err = nativeRunnerGit(ctx, ws.Path, "commit", "-m", content); err != nil {
			t.Fatal(err)
		}
		if content == "first" {
			if err = ws.Inspect(ctx); err != nil {
				t.Fatal(err)
			}
			ws.SealedCommit = ws.HeadCommit
		}
	}
	if _, err = nativeIntegrateWorkspace(ctx, root, "task.txt", ws); err == nil {
		t.Fatal("merged a commit that was not checked and accepted")
	}
	after, err := nativeRunnerGitHead(ctx, root)
	if err != nil || after != original {
		t.Fatal("main changed despite source-commit fence")
	}
}
