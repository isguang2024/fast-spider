//go:build windows

package agent

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
)

// TestCodexDesktopBridgeWindowsE2E uses a synthetic conversation ID and only
// exercises Desktop's local IPC router. It does not create, mutate, or archive
// a real Codex thread.
func TestCodexDesktopBridgeWindowsE2E(t *testing.T) {
	if os.Getenv("FAST_SPIDER_CODEX_DESKTOP_BRIDGE_E2E") != "1" {
		t.Skip("set FAST_SPIDER_CODEX_DESKTOP_BRIDGE_E2E=1 to test the installed Codex Desktop IPC router")
	}
	sessionID := fmt.Sprintf("fast-spider-ipc-e2e-%d", time.Now().UnixNano())
	adapter := NewCodexAdapter(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	adapter.loaded[sessionID] = struct{}{}
	bridge := &codexDesktopBridge{adapter: adapter, logger: adapter.logger}

	ownerConn, err := dialCodexDesktopIPC()
	if err != nil {
		t.Fatalf("connect owner to Codex Desktop IPC: %v", err)
	}
	ownerDone := make(chan error, 1)
	go func() { ownerDone <- bridge.serveConnection(ownerConn) }()
	ownerReadyDeadline := time.Now().Add(5 * time.Second)
	var ownerClientID string
	for {
		bridge.connMu.Lock()
		ownerClientID = bridge.client
		bridge.connMu.Unlock()
		if ownerClientID != "" {
			break
		}
		if time.Now().After(ownerReadyDeadline) {
			_ = ownerConn.Close()
			t.Fatal("owner bridge did not initialize with Codex Desktop")
		}
		time.Sleep(10 * time.Millisecond)
	}

	probe, err := dialCodexDesktopIPC()
	if err != nil {
		_ = ownerConn.Close()
		t.Fatalf("connect probe to Codex Desktop IPC: %v", err)
	}
	defer probe.Close()
	if deadline, ok := probe.(interface{ SetDeadline(time.Time) error }); ok {
		_ = deadline.SetDeadline(time.Now().Add(10 * time.Second))
	}
	probeWriteMu := &sync.Mutex{}
	initializeID := fmt.Sprintf("fast-spider-probe-%d", time.Now().UnixNano())
	if err := writeCodexDesktopIPCFrame(probe, probeWriteMu, map[string]any{
		"type":           "request",
		"requestId":      initializeID,
		"sourceClientId": "initializing-client",
		"version":        0,
		"method":         "initialize",
		"params":         map[string]any{"clientType": "desktop"},
		"timeoutMs":      5000,
	}); err != nil {
		t.Fatal(err)
	}
	initialize, err := readCodexDesktopIPCFrame(probe)
	if err != nil || initialize.ResultType != "success" {
		t.Fatalf("initialize probe response=%#v err=%v", initialize, err)
	}
	probeClientID := mapString(initialize.Result, "clientId")
	requestID := fmt.Sprintf("fast-spider-owner-discovery-%d", time.Now().UnixNano())
	if err := writeCodexDesktopIPCFrame(probe, probeWriteMu, map[string]any{
		"type":           "request",
		"requestId":      requestID,
		"sourceClientId": probeClientID,
		// The synthetic conversation has no real Desktop role/lock state. Target
		// the bridge registered above so another Desktop client cannot claim the
		// unknown synthetic thread before the bridge does.
		"targetClientId": ownerClientID,
		"version":        1,
		"method":         "thread-owner-discovery",
		"params":         map[string]any{"hostId": "local", "conversationId": sessionID},
		"timeoutMs":      5000,
	}); err != nil {
		t.Fatal(err)
	}
	var response codexDesktopIPCMessage
	for {
		response, err = readCodexDesktopIPCFrame(probe)
		if err != nil {
			t.Fatal(err)
		}
		if response.Type == "response" && response.RequestID == requestID {
			break
		}
	}
	if response.ResultType != "success" || response.Method != "thread-owner-discovery" || response.HandledByClientID == "" {
		t.Fatalf("owner discovery response=%#v", response)
	}

	_ = ownerConn.Close()
	select {
	case <-ownerDone:
	case <-time.After(time.Second):
		t.Fatal("owner bridge did not stop after the E2E connection closed")
	}
}
