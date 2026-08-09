package node

import (
	"context"
	"testing"
	"time"
)

func TestBuildExecUsesAbsoluteCwdAndIsIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	client, err := New(Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.buildControl(context.Background(), map[string]any{"action": "run", "argv": shellEchoArgv("bad"), "cwd": ".", "idempotencyKey": "idem_build_bad_001"}); err == nil {
		t.Fatal("relative build cwd unexpectedly succeeded")
	}
	run, err := client.buildControl(context.Background(), map[string]any{"action": "run", "argv": shellEchoArgv("build-ok"), "cwd": root, "timeoutSeconds": 10, "idempotencyKey": "idem_build_test_001"})
	if err != nil || run.Job == nil {
		t.Fatalf("build run=%+v err=%v", run, err)
	}
	final := waitJobTerminal(t, client.jobs, run.Job.JobID, 10*time.Second)
	if final.State != "completed" {
		t.Fatalf("build job=%+v", final)
	}
	again, err := client.buildControl(context.Background(), map[string]any{"action": "run", "argv": shellEchoArgv("build-ok"), "cwd": root, "timeoutSeconds": 10, "idempotencyKey": "idem_build_test_001"})
	if err != nil || again.Job == nil || again.Job.JobID != run.Job.JobID {
		t.Fatalf("idempotent build=%+v err=%v", again, err)
	}
	if _, err := client.buildControl(context.Background(), map[string]any{"action": "list"}); err == nil {
		t.Fatal("retired build list action unexpectedly succeeded")
	}
}
