package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
)

var errCodexDesktopOwnerUnavailable = errors.New("Codex Desktop owner is unavailable")

// startDesktopCallbackTurn asks the existing Desktop writer to deliver a callback.
// It never claims a thread or starts another app-server.
func startDesktopCallbackTurn(ctx context.Context, sessionID string, inputs []map[string]any, dial func() (io.ReadWriteCloser, error)) (map[string]any, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("Codex Desktop turn requires input")
	}
	conn, err := dial()
	if err != nil {
		return nil, fmt.Errorf("%w: connect IPC: %v", errCodexDesktopOwnerUnavailable, err)
	}
	defer conn.Close()
	stopCancellation := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancellation()
	deadline := time.Now().Add(70 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if setter, supported := conn.(interface{ SetDeadline(time.Time) error }); supported {
		_ = setter.SetDeadline(deadline)
	}
	writeMu := &sync.Mutex{}
	clientID, err := initializeCodexDesktopIPCClient(conn, writeMu)
	if err != nil {
		return nil, fmt.Errorf("%w: initialize IPC requester: %v", errCodexDesktopOwnerUnavailable, err)
	}
	owner, err := requestCodexDesktopIPC(ctx, conn, writeMu, clientID, "", "thread-owner-discovery", 1, map[string]any{
		"hostId":         codexDesktopIPCHostID,
		"conversationId": sessionID,
	}, 5000)
	if err != nil {
		return nil, fmt.Errorf("%w: discover thread owner: %v", errCodexDesktopOwnerUnavailable, err)
	}
	ownerClientID := strings.TrimSpace(owner.HandledByClientID)
	supportsUntrusted, _ := owner.Result["supportsUntrustedAppInput"].(bool)
	if ownerClientID == "" || !supportsUntrusted {
		return nil, fmt.Errorf("%w: Desktop owner does not accept external turn input", errCodexDesktopOwnerUnavailable)
	}
	if err := requireCodexDesktopThreadIdle(ctx, conn, writeMu, clientID, ownerClientID, sessionID); err != nil {
		return nil, err
	}
	request := map[string]any{
		"threadId": sessionID,
		"input":    inputs,
	}
	response, err := requestCodexDesktopIPC(ctx, conn, writeMu, clientID, ownerClientID, "thread-follower-start-turn", 2, map[string]any{
		"hostId":         codexDesktopIPCHostID,
		"conversationId": sessionID,
		"turnStart":      map[string]any{"request": request},
	}, 60000)
	if err != nil {
		if codexDesktopIPCSessionBusy(err) {
			return nil, node.ErrAgentSessionBusy
		}
		return nil, err
	}
	result := mapValueMap(response.Result, "result")
	if result == nil {
		result = response.Result
	}
	if mapNestedString(result, "turn", "id") == "" {
		return nil, errors.New("Codex Desktop owner accepted the turn without returning a turn ID")
	}
	return result, nil
}

func requireCodexDesktopThreadIdle(ctx context.Context, conn io.ReadWriteCloser, writeMu *sync.Mutex, clientID, ownerClientID, sessionID string) error {
	following := map[string]any{
		"hostId":         codexDesktopIPCHostID,
		"conversationId": sessionID,
		"following":      true,
	}
	if err := writeCodexDesktopIPCBroadcast(conn, writeMu, clientID, "thread-stream-following-changed", 1, following); err != nil {
		return fmt.Errorf("observe Codex Desktop thread state: %w", err)
	}
	defer func() {
		following["following"] = false
		_ = writeCodexDesktopIPCBroadcast(conn, writeMu, clientID, "thread-stream-following-changed", 1, following)
	}()

	runtimeType := ""
	_, err := requestCodexDesktopIPCWithBroadcasts(ctx, conn, writeMu, clientID, ownerClientID, "thread-follower-load-complete-history", 1, map[string]any{
		"hostId":         codexDesktopIPCHostID,
		"conversationId": sessionID,
	}, 10000, func(message codexDesktopIPCMessage) {
		if message.Method != "thread-stream-state-changed" || message.Version != 11 {
			return
		}
		conversationState := mapValueMap(mapValueMap(message.Params, "change"), "conversationState")
		if mapString(conversationState, "id") != sessionID {
			return
		}
		runtimeType = strings.ToLower(strings.TrimSpace(mapString(mapValueMap(conversationState, "threadRuntimeStatus"), "type")))
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no-client-found") {
			return fmt.Errorf("%w: Desktop owner became unavailable while reading thread state", errCodexDesktopOwnerUnavailable)
		}
		return fmt.Errorf("read Codex Desktop thread state: %w", err)
	}
	if runtimeType == "" {
		return errors.New("Codex Desktop owner returned no thread runtime status")
	}
	if runtimeType != "idle" {
		return node.ErrAgentSessionBusy
	}
	return nil
}

