package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
)

func TestDesktopCallbackRequiresIdleOwnerAndConfirmedTurn(t *testing.T) {
	for _, state := range []string{"idle", "active", ""} {
		t.Run(state, func(t *testing.T) {
			client, server := net.Pipe()
			defer server.Close()
			started := make(chan bool, 1)
			go func() {
				var mu sync.Mutex
				for {
					msg, err := readCodexDesktopIPCFrame(server)
					if err != nil {
						return
					}
					if msg.Type != "request" {
						continue
					}
					result := map[string]any{}
					switch msg.Method {
					case "initialize":
						result["clientId"] = "requester"
					case "thread-owner-discovery":
						result["supportsUntrustedAppInput"] = true
					case "thread-follower-load-complete-history":
						_ = writeCodexDesktopIPCFrame(server, &mu, codexDesktopIPCMessage{Type: "broadcast", Method: "thread-stream-state-changed", Version: 11, Params: map[string]any{"change": map[string]any{"conversationState": map[string]any{"id": "target", "threadRuntimeStatus": map[string]any{"type": state}}}}})
					case "thread-follower-start-turn":
						started <- true
						result["turn"] = map[string]any{"id": "confirmed-turn"}
					}
					if err := writeCodexDesktopIPCFrame(server, &mu, codexDesktopIPCMessage{Type: "response", RequestID: msg.RequestID, ResultType: "success", HandledByClientID: "desktop-owner", Result: result}); err != nil {
						return
					}
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			result, err := startDesktopCallbackTurn(ctx, "target", []map[string]any{{"type": "text", "text": "callback"}}, func() (io.ReadWriteCloser, error) { return client, nil })
			if state == "idle" {
				if err != nil || mapNestedString(result, "turn", "id") != "confirmed-turn" {
					t.Fatalf("result=%v err=%v", result, err)
				}
				if err := validateSessionCallbackLocalCodexTurnDelivery(sessionCallbackDeliveryResult{ExecutionMode: "codex_desktop_ipc", Owner: "codex_desktop", TurnID: "confirmed-turn"}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("non-idle owner accepted a callback")
				}
				if state == "active" && !errors.Is(err, node.ErrAgentSessionBusy) {
					t.Fatalf("busy error=%v", err)
				}
				select {
				case <-started:
					t.Fatal("non-idle owner received turn/start")
				default:
				}
			}
		})
	}
}

func TestDesktopCallbackCancellationClosesBlockedConnection(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := startDesktopCallbackTurn(ctx, "target", []map[string]any{{"type": "text", "text": "callback"}}, func() (io.ReadWriteCloser, error) { return client, nil })
	if err == nil || ctx.Err() == nil {
		t.Fatalf("err=%v context=%v", err, ctx.Err())
	}
}

func TestDesktopCallbackOwnerBusyRealE2E(t *testing.T) {
	sessionID := os.Getenv("FAST_SPIDER_CALLBACK_BUSY_SESSION")
	if sessionID == "" {
		t.Skip("explicit active Desktop session required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	conn, err := dialCodexDesktopIPC()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	var mu sync.Mutex
	clientID, err := initializeCodexDesktopIPCClient(conn, &mu)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := requestCodexDesktopIPC(ctx, conn, &mu, clientID, "", "thread-owner-discovery", 1, map[string]any{"hostId": "local", "conversationId": sessionID}, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if owner.HandledByClientID == "" || owner.Result["supportsUntrustedAppInput"] != true {
		t.Fatalf("owner unavailable: %v", owner.Result)
	}
	err = requireCodexDesktopThreadIdle(ctx, conn, &mu, clientID, owner.HandledByClientID, sessionID)
	if !errors.Is(err, node.ErrAgentSessionBusy) {
		t.Fatalf("active owner must be busy, got %v", err)
	}
	t.Log("existing Desktop owner discovered; active thread rejected without sending a turn")
}
