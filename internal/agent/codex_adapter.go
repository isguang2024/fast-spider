package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
)

const (
	codexRPCLineLimit = 8 << 20
	codexEventLimit   = 1000
)

type codexRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type codexRPCMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *codexRPCError  `json:"error,omitempty"`
}

type codexPending struct {
	ch chan codexRPCMessage
}

type AgentEvent struct {
	Sequence  int64  `json:"sequence"`
	Type      string `json:"type"`
	SessionID string `json:"sessionId,omitempty"`
	TurnID    string `json:"turnId,omitempty"`
	Text      string `json:"text,omitempty"`
	State     string `json:"state,omitempty"`
	Timestamp string `json:"timestamp"`
}

type CodexAdapter struct {
	logger *slog.Logger

	startMu     sync.Mutex
	mu          sync.Mutex
	rpcWriteMu  sync.Mutex
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	pending     map[int64]codexPending
	nextID      int64
	closed      bool
	processDone chan struct{}

	eventMu     sync.Mutex
	events      []AgentEvent
	nextEvent   int64
	eventNotify chan struct{}
	activeTurns map[string]string
}

func NewCodexAdapter(logger *slog.Logger) *CodexAdapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &CodexAdapter{
		logger:      logger,
		pending:     make(map[int64]codexPending),
		eventNotify: make(chan struct{}),
		activeTurns: make(map[string]string),
	}
}

func (a *CodexAdapter) Availability(ctx context.Context) (string, error) {
	path, err := exec.LookPath("codex")
	if err != nil {
		return "", fmt.Errorf("%w: codex executable not found", node.ErrAgentProviderUnavailable)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, path, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("%w: codex --version failed", node.ErrAgentProviderUnavailable)
	}
	version := strings.TrimSpace(string(output))
	if version == "" {
		return "", fmt.Errorf("%w: codex version is empty", node.ErrAgentProviderUnavailable)
	}
	return version, nil
}

func (a *CodexAdapter) ensureStarted(ctx context.Context) error {
	a.startMu.Lock()
	defer a.startMu.Unlock()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return fmt.Errorf("%w: adapter is closed", node.ErrAgentProviderUnavailable)
	}
	if a.cmd != nil && a.cmd.Process != nil && a.cmd.ProcessState == nil {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()

	if _, err := a.Availability(ctx); err != nil {
		return err
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		return err
	}
	cmd := exec.Command(path, "app-server", "--stdio")
	if home, homeErr := os.UserHomeDir(); homeErr == nil && home != "" {
		cmd.Dir = home
	}
	cmd.Env = append(os.Environ(), "LOG_FORMAT=json", "RUST_LOG=warn")
	node.ConfigureProcessTree(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Codex app-server: %w", err)
	}

	a.mu.Lock()
	a.cmd = cmd
	a.stdin = stdin
	a.processDone = make(chan struct{})
	a.mu.Unlock()
	go a.readLoop(stdout)
	go a.stderrLoop(stderr)
	go a.waitLoop(cmd)

	initCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := a.request(initCtx, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "Codex Desktop",
			"title":   "Fast Spider",
			"version": "0.1.0",
		},
		"capabilities": map[string]any{
			"experimentalApi":                true,
			"mcpServerOpenaiFormElicitation": false,
		},
	}); err != nil {
		_ = a.stopProcess(context.Background(), cmd)
		return fmt.Errorf("initialize Codex app-server: %w", err)
	}
	if err := a.notify("initialized", nil); err != nil {
		_ = a.stopProcess(context.Background(), cmd)
		return err
	}
	return nil
}

func (a *CodexAdapter) Close(ctx context.Context) error {
	a.mu.Lock()
	a.closed = true
	cmd := a.cmd
	a.mu.Unlock()
	if cmd == nil {
		return nil
	}
	return a.stopProcess(ctx, cmd)
}

func (a *CodexAdapter) stopProcess(ctx context.Context, cmd *exec.Cmd) error {
	a.mu.Lock()
	stdin := a.stdin
	done := a.processDone
	a.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd.Process == nil || cmd.ProcessState != nil || done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		_ = node.KillProcessTree(cmd)
		<-done
		return ctx.Err()
	case <-time.After(time.Second):
		_ = node.KillProcessTree(cmd)
		<-done
		return nil
	}
}

