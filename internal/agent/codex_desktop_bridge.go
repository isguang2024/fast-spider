package agent

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	codexDesktopIPCHostID       = "local"
	codexDesktopIPCFrameLimit   = 8 << 20
	codexDesktopIPCInitTimeout  = 5 * time.Second
	codexDesktopIPCReconnectMin = time.Second
	codexDesktopIPCReconnectMax = 10 * time.Second
)

var codexDesktopFollowerVersions = map[string]int{
	"thread-owner-discovery":                                 1,
	"thread-follower-start-turn":                             2,
	"thread-follower-compact-thread":                         1,
	"thread-follower-steer-turn":                             1,
	"thread-follower-interrupt-turn":                         4,
	"thread-follower-command-approval-decision":              1,
	"thread-follower-file-approval-decision":                 1,
	"thread-follower-submit-user-input":                      1,
	"thread-follower-submit-mcp-server-elicitation-response": 1,
}

type codexDesktopIPCMessage struct {
	Type              string                  `json:"type"`
	RequestID         string                  `json:"requestId,omitempty"`
	SourceClientID    string                  `json:"sourceClientId,omitempty"`
	TargetClientID    string                  `json:"targetClientId,omitempty"`
	Version           int                     `json:"version,omitempty"`
	Method            string                  `json:"method,omitempty"`
	Params            map[string]any          `json:"params,omitempty"`
	TimeoutMS         int                     `json:"timeoutMs,omitempty"`
	Request           *codexDesktopIPCMessage `json:"request,omitempty"`
	Response          map[string]any          `json:"response,omitempty"`
	ResultType        string                  `json:"resultType,omitempty"`
	HandledByClientID string                  `json:"handledByClientId,omitempty"`
	Result            map[string]any          `json:"result,omitempty"`
	Error             string                  `json:"error,omitempty"`
}

type codexDesktopBridge struct {
	adapter *CodexAdapter
	logger  *slog.Logger
	dial    func() (io.ReadWriteCloser, error)

	writeMu sync.Mutex
	connMu  sync.Mutex
	conn    io.ReadWriteCloser
	client  string
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func codexDesktopBridgeConfigured() (bool, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(codexDesktopBridgeEnv))) {
	case "":
		return runtime.GOOS == "windows", nil
	case "0", "false", "no":
		return false, nil
	case "1", "true", "yes":
		if runtime.GOOS != "windows" {
			return false, fmt.Errorf("%s is only supported on Windows", codexDesktopBridgeEnv)
		}
		return true, nil
	default:
		return false, fmt.Errorf("%s must be 1/true or 0/false", codexDesktopBridgeEnv)
	}
}

func (a *CodexAdapter) desktopBridgeConfiguration() (enabled, defaultEnabled bool, source string, err error) {
	a.mu.Lock()
	override := a.desktopBridgeEnabled
	a.mu.Unlock()
	if override != nil {
		return *override, false, "local_client", nil
	}
	enabled, err = codexDesktopBridgeConfigured()
	if strings.TrimSpace(os.Getenv(codexDesktopBridgeEnv)) != "" {
		return enabled, runtime.GOOS == "windows", "environment", err
	}
	return enabled, runtime.GOOS == "windows", "platform_default", err
}

func (a *CodexAdapter) desktopBridgeMetadata() map[string]any {
	enabled, defaultEnabled, configurationSource, configErr := a.desktopBridgeConfiguration()
	outboundTurnRouting := "app_server_only"
	if runtime.GOOS == "windows" {
		outboundTurnRouting = "desktop_owner_then_app_server"
	}
	out := map[string]any{
		"enabled":                     enabled,
		"defaultEnabled":              defaultEnabled,
		"configurationSource":         configurationSource,
		"ownership":                   "loaded_local_threads_only",
		"automaticRelease":            "thread_unsubscribe_on_terminal_or_archive",
		"controlRouting":              "supported",
		"outboundTurnRouting":         outboundTurnRouting,
		"nativeConversationStreaming": "unsupported",
		"stability":                   "private_codex_desktop_ipc",
	}
	if configErr != nil {
		out["state"] = "configuration_invalid"
		out["reason"] = "unsupported_platform_or_invalid_value"
		return out
	}
	if !enabled {
		out["state"] = "disabled"
		return out
	}
	a.mu.Lock()
	bridge := a.desktopBridge
	a.mu.Unlock()
	if bridge == nil {
		out["state"] = "waiting_for_harness"
		out["connected"] = false
		return out
	}
	bridge.connMu.Lock()
	connected := bridge.client != ""
	bridge.connMu.Unlock()
	out["connected"] = connected
	if connected {
		out["state"] = "connected"
	} else {
		out["state"] = "connecting"
	}
	return out
}

