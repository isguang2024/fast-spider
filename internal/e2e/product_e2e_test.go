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

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	providers := callAgent(t, ctx, dataDir, "providers.list", map[string]any{})
	if providers["providers"] == nil {
		t.Fatalf("providers.list=%#v", providers)
	}
	created := callAgent(t, ctx, dataDir, "session.create", map[string]any{"workingDirectory": root, "prompt": "只回复 OK，不调用任何工具。"})
	sessionID, _ := created["sessionId"].(string)
	turnID, _ := created["turnId"].(string)
	model, _ := created["model"].(string)
	if sessionID == "" || turnID == "" || model == "" {
		t.Fatalf("session.create=%#v", created)
	}
	defer func() {
		archiveCtx, archiveCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer archiveCancel()
		if _, err := callAgentResult(archiveCtx, dataDir, "session.archive", map[string]any{"sessionId": sessionID}); err != nil {
			t.Errorf("archive session: %v", err)
		}
	}()

	var final string
	for ctx.Err() == nil {
		result := callAgent(t, ctx, dataDir, "session.result", map[string]any{"sessionId": sessionID})
		status, _ := result["status"].(string)
		final, _ = result["finalAgentMessage"].(string)
		if status == "completed" {
			break
		}
		if status == "failed" || status == "canceled" {
			t.Fatalf("turn ended %s: %#v", status, result)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	if !strings.Contains(strings.ToUpper(final), "OK") {
		t.Fatalf("unexpected final message %q", final)
	}
	watch := callAgent(t, ctx, dataDir, "session.watch", map[string]any{"sessionId": sessionID, "cursor": 0})
	if watch["events"] == nil {
		t.Fatalf("session.watch=%#v", watch)
	}
	t.Logf("PRODUCT_E2E_OK model=%s sessionId=%s turnId=%s final=%q", model, sessionID, turnID, final)
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
