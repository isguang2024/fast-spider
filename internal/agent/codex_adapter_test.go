package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodexThreadStartParamsUsesProjectRootWithoutLosingWorktree(t *testing.T) {
	projectDirectory := filepath.Join(string(filepath.Separator), "repos", "project")
	workingDirectory := filepath.Join(string(filepath.Separator), "worktrees", "feature")
	params := codexThreadStartParams(workingDirectory, projectDirectory, "gpt-test", "high")
	if got := mapAnyString(params, "cwd"); got != workingDirectory {
		t.Fatalf("cwd=%q want %q", got, workingDirectory)
	}
	roots, _ := params["runtimeWorkspaceRoots"].([]string)
	if len(roots) != 2 || roots[0] != projectDirectory || roots[1] != workingDirectory {
		t.Fatalf("runtimeWorkspaceRoots=%#v", roots)
	}
	if _, ok := params["mode"]; ok {
		t.Fatal("unsupported mode field was sent")
	}
	if _, ok := params["threadStartKind"]; ok {
		t.Fatal("unsupported threadStartKind field was sent")
	}
}

func TestCodexThreadStartParamsDeduplicatesProjectRoot(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "repos", "project")
	params := codexThreadStartParams(root, root, "", "")
	roots, _ := params["runtimeWorkspaceRoots"].([]string)
	if len(roots) != 1 || roots[0] != root {
		t.Fatalf("runtimeWorkspaceRoots=%#v", roots)
	}
}

func TestBuildAgentTurnInputsPreservesNativeInputTypes(t *testing.T) {
	inputs := buildAgentTurnInputs("hello", []agentSkillInput{{Name: "demo", Path: "/skills/demo"}}, []string{"https://example.com/a.png"}, []string{"/tmp/a.png"}, []agentMentionInput{{Name: "ref", Path: "/tmp/ref.md"}}, "/tmp")
	if len(inputs) != 5 {
		t.Fatalf("inputs=%d", len(inputs))
	}
	want := []string{"text", "skill", "image", "localImage", "mention"}
	for i, typ := range want {
		if got := inputs[i]["type"]; got != typ {
			t.Fatalf("input[%d] type=%v want %s", i, got, typ)
		}
	}
	if inputs[1]["path"] != "/skills/demo" || inputs[1]["name"] != "demo" {
		t.Fatalf("skill input=%#v", inputs[1])
	}
}

func TestBuildAgentTurnInputsAddsNativeImageDetail(t *testing.T) {
	inputs := buildAgentTurnInputsWithDetail("", nil, []string{"https://example.com/a.png"}, []string{"/tmp/a.png"}, nil, "high")
	if len(inputs) != 2 {
		t.Fatalf("inputs=%d", len(inputs))
	}
	for i, input := range inputs {
		if input["detail"] != "high" {
			t.Fatalf("input[%d] detail=%v want high", i, input["detail"])
		}
	}
}

func TestCodexServerRequestResponsesStayBounded(t *testing.T) {
	userInput := codexServerRequest{
		Method:    "item/tool/requestUserInput",
		SessionID: "thread-1",
		Params: map[string]any{"questions": []any{
			map[string]any{"id": "choice"},
		}},
	}
	result, state, err := codexServerRequestResponse(userInput, agentControlParams{Answers: map[string][]string{"choice": {"A"}}})
	if err != nil || state != "answered" {
		t.Fatalf("user input response state=%q err=%v", state, err)
	}
	wantAnswers := map[string]any{"answers": map[string]any{"choice": map[string]any{"answers": []string{"A"}}}}
	if !reflect.DeepEqual(result, wantAnswers) {
		t.Fatalf("user input result=%#v want %#v", result, wantAnswers)
	}
	if _, _, err := codexServerRequestResponse(userInput, agentControlParams{Answers: map[string][]string{"unknown": {"A"}}}); err == nil {
		t.Fatal("unknown request_user_input question was accepted")
	}

	approval := codexServerRequest{Method: "item/commandExecution/requestApproval"}
	if result, state, err := codexServerRequestResponse(approval, agentControlParams{Decision: "accept"}); err != nil || state != "accept" || result["decision"] != "accept" {
		t.Fatalf("approval result=%#v state=%q err=%v", result, state, err)
	}
	if _, _, err := codexServerRequestResponse(approval, agentControlParams{Decision: "acceptForSession"}); err == nil {
		t.Fatal("session-wide approval widening was accepted")
	}

	form := codexServerRequest{Method: "mcpServer/elicitation/request", Params: map[string]any{"mode": "form"}}
	content := map[string]any{"region": "eu"}
	result, state, err = codexServerRequestResponse(form, agentControlParams{Decision: "accept", ResponseContent: content})
	if err != nil || state != "accept" || !reflect.DeepEqual(result["content"], content) {
		t.Fatalf("mcp elicitation result=%#v state=%q err=%v", result, state, err)
	}
}