func (a *CodexAdapter) request(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	if method != "initialize" {
		if err := a.ensureStarted(ctx); err != nil {
			return nil, err
		}
	}
	a.mu.Lock()
	if a.stdin == nil || a.cmd == nil {
		a.mu.Unlock()
		return nil, node.ErrAgentProviderUnavailable
	}
	a.nextID++
	id := a.nextID
	pending := codexPending{ch: make(chan codexRPCMessage, 1)}
	a.pending[id] = pending
	stdin := a.stdin
	a.mu.Unlock()

	message := map[string]any{"id": id, "method": method}
	if params != nil {
		message["params"] = params
	}
	if err := a.writeLine(stdin, message); err != nil {
		a.removePending(id)
		return nil, err
	}
	select {
	case <-ctx.Done():
		a.removePending(id)
		return nil, ctx.Err()
	case response := <-pending.ch:
		if response.Error != nil {
			return nil, fmt.Errorf("Codex %s: %s", method, response.Error.Message)
		}
		var result map[string]any
		if len(response.Result) == 0 || string(response.Result) == "null" {
			return map[string]any{}, nil
		}
		if err := json.Unmarshal(response.Result, &result); err != nil {
			return nil, fmt.Errorf("decode Codex %s result: %w", method, err)
		}
		return result, nil
	}
}

func (a *CodexAdapter) notify(method string, params map[string]any) error {
	a.mu.Lock()
	stdin := a.stdin
	a.mu.Unlock()
	if stdin == nil {
		return node.ErrAgentProviderUnavailable
	}
	message := map[string]any{"method": method}
	if params != nil {
		message["params"] = params
	}
	return a.writeLine(stdin, message)
}

func (a *CodexAdapter) writeLine(writer io.Writer, value any) error {
	a.rpcWriteMu.Lock()
	defer a.rpcWriteMu.Unlock()
	return writeCodexJSONLine(writer, value)
}

func writeCodexJSONLine(writer io.Writer, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw) > codexRPCLineLimit {
		return fmt.Errorf("Codex RPC request exceeds limit")
	}
	raw = append(raw, '\n')
	_, err = writer.Write(raw)
	return err
}

func (a *CodexAdapter) removePending(id int64) {
	a.mu.Lock()
	delete(a.pending, id)
	a.mu.Unlock()
}

func (a *CodexAdapter) readLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), codexRPCLineLimit)
	for scanner.Scan() {
		var message codexRPCMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			a.logger.Debug("invalid Codex app-server message", "error", err)
			continue
		}
		if len(message.ID) > 0 && (len(message.Result) > 0 || message.Error != nil) {
			id, err := codexResponseID(message.ID)
			if err != nil {
				continue
			}
			a.mu.Lock()
			pending, ok := a.pending[id]
			if ok {
				delete(a.pending, id)
			}
			a.mu.Unlock()
			if ok {
				pending.ch <- message
			}
			continue
		}
		if len(message.ID) > 0 && message.Method != "" {
			a.replyUnsupportedServerRequest(message.ID, message.Method)
			continue
		}
		if message.Method != "" {
			a.handleNotification(message.Method, message.Params)
		}
	}
	if err := scanner.Err(); err != nil {
		a.logger.Debug("Codex app-server stdout ended", "error", err)
	}
}

func codexResponseID(raw json.RawMessage) (int64, error) {
	var number int64
	if err := json.Unmarshal(raw, &number); err == nil {
		return number, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strconv.ParseInt(text, 10, 64)
	}
	return 0, fmt.Errorf("invalid Codex RPC response ID")
}

func (a *CodexAdapter) replyUnsupportedServerRequest(id json.RawMessage, method string) {
	a.mu.Lock()
	stdin := a.stdin
	a.mu.Unlock()
	if stdin == nil {
		return
	}
	var rawID any
	if err := json.Unmarshal(id, &rawID); err != nil {
		return
	}
	_ = a.writeLine(stdin, map[string]any{
		"id": rawID,
		"error": map[string]any{
			"code":    -32601,
			"message": "Fast Spider bridge does not handle interactive Codex server request: " + method,
		},
	})
}

func (a *CodexAdapter) stderrLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 8*1024), 256*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			a.logger.Debug("Codex app-server", "message", boundedAgentText(line, 512))
		}
	}
}

