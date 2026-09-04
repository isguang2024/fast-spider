package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
)

func TestSessionSendPrefersCodexDesktopOwner(t *testing.T) {
	dataDir := t.TempDir()
	manager := New(dataDir, nil)
	defer manager.Close(context.Background())
	rootHint := t.TempDir()
	manager.codexStatePath = filepath.Join(dataDir, codexDesktopStateFilename)
	state := fmt.Sprintf(`{"local-projects":{},"thread-project-assignments":{},"projectless-thread-ids":["desktop-thread"],"thread-workspace-root-hints":{"desktop-thread":%q}}`, rootHint)
	if err := os.WriteFile(manager.codexStatePath, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	manager.codex.desktopRequestDial = singleCodexDesktopDial(client)
	requests, serverErr := serveCodexDesktopTurn(t, server, "desktop-thread", "idle", "turn-desktop", "")
	manager.codex.requestOverride = func(_ context.Context, method string, _ map[string]any) (map[string]any, error) {
		return nil, fmt.Errorf("app-server must not be used for Desktop-owned session.send: %s", method)
	}

	got, err := manager.Control(context.Background(), "session.send", map[string]any{
		"providerId": "codex", "sessionId": "desktop-thread", "prompt": "from Fast Spider",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["turnId"] != "turn-desktop" || got["executionMode"] != "codex_desktop_ipc" || got["owner"] != "codex_desktop" {
		t.Fatalf("Desktop session.send=%#v", got)
	}
	request := <-requests
	turnStart := mapValueMap(mapValueMap(request, "params"), "turnStart")
	turnRequest := mapValueMap(turnStart, "request")
	if turnRequest["threadId"] != "desktop-thread" || turnRequest["cwd"] != rootHint {
		t.Fatalf("Desktop turn request=%#v", turnRequest)
	}
	inputs, _ := turnRequest["input"].([]any)
	if len(inputs) != 1 || mapString(inputs[0].(map[string]any), "text") != "from Fast Spider" {
		t.Fatalf("Desktop turn inputs=%#v", turnRequest["input"])
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestCodexDesktopRequesterMapsBusyWithoutFallback(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	client, server := net.Pipe()
	adapter.desktopRequestDial = singleCodexDesktopDial(client)
	_, serverErr := serveCodexDesktopTurn(t, server, "desktop-busy", "idle", "", "thread already has an active turn")
	adapter.requestOverride = func(_ context.Context, method string, _ map[string]any) (map[string]any, error) {
		return nil, fmt.Errorf("unexpected app-server fallback: %s", method)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := adapter.startDesktopOwnedTurn(ctx, "desktop-busy", []map[string]any{{"type": "text", "text": "busy"}}, codexTurnOptions{})
	if !errors.Is(err, node.ErrAgentSessionBusy) {
		t.Fatalf("busy error=%v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestCodexDesktopRequesterRejectsActiveRuntimeBeforeStartTurn(t *testing.T) {
	adapter := NewCodexAdapter(nil)
	client, server := net.Pipe()
	adapter.desktopRequestDial = singleCodexDesktopDial(client)
	requests, serverErr := serveCodexDesktopTurn(t, server, "desktop-active", "active", "", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := adapter.startDesktopOwnedTurn(ctx, "desktop-active", []map[string]any{{"type": "text", "text": "must wait"}}, codexTurnOptions{})
	if !errors.Is(err, node.ErrAgentSessionBusy) {
		t.Fatalf("active runtime error=%v", err)
	}
	select {
	case request := <-requests:
		t.Fatalf("active runtime unexpectedly started turn: %#v", request)
	default:
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func singleCodexDesktopDial(conn io.ReadWriteCloser) func() (io.ReadWriteCloser, error) {
	var mu sync.Mutex
	used := false
	return func() (io.ReadWriteCloser, error) {
		mu.Lock()
		defer mu.Unlock()
		if used {
			return nil, errors.New("Desktop IPC test dial reused")
		}
		used = true
		return conn, nil
	}
}

func serveCodexDesktopTurn(t *testing.T, conn io.ReadWriteCloser, sessionID, runtimeType, turnID, turnError string) (<-chan map[string]any, <-chan error) {
	t.Helper()
	requests := make(chan map[string]any, 1)
	done := make(chan error, 1)
	go func() {
		defer conn.Close()
		writeMu := &sync.Mutex{}
		initialize, err := readCodexDesktopIPCFrame(conn)
		if err != nil {
			done <- err
			return
		}
		if initialize.Method != "initialize" {
			done <- fmt.Errorf("initialize request=%#v", initialize)
			return
		}
		if err := writeCodexDesktopIPCFrame(conn, writeMu, map[string]any{
			"type": "response", "requestId": initialize.RequestID, "resultType": "success", "method": "initialize",
			"result": map[string]any{"clientId": "requester-client"},
		}); err != nil {
			done <- err
			return
		}
		owner, err := readCodexDesktopIPCFrame(conn)
		if err != nil {
			done <- err
			return
		}
		if owner.Method != "thread-owner-discovery" || owner.Version != 1 || mapString(owner.Params, "conversationId") != sessionID {
			done <- fmt.Errorf("owner request=%#v", owner)
			return
		}
		if err := writeCodexDesktopIPCFrame(conn, writeMu, map[string]any{
			"type": "client-discovery-request", "requestId": "requester-discovery-check", "request": map[string]any{"method": "thread-owner-discovery"},
		}); err != nil {
			done <- err
			return
		}
		discovery, err := readCodexDesktopIPCFrame(conn)
		if err != nil {
			done <- err
			return
		}
		canHandle, _ := discovery.Response["canHandle"].(bool)
		if discovery.Type != "client-discovery-response" || canHandle {
			done <- fmt.Errorf("requester discovery response=%#v", discovery)
			return
		}
		if err := writeCodexDesktopIPCFrame(conn, writeMu, map[string]any{
			"type": "response", "requestId": owner.RequestID, "resultType": "success", "method": owner.Method,
			"handledByClientId": "desktop-owner", "result": map[string]any{"supportsUntrustedAppInput": true},
		}); err != nil {
			done <- err
			return
		}
		following, err := readCodexDesktopIPCFrame(conn)
		if err != nil {
			done <- err
			return
		}
		if following.Type != "broadcast" || following.Method != "thread-stream-following-changed" || following.Version != 1 || mapString(following.Params, "conversationId") != sessionID || following.Params["following"] != true {
			done <- fmt.Errorf("following broadcast=%#v", following)
			return
		}
		history, err := readCodexDesktopIPCFrame(conn)
		if err != nil {
			done <- err
			return
		}
		if history.Method != "thread-follower-load-complete-history" || history.Version != 1 || history.TargetClientID != "desktop-owner" || mapString(history.Params, "conversationId") != sessionID {
			done <- fmt.Errorf("history request=%#v", history)
			return
		}
		if err := writeCodexDesktopIPCFrame(conn, writeMu, map[string]any{
			"type": "broadcast", "sourceClientId": "desktop-owner", "version": 11, "method": "thread-stream-state-changed",
			"params": map[string]any{"change": map[string]any{"conversationState": map[string]any{"id": sessionID, "threadRuntimeStatus": map[string]any{"type": runtimeType}}}},
		}); err != nil {
			done <- err
			return
		}
		if err := writeCodexDesktopIPCFrame(conn, writeMu, map[string]any{
			"type": "response", "requestId": history.RequestID, "resultType": "success", "method": history.Method,
			"handledByClientId": "desktop-owner", "result": map[string]any{"revision": 1},
		}); err != nil {
			done <- err
			return
		}
		stopped, err := readCodexDesktopIPCFrame(conn)
		if err != nil {
			done <- err
			return
		}
		if stopped.Method != "thread-stream-following-changed" || stopped.Params["following"] != false {
			done <- fmt.Errorf("stop following broadcast=%#v", stopped)
			return
		}
		if runtimeType != "idle" {
			done <- nil
			return
		}
		start, err := readCodexDesktopIPCFrame(conn)
		if err != nil {
			done <- err
			return
		}
		if start.Method != "thread-follower-start-turn" || start.Version != 2 || start.TargetClientID != "desktop-owner" || mapString(start.Params, "conversationId") != sessionID {
			done <- fmt.Errorf("start request=%#v", start)
			return
		}
		requests <- map[string]any{"params": start.Params}
		response := map[string]any{
			"type": "response", "requestId": start.RequestID, "method": start.Method, "handledByClientId": "desktop-owner",
		}
		if turnError != "" {
			response["resultType"] = "error"
			response["error"] = turnError
		} else {
			response["resultType"] = "success"
			response["result"] = map[string]any{"result": map[string]any{"turn": map[string]any{"id": turnID, "status": "inProgress"}}}
		}
		if err := writeCodexDesktopIPCFrame(conn, writeMu, response); err != nil {
			done <- err
			return
		}
		done <- nil
	}()
	return requests, done
}