func TestCodexServerRequestIDsAndTypes(t *testing.T) {
	if got, err := codexRequestIDString(json.RawMessage(`42`)); err != nil || got != "42" {
		t.Fatalf("numeric request id=%q err=%v", got, err)
	}
	if got, err := codexRequestIDString(json.RawMessage(`"req-1"`)); err != nil || got != "req-1" {
		t.Fatalf("string request id=%q err=%v", got, err)
	}
	if typ, ok := codexServerRequestType("item/tool/requestUserInput"); !ok || typ != "user_input.requested" {
		t.Fatalf("requestUserInput type=%q ok=%v", typ, ok)
	}
	if typ, ok := codexServerRequestType("item/permissions/requestApproval"); ok || typ != "permission.requested" {
		t.Fatalf("permission request type=%q ok=%v", typ, ok)
	}
}

func TestCodexAdapterQueuesAndResolvesInteractiveServerRequest(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	params := json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","questions":[{"id":"choice","header":"Choice","question":"Pick one"}]}`)
	adapter.handleServerRequest(json.RawMessage(`"req-1"`), "item/tool/requestUserInput", params)

	adapter.serverMu.Lock()
	pending, ok := adapter.serverRequests["req-1"]
	adapter.serverMu.Unlock()
	if !ok || pending.SessionID != "thread-1" || pending.TurnID != "turn-1" {
		t.Fatalf("pending request=%#v ok=%v", pending, ok)
	}
	if snapshot := adapter.PendingRequests("thread-1"); len(snapshot) != 1 || snapshot[0]["requestId"] != "req-1" {
		t.Fatalf("pending snapshot=%#v", snapshot)
	}
	events, _, _, err := adapter.Watch(context.Background(), "thread-1", 0, 0)
	if err != nil || len(events) == 0 {
		t.Fatalf("watch events=%#v err=%v", events, err)
	}
	last := events[len(events)-1]
	if last.Type != "user_input.requested" || last.RequestID != "req-1" || last.State != "pending" {
		t.Fatalf("interactive event=%#v", last)
	}

	adapter.handleNotification("serverRequest/resolved", json.RawMessage(`{"threadId":"thread-1","requestId":"req-1"}`))
	adapter.serverMu.Lock()
	_, stillPending := adapter.serverRequests["req-1"]
	adapter.serverMu.Unlock()
	if stillPending {
		t.Fatal("resolved server request remained pending")
	}
}

type bufferWriteCloser struct{ bytes.Buffer }

func (w *bufferWriteCloser) Close() error { return nil }

