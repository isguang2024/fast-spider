//go:build codexe2e

package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCCSwitchInspectorRealE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	inspector := NewCCSwitchInspector(nil)
	if _, err := os.Stat(inspector.DBPath()); err != nil {
		t.Skipf("CC Switch database unavailable: %v", err)
	}
	for _, appType := range []string{"claude", "codex", "claude-desktop"} {
		route, err := inspector.InspectApp(ctx, appType)
		if err != nil {
			t.Fatalf("InspectApp(%s): %v", appType, err)
		}
		if available, _ := route["available"].(bool); !available {
			t.Fatalf("InspectApp(%s) unavailable: %#v", appType, route)
		}
		providers, ok := route["providers"].([]map[string]any)
		if !ok || len(providers) == 0 {
			t.Fatalf("InspectApp(%s) providers=%#v", appType, route["providers"])
		}
		for _, provider := range providers {
			for _, forbidden := range []string{"settings", "settingsConfig", "settings_config", "meta", "apiKey", "token", "secret"} {
				if _, leaked := provider[forbidden]; leaked {
					t.Fatalf("InspectApp(%s) leaked %s in %#v", appType, forbidden, provider)
				}
			}
			if endpoint, _ := provider["endpointHost"].(string); strings.Contains(endpoint, "/") || strings.Contains(endpoint, "@") {
				t.Fatalf("InspectApp(%s) endpointHost is not sanitized: %q", appType, endpoint)
			}
			if health, ok := provider["health"].(map[string]any); ok {
				if _, leaked := health["lastError"]; leaked {
					t.Fatalf("InspectApp(%s) leaked raw provider health error: %#v", appType, health)
				}
			}
		}
		if mode, _ := route["routingMode"].(string); mode != "direct" && mode != "cc_switch" {
			t.Fatalf("InspectApp(%s) routingMode=%q", appType, mode)
		}
		if _, hasDevice := route["deviceCurrentProviderId"].(string); hasDevice {
			if consistent, ok := route["selectionConsistent"].(bool); !ok || !consistent {
				t.Fatalf("InspectApp(%s) DB/settings current provider drift: %#v", appType, route)
			}
		}
	}
}