func initializeCodexDesktopIPCClient(conn io.ReadWriteCloser, writeMu *sync.Mutex) (string, error) {
	requestID := fmt.Sprintf("fast-spider-requester-init-%d", time.Now().UnixNano())
	if err := writeCodexDesktopIPCFrame(conn, writeMu, map[string]any{
		"type":           "request",
		"requestId":      requestID,
		"sourceClientId": "initializing-client",
		"version":        0,
		"method":         "initialize",
		"params":         map[string]any{"clientType": "desktop"},
		"timeoutMs":      5000,
	}); err != nil {
		return "", err
	}
	response, err := waitCodexDesktopIPCResponse(conn, writeMu, requestID)
	if err != nil {
		return "", err
	}
	clientID := strings.TrimSpace(mapString(response.Result, "clientId"))
	if clientID == "" {
		return "", errors.New("Codex Desktop IPC initialize returned no clientId")
	}
	return clientID, nil
}

func requestCodexDesktopIPC(ctx context.Context, conn io.ReadWriteCloser, writeMu *sync.Mutex, clientID, targetClientID, method string, version int, params map[string]any, maxTimeoutMS int) (codexDesktopIPCMessage, error) {
	return requestCodexDesktopIPCWithBroadcasts(ctx, conn, writeMu, clientID, targetClientID, method, version, params, maxTimeoutMS, nil)
}

func requestCodexDesktopIPCWithBroadcasts(ctx context.Context, conn io.ReadWriteCloser, writeMu *sync.Mutex, clientID, targetClientID, method string, version int, params map[string]any, maxTimeoutMS int, onBroadcast func(codexDesktopIPCMessage)) (codexDesktopIPCMessage, error) {
	requestID := fmt.Sprintf("fast-spider-requester-%d", time.Now().UnixNano())
	timeoutMS := codexDesktopIPCRequestTimeoutMS(ctx, maxTimeoutMS)
	request := map[string]any{
		"type":           "request",
		"requestId":      requestID,
		"sourceClientId": clientID,
		"version":        version,
		"method":         method,
		"params":         params,
		"timeoutMs":      timeoutMS,
	}
	if strings.TrimSpace(targetClientID) != "" {
		request["targetClientId"] = targetClientID
	}
	if err := writeCodexDesktopIPCFrame(conn, writeMu, request); err != nil {
		return codexDesktopIPCMessage{}, err
	}
	return waitCodexDesktopIPCResponseWithBroadcasts(conn, writeMu, requestID, onBroadcast)
}

func waitCodexDesktopIPCResponse(conn io.ReadWriteCloser, writeMu *sync.Mutex, requestID string) (codexDesktopIPCMessage, error) {
	return waitCodexDesktopIPCResponseWithBroadcasts(conn, writeMu, requestID, nil)
}

func waitCodexDesktopIPCResponseWithBroadcasts(conn io.ReadWriteCloser, writeMu *sync.Mutex, requestID string, onBroadcast func(codexDesktopIPCMessage)) (codexDesktopIPCMessage, error) {
	for {
		message, err := readCodexDesktopIPCFrame(conn)
		if err != nil {
			return codexDesktopIPCMessage{}, err
		}
		switch message.Type {
		case "broadcast":
			if onBroadcast != nil {
				onBroadcast(message)
			}
		case "client-discovery-request":
			if err := writeCodexDesktopIPCFrame(conn, writeMu, map[string]any{
				"type":      "client-discovery-response",
				"requestId": message.RequestID,
				"response":  map[string]any{"canHandle": false},
			}); err != nil {
				return codexDesktopIPCMessage{}, err
			}
		case "request":
			if err := writeCodexDesktopIPCFrame(conn, writeMu, map[string]any{
				"type":       "response",
				"requestId":  message.RequestID,
				"resultType": "error",
				"method":     message.Method,
				"error":      "no-handler-for-request",
			}); err != nil {
				return codexDesktopIPCMessage{}, err
			}
		case "response":
			if message.RequestID != requestID {
				continue
			}
			if message.ResultType != "success" {
				return codexDesktopIPCMessage{}, fmt.Errorf("Codex Desktop IPC %s failed: %s", message.Method, strings.TrimSpace(message.Error))
			}
			return message, nil
		}
	}
}

func writeCodexDesktopIPCBroadcast(conn io.Writer, writeMu *sync.Mutex, clientID, method string, version int, params map[string]any) error {
	return writeCodexDesktopIPCFrame(conn, writeMu, map[string]any{
		"type":           "broadcast",
		"sourceClientId": clientID,
		"version":        version,
		"method":         method,
		"params":         params,
	})
}

func codexDesktopIPCRequestTimeoutMS(ctx context.Context, maximum int) int {
	if maximum <= 0 {
		maximum = 60000
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return maximum
	}
	remaining := int(time.Until(deadline) / time.Millisecond)
	if remaining < 1 {
		return 1
	}
	if remaining < maximum {
		return remaining
	}
	return maximum
}

func codexDesktopIPCSessionBusy(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "agent_session_busy") || strings.Contains(message, "active turn") || strings.Contains(message, "active run")
}