func (a *CodexAdapter) ensureDesktopBridge() error {
	enabled, _, _, err := a.desktopBridgeConfiguration()
	if err != nil || !enabled {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.desktopBridge != nil {
		return nil
	}
	bridge := &codexDesktopBridge{
		adapter: a,
		logger:  a.logger,
		dial:    dialCodexDesktopIPC,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	a.desktopBridge = bridge
	go bridge.run()
	return nil
}

func (b *codexDesktopBridge) Close() {
	b.once.Do(func() {
		close(b.stop)
		b.connMu.Lock()
		if b.conn != nil {
			_ = b.conn.Close()
		}
		b.connMu.Unlock()
		<-b.done
	})
}

func (b *codexDesktopBridge) run() {
	defer close(b.done)
	delay := codexDesktopIPCReconnectMin
	for {
		select {
		case <-b.stop:
			return
		default:
		}
		conn, err := b.dial()
		if err == nil {
			b.connMu.Lock()
			b.conn = conn
			b.connMu.Unlock()
			err = b.serveConnection(conn)
			b.connMu.Lock()
			if b.conn == conn {
				b.conn = nil
				b.client = ""
			}
			b.connMu.Unlock()
			_ = conn.Close()
			delay = codexDesktopIPCReconnectMin
		}
		select {
		case <-b.stop:
			return
		case <-time.After(delay):
		}
		if delay < codexDesktopIPCReconnectMax {
			delay *= 2
			if delay > codexDesktopIPCReconnectMax {
				delay = codexDesktopIPCReconnectMax
			}
		}
		if err != nil {
			b.logger.Debug("Codex Desktop bridge waiting for IPC", "error", err)
		}
	}
}

func (b *codexDesktopBridge) serveConnection(conn io.ReadWriteCloser) error {
	if deadline, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = deadline.SetDeadline(time.Now().Add(codexDesktopIPCInitTimeout))
		defer deadline.SetDeadline(time.Time{})
	}
	requestID := fmt.Sprintf("fast-spider-%d", time.Now().UnixNano())
	if err := writeCodexDesktopIPCFrame(conn, &b.writeMu, map[string]any{
		"type":           "request",
		"requestId":      requestID,
		"sourceClientId": "initializing-client",
		"version":        0,
		"method":         "initialize",
		"params":         map[string]any{"clientType": "desktop"},
		"timeoutMs":      int(codexDesktopIPCInitTimeout / time.Millisecond),
	}); err != nil {
		return err
	}
	message, err := readCodexDesktopIPCFrame(conn)
	if err != nil {
		return err
	}
	if message.Type != "response" || message.RequestID != requestID || message.ResultType != "success" {
		return fmt.Errorf("Codex Desktop IPC initialize failed: %s", message.Error)
	}
	clientID := mapString(message.Result, "clientId")
	if clientID == "" {
		return errors.New("Codex Desktop IPC initialize returned no clientId")
	}
	b.connMu.Lock()
	b.client = clientID
	b.connMu.Unlock()
	if deadline, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = deadline.SetDeadline(time.Time{})
	}
	for {
		message, err := readCodexDesktopIPCFrame(conn)
		if err != nil {
			return err
		}
		b.logger.Debug("Codex Desktop bridge received IPC message", "type", message.Type, "method", message.Method, "request_id", message.RequestID)
		switch message.Type {
		case "client-discovery-request":
			b.handleDiscovery(conn, message)
		case "request":
			go b.handleRequest(conn, message, clientID)
		}
	}
}

func (b *codexDesktopBridge) handleDiscovery(conn io.Writer, message codexDesktopIPCMessage) {
	canHandle := message.Request != nil && b.canHandle(message.Request.Method, message.Request.Version, message.Request.Params)
	method := ""
	if message.Request != nil {
		method = message.Request.Method
	}
	b.logger.Debug("Codex Desktop bridge answered client discovery", "method", method, "canHandle", canHandle)
	_ = writeCodexDesktopIPCFrame(conn, &b.writeMu, map[string]any{
		"type":      "client-discovery-response",
		"requestId": message.RequestID,
		"response":  map[string]any{"canHandle": canHandle},
	})
}

