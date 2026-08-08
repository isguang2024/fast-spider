package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/adminclient"
	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/server"
	"github.com/isguang2024/fast-spider/internal/hub/store"
	"github.com/isguang2024/fast-spider/internal/node"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPhase1EndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := core.New(st, registry.New(), core.Config{DataDir: dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}

	hub := server.New(service, server.Config{})
	httpServer := httptest.NewServer(hub.Handler())
	defer httpServer.Close()

	bootstrapClient, err := adminclient.New(httpServer.URL, "", true)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := bootstrapClient.Bootstrap(ctx, bootstrapToken, "Owner")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := adminclient.New(httpServer.URL, owner.OwnerToken, true)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := admin.CreateEnrollment(ctx, "test-node", runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}

	nodeDataDir := t.TempDir()
	workspaceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceDir, "hello.txt"), []byte("alpha\nneedle value\nomega\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "large.txt"), []byte(strings.Repeat("A", 160<<10)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runE2EGit(t, workspaceDir, "init")
	runE2EGit(t, workspaceDir, "config", "user.name", "Fast Spider E2E")
	runE2EGit(t, workspaceDir, "config", "user.email", "fast-spider-e2e@example.invalid")
	runE2EGit(t, workspaceDir, "add", "hello.txt", "large.txt")
	runE2EGit(t, workspaceDir, "commit", "-m", "initial")
	workspaceStore := node.NewWorkspaceStore(nodeDataDir)
	workspace, err := workspaceStore.Add(workspaceDir, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := workspaceStore.SetBuildProfile(workspace.WorkspaceID, node.BuildProfileRecord{ProfileID: "verify", DisplayName: "Verify", Argv: e2eEchoArgv(), Cwd: ".", TimeoutSeconds: 10}); err != nil {
		t.Fatal(err)
	}
	nodeClient, err := node.New(node.Config{DataDir: nodeDataDir, Version: "test", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	state, err := nodeClient.Enroll(ctx, httpServer.URL, enrollment.EnrollmentToken, "test-node")
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stopNode := context.WithCancel(ctx)
	nodeDone := make(chan error, 1)
	go func() { nodeDone <- nodeClient.Run(runCtx) }()
	defer func() {
		stopNode()
		select {
		case <-nodeDone:
		case <-time.After(3 * time.Second):
			t.Error("node did not stop after context cancellation")
		}
	}()

	waitFor(t, 5*time.Second, func() bool {
		machines, err := admin.ListMachines(ctx)
		return err == nil && len(machines) == 1 && machines[0].MachineID == state.MachineID && machines[0].Online
	})

	mcpHTTP := &http.Client{Transport: bearerTransport{token: owner.OwnerToken, base: http.DefaultTransport}}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "phase1-e2e", Version: "test"}, nil)
	mcpSession, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             httpServer.URL + "/mcp",
		HTTPClient:           mcpHTTP,
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mcpSession.Close()

	tools, err := mcpSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	wantNames := []string{"artifact_get", "browser_control", "build_control", "capability_list", "code_search", "file_edit", "file_read", "git_control", "job_cancel", "job_watch", "machine_get", "machine_list", "screenshot_take", "shell_run", "workspace_list"}
	if stringJSON(names) != stringJSON(wantNames) {
		t.Fatalf("MCP tools=%v want=%v", names, wantNames)
	}

	callResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "machine_list", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(callResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var machineList struct {
		Machines []struct {
			MachineID string `json:"machineId"`
			Online    bool   `json:"online"`
		} `json:"machines"`
	}
	if err := json.Unmarshal(raw, &machineList); err != nil {
		t.Fatalf("decode MCP machine_list result: %v raw=%s", err, raw)
	}
	if len(machineList.Machines) != 1 || machineList.Machines[0].MachineID != state.MachineID || !machineList.Machines[0].Online {
		t.Fatalf("unexpected MCP machine list: %s", raw)
	}

	workspaceResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "workspace_list", Arguments: map[string]any{"machineId": state.MachineID}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(workspaceResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var workspaceList struct {
		Workspaces []struct {
			WorkspaceID string `json:"workspaceId"`
			DisplayName string `json:"displayName"`
			Enabled     bool   `json:"enabled"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(raw, &workspaceList); err != nil {
		t.Fatalf("decode workspace_list: %v raw=%s", err, raw)
	}
	if len(workspaceList.Workspaces) != 1 || workspaceList.Workspaces[0].WorkspaceID != workspace.WorkspaceID || !workspaceList.Workspaces[0].Enabled {
		t.Fatalf("unexpected workspace list: %s", raw)
	}
	if strings.Contains(string(raw), workspaceDir) {
		t.Fatalf("workspace_list leaked local absolute path: %s", raw)
	}

	fileResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "file_read", Arguments: map[string]any{"machineId": state.MachineID, "workspaceId": workspace.WorkspaceID, "path": "hello.txt", "limit": 128}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(fileResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var fileRead struct {
		Path       string `json:"path"`
		Content    string `json:"content"`
		FileSHA256 string `json:"fileSha256"`
	}
	if err := json.Unmarshal(raw, &fileRead); err != nil {
		t.Fatalf("decode file_read: %v raw=%s", err, raw)
	}
	if fileRead.Path != "hello.txt" || !strings.Contains(fileRead.Content, "needle value") || fileRead.FileSHA256 == "" {
		t.Fatalf("unexpected file_read: %s", raw)
	}

	searchResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "code_search", Arguments: map[string]any{"machineId": state.MachineID, "workspaceId": workspace.WorkspaceID, "query": "needle", "limit": 10}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(searchResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var search struct {
		Matches []struct {
			Path string `json:"path"`
			Line int    `json:"line"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(raw, &search); err != nil {
		t.Fatalf("decode code_search: %v raw=%s", err, raw)
	}
	if len(search.Matches) != 1 || search.Matches[0].Path != "hello.txt" || search.Matches[0].Line != 2 {
		t.Fatalf("unexpected code_search: %s", raw)
	}

	if err := workspaceStore.SetPermissions(workspace.WorkspaceID, []string{"read", "write", "shell", "git-write", "build"}); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("FAST_SPIDER_SCREENSHOT_E2E") == "1" {
		runScreenshotTakeE2E(t, ctx, mcpSession, mcpHTTP, httpServer.URL, state.MachineID, workspace.WorkspaceID, nodeDataDir)
	}
	editResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "file_edit", Arguments: map[string]any{
		"machineId": state.MachineID, "workspaceId": workspace.WorkspaceID, "path": "hello.txt",
		"oldText": "needle value", "newText": "needle changed", "expectedFileSha256": fileRead.FileSHA256,
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(editResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var edited struct {
		AfterSHA256 string `json:"afterSha256"`
		Diff        string `json:"diff"`
	}
	if err := json.Unmarshal(raw, &edited); err != nil {
		t.Fatalf("decode file_edit: %v raw=%s", err, raw)
	}
	if edited.AfterSHA256 == "" || !strings.Contains(edited.Diff, "needle changed") {
		t.Fatalf("unexpected file_edit: %s", raw)
	}
	editedContent, err := os.ReadFile(filepath.Join(workspaceDir, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(editedContent), "needle changed") {
		t.Fatalf("file edit did not persist: %q", editedContent)
	}

	gitResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "git_control", Arguments: map[string]any{
		"machineId": state.MachineID, "workspaceId": workspace.WorkspaceID, "action": "status",
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(gitResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "hello.txt") {
		t.Fatalf("git_control status did not report edited file: %s", raw)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "large.txt"), []byte(strings.Repeat("B", 160<<10)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	largeDiffResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "git_control", Arguments: map[string]any{
		"machineId": state.MachineID, "workspaceId": workspace.WorkspaceID, "action": "diff",
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(largeDiffResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var largeDiffEnvelope struct {
		Result struct {
			Truncated  bool   `json:"truncated"`
			ArtifactID string `json:"artifactId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &largeDiffEnvelope); err != nil || !largeDiffEnvelope.Result.Truncated || largeDiffEnvelope.Result.ArtifactID == "" {
		t.Fatalf("large git diff did not produce artifact: %v raw=%s", err, raw)
	}
	largeDiffArtifact, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "artifact_get", Arguments: map[string]any{"action": "get", "artifactId": largeDiffEnvelope.Result.ArtifactID}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(largeDiffArtifact.StructuredContent)
	if err != nil || !strings.Contains(string(raw), `"artifactId":"`+largeDiffEnvelope.Result.ArtifactID+`"`) {
		t.Fatalf("large diff artifact lookup failed: %v raw=%s", err, raw)
	}

	buildListResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "build_control", Arguments: map[string]any{
		"machineId": state.MachineID, "workspaceId": workspace.WorkspaceID, "action": "list",
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(buildListResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"profileId":"verify"`) {
		t.Fatalf("build_control list missing local profile: %s", raw)
	}
	buildRunResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "build_control", Arguments: map[string]any{
		"machineId": state.MachineID, "workspaceId": workspace.WorkspaceID, "action": "run", "profileId": "verify", "idempotencyKey": "idem_e2e_build_0001",
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(buildRunResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var buildEnvelope struct {
		Result struct {
			Job struct {
				JobID string `json:"jobId"`
			} `json:"job"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &buildEnvelope); err != nil || buildEnvelope.Result.Job.JobID == "" {
		t.Fatalf("decode build_control run: %v raw=%s", err, raw)
	}
	waitFor(t, 10*time.Second, func() bool {
		watchResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "job_watch", Arguments: map[string]any{"machineId": state.MachineID, "workspaceId": workspace.WorkspaceID, "jobId": buildEnvelope.Result.Job.JobID}})
		if err != nil {
			return false
		}
		raw, _ := json.Marshal(watchResult.StructuredContent)
		var watch struct {
			State string `json:"state"`
		}
		return json.Unmarshal(raw, &watch) == nil && watch.State == "completed"
	})

	fileArtifactUpload, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "artifact_get", Arguments: map[string]any{
		"action": "uploadFile", "machineId": state.MachineID, "workspaceId": workspace.WorkspaceID,
		"path": "hello.txt", "logicalName": "source.txt", "contentType": "text/plain; charset=utf-8",
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(fileArtifactUpload.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var fileArtifactEnvelope struct {
		Result struct {
			ArtifactID string `json:"artifactId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &fileArtifactEnvelope); err != nil || fileArtifactEnvelope.Result.ArtifactID == "" {
		t.Fatalf("decode file artifact upload: %v raw=%s", err, raw)
	}
	fileArtifactGet, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "artifact_get", Arguments: map[string]any{
		"action": "get", "artifactId": fileArtifactEnvelope.Result.ArtifactID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(fileArtifactGet.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "needle changed") || !strings.Contains(string(raw), `"logicalName":"source.txt"`) {
		t.Fatalf("artifact_get did not return uploaded workspace file: %s", raw)
	}

	artifactUploadResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "artifact_get", Arguments: map[string]any{
		"action": "uploadJobLog", "machineId": state.MachineID, "workspaceId": workspace.WorkspaceID,
		"jobId": buildEnvelope.Result.Job.JobID, "logicalName": "build.log",
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(artifactUploadResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var artifactEnvelope struct {
		Result struct {
			ArtifactID string `json:"artifactId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &artifactEnvelope); err != nil || artifactEnvelope.Result.ArtifactID == "" {
		t.Fatalf("decode artifact upload: %v raw=%s", err, raw)
	}
	artifactGetResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "artifact_get", Arguments: map[string]any{
		"action": "get", "artifactId": artifactEnvelope.Result.ArtifactID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(artifactGetResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "phase3-e2e") || !strings.Contains(string(raw), `"logicalName":"build.log"`) {
		t.Fatalf("artifact_get did not return uploaded job log: %s", raw)
	}

	shellResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "shell_run", Arguments: map[string]any{
		"machineId": state.MachineID, "workspaceId": workspace.WorkspaceID, "argv": e2eEchoArgv(),
		"timeoutSeconds": 10, "idempotencyKey": "idem_e2e_shell_0001",
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(shellResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var shellJob struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(raw, &shellJob); err != nil || shellJob.JobID == "" {
		t.Fatalf("decode shell_run: %v raw=%s", err, raw)
	}
	cursor := int64(0)
	var shellOutput strings.Builder
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		watchResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "job_watch", Arguments: map[string]any{
			"machineId": state.MachineID, "workspaceId": workspace.WorkspaceID, "jobId": shellJob.JobID, "cursor": cursor, "waitSeconds": 1,
		}})
		if err != nil {
			t.Fatal(err)
		}
		raw, err = json.Marshal(watchResult.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var watch struct {
			State      string `json:"state"`
			NextCursor int64  `json:"nextCursor"`
			Events     []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"events"`
		}
		if err := json.Unmarshal(raw, &watch); err != nil {
			t.Fatalf("decode job_watch: %v raw=%s", err, raw)
		}
		for _, event := range watch.Events {
			if event.Type == "stdout" {
				shellOutput.WriteString(event.Text)
			}
		}
		cursor = watch.NextCursor
		if watch.State == "completed" {
			break
		}
		if watch.State == "failed" || watch.State == "canceled" || watch.State == "expired" {
			t.Fatalf("echo job ended unexpectedly: %s raw=%s", watch.State, raw)
		}
	}
	if !strings.Contains(shellOutput.String(), "phase3-e2e") {
		t.Fatalf("shell output=%q", shellOutput.String())
	}

	longResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "shell_run", Arguments: map[string]any{
		"machineId": state.MachineID, "workspaceId": workspace.WorkspaceID, "argv": e2eSleepArgv(),
		"timeoutSeconds": 30, "idempotencyKey": "idem_e2e_cancel_0001",
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(longResult.StructuredContent)
	var longJob struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(raw, &longJob); err != nil || longJob.JobID == "" {
		t.Fatalf("decode long shell_run: %v raw=%s", err, raw)
	}
	crossRoot := t.TempDir()
	crossWorkspace, err := workspaceStore.Add(crossRoot, "cross-workspace")
	if err != nil {
		t.Fatal(err)
	}
	wrongWatch, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "job_watch", Arguments: map[string]any{"machineId": state.MachineID, "workspaceId": crossWorkspace.WorkspaceID, "jobId": longJob.JobID}})
	if err == nil && !wrongWatch.IsError {
		t.Fatalf("cross-workspace job_watch was accepted: %+v", wrongWatch)
	}
	wrongCancel, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "job_cancel", Arguments: map[string]any{"machineId": state.MachineID, "workspaceId": crossWorkspace.WorkspaceID, "jobId": longJob.JobID}})
	if err == nil && !wrongCancel.IsError {
		t.Fatalf("cross-workspace job_cancel was accepted: %+v", wrongCancel)
	}
	if _, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "job_cancel", Arguments: map[string]any{"machineId": state.MachineID, "workspaceId": workspace.WorkspaceID, "jobId": longJob.JobID}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		watchResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "job_watch", Arguments: map[string]any{"machineId": state.MachineID, "workspaceId": workspace.WorkspaceID, "jobId": longJob.JobID}})
		if err != nil {
			return false
		}
		raw, _ := json.Marshal(watchResult.StructuredContent)
		var watch struct {
			State string `json:"state"`
		}
		return json.Unmarshal(raw, &watch) == nil && watch.State == "canceled"
	})

	if err := admin.RevokeMachine(ctx, state.MachineID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		machines, err := admin.ListMachines(ctx)
		return err == nil && len(machines) == 1 && machines[0].Status == "revoked" && !machines[0].Online
	})
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("condition did not become true before timeout")
}

type mcpToolCaller interface {
	CallTool(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error)
}

func runScreenshotTakeE2E(t *testing.T, ctx context.Context, session mcpToolCaller, httpClient *http.Client, hubURL, machineID, workspaceID, nodeDataDir string) {
	t.Helper()
	call := func(name string, arguments map[string]any) (map[string]any, error) {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
		if err != nil {
			return nil, err
		}
		if result.IsError {
			content, _ := json.Marshal(result.Content)
			structured, _ := json.Marshal(result.StructuredContent)
			return nil, fmt.Errorf("MCP tool %s returned an error: content=%s structured=%s", name, content, structured)
		}
		raw, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return nil, err
		}
		var output map[string]any
		if err := json.Unmarshal(raw, &output); err != nil {
			return nil, err
		}
		return output, nil
	}

	list, err := call("screenshot_take", map[string]any{"machineId": machineID, "workspaceId": workspaceID, "action": "listDisplays"})
	if err != nil {
		t.Skipf("desktop screenshot session unavailable: %v", err)
	}
	result, ok := list["result"].(map[string]any)
	if !ok {
		t.Fatalf("listDisplays missing result: %+v", list)
	}
	displays, ok := result["displays"].([]any)
	if !ok || len(displays) == 0 {
		t.Skipf("desktop screenshot session returned no displays: %+v", list)
	}
	firstDisplay, ok := displays[0].(map[string]any)
	if !ok {
		t.Fatalf("invalid display summary: %+v", displays[0])
	}
	displayIndex, ok := firstDisplay["index"].(float64)
	if !ok {
		t.Fatalf("display summary has no numeric index: %+v", firstDisplay)
	}

	screenshot, err := call("screenshot_take", map[string]any{
		"machineId": machineID, "workspaceId": workspaceID, "action": "display", "displayIndex": int(displayIndex), "format": "png",
	})
	if err != nil {
		t.Fatal(err)
	}
	shotResult, ok := screenshot["result"].(map[string]any)
	if !ok {
		t.Fatalf("display screenshot missing result: %+v", screenshot)
	}
	artifactID, _ := shotResult["artifactId"].(string)
	if artifactID == "" || shotResult["contentType"] != "image/png" {
		t.Fatalf("display screenshot artifact metadata=%+v", shotResult)
	}
	if size, ok := shotResult["sizeBytes"].(float64); !ok || size <= 0 || size > float64(32<<20) {
		t.Fatalf("display screenshot size metadata=%+v", shotResult)
	}

	metadata, err := call("artifact_get", map[string]any{"action": "get", "artifactId": artifactID})
	if err != nil {
		t.Fatal(err)
	}
	metadataResult, ok := metadata["result"].(map[string]any)
	if !ok || metadataResult["artifactId"] != artifactID || metadataResult["contentType"] != "image/png" {
		t.Fatalf("artifact_get metadata=%+v", metadata)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, hubURL+"/api/v1/artifacts/"+artifactID+"/content", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("artifact content status=%s", response.Status)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, 32<<20+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(content) == 0 || len(content) > 32<<20 {
		t.Fatalf("artifact content size=%d", len(content))
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil || format != "png" || config.Width <= 0 || config.Height <= 0 {
		t.Fatalf("artifact content is not a valid PNG: format=%q config=%+v err=%v", format, config, err)
	}
	matches, err := filepath.Glob(filepath.Join(nodeDataDir, "screenshots", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("Node screenshot temporary files remain: %v", matches)
	}

	if runtime.GOOS != "windows" {
		return
	}
	windowPath := filepath.Join(t.TempDir(), "fast-spider-window-e2e.txt")
	if err := os.WriteFile(windowPath, []byte("Fast Spider controlled window\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	windowProcess := exec.Command("notepad.exe", windowPath)
	if err := windowProcess.Start(); err != nil {
		t.Skipf("cannot start controlled Notepad window: %v", err)
	}
	defer func() {
		if windowProcess.Process != nil {
			_ = windowProcess.Process.Kill()
		}
		_ = windowProcess.Wait()
	}()

	windowTitlePart := filepath.Base(windowPath)
	var windowSummary map[string]any
	windowListDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(windowListDeadline) && windowSummary == nil {
		windowsResult, listErr := call("screenshot_take", map[string]any{"machineId": machineID, "workspaceId": workspaceID, "action": "listWindows"})
		if listErr != nil {
			t.Skipf("window screenshot listing unavailable: %v", listErr)
		}
		windowsEnvelope, ok := windowsResult["result"].(map[string]any)
		if !ok {
			t.Fatalf("listWindows missing result: %+v", windowsResult)
		}
		windows, ok := windowsEnvelope["windows"].([]any)
		if !ok {
			t.Fatalf("listWindows missing windows: %+v", windowsResult)
		}
		for _, rawWindow := range windows {
			candidate, ok := rawWindow.(map[string]any)
			if !ok {
				continue
			}
			title, _ := candidate["title"].(string)
			if strings.Contains(title, windowTitlePart) {
				windowSummary = candidate
				break
			}
		}
		if windowSummary == nil {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if windowSummary == nil {
		t.Skipf("controlled Notepad window did not appear in listWindows")
	}
	windowID, _ := windowSummary["windowId"].(string)
	if windowID == "" || filepath.IsAbs(windowID) || strings.Contains(windowID, windowTitlePart) || strings.Contains(windowID, nodeDataDir) {
		t.Fatalf("windowId is not an opaque token: %+v", windowSummary)
	}

	windowScreenshot, err := call("screenshot_take", map[string]any{
		"machineId": machineID, "workspaceId": workspaceID, "action": "window", "windowId": windowID, "format": "png",
	})
	if err != nil {
		t.Fatalf("controlled window screenshot failed for title=%q: %v", windowSummary["title"], err)
	}
	windowJSON := stringJSON(windowScreenshot)
	if strings.Contains(windowJSON, windowPath) || strings.Contains(windowJSON, nodeDataDir) {
		t.Fatalf("window screenshot leaked a local path: %s", windowJSON)
	}
	windowResult, ok := windowScreenshot["result"].(map[string]any)
	if !ok {
		t.Fatalf("window screenshot missing result: %+v", windowScreenshot)
	}
	windowArtifactID, _ := windowResult["artifactId"].(string)
	if windowArtifactID == "" || windowResult["contentType"] != "image/png" {
		t.Fatalf("window screenshot artifact metadata=%+v", windowResult)
	}
	if size, ok := windowResult["sizeBytes"].(float64); !ok || size <= 0 || size > float64(32<<20) {
		t.Fatalf("window screenshot size metadata=%+v", windowResult)
	}
	windowMetadata, err := call("artifact_get", map[string]any{"action": "get", "artifactId": windowArtifactID})
	if err != nil {
		t.Fatal(err)
	}
	windowMetadataResult, ok := windowMetadata["result"].(map[string]any)
	if !ok || windowMetadataResult["artifactId"] != windowArtifactID || windowMetadataResult["contentType"] != "image/png" {
		t.Fatalf("window artifact_get metadata=%+v", windowMetadata)
	}
	windowRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, hubURL+"/api/v1/artifacts/"+windowArtifactID+"/content", nil)
	if err != nil {
		t.Fatal(err)
	}
	windowResponse, err := httpClient.Do(windowRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer windowResponse.Body.Close()
	if windowResponse.StatusCode != http.StatusOK {
		t.Fatalf("window artifact content status=%s", windowResponse.Status)
	}
	windowContent, err := io.ReadAll(io.LimitReader(windowResponse.Body, 32<<20+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(windowContent) == 0 || len(windowContent) > 32<<20 {
		t.Fatalf("window artifact content size=%d", len(windowContent))
	}
	windowConfig, windowFormat, err := image.DecodeConfig(bytes.NewReader(windowContent))
	if err != nil || windowFormat != "png" || windowConfig.Width <= 0 || windowConfig.Height <= 0 {
		t.Fatalf("window artifact is not a valid PNG: format=%q config=%+v err=%v", windowFormat, windowConfig, err)
	}
	matches, err = filepath.Glob(filepath.Join(nodeDataDir, "screenshots", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("Node window screenshot temporary files remain: %v", matches)
	}

	if _, err := call("screenshot_take", map[string]any{
		"machineId": machineID, "workspaceId": workspaceID, "action": "window", "windowId": windowID + "tampered", "format": "png",
	}); err == nil {
		t.Fatal("tampered windowId was accepted")
	}
}

func runE2EGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func e2eEchoArgv() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd.exe", "/d", "/s", "/c", "echo phase3-e2e"}
	}
	return []string{"sh", "-c", "printf 'phase3-e2e\\n'"}
}

func e2eSleepArgv() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd.exe", "/d", "/s", "/c", "ping -n 30 127.0.0.1 >NUL"}
	}
	return []string{"sh", "-c", "sleep 30"}
}

func stringJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
