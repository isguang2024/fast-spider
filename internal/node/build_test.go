package node

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildProfilesAreLocalAndPermissionBound(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	store := NewWorkspaceStore(dataDir)
	workspace, err := store.Add(root, "build-fixture")
	if err != nil {
		t.Fatal(err)
	}
	profile := BuildProfileRecord{ProfileID: "test", DisplayName: "Test", Argv: shellEchoArgv("build-ok"), Cwd: ".", TimeoutSeconds: 10}
	if err := store.SetBuildProfile(workspace.WorkspaceID, profile); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}

	listed, err := client.buildControl(context.Background(), workspace.WorkspaceID, map[string]any{"action": "list"})
	if err != nil || len(listed.Profiles) != 1 || listed.Profiles[0].ProfileID != "test" {
		t.Fatalf("build list=%+v err=%v", listed, err)
	}
	if _, err := client.buildControl(context.Background(), workspace.WorkspaceID, map[string]any{"action": "run", "profileId": "test", "idempotencyKey": "idem_build_test_001"}); err != ErrPermissionDenied {
		t.Fatalf("build run without permission error=%v", err)
	}
	if err := store.SetPermissions(workspace.WorkspaceID, []string{"read", "build"}); err != nil {
		t.Fatal(err)
	}
	run, err := client.buildControl(context.Background(), workspace.WorkspaceID, map[string]any{"action": "run", "profileId": "test", "idempotencyKey": "idem_build_test_001"})
	if err != nil || run.Job == nil {
		t.Fatalf("build run=%+v err=%v", run, err)
	}
	final := waitJobTerminal(t, client.jobs, run.Job.JobID, 10*time.Second)
	if final.State != "completed" {
		t.Fatalf("build job=%+v", final)
	}
	again, err := client.buildControl(context.Background(), workspace.WorkspaceID, map[string]any{"action": "run", "profileId": "test", "idempotencyKey": "idem_build_test_001"})
	if err != nil || again.Job == nil || again.Job.JobID != run.Job.JobID {
		t.Fatalf("idempotent build run=%+v err=%v original=%s", again, err, run.Job.JobID)
	}
	if _, err := client.buildControl(context.Background(), workspace.WorkspaceID, map[string]any{"action": "run", "profileId": "test", "idempotencyKey": "idem_build_test_002", "argv": shellEchoArgv("remote-command")}); err == nil {
		t.Fatal("remote caller supplied an arbitrary build command")
	}

	if err := store.SetBuildProfile(workspace.WorkspaceID, BuildProfileRecord{ProfileID: "bad-cwd", Argv: shellEchoArgv("bad"), Cwd: filepath.Join(root, "outside"), TimeoutSeconds: 10}); !errors.Is(err, ErrPathOutsideWorkspace) {
		t.Fatalf("absolute build cwd error=%v", err)
	}
	if err := store.SetBuildProfile(workspace.WorkspaceID, BuildProfileRecord{ProfileID: "parent-cwd", Argv: shellEchoArgv("bad"), Cwd: "../outside", TimeoutSeconds: 10}); !errors.Is(err, ErrPathOutsideWorkspace) {
		t.Fatalf("parent build cwd error=%v", err)
	}
	if err := store.SetBuildProfile(workspace.WorkspaceID, BuildProfileRecord{ProfileID: "long-timeout", Argv: shellEchoArgv("bad"), Cwd: ".", TimeoutSeconds: int64(maxJobTimeout/time.Second) + 1}); err == nil {
		t.Fatal("build profile accepted timeout beyond limit")
	}
	if err := store.SetBuildProfile(workspace.WorkspaceID, BuildProfileRecord{ProfileID: "bad-id!", Argv: shellEchoArgv("bad"), Cwd: ".", TimeoutSeconds: 10}); err == nil {
		t.Fatal("build profile accepted invalid profile ID")
	}

	slow := BuildProfileRecord{ProfileID: "slow", DisplayName: "Slow", Argv: shellSleepArgv(), Cwd: ".", TimeoutSeconds: 20}
	if err := store.SetBuildProfile(workspace.WorkspaceID, slow); err != nil {
		t.Fatal(err)
	}
	slowRun, err := client.buildControl(context.Background(), workspace.WorkspaceID, map[string]any{"action": "run", "profileId": "slow", "idempotencyKey": "idem_build_test_slow"})
	if err != nil || slowRun.Job == nil {
		t.Fatalf("slow build run=%+v err=%v", slowRun, err)
	}
	if err := store.SetPermissions(workspace.WorkspaceID, []string{"read"}); err != nil {
		t.Fatal(err)
	}
	revoked := waitJobTerminal(t, client.jobs, slowRun.Job.JobID, 8*time.Second)
	if revoked.State != "canceled" || !strings.Contains(revoked.Error, "permission revoked") {
		t.Fatalf("build permission revocation job=%+v", revoked)
	}
}