func (a *CodexAdapter) waitLoop(cmd *exec.Cmd) {
	err := cmd.Wait()
	a.mu.Lock()
	done := a.processDone
	if a.cmd == cmd {
		a.cmd = nil
		a.stdin = nil
		a.processDone = nil
	}
	pending := a.pending
	a.pending = make(map[int64]codexPending)
	closed := a.closed
	a.mu.Unlock()
	if done != nil {
		close(done)
	}
	for _, item := range pending {
		item.ch <- codexRPCMessage{Error: &codexRPCError{Code: -1, Message: "Codex app-server exited"}}
	}
	if err != nil && !closed {
		a.logger.Debug("Codex app-server exited", "error", err)
	}
}

func (a *CodexAdapter) handleNotification(method string, raw json.RawMessage) {
	var params map[string]any
	_ = json.Unmarshal(raw, &params)
	sessionID := mapString(params, "threadId")
	turnID := mapString(params, "turnId")
	if turnID == "" {
		turnID = mapNestedString(params, "turn", "id")
	}
	event := AgentEvent{
		Type:      method,
		SessionID: sessionID,
		TurnID:    turnID,
		Timestamp: protocolTimestampNow(),
	}
	switch method {
	case "turn/started":
		event.Type = "turn.started"
		event.State = "running"
		if sessionID != "" && turnID != "" {
			a.eventMu.Lock()
			a.activeTurns[sessionID] = turnID
			a.eventMu.Unlock()
		}
	case "turn/completed":
		event.Type = "turn.completed"
		event.State = mapNestedString(params, "turn", "status")
		if event.State == "" {
			event.State = "completed"
		}
		if sessionID != "" {
			a.eventMu.Lock()
			delete(a.activeTurns, sessionID)
			a.eventMu.Unlock()
		}
	case "item/agentMessage/delta":
		event.Type = "assistant.delta"
		event.Text = boundedAgentText(mapString(params, "delta"), 4096)
	case "item/completed":
		itemType := mapNestedString(params, "item", "type")
		if itemType == "agentMessage" {
			event.Type = "assistant.message"
			event.Text = boundedAgentText(mapNestedString(params, "item", "text"), 16*1024)
		} else {
			event.Type = "item.completed"
			event.Text = boundedAgentText(itemType, 128)
		}
	case "thread/status/changed":
		event.Type = "session.status"
		event.State = mapNestedString(params, "status", "type")
	case "warning", "error":
		event.Type = method
		event.Text = boundedAgentText(firstNonEmptyString(
			mapString(params, "message"),
			mapNestedString(params, "error", "message"),
			mapNestedString(params, "error", "msg"),
		), 2048)
	}
	a.recordEvent(event)
}

func (a *CodexAdapter) recordEvent(event AgentEvent) {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	a.nextEvent++
	event.Sequence = a.nextEvent
	if event.Timestamp == "" {
		event.Timestamp = protocolTimestampNow()
	}
	a.events = append(a.events, event)
	if len(a.events) > codexEventLimit {
		a.events = append([]AgentEvent(nil), a.events[len(a.events)-codexEventLimit:]...)
	}
	close(a.eventNotify)
	a.eventNotify = make(chan struct{})
}

func (a *CodexAdapter) Watch(ctx context.Context, sessionID string, cursor int64, wait time.Duration) ([]AgentEvent, int64, int64, error) {
	if wait < 0 || wait > 15*time.Second {
		return nil, cursor, 0, fmt.Errorf("wait is outside the allowed range")
	}
	deadline := time.Now().Add(wait)
	for {
		a.eventMu.Lock()
		first := int64(0)
		if len(a.events) > 0 {
			first = a.events[0].Sequence
		}
		var events []AgentEvent
		for _, event := range a.events {
			if event.Sequence > cursor && (sessionID == "" || event.SessionID == sessionID) {
				events = append(events, event)
				if len(events) >= 200 {
					break
				}
			}
		}
		notify := a.eventNotify
		next := cursor
		if len(events) > 0 {
			next = events[len(events)-1].Sequence
		}
		a.eventMu.Unlock()
		truncatedBefore := int64(0)
		if first > 0 && cursor > 0 && cursor < first-1 {
			truncatedBefore = first
		}
		if len(events) > 0 || wait == 0 || !time.Now().Before(deadline) {
			return events, next, truncatedBefore, nil
		}
		remaining := time.Until(deadline)
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, cursor, 0, ctx.Err()
		case <-notify:
			timer.Stop()
		case <-timer.C:
			return nil, cursor, 0, nil
		}
	}
}

func (a *CodexAdapter) ActiveTurn(sessionID string) string {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	return a.activeTurns[sessionID]
}

func (a *CodexAdapter) ListModels(ctx context.Context) (map[string]any, error) {
	return a.request(ctx, "model/list", map[string]any{})
}

