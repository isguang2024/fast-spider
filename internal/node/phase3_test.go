package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestPhase3EditShellAndJobsUseMachineBoundary(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	path := filepath.Join(root, "main.txt")
	if err := os.WriteFile(path, []byte("alpha\nold value\nomega\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{DataDir: dataDir, Version: "test", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.jobs.CancelAll(context.Background()) }()

	call := func(capability, action string, params map[string]any) protocolv1.CapabilityResponse {
		return client.handleCapabilityRequest(context.Background(), protocolv1.CapabilityRequest{
			MessageType: protocolv1.MessageCapabilityRequest,
			RequestId:   "req_phase3_1234567890", Capability: capability, Action: action, Params: params,
			Deadline: protocolv1.Timestamp(time.Now().Add(10 * time.Second)), Timestamp: protocolv1.Timestamp(time.Now()),
		})
	}
	read := call("file.read", "read", map[string]any{"path": path})
	if read.Error != nil {
		t.Fatalf("initial read error=%+v", read.Error)
	}
	fileSHA, _ := read.Result["fileSha256"].(string)
	if fileSHA == "" {
		t.Fatalf("file_read did not return sha: %#v", read.Result)
	}

	edit := call("file.write", "edit", map[string]any{"path": path, "oldText": "old value", "newText": "new value", "expectedFileSha256": fileSHA})
	if edit.Error != nil {
		t.Fatalf("file edit error=%+v", edit.Error)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "new value") || strings.Contains(string(content), "old value") {
		t.Fatalf("edited content=%q", content)
	}
	stale := call("file.write", "edit", map[string]any{"path": path, "oldText": "new value", "newText": "again", "expectedFileSha256": fileSHA})
	if stale.Error == nil || stale.Error.Code != "REVISION_CONFLICT" {
		t.Fatalf("stale edit=%+v", stale)
	}
	detailPath, _ := stale.Error.Details["path"].(string)
	expectedPath, _ := ResolveMachinePath(path)
	if !strings.EqualFold(filepath.Clean(detailPath), filepath.Clean(expectedPath)) || fmt.Sprint(stale.Error.Details["expectedSha256"]) != fileSHA || fmt.Sprint(stale.Error.Details["actualSha256"]) == "" {
		t.Fatalf("stale edit details=%+v", stale.Error.Details)
	}
	relative := call("file.write", "edit", map[string]any{"path": "main.txt", "oldText": "new value", "newText": "again", "expectedFileSha256": fileSHA})
	if relative.Error == nil || relative.Error.Code != "ABSOLUTE_PATH_REQUIRED" {
		t.Fatalf("relative edit=%+v", relative)
	}

	run := call("shell.exec", "run", map[string]any{"argv": shellEchoArgv("phase3-ok"), "cwd": root, "idempotencyKey": "idem_phase3_echo_0001", "timeoutSeconds": 10})
	if run.Error != nil {
		t.Fatalf("shell run error=%+v", run.Error)
	}
	jobID, _ := run.Result["jobId"].(string)
	if jobID == "" {
		t.Fatalf("shell run returned no job id: %#v", run.Result)
	}
	duplicate := call("shell.exec", "run", map[string]any{"argv": shellEchoArgv("phase3-ok"), "cwd": root, "idempotencyKey": "idem_phase3_echo_0001", "timeoutSeconds": 10})
	if duplicate.Error != nil || duplicate.Result["jobId"] != jobID {
		t.Fatalf("idempotent retry=%+v", duplicate)
	}
	conflict := call("shell.exec", "run", map[string]any{"argv": shellEchoArgv("different"), "cwd": root, "idempotencyKey": "idem_phase3_echo_0001", "timeoutSeconds": 10})
	if conflict.Error == nil || conflict.Error.Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("idempotency conflict=%+v", conflict)
	}
	final := waitJobTerminal(t, client.jobs, jobID, 10*time.Second)
	if final.State != "completed" || final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("job=%+v", final)
	}

	longRun := call("shell.exec", "run", map[string]any{"argv": shellSleepArgv(), "cwd": root, "idempotencyKey": "idem_phase3_cancel_001", "timeoutSeconds": 30})
	if longRun.Error != nil {
		t.Fatalf("long run error=%+v", longRun.Error)
	}
	longJobID, _ := longRun.Result["jobId"].(string)
	cancel := call("job.control", "cancel", map[string]any{"jobId": longJobID})
	if cancel.Error != nil {
		t.Fatalf("job cancel error=%+v", cancel.Error)
	}
	canceled := waitJobTerminal(t, client.jobs, longJobID, 10*time.Second)
	if canceled.State != "canceled" {
		t.Fatalf("canceled state=%s", canceled.State)
	}
}

func waitJobTerminal(t *testing.T, jobs *JobManager, jobID string, timeout time.Duration) JobSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cursor := int64(0)
	for {
		snapshot, err := jobs.Watch(ctx, jobID, cursor, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		cursor = snapshot.NextCursor
		if isTerminalJobState(snapshot.State) {
			all, err := jobs.Watch(context.Background(), jobID, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			return all
		}
	}
}

func shellEchoArgv(text string) []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd.exe", "/d", "/s", "/c", "echo " + text}
	}
	return []string{"sh", "-c", "printf '%s\\n' " + text}
}

func shellSleepArgv() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd.exe", "/d", "/s", "/c", "ping -n 30 127.0.0.1 >NUL"}
	}
	return []string{"sh", "-c", "sleep 30"}
}