func (b *codexDesktopBridge) handleRequest(conn io.Writer, message codexDesktopIPCMessage, clientID string) {
	if !b.canHandle(message.Method, message.Version, message.Params) {
		b.writeError(conn, message.RequestID, "no-handler-for-request")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, err := b.routeFollowerRequest(ctx, message.Method, message.Params)
	if err != nil {
		b.writeError(conn, message.RequestID, err.Error())
		return
	}
	b.logger.Debug("Codex Desktop bridge handled follower request", "method", message.Method)
	if err := writeCodexDesktopIPCFrame(conn, &b.writeMu, map[string]any{
		"type":              "response",
		"requestId":         message.RequestID,
		"resultType":        "success",
		"method":            message.Method,
		"handledByClientId": clientID,
		"result":            result,
	}); err != nil {
		b.logger.Debug("Codex Desktop bridge failed to answer follower request", "method", message.Method, "error", err)
	} else {
		b.logger.Debug("Codex Desktop bridge answered follower request", "method", message.Method)
	}
}

func (b *codexDesktopBridge) writeError(conn io.Writer, requestID, message string) {
	_ = writeCodexDesktopIPCFrame(conn, &b.writeMu, map[string]any{
		"type":       "response",
		"requestId":  requestID,
		"resultType": "error",
		"error":      message,
	})
}

func (b *codexDesktopBridge) canHandle(method string, version int, params map[string]any) bool {
	wantVersion, ok := codexDesktopFollowerVersions[method]
	if !ok {
		return false
	}
	if method == "thread-follower-interrupt-turn" && version == 3 && mapString(params, "expectedTurnId") == "" {
		// Desktop 26.820 keeps version 3 compatibility for requests without an expected turn.
		wantVersion = 3
	}
	if version != wantVersion {
		return false
	}
	if hostID := mapString(params, "hostId"); hostID != "" && hostID != codexDesktopIPCHostID {
		return false
	}
	return b.ownsThread(mapString(params, "conversationId"))
}

func (b *codexDesktopBridge) ownsThread(sessionID string) bool {
	if strings.TrimSpace(sessionID) == "" {
		return false
	}
	b.adapter.mu.Lock()
	_, loaded := b.adapter.loaded[sessionID]
	closed := b.adapter.closed
	b.adapter.mu.Unlock()
	return loaded && !closed
}

func (b *codexDesktopBridge) routeFollowerRequest(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	sessionID := mapString(params, "conversationId")
	switch method {
	case "thread-owner-discovery":
		return map[string]any{}, nil
	case "thread-follower-start-turn":
		turnStart, _ := params["turnStart"].(map[string]any)
		turnRequest := mapValueMap(turnStart, "request")
		if requestThreadID := mapString(turnRequest, "threadId"); requestThreadID != "" && requestThreadID != sessionID {
			return nil, errors.New("Codex Desktop follower turn targets a different thread")
		}
		inputs, err := codexDesktopInputs(turnRequest["input"])
		if err != nil {
			return nil, err
		}
		result, err := b.adapter.StartTurnWithOptions(ctx, sessionID, inputs, codexTurnOptions{
			WorkingDirectory: mapString(turnRequest, "cwd"),
			Model:            mapString(turnRequest, "model"),
			Effort:           mapString(turnRequest, "effort"),
			Summary:          mapString(turnRequest, "summary"),
			Personality:      mapString(turnRequest, "personality"),
			ServiceTier:      mapString(turnRequest, "serviceTier"),
			OutputSchema:     mapValueMap(turnRequest, "outputSchema"),
		})
		return map[string]any{"method": method, "result": map[string]any{"result": result}}, err
	case "thread-follower-steer-turn":
		inputs, err := codexDesktopInputs(params["input"])
		if err != nil {
			return nil, err
		}
		result, err := b.adapter.SteerTurn(ctx, sessionID, b.adapter.ActiveTurn(sessionID), inputs)
		return map[string]any{"method": method, "result": map[string]any{"result": result}}, err
	case "thread-follower-interrupt-turn":
		turnID := mapString(params, "expectedTurnId")
		if turnID == "" {
			turnID = b.adapter.ActiveTurn(sessionID)
		}
		if err := b.adapter.InterruptTurn(ctx, sessionID, turnID); err != nil {
			return nil, err
		}
		return map[string]any{"method": method, "result": map[string]any{"ok": true, "interruptedTurnId": turnID}}, nil
	case "thread-follower-compact-thread":
		if err := b.adapter.CompactThread(ctx, sessionID); err != nil {
			return nil, err
		}
		return map[string]any{"method": method, "result": map[string]any{"ok": true}}, nil
	case "thread-follower-command-approval-decision", "thread-follower-file-approval-decision":
		decision := desktopDecision(params["decision"])
		_, err := b.adapter.RespondPendingRequest(ctx, sessionID, anyRequestIDString(params["requestId"]), agentControlParams{Decision: decision})
		if err != nil {
			return nil, err
		}
		return map[string]any{"method": method, "result": map[string]any{"ok": true}}, nil
	case "thread-follower-submit-user-input":
		answers := desktopUserInputAnswers(params["response"])
		_, err := b.adapter.RespondPendingRequest(ctx, sessionID, anyRequestIDString(params["requestId"]), agentControlParams{Answers: answers})
		if err != nil {
			return nil, err
		}
		return map[string]any{"method": method, "result": map[string]any{"ok": true}}, nil
	case "thread-follower-submit-mcp-server-elicitation-response":
		response, _ := params["response"].(map[string]any)
		_, err := b.adapter.RespondPendingRequest(ctx, sessionID, anyRequestIDString(params["requestId"]), agentControlParams{
			Decision:        desktopDecision(response["action"]),
			ResponseContent: mapValueMap(response, "content"),
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"method": method, "result": map[string]any{"ok": true}}, nil
	default:
		return nil, fmt.Errorf("unsupported Codex Desktop follower method: %s", method)
	}
}

func codexDesktopInputs(raw any) ([]map[string]any, error) {
	values, ok := raw.([]any)
	if !ok || len(values) == 0 {
		return nil, errors.New("Codex Desktop follower turn requires input")
	}
	inputs := make([]map[string]any, 0, len(values))
	for _, value := range values {
		input, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("Codex Desktop follower input is invalid")
		}
		inputs = append(inputs, input)
	}
	return inputs, nil
}

func desktopDecision(raw any) string {
	if decision, ok := raw.(string); ok {
		return decision
	}
	if decision, ok := raw.(map[string]any); ok {
		return mapString(decision, "decision")
	}
	return ""
}

func desktopUserInputAnswers(raw any) map[string][]string {
	response, _ := raw.(map[string]any)
	rawAnswers, _ := response["answers"].(map[string]any)
	answers := make(map[string][]string, len(rawAnswers))
	for id, rawAnswer := range rawAnswers {
		entry, _ := rawAnswer.(map[string]any)
		values, _ := entry["answers"].([]any)
		for _, value := range values {
			if text, ok := value.(string); ok {
				answers[id] = append(answers[id], text)
			}
		}
	}
	return answers
}

func mapValueMap(values map[string]any, key string) map[string]any {
	value, _ := values[key].(map[string]any)
	return value
}

func readCodexDesktopIPCFrame(reader io.Reader) (codexDesktopIPCMessage, error) {
	var sizeBytes [4]byte
	if _, err := io.ReadFull(reader, sizeBytes[:]); err != nil {
		return codexDesktopIPCMessage{}, err
	}
	size := binary.LittleEndian.Uint32(sizeBytes[:])
	if size == 0 || size > codexDesktopIPCFrameLimit {
		return codexDesktopIPCMessage{}, fmt.Errorf("invalid Codex Desktop IPC frame size: %d", size)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return codexDesktopIPCMessage{}, err
	}
	var message codexDesktopIPCMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return codexDesktopIPCMessage{}, fmt.Errorf("decode Codex Desktop IPC frame: %w", err)
	}
	return message, nil
}

func writeCodexDesktopIPCFrame(writer io.Writer, mu *sync.Mutex, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(payload) == 0 || len(payload) > codexDesktopIPCFrameLimit {
		return fmt.Errorf("invalid Codex Desktop IPC frame size: %d", len(payload))
	}
	frame := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	mu.Lock()
	defer mu.Unlock()
	for len(frame) > 0 {
		written, writeErr := writer.Write(frame)
		if written > 0 {
			frame = frame[written:]
		}
		if writeErr != nil {
			return writeErr
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