func TestCodexAdapterRespondPendingRequestWritesJSONRPCResponse(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	writer := &bufferWriteCloser{}
	adapter.mu.Lock()
	adapter.stdin = writer
	adapter.mu.Unlock()
	adapter.handleServerRequest(json.RawMessage(`7`), "item/tool/requestUserInput", json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","questions":[{"id":"choice"}]}`))
	result, err := adapter.RespondPendingRequest(context.Background(), "thread-1", "7", agentControlParams{Answers: map[string][]string{"choice": {"A"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result["responded"] != true {
		t.Fatalf("respond result=%#v", result)
	}
	var message map[string]any
	line := strings.TrimSpace(writer.String())
	if err := json.Unmarshal([]byte(line), &message); err != nil {
		t.Fatalf("invalid JSON-RPC response %q: %v", line, err)
	}
	if id, _ := message["id"].(float64); id != 7 {
		t.Fatalf("response id=%v", message["id"])
	}
	response, _ := message["result"].(map[string]any)
	answers, _ := response["answers"].(map[string]any)
	if _, ok := answers["choice"]; !ok {
		t.Fatalf("response answers=%#v", response)
	}
	adapter.serverMu.Lock()
	_, pending := adapter.serverRequests["7"]
	adapter.serverMu.Unlock()
	if pending {
		t.Fatal("responded request remained pending")
	}
}

func TestNormalizeMCPStatusDropsToolSchemas(t *testing.T) {
	result := normalizeMCPStatus(map[string]any{
		"data": []any{map[string]any{
			"name":       "demo",
			"authStatus": "oAuth",
			"tools": map[string]any{
				"zeta":  map[string]any{"inputSchema": map[string]any{"type": "object"}},
				"alpha": map[string]any{"inputSchema": map[string]any{"type": "object"}},
			},
			"resources": []any{map[string]any{"name": "doc", "uri": "demo://doc", "secret": "drop-me"}},
		}},
		"nextCursor": "next",
	})
	servers, _ := result["servers"].([]map[string]any)
	if len(servers) != 1 {
		t.Fatalf("servers=%#v", result)
	}
	if !reflect.DeepEqual(servers[0]["tools"], []string{"alpha", "zeta"}) {
		t.Fatalf("tool summary=%#v", servers[0]["tools"])
	}
	resources, _ := servers[0]["resources"].([]map[string]any)
	if len(resources) != 1 || resources[0]["uri"] != "demo://doc" {
		t.Fatalf("resource summary=%#v", resources)
	}
	if _, leaked := resources[0]["secret"]; leaked {
		t.Fatal("resource summary leaked unapproved fields")
	}
}

func TestCCSwitchModelExtractionAndRoutingRules(t *testing.T) {
	claudeSettings := map[string]any{"env": map[string]any{
		"ANTHROPIC_MODEL":                "deepseek-v4-flash",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek-v4-pro",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "deepseek-v4-pro",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "deepseek-v4-flash",
	}}
	models := extractCCSwitchModels("claude", claudeSettings, map[string]any{})
	if len(models) != 4 {
		t.Fatalf("claude role models=%#v", models)
	}
	if got := models[0]["model"]; got == "" {
		t.Fatalf("claude model missing: %#v", models[0])
	}

	desktopMeta := map[string]any{"claudeDesktopModelRoutes": map[string]any{
		"claude-sonnet-5": map[string]any{"model": "gpt-5.6-terra", "labelOverride": "GPT-5.6 Terra"},
		"claude-opus-5":   map[string]any{"model": "gpt-5.6-sol", "labelOverride": "GPT-5.6 Sol", "supports1m": true},
	}}
	desktopModels := extractCCSwitchModels("claude-desktop", map[string]any{}, desktopMeta)
	if len(desktopModels) != 2 {
		t.Fatalf("desktop model routes=%#v", desktopModels)
	}
	foundLabel := false
	for _, model := range desktopModels {
		if model["model"] == "gpt-5.6-terra" && model["displayName"] == "GPT-5.6 Terra" {
			foundLabel = true
		}
	}
	if !foundLabel {
		t.Fatalf("desktop label override not preserved: %#v", desktopModels)
	}

	codexSettings := map[string]any{"config": "model = \"gpt-5.6-sol\"\nreview_model = \"gpt-5.6-terra\"\nmodel_provider = \"custom\"\nwire_api = \"responses\"\n[model_providers.custom]\nbase_url = \"https://example.invalid\"\n"}
	codexModels := extractCCSwitchModels("codex", codexSettings, map[string]any{})
	if len(codexModels) != 2 {
		t.Fatalf("codex config models=%#v", codexModels)
	}
	fields := parseCCSwitchTopLevelConfig(codexSettings["config"].(string))
	if fields["model_provider"] != "custom" || fields["wire_api"] != "responses" {
		t.Fatalf("codex top-level config=%#v", fields)
	}

	if required, known := ccSwitchNeedsRouting("claude", "openai_responses", nil); !known || !required {
		t.Fatalf("Claude OpenAI Responses must require local routing: required=%v known=%v", required, known)
	}
	if required, known := ccSwitchNeedsRouting("claude", "anthropic", nil); !known || required {
		t.Fatalf("Claude Anthropic format should not require conversion: required=%v known=%v", required, known)
	}
	if required, known := ccSwitchNeedsRouting("codex", "openai_responses", nil); !known || required {
		t.Fatalf("Codex Responses should not require conversion: required=%v known=%v", required, known)
	}
	if required, known := ccSwitchNeedsRouting("codex", "openai_chat", nil); !known || !required {
		t.Fatalf("Codex Chat must require local routing: required=%v known=%v", required, known)
	}

	if got := ccSwitchRoutingMode(map[string]any{"takeoverEnabled": false, "liveTakeoverActive": false}, map[string]any{}); got != "direct" {
		t.Fatalf("routingMode=%q want direct", got)
	}
	if got := ccSwitchRoutingMode(map[string]any{"takeoverEnabled": true, "liveTakeoverActive": false}, map[string]any{}); got != "cc_switch" {
		t.Fatalf("routingMode=%q want cc_switch", got)
	}

	caps := deriveCCSwitchEffectiveCapabilities("claude", "cc_switch", map[string]any{"providerId": "third-party", "category": "custom", "apiFormat": "openai_responses"})
	web, _ := caps["webSearch"].(map[string]any)
	if web["state"] != "unsupported" {
		t.Fatalf("routed Claude webSearch=%#v", web)
	}
}

func TestCCSwitchSanitizersExposeNoCredentials(t *testing.T) {
	settings := map[string]any{
		"apiKey": "super-secret",
		"nested": map[string]any{"access_token": "token-value"},
	}
	if !ccSwitchCredentialPresent(settings) {
		t.Fatal("credential presence was not detected")
	}
	if host := ccSwitchEndpointHost("https://user:pass@example.com:8443/v1/messages?token=secret"); host != "example.com:8443" {
		t.Fatalf("endpoint host=%q", host)
	}
}

func TestClaudeCodeStreamParserAndSessionIndex(t *testing.T) {
	dataDir := t.TempDir()
	adapter := NewClaudeCodeAdapter(dataDir, nil, nil)
	sessionID := "11111111-2222-4333-8444-555555555555"
	record := &ClaudeSessionRecord{
		SessionID:        sessionID,
		WorkingDirectory: dataDir,
		Status:           "running",
		CreatedAt:        protocolTimestampNow(),
		UpdatedAt:        protocolTimestampNow(),
	}
	adapter.mu.Lock()
	adapter.sessions[sessionID] = record
	adapter.mu.Unlock()

	adapter.handleStreamLine(sessionID, "turn-1", []byte(`{"type":"system","subtype":"init","session_id":"11111111-2222-4333-8444-555555555555","model":"claude-sonnet-5"}`))
	adapter.handleStreamLine(sessionID, "turn-1", []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"},{"type":"tool_use","name":"Read"}]}}`))
	adapter.handleStreamLine(sessionID, "turn-1", []byte(`{"type":"result","subtype":"success","is_error":false,"result":"done","duration_ms":12,"num_turns":1,"usage":{"input_tokens":3,"output_tokens":2}}`))

	result, err := adapter.Result(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "completed" || result["finalAgentMessage"] != "done" || result["nativeModel"] != "claude-sonnet-5" {
		t.Fatalf("unexpected Claude result: %#v", result)
	}
	events, _, _, err := adapter.Watch(context.Background(), sessionID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	seenAssistant := false
	seenTool := false
	for _, event := range events {
		if event.Type == "assistant.message" && event.Text == "hello" {
			seenAssistant = true
		}
		if event.Type == "tool.started" && event.Text == "Read" {
			seenTool = true
		}
	}
	if !seenAssistant || !seenTool {
		t.Fatalf("Claude normalized events=%#v", events)
	}

	adapter.mu.Lock()
	if err := adapter.saveIndexLocked(); err != nil {
		adapter.mu.Unlock()
		t.Fatal(err)
	}
	adapter.mu.Unlock()
	reloaded := NewClaudeCodeAdapter(dataDir, nil, nil)
	got, err := reloaded.Get(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	session, _ := got["session"].(map[string]any)
	if session["nativeModel"] != "claude-sonnet-5" || session["status"] != "completed" {
		t.Fatalf("reloaded Claude session=%#v", session)
	}
}

func TestClaudeActualUpstreamRequiresExactSessionCorrelation(t *testing.T) {
	route := map[string]any{
		"routingMode": "cc_switch",
		"lastRequest": map[string]any{
			"sessionId":     "session-a",
			"providerId":    "route-provider",
			"upstreamModel": "deepseek-v4-pro",
			"requestModel":  "sonnet",
		},
	}
	if got := claudeActualUpstream("session-b", route); got != nil {
		t.Fatalf("mismatched session correlated upstream=%#v", got)
	}
	got := claudeActualUpstream("session-a", route)
	if got["providerId"] != "route-provider" || got["upstreamModel"] != "deepseek-v4-pro" {
		t.Fatalf("correlated upstream=%#v", got)
	}
	if got := claudeActualUpstream("session-a", map[string]any{"routingMode": "direct", "lastRequest": route["lastRequest"]}); got != nil {
		t.Fatalf("direct route should not claim upstream correlation: %#v", got)
	}
}

func TestClaudeTurnInputValidationIsProviderSpecific(t *testing.T) {
	if err := validateClaudeTurnInput(agentControlParams{Prompt: "hello", Thinking: "max"}); err != nil {
		t.Fatalf("valid Claude effort rejected: %v", err)
	}
	if err := validateClaudeTurnInput(agentControlParams{Prompt: "hello", Thinking: "ultra"}); err == nil {
		t.Fatal("unknown Claude effort accepted")
	}
	if err := validateClaudeTurnInput(agentControlParams{Prompt: "hello", Skills: []agentSkillInput{{Name: "demo", Path: "/tmp/demo"}}}); err == nil {
		t.Fatal("Claude provider silently accepted an unsupported native Skill input")
	}
	if err := validateClaudeTurnInput(agentControlParams{}); err == nil {
		t.Fatal("Claude provider accepted an empty turn")
	}
}

func TestCodexNativeControlParamsMatchAppServerSchema(t *testing.T) {
	cwd := filepath.Join(string(filepath.Separator), "repos", "project")
	tests := []struct {
		name string
		got  map[string]any
		want map[string]any
	}{
		{
			name: "skills list uses cwds",
			got:  codexSkillsListParams(cwd, true),
			want: map[string]any{"cwds": []string{cwd}, "forceReload": true},
		},
		{
			name: "plugin list uses protocol filters",
			got:  codexPluginListParams(cwd, []string{"local", "workspace-directory"}),
			want: map[string]any{"cwds": []string{cwd}, "marketplaceKinds": []string{"local", "workspace-directory"}},
		},
		{
			name: "plugin read uses plugin name",
			got:  codexPluginReadParams("documents", cwd, "openai-primary-runtime"),
			want: map[string]any{"pluginName": "documents", "marketplacePath": cwd, "remoteMarketplaceName": "openai-primary-runtime"},
		},
		{
			name: "plugin skill read uses remote identifiers",
			got:  codexPluginSkillReadParams("openai-curated", "remote-123", "review"),
			want: map[string]any{"remoteMarketplaceName": "openai-curated", "remotePluginId": "remote-123", "skillName": "review"},
		},
		{
			name: "rollback uses numTurns",
			got:  codexRollbackParams("thread-1", 3),
			want: map[string]any{"threadId": "thread-1", "numTurns": 3},
		},
		{
			name: "goal set preserves typed fields",
			got:  codexGoalSetParams("thread-1", "ship release", "active", 50000),
			want: map[string]any{"threadId": "thread-1", "objective": "ship release", "status": "active", "tokenBudget": int64(50000)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Fatalf("params=%#v want %#v", tt.got, tt.want)
			}
		})
	}
}

func TestCodexSettingsAndReviewParamsMatchAppServerSchema(t *testing.T) {
	settings := agentControlParams{
		WorkingDirectory: "/repo",
		Model:            "gpt-5.5",
		Effort:           "high",
		Permissions:      "workspace-write",
		Personality:      "pragmatic",
		ServiceTier:      "priority",
		Summary:          "concise",
	}
	gotSettings := codexSettingsUpdateParams("thread-1", settings)
	wantSettings := map[string]any{
		"threadId": "thread-1", "cwd": "/repo", "model": "gpt-5.5", "effort": "high",
		"permissions": "workspace-write", "personality": "pragmatic", "serviceTier": "priority", "summary": "concise",
	}
	if !reflect.DeepEqual(gotSettings, wantSettings) {
		t.Fatalf("settings=%#v want %#v", gotSettings, wantSettings)
	}

	tests := []struct {
		name  string
		input agentControlParams
		want  map[string]any
	}{
		{"default", agentControlParams{}, map[string]any{"threadId": "thread-1", "delivery": "inline", "target": map[string]any{"type": "uncommittedChanges"}}},
		{"base branch", agentControlParams{ReviewType: "baseBranch", ReviewDelivery: "detached", ReviewBranch: "main"}, map[string]any{"threadId": "thread-1", "delivery": "detached", "target": map[string]any{"type": "baseBranch", "branch": "main"}}},
		{"commit", agentControlParams{ReviewType: "commit", ReviewSHA: "abc123", ReviewTitle: "release"}, map[string]any{"threadId": "thread-1", "delivery": "inline", "target": map[string]any{"type": "commit", "sha": "abc123", "title": "release"}}},
		{"custom", agentControlParams{ReviewType: "custom", ReviewInstructions: "focus on races"}, map[string]any{"threadId": "thread-1", "delivery": "inline", "target": map[string]any{"type": "custom", "instructions": "focus on races"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexReviewStartParams("thread-1", tt.input); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("review=%#v want %#v", got, tt.want)
			}
		})
	}
}

func TestAgentControlValidationMatchesCodexEnums(t *testing.T) {
	if !hasTurnInput(agentControlParams{Skills: []agentSkillInput{{Name: "demo", Path: "/skill"}}}) {
		t.Fatal("skill-only create must count as a turn input")
	}
	if err := validateGoalInput(agentControlParams{GoalStatus: "active"}); err != nil {
		t.Fatalf("valid goal status rejected: %v", err)
	}
	if err := validateGoalInput(agentControlParams{GoalStatus: "done"}); err == nil {
		t.Fatal("unknown goal status was accepted")
	}
	if err := validateGoalInput(agentControlParams{TokenBudget: -1}); err == nil {
		t.Fatal("negative token budget was accepted")
	}
	if err := validateReviewInput(agentControlParams{ReviewType: "baseBranch", ReviewBranch: "main", ReviewDelivery: "detached"}); err != nil {
		t.Fatalf("valid baseBranch review rejected: %v", err)
	}
	if err := validateReviewInput(agentControlParams{ReviewType: "commit"}); err == nil {
		t.Fatal("commit review without sha was accepted")
	}
	if err := validateMarketplaceKinds([]string{"local", "workspace-directory"}); err != nil {
		t.Fatalf("valid marketplace kinds rejected: %v", err)
	}
	if err := validateMarketplaceKinds([]string{"marketplace"}); err == nil {
		t.Fatal("unknown marketplace kind was accepted")
	}
}

func TestValidateOutputSchemaBounds(t *testing.T) {
	if err := validateOutputSchema(map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}}); err != nil {
		t.Fatal(err)
	}
	deep := map[string]any{}
	current := deep
	for i := 0; i < 14; i++ {
		next := map[string]any{}
		current["x"] = next
		current = next
	}
	if err := validateOutputSchema(deep); err == nil {
		t.Fatal("expected depth error")
	}
}

type concurrentLineWriter struct {
	mu     sync.Mutex
	bytes  bytes.Buffer
	active int32
}

func (w *concurrentLineWriter) Write(p []byte) (int, error) {
	if !atomic.CompareAndSwapInt32(&w.active, 0, 1) {
		return 0, fmt.Errorf("concurrent write")
	}
	defer atomic.StoreInt32(&w.active, 0)
	time.Sleep(time.Microsecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bytes.Write(p)
}

func (w *concurrentLineWriter) Lines() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	data := append([]byte(nil), w.bytes.Bytes()...)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}

func TestCodexThreadNotMaterializedClassification(t *testing.T) {
	if !isCodexThreadNotMaterialized(fmt.Errorf("Codex thread/read: thread abc is not materialized yet; includeTurns is unavailable before first user message")) {
		t.Fatal("expected Codex not-materialized error to be recognized")
	}
	if isCodexThreadNotMaterialized(fmt.Errorf("Codex thread/read: session not found")) {
		t.Fatal("unrelated Codex error was misclassified")
	}
}

func TestCodexAdapterWriteLineSerializesConcurrentRPCMessages(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	writer := &concurrentLineWriter{}
	const count = 128
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := adapter.writeLine(writer, map[string]any{"id": i, "text": fmt.Sprintf("message-%03d", i)}); err != nil {
				t.Errorf("writeLine(%d): %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	lines := writer.Lines()
	if len(lines) != count {
		t.Fatalf("got %d complete lines, want %d", len(lines), count)
	}
	seen := make(map[int]bool, count)
	for _, line := range lines {
		var message struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("interleaved or invalid JSON line %q: %v", line, err)
		}
		seen[message.ID] = true
	}
	if len(seen) != count {
		t.Fatalf("got %d distinct message IDs, want %d", len(seen), count)
	}
}

var _ io.Writer = (*concurrentLineWriter)(nil)