func (a *CodexAdapter) ListThreads(ctx context.Context, root string, limit int) (map[string]any, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	params := map[string]any{"limit": limit, "archived": false}
	if strings.TrimSpace(root) != "" {
		params["cwd"] = []string{root}
	}
	return a.request(ctx, "thread/list", params)
}

func (a *CodexAdapter) ReadThread(ctx context.Context, sessionID string) (map[string]any, error) {
	result, err := a.request(ctx, "thread/read", map[string]any{"threadId": sessionID, "includeTurns": true})
	if err == nil || !isCodexThreadNotMaterialized(err) {
		return result, err
	}
	// Codex can acknowledge turn/start before the persisted thread is ready for
	// includeTurns. A metadata-only read keeps immediate get/watch/result calls
	// stable and also supports a newly-created session that has no first turn yet.
	return a.request(ctx, "thread/read", map[string]any{"threadId": sessionID, "includeTurns": false})
}

func isCodexThreadNotMaterialized(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not materialized yet") && strings.Contains(message, "includeturns")
}

func (a *CodexAdapter) StartThread(ctx context.Context, workingDirectory, projectDirectory, model, thinking string) (map[string]any, error) {
	params := codexThreadStartParams(workingDirectory, projectDirectory, model, thinking)
	return a.request(ctx, "thread/start", params)
}

func codexThreadStartParams(workingDirectory, projectDirectory, model, thinking string) map[string]any {
	roots := make([]string, 0, 2)
	if strings.TrimSpace(projectDirectory) != "" {
		roots = append(roots, projectDirectory)
	}
	if strings.TrimSpace(workingDirectory) != "" && (len(roots) == 0 || !sameAgentPath(roots[0], workingDirectory)) {
		roots = append(roots, workingDirectory)
	}
	params := map[string]any{
		"cwd":                   workingDirectory,
		"runtimeWorkspaceRoots": roots,
		"approvalPolicy":        "never",
		"sandbox":               "workspace-write",
		"ephemeral":             false,
		"historyMode":           "legacy",
		"threadSource":          "user",
		"serviceName":           "fast_spider",
	}
	if model != "" {
		params["model"] = model
	}
	if thinking != "" {
		params["config"] = map[string]any{"model_reasoning_effort": thinking}
	}
	return params
}

func (a *CodexAdapter) StartTurn(ctx context.Context, sessionID, prompt, workingDirectory, model, thinking string) (map[string]any, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if active := a.ActiveTurn(sessionID); active != "" {
		return nil, node.ErrAgentSessionBusy
	}
	params := map[string]any{
		"threadId": sessionID,
		"input": []map[string]any{{
			"type":          "text",
			"text":          prompt,
			"text_elements": []any{},
		}},
	}
	if workingDirectory != "" {
		params["cwd"] = workingDirectory
	}
	if model != "" {
		params["model"] = model
	}
	if thinking != "" {
		params["effort"] = thinking
	}
	result, err := a.request(ctx, "turn/start", params)
	if err != nil {
		return nil, err
	}
	turnID := mapNestedString(result, "turn", "id")
	if turnID != "" {
		a.eventMu.Lock()
		a.activeTurns[sessionID] = turnID
		a.eventMu.Unlock()
	}
	return result, nil
}

func (a *CodexAdapter) InterruptTurn(ctx context.Context, sessionID, turnID string) error {
	if turnID == "" {
		turnID = a.ActiveTurn(sessionID)
	}
	if turnID == "" {
		return node.ErrAgentSessionNotFound
	}
	_, err := a.request(ctx, "turn/interrupt", map[string]any{"threadId": sessionID, "turnId": turnID})
	return err
}

func (a *CodexAdapter) RenameThread(ctx context.Context, sessionID, name string) error {
	_, err := a.request(ctx, "thread/name/set", map[string]any{"threadId": sessionID, "name": name})
	return err
}

func (a *CodexAdapter) ArchiveThread(ctx context.Context, sessionID string) error {
	_, err := a.request(ctx, "thread/archive", map[string]any{"threadId": sessionID})
	return err
}

func mapString(record map[string]any, key string) string {
	value, _ := record[key].(string)
	return value
}

func mapNestedString(record map[string]any, key, nested string) string {
	child, _ := record[key].(map[string]any)
	return mapString(child, nested)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func boundedAgentText(value string, limit int) string {
	value = strings.ReplaceAll(value, "\x00", "")
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

func protocolTimestampNow() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
