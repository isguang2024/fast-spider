package node

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestPhase3EditShellJobsAndPermissionRevocation(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	path := filepath.Join(root, "main.txt")
	if err := os.WriteFile(path, []byte("alpha\nold value\nomega\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceStore := NewWorkspaceStore(dataDir)
	workspace, err := workspaceStore.Add(root, "phase3")
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{DataDir: dataDir, Version: "test", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.jobs.CancelAll(context.Background()) }()

	call := func(capability, action, workspaceID string, params map[string]any) protocolv1.CapabilityResponse {
		return client.handleCapabilityRequest(context.Background(), protocolv1.CapabilityRequest{
			MessageType: protocolv1.MessageCapabilityRequest,
			RequestId:   "req_phase3_1234567890",
			Capability:  capability,
			Action:      action,
			WorkspaceId: workspaceID,
			Params:      params,
			Deadline:    protocolv1.Timestamp(time.Now().Add(10 * time.Second)),
			Timestamp:   protocolv1.Timestamp(time.Now()),
		})
	}

	read := call("file.read", "read", workspace.WorkspaceID, map[string]any{"path": "main.txt"})
	if read.Error != nil {
		t.Fatalf("initial read error=%+v", read.Error)
	}
	fileSHA, _ := read.Result["fileSha256"].(string)
	if fileSHA == "" {
		t.Fatalf("file_read did not return full file sha: %#v", read.Result)
	}

	deniedEdit := call("file.write", "edit", workspace.WorkspaceID, map[string]any{
		"path": "main.txt", "oldText": "old value", "newText": "new value", "expectedFileSha256": fileSHA,
	})
	if deniedEdit.Error == nil || deniedEdit.Error.Code != "PERMISSION_DENIED" {
		t.Fatalf("default write permission response=%+v", deniedEdit)
	}
	deniedShell := call("shell.exec", "run", workspace.WorkspaceID, map[string]any{
		"argv": shellEchoArgv("denied"), "idempotencyKey": "idem_phase3_denied_001",
	})
	if deniedShell.Error == nil || deniedShell.Error.Code != "PERMISSION_DENIED" {
		t.Fatalf("default shell permission response=%+v", deniedShell)
	}

	if err := workspaceStore.SetPermissions(workspace.WorkspaceID, []string{"read", "write", "shell"}); err != nil {
		t.Fatal(err)
	}
	edit := call("file.write", "edit", workspace.WorkspaceID, map[string]any{
		"path": "main.txt", "oldText": "old value", "newText": "new value", "expectedFileSha256": fileSHA,
	})
	if edit.Error != nil {
		t.Fatalf("file edit error=%+v", edit.Error)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "new value") || strings.Contains(string(content), "old value") {
		t.Fatalf("unexpected edited content: %q", content)
	}
	stale := call("file.write", "edit", workspace.WorkspaceID, map[string]any{
		"path": "main.txt", "oldText": "new value", "newText": "again", "expectedFileSha256": fileSHA,
	})
	if stale.Error == nil || stale.Error.Code != "REVISION_CONFLICT" {
		t.Fatalf("stale edit response=%+v", stale)
	}

	run := call("shell.exec", "run", workspace.WorkspaceID, map[string]any{
		"argv": shellEchoArgv("phase3-ok"), "idempotencyKey": "idem_phase3_echo_0001", "timeoutSeconds": 10,
	})
	if run.Error != nil {
		t.Fatalf("shell run error=%+v", run.Error)
	}
	jobID, _ := run.Result["jobId"].(string)
	if jobID == "" {
		t.Fatalf("shell run returned no job id: %#v", run.Result)
	}
	duplicate := call("shell.exec", "run", workspace.WorkspaceID, map[string]any{
		"argv": shellEchoArgv("phase3-ok"), "idempotencyKey": "idem_phase3_echo_0001", "timeoutSeconds": 10,
	})
	if duplicate.Error != nil || duplicate.Result["jobId"] != jobID {
		t.Fatalf("idempotent retry returned %+v", duplicate)
	}
	conflict := call("shell.exec", "run", workspace.WorkspaceID, map[string]any{
		"argv": shellEchoArgv("different"), "idempotencyKey": "idem_phase3_echo_0001", "timeoutSeconds": 10,
	})
	if conflict.Error == nil || conflict.Error.Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("idempotency conflict response=%+v", conflict)
	}

	snapshot := waitJobTerminal(t, client.jobs, jobID, 10*time.Second)
	if snapshot.State != "completed" || snapshot.ExitCode == nil || *snapshot.ExitCode != 0 {
		t.Fatalf("completed job snapshot=%+v", snapshot)
	}
	var output strings.Builder
	for _, event := range snapshot.Events {
		if event.Type == "stdout" {
			output.WriteString(event.Text)
		}
	}
	if !strings.Contains(output.String(), "phase3-ok") {
		t.Fatalf("stdout=%q", output.String())
	}

	longRun := call("shell.exec", "run", workspace.WorkspaceID, map[string]any{
		"argv": shellSleepArgv(), "idempotencyKey": "idem_phase3_cancel_001", "timeoutSeconds": 30,
	})
	if longRun.Error != nil {
		t.Fatalf("long shell run error=%+v", longRun.Error)
	}
	longJobID, _ := longRun.Result["jobId"].(string)
	cancel := call("job.control", "cancel", workspace.WorkspaceID, map[string]any{"jobId": longJobID})
	if cancel.Error != nil {
		t.Fatalf("job cancel error=%+v", cancel.Error)
	}
	canceled := waitJobTerminal(t, client.jobs, longJobID, 10*time.Second)
	if canceled.State != "canceled" {
		t.Fatalf("canceled job state=%s", canceled.State)
	}

	revokedRun := call("shell.exec", "run", workspace.WorkspaceID, map[string]any{
		"argv": shellSleepArgv(), "idempotencyKey": "idem_phase3_revoke_001", "timeoutSeconds": 30,
	})
	if revokedRun.Error != nil {
		t.Fatalf("revocation shell run error=%+v", revokedRun.Error)
	}
	revokedJobID, _ := revokedRun.Result["jobId"].(string)
	// Let at least one permission-check tick pass while shell permission is
	// still enabled; revocation must also be observed by later ticks.
	time.Sleep(2500 * time.Millisecond)
	if err := workspaceStore.SetPermissions(workspace.WorkspaceID, []string{"read", "write"}); err != nil {
		t.Fatal(err)
	}
	revoked := waitJobTerminal(t, client.jobs, revokedJobID, 8*time.Second)
	if revoked.State != "canceled" || !strings.Contains(revoked.Error, "permission revoked") {
		t.Fatalf("permission-revoked job=%+v", revoked)
	}
}

func waitJobTerminal(t *testing.T, jobs *JobManager, jobID string, timeout time.Duration) JobSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cursor := int64(0)
	var combined JobSnapshot
	for {
		snapshot, err := jobs.Watch(ctx, jobID, cursor, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		combined = snapshot
		if len(snapshot.Events) > 0 {
			combined.Events = append([]JobEvent(nil), snapshot.Events...)
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