func TestClaudeCodeAdapterRealE2E(t *testing.T) {
	if os.Getenv("FAST_SPIDER_CLAUDE_E2E") != "1" {
		t.Skip("set FAST_SPIDER_CLAUDE_E2E=1 to run the real local Claude Code test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	adapter := NewClaudeCodeAdapter(t.TempDir(), NewCCSwitchInspector(nil), nil)
	adapter.disableSessionPersistence = true
	if version, err := adapter.Availability(ctx); err != nil || version == "" {
		t.Fatalf("Claude Code availability version=%q err=%v", version, err)
	}
	root := t.TempDir()
	created, err := adapter.Create(ctx, root, "只回复 OK，不调用任何工具。", "", "low", "", nil)
	if err != nil {
		t.Fatalf("Claude session.create: %v", err)
	}
	sessionID := mapString(created, "sessionId")
	if sessionID == "" {
		t.Fatalf("Claude create returned no sessionId: %#v", created)
	}

	var terminal map[string]any
	for ctx.Err() == nil {
		result, resultErr := adapter.Result(sessionID)
		if resultErr != nil {
			t.Fatalf("Claude result: %v", resultErr)
		}
		status := mapString(result, "status")
		if status == "completed" || status == "failed" || status == "canceled" {
			terminal = result
			break
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	if terminal == nil {
		t.Fatal("Claude Code did not reach a terminal state")
	}
	if mapString(terminal, "nativeModel") == "" {
		t.Fatalf("Claude Code init did not report its native model: %#v", terminal)
	}
	status := mapString(terminal, "status")
	if status == "completed" {
		if strings.TrimSpace(mapString(terminal, "finalAgentMessage")) == "" {
			t.Fatalf("Claude completed without final message: %#v", terminal)
		}
	} else if status == "failed" {
		if strings.TrimSpace(mapString(terminal, "error")) == "" {
			t.Fatalf("Claude failed without normalized error: %#v", terminal)
		}
		t.Logf("Claude Code runtime is healthy but current upstream execution failed: %s", mapString(terminal, "error"))
	} else {
		t.Fatalf("unexpected Claude terminal status %q: %#v", status, terminal)
	}

	events, _, _, err := adapter.Watch(ctx, sessionID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	seenInitialized := false
	seenTerminal := false
	for _, event := range events {
		if event.Type == "session.status" && event.State == "initialized" {
			seenInitialized = true
		}
		if event.Type == "turn.completed" || event.Type == "turn.failed" {
			seenTerminal = true
		}
	}
	if !seenInitialized || !seenTerminal {
		t.Fatalf("Claude normalized lifecycle incomplete: %#v", events)
	}
}

func TestCodexAdapterRealE2E(t *testing.T) {
	if os.Getenv("FAST_SPIDER_CODEX_E2E") != "1" {
		t.Skip("set FAST_SPIDER_CODEX_E2E=1 to run the real local Codex app-server test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	adapter := NewCodexAdapter(nil)
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		_ = adapter.Close(closeCtx)
	}()

	if version, err := adapter.Availability(ctx); err != nil || version == "" {
		t.Fatalf("Codex availability version=%q err=%v", version, err)
	}
	models, err := adapter.ListModels(ctx)
	if err != nil {
		t.Fatalf("model/list: %v", err)
	}
	items, ok := models["data"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("model/list result has no usable data field: %#v", models)
	}
	firstModel, _ := items[0].(map[string]any)
	modelID := mapString(firstModel, "id")
	if modelID == "" {
		modelID = mapString(firstModel, "model")
	}
	if modelID == "" {
		t.Fatalf("model/list first item has no model ID: %#v", firstModel)
	}

	root := t.TempDir()
	providerCapabilities, err := adapter.ProviderCapabilities(ctx)
	if err != nil {
		t.Fatalf("modelProvider/capabilities/read: %v", err)
	}
	for _, key := range []string{"webSearch", "imageGeneration", "namespaceTools"} {
		if _, ok := providerCapabilities[key].(bool); !ok {
			t.Fatalf("provider capability %s missing from %#v", key, providerCapabilities)
		}
	}
	if hooks, err := adapter.ListHooks(ctx, root); err != nil {
		t.Fatalf("hooks/list: %v", err)
	} else if _, ok := hooks["data"].([]any); !ok {
		t.Fatalf("hooks/list result has no data array: %#v", hooks)
	}
	if permissions, err := adapter.ListPermissionProfiles(ctx, root, 100, ""); err != nil {
		t.Fatalf("permissionProfile/list: %v", err)
	} else if _, ok := permissions["data"].([]any); !ok {
		t.Fatalf("permissionProfile/list result has no data array: %#v", permissions)
	}
	if installed, err := adapter.ListInstalledPlugins(ctx, root); err != nil {
		t.Fatalf("plugin/installed: %v", err)
	} else if _, ok := installed["marketplaces"].([]any); !ok {
		t.Fatalf("plugin/installed result has no marketplaces array: %#v", installed)
	}
	if mcpStatus, err := adapter.ListMCPServerStatus(ctx, "", "toolsAndAuthOnly", 100, ""); err != nil {
		t.Fatalf("mcpServerStatus/list: %v", err)
	} else if _, ok := mcpStatus["data"].([]any); !ok {
		t.Fatalf("mcpServerStatus/list result has no data array: %#v", mcpStatus)
	}

	if skills, err := adapter.ListSkills(ctx, root, true); err != nil {
		t.Fatalf("skills/list: %v", err)
	} else if _, ok := skills["data"].([]any); !ok {
		t.Fatalf("skills/list result has no data array: %#v", skills)
	}
	plugins, err := adapter.ListPlugins(ctx, root, nil)
	if err != nil {
		t.Fatalf("plugin/list: %v", err)
	}
	marketplaces, ok := plugins["marketplaces"].([]any)
	if !ok {
		t.Fatalf("plugin/list result has no marketplaces array: %#v", plugins)
	}
	pluginReadVerified := false
	for _, rawMarketplace := range marketplaces {
		marketplace, _ := rawMarketplace.(map[string]any)
		marketplacePath := mapString(marketplace, "path")
		pluginItems, _ := marketplace["plugins"].([]any)
		if marketplacePath == "" {
			continue
		}
		for _, rawPlugin := range pluginItems {
			plugin, _ := rawPlugin.(map[string]any)
			installed, _ := plugin["installed"].(bool)
			pluginName := mapString(plugin, "name")
			if !installed || pluginName == "" {
				continue
			}
			if detail, readErr := adapter.ReadPlugin(ctx, pluginName, marketplacePath, ""); readErr != nil {
				t.Fatalf("plugin/read %s: %v", pluginName, readErr)
			} else if _, ok := detail["plugin"].(map[string]any); !ok {
				t.Fatalf("plugin/read %s returned no plugin detail: %#v", pluginName, detail)
			}
			pluginReadVerified = true
			break
		}
		if pluginReadVerified {
			break
		}
	}
	if !pluginReadVerified {
		t.Log("plugin/read skipped because no installed local marketplace plugin was discovered")
	}

	threadResult, err := adapter.StartThread(ctx, root, root, modelID, "")
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	sessionID := mapNestedString(threadResult, "thread", "id")
	if sessionID == "" {
		t.Fatalf("thread/start returned no session ID: %#v", threadResult)
	}
	defer func() {
		archiveCtx, archiveCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer archiveCancel()
		_ = adapter.ArchiveThread(archiveCtx, sessionID)
	}()

	if _, err := adapter.SetGoal(ctx, sessionID, "Fast Spider Codex adapter E2E", "paused", 0); err != nil {
		t.Fatalf("thread/goal/set: %v", err)
	}
	goalResult, err := adapter.GetGoal(ctx, sessionID)
	if err != nil {
		t.Fatalf("thread/goal/get: %v", err)
	}
	goal, _ := goalResult["goal"].(map[string]any)
	if mapString(goal, "objective") != "Fast Spider Codex adapter E2E" || mapString(goal, "status") != "paused" {
		t.Fatalf("unexpected goal: %#v", goalResult)
	}
	if cleared, err := adapter.ClearGoal(ctx, sessionID); err != nil {
		t.Fatalf("thread/goal/clear: %v", err)
	} else if ok, _ := cleared["cleared"].(bool); !ok {
		t.Fatalf("thread/goal/clear did not confirm clear: %#v", cleared)
	}

	preTurnRead, err := adapter.ReadThread(ctx, sessionID)
	if err != nil {
		t.Fatalf("thread/read before first turn: %v", err)
	}
	preTurnThread, _ := preTurnRead["thread"].(map[string]any)
	if mapString(preTurnThread, "id") != sessionID {
		t.Fatalf("metadata-only thread/read returned wrong session: %#v", preTurnRead)
	}

	outputSchema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"status": map[string]any{"type": "string"},
		},
		"required": []any{"status"},
	}
	turnResult, err := adapter.StartTurnWithInputs(ctx, sessionID, buildAgentTurnInputs("只输出一个符合 schema 的 JSON 对象，status 必须是 OK，不调用任何工具。", nil, nil, nil, nil, root), root, modelID, "", outputSchema)
	if err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	turnID := mapNestedString(turnResult, "turn", "id")
	if turnID == "" {
		t.Fatalf("turn/start returned no turn ID: %#v", turnResult)
	}

	var final string
	for ctx.Err() == nil {
		threadRead, readErr := adapter.ReadThread(ctx, sessionID)
		if readErr != nil {
			t.Fatalf("thread/read: %v", readErr)
		}
		thread, _ := threadRead["thread"].(map[string]any)
		result := normalizeCodexResult(thread)
		status, _ := result["status"].(string)
		final, _ = result["finalAgentMessage"].(string)
		if status == "completed" {
			break
		}
		if status == "failed" || status == "canceled" {
			t.Fatalf("turn %s ended with status %s: %#v", turnID, status, result)
		}
		timer := time.NewTimer(300 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	if final == "" {
		events, _, _, watchErr := adapter.Watch(ctx, sessionID, 0, 0)
		if watchErr != nil {
			t.Fatalf("session watch: %v", watchErr)
		}
		for i := len(events) - 1; i >= 0; i-- {
			if strings.HasPrefix(events[i].Type, "assistant") && events[i].Text != "" {
				final = events[i].Text
				break
			}
			if events[i].Type == "error" && events[i].Text != "" {
				t.Fatalf("Codex emitted error event: %s", events[i].Text)
			}
		}
	}
	var structured map[string]any
	if err := json.Unmarshal([]byte(final), &structured); err != nil {
		t.Fatalf("outputSchema final message is not JSON: %q err=%v", final, err)
	}
	if got := strings.ToUpper(mapString(structured, "status")); got != "OK" {
		t.Fatalf("unexpected structured Codex status %q from %q", got, final)
	}

	adapter.mu.Lock()
	cmd := adapter.cmd
	adapter.mu.Unlock()
	if cmd == nil {
		t.Fatal("Codex app-server process was unexpectedly unavailable before resume test")
	}
	stopCtx, stopCancel := context.WithTimeout(ctx, 10*time.Second)
	if err := adapter.stopProcess(stopCtx, cmd); err != nil {
		stopCancel()
		t.Fatalf("stop Codex app-server before resume test: %v", err)
	}
	stopCancel()

	resumeTurn, err := adapter.StartTurn(ctx, sessionID, "只回复 RESUMED，不调用任何工具。", root, modelID, "")
	if err != nil {
		t.Fatalf("turn/start after app-server restart (auto resume): %v", err)
	}
	resumeTurnID := mapNestedString(resumeTurn, "turn", "id")
	if resumeTurnID == "" {
		t.Fatalf("auto-resumed turn returned no turn ID: %#v", resumeTurn)
	}
	resumeFinal := ""
	for ctx.Err() == nil {
		threadRead, readErr := adapter.ReadThread(ctx, sessionID)
		if readErr != nil {
			t.Fatalf("thread/read after auto resume: %v", readErr)
		}
		thread, _ := threadRead["thread"].(map[string]any)
		result := normalizeCodexResult(thread)
		status, _ := result["status"].(string)
		resumeFinal, _ = result["finalAgentMessage"].(string)
		if status == "completed" {
			break
		}
		if status == "failed" || status == "canceled" {
			t.Fatalf("auto-resumed turn %s ended with status %s: %#v", resumeTurnID, status, result)
		}
		timer := time.NewTimer(300 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	if !strings.Contains(strings.ToUpper(resumeFinal), "RESUMED") {
		t.Fatalf("unexpected auto-resumed final Codex message %q", resumeFinal)
	}
}
