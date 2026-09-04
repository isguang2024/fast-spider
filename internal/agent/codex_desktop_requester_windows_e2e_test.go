//go:build windows

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
)

// TestCodexDesktopRequesterWindowsE2E starts a real Turn in an explicitly
// supplied, idle Codex Desktop thread. It is opt-in because it mutates that
// conversation and must never guess a target session.
func TestCodexDesktopRequesterWindowsE2E(t *testing.T) {
	if os.Getenv("FAST_SPIDER_CODEX_DESKTOP_SEND_E2E") != "1" {
		t.Skip("set FAST_SPIDER_CODEX_DESKTOP_SEND_E2E=1 with an explicit session and prompt")
	}
	sessionID := strings.TrimSpace(os.Getenv("FAST_SPIDER_CODEX_DESKTOP_SEND_SESSION_ID"))
	prompt := strings.TrimSpace(os.Getenv("FAST_SPIDER_CODEX_DESKTOP_SEND_PROMPT"))
	if sessionID == "" || prompt == "" {
		t.Fatal("FAST_SPIDER_CODEX_DESKTOP_SEND_SESSION_ID and FAST_SPIDER_CODEX_DESKTOP_SEND_PROMPT are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adapter := NewCodexAdapter(nil)
	result, err := adapter.startDesktopOwnedTurn(ctx, sessionID, []map[string]any{{"type": "text", "text": prompt}}, codexTurnOptions{
		WorkingDirectory: strings.TrimSpace(os.Getenv("FAST_SPIDER_CODEX_DESKTOP_SEND_CWD")),
	})
	if err != nil {
		t.Fatal(err)
	}
	turnID := mapNestedString(result, "turn", "id")
	if turnID == "" {
		t.Fatalf("Desktop requester result=%#v", result)
	}
	t.Logf("sessionId=%s turnId=%s", sessionID, turnID)
}

func TestCodexDesktopHistoryWindowsE2E(t *testing.T) {
	if os.Getenv("FAST_SPIDER_CODEX_DESKTOP_HISTORY_E2E") != "1" {
		t.Skip("set FAST_SPIDER_CODEX_DESKTOP_HISTORY_E2E=1 with an explicit session")
	}
	sessionID := strings.TrimSpace(os.Getenv("FAST_SPIDER_CODEX_DESKTOP_SEND_SESSION_ID"))
	if sessionID == "" {
		t.Fatal("FAST_SPIDER_CODEX_DESKTOP_SEND_SESSION_ID is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := dialCodexDesktopIPC()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if setter, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = setter.SetDeadline(time.Now().Add(15 * time.Second))
	}
	writeMu := &sync.Mutex{}
	clientID, err := initializeCodexDesktopIPCClient(conn, writeMu)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := requestCodexDesktopIPC(ctx, conn, writeMu, clientID, "", "thread-owner-discovery", 1, map[string]any{
		"hostId": codexDesktopIPCHostID, "conversationId": sessionID,
	}, 5000)
	if err != nil {
		t.Fatal(err)
	}
	following := map[string]any{"hostId": codexDesktopIPCHostID, "conversationId": sessionID, "following": true}
	if err := writeCodexDesktopIPCBroadcast(conn, writeMu, clientID, "thread-stream-following-changed", 1, following); err != nil {
		t.Fatal(err)
	}
	defer func() {
		following["following"] = false
		_ = writeCodexDesktopIPCBroadcast(conn, writeMu, clientID, "thread-stream-following-changed", 1, following)
	}()
	var broadcasts []codexDesktopIPCMessage
	history, err := requestCodexDesktopIPCWithBroadcasts(ctx, conn, writeMu, clientID, owner.HandledByClientID, "thread-follower-load-complete-history", 1, map[string]any{
		"hostId": codexDesktopIPCHostID, "conversationId": sessionID,
	}, 10000, func(message codexDesktopIPCMessage) {
		broadcasts = append(broadcasts, message)
	})
	if err != nil {
		t.Fatal(err)
	}
	states := make([]any, 0, len(broadcasts))
	for _, broadcast := range broadcasts {
		change := mapValueMap(broadcast.Params, "change")
		conversationState := mapValueMap(change, "conversationState")
		states = append(states, map[string]any{
			"method":              broadcast.Method,
			"conversationId":      mapString(conversationState, "id"),
			"threadRuntimeStatus": conversationState["threadRuntimeStatus"],
		})
	}
	raw, _ := json.Marshal(map[string]any{"historyResult": history.Result, "states": states})
	t.Log(string(raw))
}

// TestCodexDesktopRequesterBusyWindowsE2E verifies that a second external send
// targets the same Desktop owner and is rejected instead of creating a
// substitute thread or falling back to app-server.
func TestCodexDesktopRequesterBusyWindowsE2E(t *testing.T) {
	if os.Getenv("FAST_SPIDER_CODEX_DESKTOP_BUSY_E2E") != "1" {
		t.Skip("set FAST_SPIDER_CODEX_DESKTOP_BUSY_E2E=1 with an explicit session and long-running prompt")
	}
	sessionID := strings.TrimSpace(os.Getenv("FAST_SPIDER_CODEX_DESKTOP_SEND_SESSION_ID"))
	prompt := strings.TrimSpace(os.Getenv("FAST_SPIDER_CODEX_DESKTOP_SEND_PROMPT"))
	if sessionID == "" || prompt == "" {
		t.Fatal("FAST_SPIDER_CODEX_DESKTOP_SEND_SESSION_ID and FAST_SPIDER_CODEX_DESKTOP_SEND_PROMPT are required")
	}
	adapter := NewCodexAdapter(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	activeTurnID := "existing-active-turn"
	if os.Getenv("FAST_SPIDER_CODEX_DESKTOP_BUSY_EXISTING") != "1" {
		first, err := adapter.startDesktopOwnedTurn(ctx, sessionID, []map[string]any{{"type": "text", "text": prompt}}, codexTurnOptions{
			WorkingDirectory: strings.TrimSpace(os.Getenv("FAST_SPIDER_CODEX_DESKTOP_SEND_CWD")),
		})
		if err != nil {
			t.Fatal(err)
		}
		activeTurnID = mapNestedString(first, "turn", "id")
	}
	_, err := adapter.startDesktopOwnedTurn(ctx, sessionID, []map[string]any{{"type": "text", "text": "FAST_SPIDER_BUSY_PROBE_MUST_NOT_START"}}, codexTurnOptions{})
	if !errors.Is(err, node.ErrAgentSessionBusy) {
		t.Fatalf("second send error=%v", err)
	}
	t.Logf("sessionId=%s activeTurnId=%s busy=true", sessionID, activeTurnID)
}
