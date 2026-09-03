package server_test

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/server"
	"github.com/isguang2024/fast-spider/internal/hub/store"
	"github.com/isguang2024/fast-spider/internal/node"
	"github.com/isguang2024/fast-spider/internal/operationlog"
	"github.com/isguang2024/fast-spider/internal/security"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMachineBoundaryEndToEnd(t *testing.T) {
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
	account, err := service.BootstrapAccount(ctx, bootstrapToken, "e2e-owner", "Owner", "correct horse battery staple", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	connectionToken, err := service.CreateConnectionToken(ctx, account.OwnerID, "E2E Node", time.Hour, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	nodeDataDir := t.TempDir()
	operationLog, err := operationlog.NewStore(nodeDataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	filePath := filepath.Join(root, "hello.txt")
	if err := os.WriteFile(filePath, []byte("alpha\nneedle value\nomega\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyPath := filepath.Join(root, "empty.txt")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runE2EGit(t, root, "init")
	runE2EGit(t, root, "config", "user.name", "Fast Spider E2E")
	runE2EGit(t, root, "config", "user.email", "fast-spider-e2e@example.invalid")
	runE2EGit(t, root, "add", "hello.txt")
	runE2EGit(t, root, "commit", "-m", "initial")

	nodeClient, err := node.New(node.Config{DataDir: nodeDataDir, Version: "test", AllowInsecure: true, OperationLog: operationLog})
	if err != nil {
		t.Fatal(err)
	}
	state, err := nodeClient.Connect(ctx, httpServer.URL, connectionToken.Token, "test-node")
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
			t.Error("node did not stop")
		}
	}()
	waitFor(t, 5*time.Second, func() bool {
		machines, err := service.ListMachines(ctx, account.OwnerID)
		return err == nil && len(machines) == 1 && machines[0].MachineID == state.MachineID && machines[0].Online
	})

	mcpAccessToken := "oauth_e2e_access_token"
	now := time.Now().UTC()
	if err := st.RegisterOAuthClient(ctx, store.OAuthClientRecord{ClientID: "mcpcli_e2e", ClientName: "E2E MCP", RedirectURIs: []string{"http://127.0.0.1/callback"}, GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}, Scope: "fast-spider", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateOAuthAuthorization(ctx, store.OAuthAuthorizationRecord{AuthorizationID: "authz_e2e", OwnerID: account.OwnerID, ClientID: "mcpcli_e2e", ClientName: "E2E MCP", Scopes: []string{"fast-spider"}, Resource: httpServer.URL + "/mcp", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveOAuthTokenPair(ctx, security.HashToken(mcpAccessToken), "", store.OAuthTokenRecord{AuthorizationID: "authz_e2e", OwnerID: account.OwnerID, ClientID: "mcpcli_e2e", Scopes: []string{"fast-spider"}, Resource: httpServer.URL + "/mcp"}, now.Add(time.Hour), now.Add(time.Hour), "", now); err != nil {
		t.Fatal(err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "machine-e2e", Version: "test"}, nil)
	mcpSession, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token: mcpAccessToken, base: http.DefaultTransport}}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mcpSession.Close()
	initResult := mcpSession.InitializeResult()
	if initResult == nil || initResult.ServerInfo == nil {
		t.Fatal("missing MCP initialize result/server info")
	}
	if initResult.ServerInfo.Title != "FastSpider_FS" {
		t.Fatalf("server title=%q", initResult.ServerInfo.Title)
	}
	for _, needle := range []string{"@FastSpider_FS", "capability_list", "machine_list", "session.list"} {
		if !strings.Contains(initResult.Instructions, needle) {
			t.Fatalf("MCP instructions missing %q: %q", needle, initResult.Instructions)
		}
	}

	tools, err := mcpSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	var machineListSchema []byte
	var codeSearchSchema []byte
	var fileReadSchema []byte
	var fileEditSchema []byte
	var workingContextSchema []byte
	var capabilityListSchema []byte
	var aiControlSchema []byte
	aiControlDescription := ""
	shellToolDescription := ""
	toolCatalogBytes := 0
	connectionToolBytes := 0
	largestToolBytes := 0
	largestToolName := ""
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		encodedTool, err := json.Marshal(tool)
		if err != nil {
			t.Fatal(err)
		}
		toolCatalogBytes += len(encodedTool)
		if tool.Name == "capability_list" || tool.Name == "machine_list" || tool.Name == "machine_get" {
			connectionToolBytes += len(encodedTool)
		}
		if len(encodedTool) > largestToolBytes {
			largestToolBytes = len(encodedTool)
			largestToolName = tool.Name
		}
		if tool.Name == "code_search" {
			codeSearchSchema, _ = json.Marshal(tool.InputSchema)
		}
		if tool.Name == "machine_list" {
			machineListSchema, _ = json.Marshal(tool.InputSchema)
		}
		if tool.Name == "file_read" {
			fileReadSchema, _ = json.Marshal(tool.InputSchema)
		}
		if tool.Name == "file_edit" {
			fileEditSchema, _ = json.Marshal(tool.InputSchema)
		}
		if tool.Name == "working_context" {
			workingContextSchema, _ = json.Marshal(tool.InputSchema)
		}
		if tool.Name == "capability_list" {
			capabilityListSchema, _ = json.Marshal(tool.InputSchema)
		}
		if tool.Name == "ai_control" {
			aiControlSchema, _ = json.Marshal(tool.InputSchema)
			aiControlDescription = tool.Description
		}
		if tool.Name == "shell_run" {
			shellToolDescription = tool.Description
		}
	}
	t.Logf("MCP tool catalog bytes=%d connection=%d largest=%s/%d", toolCatalogBytes, connectionToolBytes, largestToolName, largestToolBytes)
	if toolCatalogBytes > 50<<10 {
		t.Fatalf("MCP tool catalog grew beyond 50 KiB context budget: %d", toolCatalogBytes)
	}
	if connectionToolBytes > 8<<10 {
		t.Fatalf("MCP connection discovery tools grew beyond 8 KiB budget: %d", connectionToolBytes)
	}
	if largestToolBytes > 8<<10 {
		t.Fatalf("MCP tool %s grew beyond 8 KiB individual budget: %d", largestToolName, largestToolBytes)
	}
	sort.Strings(names)
	want := []string{"ai_control", "artifact_get", "audit_log", "browser_control", "build_control", "capability_list", "code_search", "codex_cloud_collaboration", "file_edit", "file_read", "git_control", "job_cancel", "job_watch", "machine_get", "machine_list", "operation_log", "result_get", "screenshot_take", "shell_run", "thinking_team", "working_context"}
	if stringJSON(names) != stringJSON(want) {
		t.Fatalf("tools=%v want=%v", names, want)
	}
	for _, field := range []string{"mode", "include", "exclude", "context", "beforeContext", "afterContext"} {
		if !bytes.Contains(codeSearchSchema, []byte(`"`+field+`"`)) {
			t.Fatalf("code_search schema missing %q: %s", field, codeSearchSchema)
		}
	}
	for _, field := range []string{"lineStart", "lineCount", "headLines", "tailLines", "aroundLine", "contextLines", "statOnly", "includeLineNumbers"} {
		if !bytes.Contains(fileReadSchema, []byte(`"`+field+`"`)) {
			t.Fatalf("file_read schema missing %q: %s", field, fileReadSchema)
		}
	}
	for name, schema := range map[string][]byte{"file_read": fileReadSchema, "ai_control": aiControlSchema} {
		if !bytes.Contains(schema, []byte(`"diagnostics"`)) {
			t.Fatalf("%s schema missing diagnostics opt-in: %s", name, schema)
		}
	}
	for _, field := range []string{"limit", "cursor", "includeCapabilities"} {
		if !bytes.Contains(machineListSchema, []byte(`"`+field+`"`)) {
			t.Fatalf("machine_list schema missing %s: %s", field, machineListSchema)
		}
	}
	for _, field := range []string{"action", "previewOf", "content", "oldText", "newText", "edits", "expectedFileSha256", "expectedAbsent"} {
		if !bytes.Contains(fileEditSchema, []byte(`"`+field+`"`)) {
			t.Fatalf("file_edit schema missing %q: %s", field, fileEditSchema)
		}
	}
	if !bytes.Contains(workingContextSchema, []byte("required for set and plan.init")) {
		t.Fatalf("working_context goal schema does not describe plan.init requirement: %s", workingContextSchema)
	}
	for _, field := range []string{"view", "name"} {
		if !bytes.Contains(capabilityListSchema, []byte(`"`+field+`"`)) {
			t.Fatalf("capability_list schema missing %q: %s", field, capabilityListSchema)
		}
	}
	if !strings.Contains(strings.ToLower(shellToolDescription), "powershell.exe") || !strings.Contains(shellToolDescription, "not a separate FS tool") {
		t.Fatalf("shell_run description does not explain Windows PowerShell discovery: %q", shellToolDescription)
	}
	for _, needle := range []string{"chatgpt_cloud", "providerId=codex", "visibility=visible", "session.create", "quick_chat"} {
		if !bytes.Contains(aiControlSchema, []byte(needle)) && !strings.Contains(aiControlDescription, needle) {
			t.Fatalf("ai_control tools/list metadata missing %q: schema=%s description=%q", needle, aiControlSchema, aiControlDescription)
		}
	}
	if !strings.Contains(aiControlDescription, "ChatGPT cloud CHAT") {
		t.Fatalf("ai_control description does not advertise ChatGPT CHAT creation: %q", aiControlDescription)
	}
	for _, needle := range []string{"desktopBridge", "Desktop owner/control bridge", "nativeConversationStreaming=unsupported"} {
		if !strings.Contains(aiControlDescription, needle) {
			t.Fatalf("ai_control description missing %q: %q", needle, aiControlDescription)
		}
	}

	defaultGuide := coldMCPCall(t, ctx, httpServer.URL+"/mcp", mcpAccessToken, "capability_list", map[string]any{})
	defaultGuideRaw, _ := json.Marshal(defaultGuide.StructuredContent)
	var defaultGuidePayload struct {
		CapabilitySummaries []json.RawMessage `json:"capabilitySummaries"`
		Guide               struct {
			ToolSummaries []json.RawMessage `json:"toolSummaries"`
		} `json:"guide"`
	}
	if err := json.Unmarshal(defaultGuideRaw, &defaultGuidePayload); err != nil {
		t.Fatal(err)
	}
	if defaultGuide.IsError || !strings.Contains(string(defaultGuideRaw), `"capabilities"`) || !strings.Contains(string(defaultGuideRaw), `"view":"overview"`) || len(defaultGuidePayload.Guide.ToolSummaries) != 21 || len(defaultGuidePayload.CapabilitySummaries) == 0 {
		t.Fatalf("default capability_list=%s", defaultGuideRaw)
	}
	if len(defaultGuide.Content) != 0 {
		t.Fatalf("capability_list duplicated structured output into content: %#v", defaultGuide.Content)
	}
	for _, guideCall := range []struct {
		arguments map[string]any
		want      string
	}{
		{map[string]any{"view": "overview"}, `"view":"overview"`},
		{map[string]any{"view": "capability", "name": "shell.exec"}, `"capabilityId":"shell.exec"`},
		{map[string]any{"view": "tool", "name": "browser_control"}, `"name":"browser_control"`},
		{map[string]any{"view": "workflow", "name": "codex-session"}, `"name":"codex-session"`},
		{map[string]any{"view": "error", "name": "INVALID_REQUEST"}, `"name":"INVALID_REQUEST"`},
	} {
		result := coldMCPCall(t, ctx, httpServer.URL+"/mcp", mcpAccessToken, "capability_list", guideCall.arguments)
		raw, _ := json.Marshal(result.StructuredContent)
		if result.IsError || !strings.Contains(string(raw), guideCall.want) || len(raw) > 12<<10 {
			t.Fatalf("guide args=%v result=%+v raw=%s", guideCall.arguments, result, raw)
		}
	}
	explicitOverview := coldMCPCall(t, ctx, httpServer.URL+"/mcp", mcpAccessToken, "capability_list", map[string]any{"view": "overview"})
	explicitOverviewRaw, _ := json.Marshal(explicitOverview.StructuredContent)
	if explicitOverview.IsError || !strings.Contains(string(explicitOverviewRaw), `"capabilitySummaries"`) {
		t.Fatalf("explicit overview missing low-level capability summaries=%s", explicitOverviewRaw)
	}
	for _, arguments := range []map[string]any{
		{"view": "unknown"}, {"view": "capability"}, {"view": "capability", "name": "unknown"}, {"view": "tool"}, {"view": "workflow"}, {"view": "error"}, {"view": "tool", "name": "unknown"},
	} {
		if result := coldMCPCall(t, ctx, httpServer.URL+"/mcp", mcpAccessToken, "capability_list", arguments); !result.IsError {
			t.Fatalf("invalid capability_list args=%v result=%+v", arguments, result)
		}
	}

	webSession, err := service.CreateWebSession(ctx, account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	unauthorizedDiagnostics, err := http.Get(httpServer.URL + "/app/api/mcp-diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	_ = unauthorizedDiagnostics.Body.Close()
	if unauthorizedDiagnostics.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized MCP diagnostics status=%d", unauthorizedDiagnostics.StatusCode)
	}
	diagnosticsReq, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/app/api/mcp-diagnostics", nil)
	diagnosticsReq.AddCookie(&http.Cookie{Name: "fast_spider_session", Value: webSession.Token})
	diagnosticsResponse, err := http.DefaultClient.Do(diagnosticsReq)
	if err != nil {
		t.Fatal(err)
	}
	diagnosticsRaw, _ := io.ReadAll(diagnosticsResponse.Body)
	_ = diagnosticsResponse.Body.Close()
	if diagnosticsResponse.StatusCode != http.StatusOK || !bytes.Contains(diagnosticsRaw, []byte(`"lastMcpRequestAt"`)) || !bytes.Contains(diagnosticsRaw, []byte(`"lastInitializeAt"`)) || !bytes.Contains(diagnosticsRaw, []byte(`"lastToolsListAt"`)) || !bytes.Contains(diagnosticsRaw, []byte(`"lastToolCallAt"`)) || !bytes.Contains(diagnosticsRaw, []byte(`"clientType":"mcpcli"`)) || !bytes.Contains(diagnosticsRaw, []byte(`"result":"failure"`)) || !bytes.Contains(diagnosticsRaw, []byte(`"errorCode":"NOT_FOUND"`)) {
		t.Fatalf("authorized MCP diagnostics status=%d body=%s", diagnosticsResponse.StatusCode, diagnosticsRaw)
	}
	for _, forbidden := range []string{mcpAccessToken, "arguments", "prompt", "authorization", filePath, "User-Agent"} {
		if strings.Contains(strings.ToLower(string(diagnosticsRaw)), strings.ToLower(forbidden)) {
			t.Fatalf("MCP diagnostics leaked %q: %s", forbidden, diagnosticsRaw)
		}
	}

	machineResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "machine_list", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(machineResult.StructuredContent)
	if !strings.Contains(string(raw), state.MachineID) || !strings.Contains(string(raw), `"hasMore":false`) || strings.Contains(string(raw), `"capabilities"`) || len(machineResult.Content) != 0 {
		t.Fatalf("machine_list=%s", raw)
	}
	machineResultFull, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "machine_list", Arguments: map[string]any{"includeCapabilities": true}})
	if err != nil {
		t.Fatal(err)
	}
	machineResultFullRaw, _ := json.Marshal(machineResultFull.StructuredContent)
	if !strings.Contains(string(machineResultFullRaw), `"capabilities"`) {
		t.Fatalf("machine_list includeCapabilities=%s", machineResultFullRaw)
	}
	machineCatalog := coldMCPCall(t, ctx, httpServer.URL+"/mcp", mcpAccessToken, "capability_list", map[string]any{"machineId": state.MachineID})
	machineCatalogRaw, _ := json.Marshal(machineCatalog.StructuredContent)
	if machineCatalog.IsError || !strings.Contains(string(machineCatalogRaw), `"capabilities"`) || strings.Contains(string(machineCatalogRaw), `"guide"`) {
		t.Fatalf("legacy machine capability_list=%s", machineCatalogRaw)
	}
	machineCapabilityGuide := coldMCPCall(t, ctx, httpServer.URL+"/mcp", mcpAccessToken, "capability_list", map[string]any{"machineId": state.MachineID, "view": "capability", "name": "shell.exec"})
	machineCapabilityGuideRaw, _ := json.Marshal(machineCapabilityGuide.StructuredContent)
	if machineCapabilityGuide.IsError || !strings.Contains(string(machineCapabilityGuideRaw), `"capabilityId":"shell.exec"`) || !strings.Contains(string(machineCapabilityGuideRaw), `"shell_run"`) {
		t.Fatalf("machine capability guide=%s", machineCapabilityGuideRaw)
	}

	fileResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "file_read", Arguments: map[string]any{"machineId": state.MachineID, "path": filePath, "limit": 128}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(fileResult.StructuredContent)
	var read struct {
		Content    string `json:"content"`
		FileSHA256 string `json:"fileSha256"`
	}
	if err := json.Unmarshal(raw, &read); err != nil || read.FileSHA256 == "" || !strings.Contains(read.Content, "needle value") {
		t.Fatalf("file_read err=%v raw=%s", err, raw)
	}
	if bytes.Contains(raw, []byte(`"requestId"`)) || bytes.Contains(raw, []byte(`"traceId"`)) || bytes.Contains(raw, []byte(`"timing"`)) || len(fileResult.Content) != 0 || len(fileResult.Meta) == 0 {
		t.Fatalf("file_read default response is not compact: result=%+v raw=%s", fileResult, raw)
	}
	operationLogResult := coldMCPCall(t, ctx, httpServer.URL+"/mcp", mcpAccessToken, "operation_log", map[string]any{"machineId": state.MachineID, "limit": 10})
	operationLogRaw, _ := json.Marshal(operationLogResult.StructuredContent)
	if operationLogResult.IsError || !strings.Contains(string(operationLogRaw), `"entries"`) || !strings.Contains(string(operationLogRaw), `"action":"file.read.read"`) || strings.Contains(string(operationLogRaw), filePath) {
		t.Fatalf("operation_log=%s", operationLogRaw)
	}
	lineResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "file_read", Arguments: map[string]any{
		"machineId": state.MachineID, "path": filePath, "headLines": 1, "includeLineNumbers": true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(lineResult.StructuredContent)
	if !strings.Contains(string(raw), `"lineStart":1`) || !strings.Contains(string(raw), "1: alpha") {
		t.Fatalf("file_read line selector=%s", raw)
	}

	searchResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "code_search", Arguments: map[string]any{"machineId": state.MachineID, "path": root, "query": "needle", "limit": 10}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(searchResult.StructuredContent)
	if !strings.Contains(string(raw), "hello.txt") {
		t.Fatalf("code_search=%s", raw)
	}

	previewResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "file_edit", Arguments: map[string]any{
		"machineId": state.MachineID, "action": "preview", "previewOf": "replace", "path": filePath,
		"oldText": "needle value", "newText": "needle preview", "expectedFileSha256": read.FileSHA256,
	}})
	if err != nil || previewResult.IsError {
		t.Fatalf("file_edit preview=%+v err=%v", previewResult, err)
	}
	previewRaw, _ := json.Marshal(previewResult.StructuredContent)
	if !strings.Contains(string(previewRaw), `"preview":true`) || !strings.Contains(string(previewRaw), `"operation":"replace"`) || !strings.Contains(string(previewRaw), `"changed":true`) {
		t.Fatalf("file_edit preview output=%s", previewRaw)
	}
	unchanged, err := os.ReadFile(filePath)
	if err != nil || !strings.Contains(string(unchanged), "needle value") || strings.Contains(string(unchanged), "needle preview") {
		t.Fatalf("preview changed file err=%v content=%q", err, unchanged)
	}

	editResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "file_edit", Arguments: map[string]any{"machineId": state.MachineID, "path": filePath, "oldText": "needle value", "newText": "needle changed", "expectedFileSha256": read.FileSHA256}})
	if err != nil {
		t.Fatal(err)
	}
	if editResult.IsError {
		t.Fatalf("file_edit=%+v", editResult)
	}

	createdPath := filepath.Join(root, "created.txt")
	createResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "file_edit", Arguments: map[string]any{
		"machineId": state.MachineID, "action": "create", "path": createdPath, "content": "one two three\n", "expectedAbsent": true,
	}})
	if err != nil || createResult.IsError {
		t.Fatalf("file_edit create=%+v err=%v", createResult, err)
	}
	createRaw, _ := json.Marshal(createResult.StructuredContent)
	var created struct {
		NewSHA256 string `json:"newSha256"`
	}
	if err := json.Unmarshal(createRaw, &created); err != nil || created.NewSHA256 == "" || strings.Contains(string(createRaw), `"diff"`) {
		t.Fatalf("file_edit create output=%s err=%v", createRaw, err)
	}
	manyResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "file_edit", Arguments: map[string]any{
		"machineId": state.MachineID, "action": "editMany", "path": createdPath, "expectedFileSha256": created.NewSHA256,
		"edits": []map[string]any{{"oldText": "one", "newText": "1"}, {"oldText": "three", "newText": "3"}},
	}})
	if err != nil || manyResult.IsError {
		t.Fatalf("file_edit editMany=%+v err=%v", manyResult, err)
	}
	if content, err := os.ReadFile(createdPath); err != nil || string(content) != "1 two 3\n" {
		t.Fatalf("file_edit editMany content=%q err=%v", content, err)
	}

	gitResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "git_control", Arguments: map[string]any{"machineId": state.MachineID, "repositoryPath": root, "action": "status"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(gitResult.StructuredContent)
	if !strings.Contains(string(raw), "hello.txt") {
		t.Fatalf("git status=%s", raw)
	}

	contextSet, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "working_context", Arguments: map[string]any{
		"machineId": state.MachineID, "action": "set", "projectPath": root,
		"goal": "keep a compact task snapshot", "completed": []string{"file read and edit verified"},
		"constraints": []string{"do not store chat transcripts"}, "pending": []string{"finish e2e"}, "keyFiles": []string{"hello.txt"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(contextSet.StructuredContent)
	if !strings.Contains(string(raw), "keep a compact task snapshot") || !strings.Contains(string(raw), `"exists":true`) {
		t.Fatalf("working_context set=%s", raw)
	}
	contextGet, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "working_context", Arguments: map[string]any{
		"machineId": state.MachineID, "action": "get", "projectPath": root,
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(contextGet.StructuredContent)
	if !strings.Contains(string(raw), "keep a compact task snapshot") || !strings.Contains(string(raw), `"currentGit"`) {
		t.Fatalf("working_context get=%s", raw)
	}
	planInit, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "working_context", Arguments: map[string]any{
		"machineId": state.MachineID, "action": "plan.init", "projectPath": root, "planId": "e2e-plan", "goal": "verify plan actions", "targetVersion": "0.4.1",
		"tasks": []map[string]any{{"id": "FS-041-E2E", "title": "MCP plan flow", "status": "in_progress"}}, "initializeMarkdown": true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(planInit.StructuredContent)
	var planEnvelope struct {
		Result struct {
			Revision string `json:"revision"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &planEnvelope); err != nil || planEnvelope.Result.Revision == "" {
		t.Fatalf("working_context plan.init err=%v raw=%s", err, raw)
	}
	planUpdate, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "working_context", Arguments: map[string]any{
		"machineId": state.MachineID, "action": "task.update", "projectPath": root, "planId": "e2e-plan", "expectedRevision": planEnvelope.Result.Revision,
		"taskId": "FS-041-E2E", "taskStatus": "done", "completion": 100, "evidence": map[string]any{"summary": "MCP task update passed", "kind": "e2e"},
	}})
	if err != nil || planUpdate.IsError {
		t.Fatalf("working_context task.update err=%v result=%+v", err, planUpdate)
	}
	markdownList, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "working_context", Arguments: map[string]any{"machineId": state.MachineID, "action": "markdown.list", "projectPath": root, "planId": "e2e-plan"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(markdownList.StructuredContent)
	if !strings.Contains(string(raw), "00-current-state.md") {
		t.Fatalf("working_context markdown.list=%s", raw)
	}

	buildResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "build_control", Arguments: map[string]any{"machineId": state.MachineID, "action": "run", "argv": e2eEchoArgv(), "cwd": root, "timeoutSeconds": 10, "idempotencyKey": "idem_e2e_build_0001", "diagnostics": true}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(buildResult.StructuredContent)
	var build struct {
		Result struct {
			RequestID string `json:"requestId"`
			TraceID   string `json:"traceId"`
			Job       struct {
				JobID     string       `json:"jobId"`
				RequestID string       `json:"requestId"`
				TraceID   string       `json:"traceId"`
				Runtime   string       `json:"runtime"`
				Timing    e2eJobTiming `json:"timing"`
			} `json:"job"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &build); err != nil || build.Result.Job.JobID == "" || build.Result.RequestID != build.Result.Job.RequestID || build.Result.TraceID != build.Result.Job.TraceID || build.Result.Job.Runtime != "host" || build.Result.Job.Timing.ProcessStartedAt == "" {
		t.Fatalf("build decode=%v raw=%s", err, raw)
	}
	if len(buildResult.Meta) == 0 || len(buildResult.Content) != 0 {
		t.Fatalf("build diagnostics metadata/content=%+v", buildResult)
	}
	waitFor(t, 10*time.Second, func() bool {
		result, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "job_watch", Arguments: map[string]any{"machineId": state.MachineID, "jobId": build.Result.Job.JobID}})
		if err != nil {
			return false
		}
		raw, _ := json.Marshal(result.StructuredContent)
		return strings.Contains(string(raw), `"state":"completed"`)
	})

	artifactUpload, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "artifact_get", Arguments: map[string]any{"action": "uploadFile", "machineId": state.MachineID, "path": filePath, "logicalName": "source.txt", "contentType": "text/plain; charset=utf-8"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(artifactUpload.StructuredContent)
	var artifact struct {
		Result struct {
			ArtifactID string `json:"artifactId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &artifact); err != nil || artifact.Result.ArtifactID == "" {
		t.Fatalf("artifact upload=%v raw=%s", err, raw)
	}
	if len(artifactUpload.Content) != 1 {
		t.Fatalf("artifact upload native content=%#v", artifactUpload.Content)
	}
	if embedded, ok := artifactUpload.Content[0].(*mcp.EmbeddedResource); !ok || embedded.Resource == nil || embedded.Resource.Text == "" || !strings.Contains(embedded.Resource.Text, "needle changed") {
		t.Fatalf("artifact upload native resource=%#v", artifactUpload.Content[0])
	}
	artifactGet, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "artifact_get", Arguments: map[string]any{"action": "get", "artifactId": artifact.Result.ArtifactID}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(artifactGet.StructuredContent)
	if !strings.Contains(string(raw), "needle changed") {
		t.Fatalf("artifact_get=%s", raw)
	}
	if len(artifactGet.Content) != 1 {
		t.Fatalf("artifact get native content=%#v", artifactGet.Content)
	}
	if embedded, ok := artifactGet.Content[0].(*mcp.EmbeddedResource); !ok || embedded.Resource == nil || !strings.Contains(embedded.Resource.Text, "needle changed") {
		t.Fatalf("artifact get native resource=%#v", artifactGet.Content[0])
	}

	emptyUpload, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "artifact_get", Arguments: map[string]any{"action": "uploadFile", "machineId": state.MachineID, "path": emptyPath, "logicalName": "empty.txt", "contentType": "text/plain; charset=utf-8"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range emptyUpload.Content {
		if _, ok := content.(*mcp.EmbeddedResource); ok {
			t.Fatalf("empty artifact must not emit malformed native resource=%#v", emptyUpload.Content)
		}
	}

	shellResult, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "shell_run", Arguments: map[string]any{"machineId": state.MachineID, "argv": e2eSleepArgv(), "cwd": root, "timeoutSeconds": 30, "idempotencyKey": "idem_e2e_cancel_0001"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(shellResult.StructuredContent)
	var job struct {
		JobID   string `json:"jobId"`
		Runtime string `json:"runtime"`
	}
	if err := json.Unmarshal(raw, &job); err != nil || job.JobID == "" || job.Runtime != "host" {
		t.Fatalf("shell=%v raw=%s", err, raw)
	}
	if bytes.Contains(raw, []byte(`"requestId"`)) || bytes.Contains(raw, []byte(`"traceId"`)) || bytes.Contains(raw, []byte(`"timing"`)) || len(shellResult.Content) != 0 || len(shellResult.Meta) == 0 {
		t.Fatalf("shell_run default response is not compact: result=%+v raw=%s", shellResult, raw)
	}
	if _, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "job_cancel", Arguments: map[string]any{"machineId": state.MachineID, "jobId": job.JobID}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		result, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{Name: "job_watch", Arguments: map[string]any{"machineId": state.MachineID, "jobId": job.JobID}})
		if err != nil {
			return false
		}
		raw, _ := json.Marshal(result.StructuredContent)
		return strings.Contains(string(raw), `"state":"canceled"`)
	})
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

type e2eJobTiming struct {
	NodeReceivedAt   string `json:"nodeReceivedAt"`
	ProcessStartedAt string `json:"processStartedAt"`
	FinishedAt       string `json:"finishedAt,omitempty"`
	QueueMs          int64  `json:"queueMs"`
	RunMs            int64  `json:"runMs,omitempty"`
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
func stringJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }
func coldMCPCall(t *testing.T, ctx context.Context, endpoint, accessToken, toolName string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpcli-cold-e2e", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: endpoint,
		HTTPClient: &http.Client{Transport: bearerTransport{
			token: accessToken, base: http.DefaultTransport,
		}},
		MaxRetries: -1, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func runE2EGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
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
