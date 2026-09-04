//go:build producte2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/agent"
	"github.com/isguang2024/fast-spider/internal/localbridge"
	"github.com/isguang2024/fast-spider/internal/node"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestLocalBridgeCodexProductE2E(t *testing.T) {
	if os.Getenv("FAST_SPIDER_CODEX_E2E") != "1" {
		t.Skip("set FAST_SPIDER_CODEX_E2E=1 to run the real Local Bridge to Codex product E2E")
	}
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	root := filepath.Join(base, "project")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}

	agentController := agent.New(dataDir, nil)
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := agentController.Close(closeCtx); err != nil {
			t.Errorf("close agent controller: %v", err)
		}
	}()
	client, err := node.New(node.Config{DataDir: dataDir, Version: "product-e2e", Agent: agentController})
	if err != nil {
		t.Fatal(err)
	}
	bridgeCtx, bridgeCancel := context.WithCancel(context.Background())
	bridgeDone := make(chan error, 1)
	go func() { bridgeDone <- localbridge.Run(bridgeCtx, dataDir, client.HandleLocalCapability) }()
	defer func() {
		bridgeCancel()
		select {
		case err := <-bridgeDone:
			if err != nil {
				t.Errorf("local bridge shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("local bridge did not stop")
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	providers := callAgent(t, ctx, dataDir, "providers.list", map[string]any{})
	if providers["providers"] == nil {
		t.Fatalf("providers.list=%#v", providers)
	}
	assertSingleCodexExecutionMode(t, providers)
	created := callAgent(t, ctx, dataDir, "session.create", map[string]any{"workingDirectory": root, "prompt": "只回复 FASTSPIDER_SESSION_CREATE_OK", "idempotencyKey": "product-e2e-create-01"})
	sessionID, _ := created["sessionId"].(string)
	turnID, _ := created["turnId"].(string)
	model, _ := created["model"].(string)
	if sessionID == "" || turnID == "" || model == "" {
		t.Fatalf("session.create=%#v", created)
	}
	assertNodeOwnedCodexResult(t, created)
	defer func() {
		archiveCtx, archiveCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer archiveCancel()
		if _, err := callAgentResult(archiveCtx, dataDir, "session.archive", map[string]any{"sessionId": sessionID}); err != nil {
			t.Errorf("archive session: %v", err)
		}
	}()

	final, cursor := waitAgentFinal(t, ctx, dataDir, sessionID, turnID, 0)
	if !strings.Contains(final, "FASTSPIDER_SESSION_CREATE_OK") {
		t.Fatalf("unexpected final message %q", final)
	}
	if got := callAgent(t, ctx, dataDir, "session.get", map[string]any{"sessionId": sessionID}); got["session"] == nil {
		t.Fatalf("session.get=%#v", got)
	}
	listed := callAgent(t, ctx, dataDir, "session.list", map[string]any{"workingDirectory": root, "limit": 100})
	if !sessionListContains(listed, sessionID) {
		t.Fatalf("session.list omitted created session %s: %#v", sessionID, listed)
	}
	sent := callAgent(t, ctx, dataDir, "session.send", map[string]any{"sessionId": sessionID, "prompt": "只回复 FASTSPIDER_SESSION_SEND_OK"})
	sendTurnID, _ := sent["turnId"].(string)
	if sendTurnID == "" {
		t.Fatalf("session.send=%#v", sent)
	}
	assertNodeOwnedCodexResult(t, sent)
	sendFinal, cursor := waitAgentFinal(t, ctx, dataDir, sessionID, sendTurnID, cursor)
	if !strings.Contains(sendFinal, "FASTSPIDER_SESSION_SEND_OK") {
		t.Fatalf("unexpected send final message %q", sendFinal)
	}

	forked := callAgent(t, ctx, dataDir, "session.fork", map[string]any{"sessionId": sessionID, "workingDirectory": root})
	forkID, _ := forked["sessionId"].(string)
	if forkID == "" || forkID == sessionID || forked["sourceSessionId"] != sessionID {
		t.Fatalf("session.fork=%#v", forked)
	}
	defer func() {
		archiveCtx, archiveCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer archiveCancel()
		if _, err := callAgentResult(archiveCtx, dataDir, "session.archive", map[string]any{"sessionId": forkID}); err != nil {
			t.Errorf("archive fork session: %v", err)
		}
	}()
	forkSent := callAgent(t, ctx, dataDir, "session.send", map[string]any{"sessionId": forkID, "prompt": "只回复 FASTSPIDER_SESSION_FORK_OK"})
	forkTurnID, _ := forkSent["turnId"].(string)
	if forkTurnID == "" {
		t.Fatalf("session.send fork=%#v", forkSent)
	}
	forkFinal, _ := waitAgentFinal(t, ctx, dataDir, forkID, forkTurnID, 0)
	if !strings.Contains(forkFinal, "FASTSPIDER_SESSION_FORK_OK") {
		t.Fatalf("unexpected fork final message %q", forkFinal)
	}
	originalFinal, _ := waitAgentFinal(t, ctx, dataDir, sessionID, sendTurnID, 0)
	if !strings.Contains(originalFinal, "FASTSPIDER_SESSION_SEND_OK") {
		t.Fatalf("fork modified original final message %q", originalFinal)
	}

	cancelCreated := callAgent(t, ctx, dataDir, "session.create", map[string]any{"workingDirectory": root, "idempotencyKey": "product-e2e-cancel-01"})
	cancelSessionID, _ := cancelCreated["sessionId"].(string)
	if cancelSessionID == "" {
		t.Fatalf("cancel session.create=%#v", cancelCreated)
	}
	defer func() {
		archiveCtx, archiveCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer archiveCancel()
		if _, err := callAgentResult(archiveCtx, dataDir, "session.archive", map[string]any{"sessionId": cancelSessionID}); err != nil {
			t.Errorf("archive cancel session: %v", err)
		}
	}()
	cancelSent := callAgent(t, ctx, dataDir, "session.send", map[string]any{
		"sessionId": cancelSessionID,
		"prompt":    "使用 shell_command 执行 PowerShell 命令 Start-Sleep -Seconds 30，等待结束后只回复 SHOULD_NOT_COMPLETE。",
	})
	cancelTurnID, _ := cancelSent["turnId"].(string)
	if cancelTurnID == "" {
		t.Fatalf("cancel session.send=%#v", cancelSent)
	}
	canceled := callAgent(t, ctx, dataDir, "session.cancel", map[string]any{"sessionId": cancelSessionID, "turnId": cancelTurnID})
	if canceled["cancelRequested"] != true {
		t.Fatalf("session.cancel=%#v", canceled)
	}
	cancelCursor := waitAgentCanceled(t, ctx, dataDir, cancelSessionID, cancelTurnID, 0)
	if _, err := callAgentResult(ctx, dataDir, "session.cancel", map[string]any{"sessionId": cancelSessionID, "turnId": cancelTurnID}); err == nil || !strings.Contains(err.Error(), "AGENT_SESSION_NOT_FOUND") {
		t.Fatalf("repeated session.cancel error=%v", err)
	}
	afterCancel := callAgent(t, ctx, dataDir, "session.send", map[string]any{"sessionId": cancelSessionID, "prompt": "只回复 FASTSPIDER_SESSION_AFTER_CANCEL_OK"})
	afterCancelTurnID, _ := afterCancel["turnId"].(string)
	afterCancelFinal, _ := waitAgentFinal(t, ctx, dataDir, cancelSessionID, afterCancelTurnID, cancelCursor)
	if !strings.Contains(afterCancelFinal, "FASTSPIDER_SESSION_AFTER_CANCEL_OK") {
		t.Fatalf("unexpected post-cancel final message %q", afterCancelFinal)
	}

	t.Logf("PRODUCT_E2E_OK model=%s sessionId=%s turnId=%s forkSessionId=%s cancelSessionId=%s", model, sessionID, turnID, forkID, cancelSessionID)
}

func TestLocalBridgeCodexProductE2EResumesAfterNodeRestart(t *testing.T) {
	if os.Getenv("FAST_SPIDER_CODEX_E2E") != "1" {
		t.Skip("set FAST_SPIDER_CODEX_E2E=1 to run the real Local Bridge to Codex product E2E")
	}
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	root := filepath.Join(base, "project")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}

	first := startProductLocalRuntime(t, dataDir)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	created := callAgent(t, ctx, dataDir, "session.create", map[string]any{
		"workingDirectory": root, "prompt": "只回复 FASTSPIDER_NODE_RESTART_CREATED", "idempotencyKey": "product-e2e-restart-01",
	})
	sessionID, _ := created["sessionId"].(string)
	createTurnID, _ := created["turnId"].(string)
	if sessionID == "" || createTurnID == "" {
		t.Fatalf("session.create=%#v", created)
	}
	assertNodeOwnedCodexResult(t, created)
	if final, _ := waitAgentFinal(t, ctx, dataDir, sessionID, createTurnID, 0); !strings.Contains(final, "FASTSPIDER_NODE_RESTART_CREATED") {
		t.Fatalf("unexpected create final message %q", final)
	}
	first.stop(t)

	second := startProductLocalRuntime(t, dataDir)
	defer second.stop(t)
	if got := callAgent(t, ctx, dataDir, "session.get", map[string]any{"sessionId": sessionID}); got["session"] == nil {
		t.Fatalf("session.get after restart=%#v", got)
	}
	sent := callAgent(t, ctx, dataDir, "session.send", map[string]any{"sessionId": sessionID, "prompt": "只回复 FASTSPIDER_NODE_RESTART_RESUME_OK"})
	turnID, _ := sent["turnId"].(string)
	if turnID == "" {
		t.Fatalf("session.send after restart=%#v", sent)
	}
	assertNodeOwnedCodexResult(t, sent)
	final, _ := waitAgentFinal(t, ctx, dataDir, sessionID, turnID, 0)
	if !strings.Contains(final, "FASTSPIDER_NODE_RESTART_RESUME_OK") {
		t.Fatalf("unexpected restart final message %q", final)
	}
	callAgent(t, ctx, dataDir, "session.archive", map[string]any{"sessionId": sessionID})
}

type productLocalRuntime struct {
	agent  *agent.AgentManager
	cancel context.CancelFunc
	done   chan error
	closed bool
}

func startProductLocalRuntime(t *testing.T, dataDir string) *productLocalRuntime {
	t.Helper()
	agentController := agent.New(dataDir, nil)
	client, err := node.New(node.Config{DataDir: dataDir, Version: "product-e2e", Agent: agentController})
	if err != nil {
		t.Fatal(err)
	}
	bridgeCtx, bridgeCancel := context.WithCancel(context.Background())
	bridgeDone := make(chan error, 1)
	go func() { bridgeDone <- localbridge.Run(bridgeCtx, dataDir, client.HandleLocalCapability) }()
	return &productLocalRuntime{agent: agentController, cancel: bridgeCancel, done: bridgeDone}
}

func (r *productLocalRuntime) stop(t *testing.T) {
	t.Helper()
	if r == nil || r.closed {
		return
	}
	r.closed = true
	r.cancel()
	select {
	case err := <-r.done:
		if err != nil {
			t.Fatalf("local bridge shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("local bridge did not stop")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.agent.Close(closeCtx); err != nil {
		t.Fatalf("close agent controller: %v", err)
	}
}

func assertNodeOwnedCodexResult(t *testing.T, result map[string]any) {
	t.Helper()
	if result["executionMode"] != "codex_app_server" || result["owner"] != "fast_spider_node" {
		t.Fatalf("Codex execution metadata=%#v", result)
	}
	for _, field := range []string{"desktopBridge", "desktopProjectSynced"} {
		if _, exists := result[field]; exists {
			t.Fatalf("Codex result still contains %s: %#v", field, result)
		}
	}
}

func assertSingleCodexExecutionMode(t *testing.T, providers map[string]any) {
	t.Helper()
	items, _ := providers["providers"].([]any)
	for _, raw := range items {
		provider, _ := raw.(map[string]any)
		if provider["providerId"] != "codex" {
			continue
		}
		modes, _ := provider["executionModes"].([]any)
		if len(modes) != 1 || modes[0] != "codex_app_server" {
			t.Fatalf("Codex executionModes=%#v", provider["executionModes"])
		}
		return
	}
	t.Fatal("Codex provider was not returned")
}

func waitAgentFinal(t *testing.T, ctx context.Context, dataDir, sessionID, turnID string, cursor int64) (string, int64) {
	t.Helper()
	terminalSeen := false
	for ctx.Err() == nil {
		cursor, terminalSeen = watchAgentTurn(t, ctx, dataDir, sessionID, turnID, cursor, terminalSeen, "completed")
		result := callAgent(t, ctx, dataDir, "session.result", map[string]any{"sessionId": sessionID})
		status, _ := result["status"].(string)
		final, _ := result["finalAgentMessage"].(string)
		resultTurnID, _ := result["id"].(string)
		if status == "completed" && resultTurnID == turnID && terminalSeen {
			return final, cursor
		}
		if status == "failed" || status == "canceled" {
			t.Fatalf("session %s ended %s: %#v", sessionID, status, result)
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal(ctx.Err())
	return "", cursor
}

func waitAgentCanceled(t *testing.T, ctx context.Context, dataDir, sessionID, turnID string, cursor int64) int64 {
	t.Helper()
	terminalSeen := false
	for ctx.Err() == nil {
		cursor, terminalSeen = watchAgentTurn(t, ctx, dataDir, sessionID, turnID, cursor, terminalSeen, "canceled")
		result := callAgent(t, ctx, dataDir, "session.result", map[string]any{"sessionId": sessionID})
		status, _ := result["status"].(string)
		resultTurnID, _ := result["id"].(string)
		if status == "canceled" && resultTurnID == turnID && terminalSeen {
			return cursor
		}
		if status == "completed" || status == "failed" {
			t.Fatalf("canceled session %s ended %s: %#v", sessionID, status, result)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(ctx.Err())
	return cursor
}

func watchAgentTurn(t *testing.T, ctx context.Context, dataDir, sessionID, turnID string, cursor int64, terminalSeen bool, terminalState string) (int64, bool) {
	t.Helper()
	watch := callAgent(t, ctx, dataDir, "session.watch", map[string]any{"sessionId": sessionID, "cursor": cursor, "waitSeconds": 1})
	next, ok := watch["nextCursor"].(float64)
	if !ok || int64(next) < cursor {
		t.Fatalf("session.watch cursor regressed: cursor=%d response=%#v", cursor, watch)
	}
	events, ok := watch["events"].([]any)
	if !ok && watch["events"] != nil {
		t.Fatalf("session.watch events=%#v", watch)
	}
	last := cursor
	for _, raw := range events {
		event, _ := raw.(map[string]any)
		sequence, _ := event["sequence"].(float64)
		if int64(sequence) <= last || event["sessionId"] != sessionID {
			t.Fatalf("session.watch invalid event ordering/scope: last=%d event=%#v", last, event)
		}
		last = int64(sequence)
		if event["turnId"] == turnID {
			typeName, _ := event["type"].(string)
			state, _ := event["state"].(string)
			if (typeName == "turn.completed" || typeName == "turn.interrupted") && state == terminalState {
				terminalSeen = true
			}
		}
	}
	if int64(next) != last {
		t.Fatalf("session.watch nextCursor=%d lastSequence=%d response=%#v", int64(next), last, watch)
	}
	return int64(next), terminalSeen
}

func sessionListContains(result map[string]any, sessionID string) bool {
	sessions, _ := result["sessions"].([]any)
	for _, raw := range sessions {
		session, _ := raw.(map[string]any)
		if session["sessionId"] == sessionID || session["id"] == sessionID {
			return true
		}
	}
	return false
}

func TestLocalBridgeProviderDiscoveryE2E(t *testing.T) {
	base, err := os.MkdirTemp("", "fs-provider-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	dataDir := filepath.Join(base, "data")
	agentController := agent.New(dataDir, nil)
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := agentController.Close(closeCtx); err != nil {
			t.Errorf("close agent controller: %v", err)
		}
	}()
	client, err := node.New(node.Config{DataDir: dataDir, Version: "provider-discovery-e2e", Agent: agentController})
	if err != nil {
		t.Fatal(err)
	}
	bridgeCtx, bridgeCancel := context.WithCancel(context.Background())
	bridgeDone := make(chan error, 1)
	go func() { bridgeDone <- localbridge.Run(bridgeCtx, dataDir, client.HandleLocalCapability) }()
	defer func() {
		bridgeCancel()
		select {
		case err := <-bridgeDone:
			if err != nil {
				t.Errorf("local bridge shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("local bridge did not stop")
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	routing := callAgent(t, ctx, dataDir, "routing.status", map[string]any{"appType": "claude"})
	if route, ok := routing["route"].(map[string]any); !ok || route["source"] != "cc_switch_db" {
		t.Fatalf("routing.status=%#v", routing)
	}
	providers := callAgent(t, ctx, dataDir, "providers.list", map[string]any{})
	items, _ := providers["providers"].([]any)
	foundCodex := false
	foundClaude := false
	for _, raw := range items {
		provider, _ := raw.(map[string]any)
		switch provider["providerId"] {
		case "codex":
			foundCodex = true
		case "claude_code":
			foundClaude = true
			if provider["route"] == nil || provider["authConfiguration"] == nil {
				t.Fatalf("Claude provider missing route/auth facts: %#v", provider)
			}
		}
	}
	if !foundCodex || !foundClaude {
		t.Fatalf("providers.list missing multi-provider catalog: %#v", providers)
	}
	models := callAgent(t, ctx, dataDir, "models.list", map[string]any{"providerId": "claude_code"})
	if models["route"] == nil || models["models"] == nil {
		t.Fatalf("Claude models.list=%#v", models)
	}
	capabilities := callAgent(t, ctx, dataDir, "provider.capabilities", map[string]any{"providerId": "claude_code"})
	if capabilities["providerId"] != "claude_code" || capabilities["effectiveCapabilities"] == nil {
		t.Fatalf("Claude provider.capabilities=%#v", capabilities)
	}
	codexCapabilities := callAgent(t, ctx, dataDir, "provider.capabilities", map[string]any{"providerId": "codex"})
	if codexCapabilities["providerId"] != "codex" || codexCapabilities["harnessCapabilities"] == nil || codexCapabilities["effectiveCapabilities"] == nil || codexCapabilities["route"] == nil {
		t.Fatalf("Codex provider.capabilities=%#v", codexCapabilities)
	}
}

func callAgent(t *testing.T, ctx context.Context, dataDir, action string, params map[string]any) map[string]any {
	t.Helper()
	result, err := callAgentResult(ctx, dataDir, action, params)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func callAgentResult(ctx context.Context, dataDir, action string, params map[string]any) (map[string]any, error) {
	for {
		response, err := localbridge.Call(ctx, dataDir, protocolv1.CapabilityRequest{Capability: "agent.control", Action: action, Params: params})
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			time.Sleep(25 * time.Millisecond)
			continue
		}
		if response.Error != nil {
			return nil, fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
		}
		return response.Result, nil
	}
}
