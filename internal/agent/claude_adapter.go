package agent

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
)

const (
	claudeCodeEventLimit      = 1000
	claudeCodeIndexMaxBytes   = 4 << 20
	claudeCodeResultTextLimit = 64 << 10
)

type ClaudeSessionRecord struct {
	SessionID        string         `json:"sessionId"`
	Name             string         `json:"name,omitempty"`
	WorkingDirectory string         `json:"workingDirectory"`
	RequestedModel   string         `json:"requestedModel,omitempty"`
	RequestedEffort  string         `json:"requestedEffort,omitempty"`
	NativeModel      string         `json:"nativeModel,omitempty"`
	Status           string         `json:"status"`
	LatestTurnID     string         `json:"latestTurnId,omitempty"`
	LatestResult     string         `json:"latestResult,omitempty"`
	LastError        string         `json:"lastError,omitempty"`
	ErrorClass       ErrorClass     `json:"errorClass,omitempty"`
	Usage            map[string]any `json:"usage,omitempty"`
	RouteBefore      map[string]any `json:"routeBefore,omitempty"`
	RouteAfter       map[string]any `json:"routeAfter,omitempty"`
	ActualUpstream   map[string]any `json:"actualUpstream,omitempty"`
	Archived         bool           `json:"archived,omitempty"`
	CreatedAt        string         `json:"createdAt"`
	UpdatedAt        string         `json:"updatedAt"`
}

type claudeRun struct {
	cmd        *exec.Cmd
	turnID     string
	canceled   bool
	outputDone chan struct{}
}

type ClaudeCodeAdapter struct {
	logger                    *slog.Logger
	dataDir                   string
	indexPath                 string
	executable                string
	routing                   *CCSwitchInspector
	disableSessionPersistence bool
	versionCache              *ttlCache[versionProbe]
	authCache                 *ttlCache[map[string]any]
	modelsCache               *ttlCache[map[string]any]

	mu       sync.Mutex
	sessions map[string]*ClaudeSessionRecord
	active   map[string]*claudeRun

	eventMu     sync.Mutex
	events      []AgentEvent
	nextEvent   int64
	eventNotify chan struct{}
}

func NewClaudeCodeAdapter(dataDir string, routing *CCSwitchInspector, logger *slog.Logger) *ClaudeCodeAdapter {
	if logger == nil {
		logger = slog.Default()
	}
	if strings.TrimSpace(dataDir) == "" {
		dataDir = filepath.Join(os.TempDir(), "fast-spider-agent")
	}
	a := &ClaudeCodeAdapter{
		logger:       logger,
		dataDir:      dataDir,
		indexPath:    filepath.Join(dataDir, "agent", "claude-code-sessions.json"),
		executable:   "claude",
		routing:      routing,
		sessions:     map[string]*ClaudeSessionRecord{},
		active:       map[string]*claudeRun{},
		eventNotify:  make(chan struct{}),
		versionCache: newTTLCache[versionProbe](cliProbeTTL, 1, nil),
		authCache:    newTTLCache[map[string]any](cliProbeTTL, 1, cloneAgentMap),
		modelsCache:  newTTLCache[map[string]any](modelsTTL, 1, cloneAgentMap),
	}
	if err := a.loadIndex(); err != nil {
		logger.Warn("load Claude Code session index", "error", err)
	}
	return a
}

func (a *ClaudeCodeAdapter) Availability(ctx context.Context) (string, error) {
	if value, ok := a.versionCache.get("version"); ok {
		return value.version, value.err
	}
	path, err := exec.LookPath(a.executable)
	if err != nil {
		err = fmt.Errorf("%w: claude executable not found", node.ErrAgentProviderUnavailable)
		a.versionCache.set("version", versionProbe{err: err})
		return "", err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, path, "--version").Output()
	if err != nil {
		err = fmt.Errorf("%w: claude --version failed", node.ErrAgentProviderUnavailable)
		a.versionCache.set("version", versionProbe{err: err})
		return "", err
	}
	version := strings.TrimSpace(string(output))
	if version == "" {
		err = fmt.Errorf("%w: Claude Code version is empty", node.ErrAgentProviderUnavailable)
		a.versionCache.set("version", versionProbe{err: err})
		return "", err
	}
	a.versionCache.set("version", versionProbe{version: version})
	return version, nil
}

func (a *ClaudeCodeAdapter) AuthConfiguration(ctx context.Context) map[string]any {
	if value, ok := a.authCache.get("auth"); ok {
		return value
	}
	out := map[string]any{"configured": false, "verified": false, "source": "claude_auth_status"}
	path, err := exec.LookPath(a.executable)
	if err != nil {
		out["reason"] = "Claude runtime is unavailable"
		out["errorClass"] = ErrorRuntimeUnavailable
		a.authCache.set("auth", out)
		return out
	}
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(probeCtx, path, "auth", "status", "--json").Output()
	if err != nil || len(raw) > 64<<10 {
		out["reason"] = "Claude auth configuration could not be read"
		out["errorClass"] = ErrorUnknown
		a.authCache.set("auth", out)
		return out
	}
	var status map[string]any
	if json.Unmarshal(raw, &status) != nil {
		out["reason"] = "Claude auth configuration returned invalid JSON"
		out["errorClass"] = ErrorUnknown
		a.authCache.set("auth", out)
		return out
	}
	if loggedIn, ok := status["loggedIn"].(bool); ok {
		out["configured"] = loggedIn
		if !loggedIn {
			out["reason"] = "Claude authentication is not configured"
			out["errorClass"] = ErrorAuthFailed
		}
	}
	for _, key := range []string{"authMethod", "apiProvider", "subscriptionType"} {
		if value, ok := status[key].(string); ok && strings.TrimSpace(value) != "" {
			out[key] = value
		}
	}
	out["health"] = "not_verified_until_turn"
	a.authCache.set("auth", out)
	return out
}

func (a *ClaudeCodeAdapter) Models(ctx context.Context) (map[string]any, error) {
	if value, ok := a.modelsCache.get("models"); ok {
		return value, nil
	}
	route, err := a.inspectRoute(ctx)
	if err != nil {
		return nil, wrapExecutionError("claude_code", "models.list", err)
	}
	models := make([]map[string]any, 0)
	if current, ok := route["currentProvider"].(map[string]any); ok {
		providerID, _ := current["providerId"].(string)
		providerName, _ := current["name"].(string)
		if routed, ok := current["models"].([]map[string]any); ok {
			for _, raw := range routed {
				upstream, _ := raw["model"].(string)
				role, _ := raw["clientRole"].(string)
				id := strings.TrimSpace(role)
				if id == "main" {
					id = "default"
				}
				if id == "" {
					id = upstream
				}
				item := map[string]any{
					"id":                 id,
					"providerId":         "claude_code",
					"clientAlias":        role,
					"upstreamModel":      upstream,
					"routeProviderId":    providerID,
					"routeProviderName":  providerName,
					"source":             "cc_switch_db",
					"authoritativeRoute": true,
				}
				for _, key := range []string{"displayName", "contextWindow", "supports1m"} {
					if value, exists := raw[key]; exists {
						item[key] = value
					}
				}
				models = append(models, item)
			}
		}
	}
	if len(models) == 0 {
		for _, alias := range []string{"sonnet", "opus", "haiku", "fable"} {
			models = append(models, map[string]any{
				"id":                 alias,
				"providerId":         "claude_code",
				"clientAlias":        alias,
				"upstreamModelKnown": false,
				"source":             "claude_code_cli_alias",
				"authoritativeRoute": false,
			})
		}
	}
	out := map[string]any{
		"models": models,
		"route":  route,
		"source": "cc_switch_db+claude_code_cli",
	}
	a.modelsCache.set("models", out)
	return out, nil
}

func (a *ClaudeCodeAdapter) Capabilities(ctx context.Context) (map[string]any, error) {
	route, err := a.inspectRoute(ctx)
	if err != nil {
		return nil, wrapExecutionError("claude_code", "provider.capabilities", err)
	}
	effective, _ := route["effectiveCapabilities"].(map[string]any)
	return map[string]any{
		"providerId": "claude_code",
		"harness": map[string]any{
			"sessionPersistence": true,
			"resume":             true,
			"streamJSON":         true,
			"structuredOutput":   true,
			"effortLevels":       []string{"low", "medium", "high", "xhigh", "max"},
			"permissionMode":     "acceptEdits",
			"activeTurnSteer":    false,
			"interactiveRespond": false,
		},
		"effectiveCapabilities": effective,
		"route":                 route,
		"source":                "claude_code_cli+cc_switch_db",
		"authoritative":         true,
	}, nil
}

func (a *ClaudeCodeAdapter) Create(ctx context.Context, workingDirectory, prompt, model, effort, name string, outputSchema map[string]any) (map[string]any, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("Claude Code session.create requires a text prompt")
	}
	if _, err := os.Stat(workingDirectory); err != nil {
		return nil, err
	}
	sessionID, err := newClaudeUUID()
	if err != nil {
		return nil, err
	}
	record := &ClaudeSessionRecord{
		SessionID:        sessionID,
		Name:             boundedAgentText(strings.TrimSpace(name), 128),
		WorkingDirectory: workingDirectory,
		RequestedModel:   strings.TrimSpace(model),
		RequestedEffort:  strings.TrimSpace(effort),
		Status:           "created",
		CreatedAt:        protocolTimestampNow(),
		UpdatedAt:        protocolTimestampNow(),
	}
	a.mu.Lock()
	a.sessions[sessionID] = record
	_ = a.saveIndexLocked()
	a.mu.Unlock()
	result, err := a.startTurn(ctx, record, prompt, false, outputSchema)
	if err != nil {
		a.mu.Lock()
		delete(a.sessions, sessionID)
		_ = a.saveIndexLocked()
		a.mu.Unlock()
		return nil, err
	}
	return result, nil
}

func (a *ClaudeCodeAdapter) Send(ctx context.Context, sessionID, prompt, workingDirectory, model, effort string, outputSchema map[string]any) (map[string]any, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("Claude Code session.send requires a text prompt")
	}
	a.mu.Lock()
	record := a.sessions[sessionID]
	if record == nil {
		a.mu.Unlock()
		return nil, node.ErrAgentSessionNotFound
	}
	if _, busy := a.active[sessionID]; busy {
		a.mu.Unlock()
		return nil, node.ErrAgentSessionBusy
	}
	if strings.TrimSpace(workingDirectory) != "" && !sameAgentPath(record.WorkingDirectory, workingDirectory) {
		a.mu.Unlock()
		return nil, fmt.Errorf("Claude Code session workingDirectory is fixed; create a new session for a different directory")
	}
	if strings.TrimSpace(model) != "" {
		record.RequestedModel = strings.TrimSpace(model)
	}
	if strings.TrimSpace(effort) != "" {
		record.RequestedEffort = strings.TrimSpace(effort)
	}
	a.mu.Unlock()
	return a.startTurn(ctx, record, prompt, true, outputSchema)
}

func (a *ClaudeCodeAdapter) startTurn(ctx context.Context, record *ClaudeSessionRecord, prompt string, resume bool, outputSchema map[string]any) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := exec.LookPath(a.executable)
	if err != nil {
		return nil, fmt.Errorf("%w: claude executable not found", node.ErrAgentProviderUnavailable)
	}
	turnID, err := newClaudeOpaque("turn_")
	if err != nil {
		return nil, err
	}

	a.mu.Lock()
	if _, busy := a.active[record.SessionID]; busy {
		a.mu.Unlock()
		return nil, node.ErrAgentSessionBusy
	}
	sessionID := record.SessionID
	name := record.Name
	workingDirectory := record.WorkingDirectory
	requestedModel := record.RequestedModel
	requestedEffort := record.RequestedEffort
	run := &claudeRun{turnID: turnID, outputDone: make(chan struct{})}
	a.active[sessionID] = run
	a.mu.Unlock()
	cleanupReservation := func() {
		a.mu.Lock()
		if current := a.active[sessionID]; current == run {
			delete(a.active, sessionID)
		}
		a.mu.Unlock()
	}

	if requestedEffort != "" && !stringInSet(requestedEffort, "low", "medium", "high", "xhigh", "max") {
		cleanupReservation()
		return nil, fmt.Errorf("Claude Code effort must be low, medium, high, xhigh, or max")
	}
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--permission-mode", "acceptEdits"}
	if a.disableSessionPersistence {
		args = append(args, "--no-session-persistence")
	}
	if resume {
		args = append(args, "--resume", sessionID)
	} else {
		args = append(args, "--session-id", sessionID)
		if name != "" {
			args = append(args, "--name", name)
		}
	}
	if requestedModel != "" && requestedModel != "default" {
		args = append(args, "--model", requestedModel)
	}
	if requestedEffort != "" {
		args = append(args, "--effort", requestedEffort)
	}
	if outputSchema != nil {
		if err := validateOutputSchema(outputSchema); err != nil {
			cleanupReservation()
			return nil, err
		}
		raw, err := json.Marshal(outputSchema)
		if err != nil {
			cleanupReservation()
			return nil, err
		}
		if len(raw) > 16<<10 {
			cleanupReservation()
			return nil, fmt.Errorf("Claude Code outputSchema exceeds 16384 bytes")
		}
		args = append(args, "--json-schema", string(raw))
	}

	cmd := exec.Command(path, args...)
	cmd.Dir = workingDirectory
	cmd.Env = os.Environ()
	cmd.Stdin = strings.NewReader(prompt)
	node.ConfigureProcessTree(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanupReservation()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cleanupReservation()
		return nil, err
	}

	routeBefore := a.captureRoute()
	a.mu.Lock()
	run.cmd = cmd
	canceledBeforeStart := run.canceled
	a.mu.Unlock()
	if canceledBeforeStart {
		cleanupReservation()
		return nil, context.Canceled
	}
	if err := cmd.Start(); err != nil {
		cleanupReservation()
		return nil, fmt.Errorf("start Claude Code: %w", err)
	}

	a.mu.Lock()
	record.Status = "running"
	record.LatestTurnID = turnID
	record.LatestResult = ""
	record.LastError = ""
	record.ErrorClass = ""
	record.RouteBefore = routeBefore
	record.RouteAfter = nil
	record.ActualUpstream = nil
	record.UpdatedAt = protocolTimestampNow()
	_ = a.saveIndexLocked()
	a.mu.Unlock()
	a.recordEvent(AgentEvent{Type: "turn.started", SessionID: sessionID, TurnID: turnID, State: "running", Timestamp: protocolTimestampNow()})
	go a.readStdout(sessionID, turnID, stdout, run.outputDone)
	go a.readStderr(sessionID, stderr)
	go a.waitRun(sessionID, run)

	return map[string]any{
		"providerId":       "claude_code",
		"sessionId":        sessionID,
		"turnId":           turnID,
		"phase":            "running",
		"executionMode":    "cli_stream_json",
		"workingDirectory": workingDirectory,
		"requestedModel":   requestedModel,
		"route":            routeBefore,
	}, nil
}

func (a *ClaudeCodeAdapter) readStdout(sessionID, turnID string, reader io.Reader, done chan struct{}) {
	defer close(done)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		a.handleStreamLine(sessionID, turnID, line)
	}
	if err := scanner.Err(); err != nil {
		a.logger.Debug("Claude Code stdout ended", "error", err)
	}
}

func (a *ClaudeCodeAdapter) readStderr(sessionID string, reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 8*1024), 256*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			a.logger.Debug("Claude Code stderr", "sessionId", sessionID, "errorClass", classifyExecutionText(line))
		}
	}
}

func (a *ClaudeCodeAdapter) handleStreamLine(sessionID, turnID string, raw []byte) {
	var message map[string]any
	if err := json.Unmarshal(raw, &message); err != nil {
		a.recordEvent(AgentEvent{Type: "warning", SessionID: sessionID, TurnID: turnID, Text: "Claude Code emitted an invalid JSON event", Timestamp: protocolTimestampNow()})
		return
	}
	typeName := mapString(message, "type")
	switch typeName {
	case "system":
		subtype := mapString(message, "subtype")
		switch subtype {
		case "init":
			a.mu.Lock()
			if record := a.sessions[sessionID]; record != nil {
				record.NativeModel = mapString(message, "model")
				record.UpdatedAt = protocolTimestampNow()
				_ = a.saveIndexLocked()
			}
			a.mu.Unlock()
			a.recordEvent(AgentEvent{Type: "session.status", SessionID: sessionID, TurnID: turnID, State: "initialized", Timestamp: protocolTimestampNow()})
		case "api_retry":
			class := classifyExecutionText(mapString(message, "error"))
			a.recordEvent(AgentEvent{Type: "warning", SessionID: sessionID, TurnID: turnID, Text: publicErrorMessage(class), State: "api_retry", Detail: map[string]any{"errorClass": class}, Timestamp: protocolTimestampNow()})
		default:
			a.recordEvent(AgentEvent{Type: "runtime.notification", SessionID: sessionID, TurnID: turnID, State: subtype, Timestamp: protocolTimestampNow()})
		}
	case "assistant":
		child, _ := message["message"].(map[string]any)
		text, tools := claudeMessageContent(child)
		if text != "" {
			a.recordEvent(AgentEvent{Type: "assistant.message", SessionID: sessionID, TurnID: turnID, Text: boundedAgentText(text, 16*1024), Timestamp: protocolTimestampNow()})
		}
		for _, tool := range tools {
			a.recordEvent(AgentEvent{Type: "tool.started", SessionID: sessionID, TurnID: turnID, Text: boundedAgentText(tool, 256), Timestamp: protocolTimestampNow()})
		}
	case "user":
		child, _ := message["message"].(map[string]any)
		for _, tool := range claudeToolResults(child) {
			a.recordEvent(AgentEvent{Type: "tool.completed", SessionID: sessionID, TurnID: turnID, Text: boundedAgentText(tool, 256), Timestamp: protocolTimestampNow()})
		}
	case "result":
		result := boundedAgentText(mapString(message, "result"), claudeCodeResultTextLimit)
		isError, _ := message["is_error"].(bool)
		status := "completed"
		eventType := "turn.completed"
		if isError {
			status = "failed"
			eventType = "turn.failed"
		}
		usage := claudeUsageSummary(message)
		routeAfter := a.captureRoute()
		a.mu.Lock()
		if record := a.sessions[sessionID]; record != nil {
			record.Status = status
			record.LatestResult = result
			if isError {
				record.ErrorClass = classifyExecutionText(result)
				record.LastError = publicErrorMessage(record.ErrorClass)
				record.LatestResult = ""
			} else {
				record.ErrorClass = ""
			}
			record.Usage = usage
			record.RouteAfter = routeAfter
			record.ActualUpstream = claudeActualUpstream(sessionID, routeAfter)
			record.UpdatedAt = protocolTimestampNow()
			_ = a.saveIndexLocked()
		}
		a.mu.Unlock()
		eventText := result
		detail := map[string]any(nil)
		if isError {
			class := classifyExecutionText(result)
			eventText = publicErrorMessage(class)
			detail = map[string]any{"errorClass": class}
		}
		a.recordEvent(AgentEvent{Type: eventType, SessionID: sessionID, TurnID: turnID, State: status, Text: eventText, Detail: detail, Timestamp: protocolTimestampNow()})
	}
}

func (a *ClaudeCodeAdapter) waitRun(sessionID string, run *claudeRun) {
	err := run.cmd.Wait()
	<-run.outputDone
	routeAfter := a.captureRoute()
	a.mu.Lock()
	delete(a.active, sessionID)
	record := a.sessions[sessionID]
	if record != nil && record.Status == "running" {
		if run.canceled {
			record.Status = "canceled"
			record.LastError = ""
		} else if err != nil {
			record.Status = "failed"
			record.ErrorClass = classifyExecutionError(err)
			record.LastError = publicErrorMessage(record.ErrorClass)
		} else {
			record.Status = "completed"
		}
		record.RouteAfter = routeAfter
		record.ActualUpstream = claudeActualUpstream(sessionID, routeAfter)
		record.UpdatedAt = protocolTimestampNow()
		_ = a.saveIndexLocked()
		state := record.Status
		text := record.LastError
		errorClass := record.ErrorClass
		a.mu.Unlock()
		eventType := "turn.completed"
		if state == "canceled" {
			eventType = "turn.interrupted"
		} else if state == "failed" {
			eventType = "turn.failed"
		}
		var detail map[string]any
		if state == "failed" {
			detail = map[string]any{"errorClass": errorClass}
		}
		a.recordEvent(AgentEvent{Type: eventType, SessionID: sessionID, TurnID: run.turnID, State: state, Text: text, Detail: detail, Timestamp: protocolTimestampNow()})
		return
	}
	if record != nil {
		record.UpdatedAt = protocolTimestampNow()
		_ = a.saveIndexLocked()
	}
	a.mu.Unlock()
}

func (a *ClaudeCodeAdapter) Cancel(sessionID, turnID string) error {
	a.mu.Lock()
	run := a.active[sessionID]
	if run == nil {
		a.mu.Unlock()
		return node.ErrAgentSessionNotFound
	}
	if strings.TrimSpace(turnID) != "" && turnID != run.turnID {
		a.mu.Unlock()
		return fmt.Errorf("turnId does not match the active Claude Code turn")
	}
	run.canceled = true
	cmd := run.cmd
	a.mu.Unlock()
	if cmd == nil {
		return nil
	}
	return node.KillProcessTree(cmd)
}

func (a *ClaudeCodeAdapter) Close(ctx context.Context) error {
	a.mu.Lock()
	commands := make([]*exec.Cmd, 0, len(a.active))
	for _, run := range a.active {
		run.canceled = true
		if run.cmd != nil {
			commands = append(commands, run.cmd)
		}
	}
	a.mu.Unlock()
	for _, cmd := range commands {
		if err := node.KillProcessTree(cmd); err != nil && ctx.Err() == nil {
			a.logger.Debug("kill Claude Code process tree", "error", err)
		}
	}
	return ctx.Err()
}

func (a *ClaudeCodeAdapter) List(workingDirectory string, limit int) map[string]any {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	a.mu.Lock()
	records := make([]*ClaudeSessionRecord, 0, len(a.sessions))
	for _, record := range a.sessions {
		if record.Archived {
			continue
		}
		if workingDirectory != "" && !sameAgentPath(record.WorkingDirectory, workingDirectory) {
			continue
		}
		copy := *record
		records = append(records, &copy)
	}
	a.mu.Unlock()
	sort.Slice(records, func(i, j int) bool { return records[i].UpdatedAt > records[j].UpdatedAt })
	if len(records) > limit {
		records = records[:limit]
	}
	items := make([]map[string]any, 0, len(records))
	for _, record := range records {
		items = append(items, claudeSessionSummary(record))
	}
	return map[string]any{"sessions": items}
}

func (a *ClaudeCodeAdapter) Get(sessionID string) (map[string]any, error) {
	a.mu.Lock()
	record := a.sessions[sessionID]
	if record == nil {
		a.mu.Unlock()
		return nil, node.ErrAgentSessionNotFound
	}
	copy := *record
	a.mu.Unlock()
	return map[string]any{"session": claudeSessionSummary(&copy)}, nil
}

func (a *ClaudeCodeAdapter) Result(sessionID string) (map[string]any, error) {
	a.mu.Lock()
	record := a.sessions[sessionID]
	if record == nil {
		a.mu.Unlock()
		return nil, node.ErrAgentSessionNotFound
	}
	out := map[string]any{
		"providerId":        "claude_code",
		"sessionId":         record.SessionID,
		"turnId":            record.LatestTurnID,
		"status":            record.Status,
		"finalAgentMessage": record.LatestResult,
		"nativeModel":       record.NativeModel,
		"requestedModel":    record.RequestedModel,
		"routeBefore":       record.RouteBefore,
		"routeAfter":        record.RouteAfter,
		"actualUpstream":    record.ActualUpstream,
		"usage":             record.Usage,
	}
	if record.LastError != "" {
		out["error"] = record.LastError
		out["errorClass"] = record.ErrorClass
	}
	a.mu.Unlock()
	return out, nil
}

func (a *ClaudeCodeAdapter) Rename(sessionID, name string) (map[string]any, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return nil, fmt.Errorf("name is required and must be at most 128 characters")
	}
	a.mu.Lock()
	record := a.sessions[sessionID]
	if record == nil {
		a.mu.Unlock()
		return nil, node.ErrAgentSessionNotFound
	}
	record.Name = name
	record.UpdatedAt = protocolTimestampNow()
	_ = a.saveIndexLocked()
	a.mu.Unlock()
	return map[string]any{"sessionId": sessionID, "name": name, "nativeHistoryRenamed": false}, nil
}

func (a *ClaudeCodeAdapter) SetArchived(sessionID string, archived bool) (map[string]any, error) {
	a.mu.Lock()
	record := a.sessions[sessionID]
	if record == nil {
		a.mu.Unlock()
		return nil, node.ErrAgentSessionNotFound
	}
	record.Archived = archived
	record.UpdatedAt = protocolTimestampNow()
	_ = a.saveIndexLocked()
	a.mu.Unlock()
	return map[string]any{"sessionId": sessionID, "archived": archived, "nativeHistoryPreserved": true}, nil
}

func (a *ClaudeCodeAdapter) Delete(sessionID string) (map[string]any, error) {
	a.mu.Lock()
	record := a.sessions[sessionID]
	if record == nil {
		a.mu.Unlock()
		return nil, node.ErrAgentSessionNotFound
	}
	if _, busy := a.active[sessionID]; busy {
		a.mu.Unlock()
		return nil, fmt.Errorf("cannot delete an active Claude Code session")
	}
	delete(a.sessions, sessionID)
	if err := a.saveIndexLocked(); err != nil {
		a.sessions[sessionID] = record
		a.mu.Unlock()
		return nil, err
	}
	a.mu.Unlock()
	return map[string]any{"sessionId": sessionID, "deleted": true, "nativeHistoryPreserved": true}, nil
}

func (a *ClaudeCodeAdapter) Watch(ctx context.Context, sessionID string, cursor int64, wait time.Duration) ([]AgentEvent, int64, int64, error) {
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
			if event.Sequence > cursor && event.SessionID == sessionID {
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
		timer := time.NewTimer(time.Until(deadline))
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

func (a *ClaudeCodeAdapter) recordEvent(event AgentEvent) {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	a.nextEvent++
	event.Sequence = a.nextEvent
	if event.Timestamp == "" {
		event.Timestamp = protocolTimestampNow()
	}
	a.events = append(a.events, event)
	if len(a.events) > claudeCodeEventLimit {
		a.events = append([]AgentEvent(nil), a.events[len(a.events)-claudeCodeEventLimit:]...)
	}
	close(a.eventNotify)
	a.eventNotify = make(chan struct{})
}

func (a *ClaudeCodeAdapter) inspectRoute(ctx context.Context) (map[string]any, error) {
	if a.routing == nil {
		return map[string]any{"appType": "claude", "harness": "claude_code", "routingMode": "direct", "source": "none", "authoritative": false}, nil
	}
	return a.routing.InspectApp(ctx, "claude")
}

func (a *ClaudeCodeAdapter) captureRoute() map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	route, err := a.inspectRoute(ctx)
	if err != nil {
		class := classifyExecutionError(err)
		return map[string]any{"appType": "claude", "harness": "claude_code", "source": "cc_switch_db", "available": false, "reason": "route_inspection_failed", "errorClass": class}
	}
	return claudeRouteSnapshot(route)
}

func claudeRouteSnapshot(route map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"appType", "harness", "source", "authoritative", "capturedAt", "localProxyEnabled", "deviceCurrentProviderId", "routingMode", "proxy", "currentProvider", "effectiveCapabilities", "lastRequest"} {
		if value, ok := route[key]; ok {
			out[key] = value
		}
	}
	return out
}

func claudeActualUpstream(sessionID string, route map[string]any) map[string]any {
	if route == nil {
		return nil
	}
	mode, _ := route["routingMode"].(string)
	if mode != "cc_switch" {
		return nil
	}
	last, _ := route["lastRequest"].(map[string]any)
	if last == nil || mapString(last, "sessionId") != sessionID {
		return nil
	}
	out := map[string]any{}
	for _, key := range []string{"providerId", "upstreamModel", "requestModel", "createdAt"} {
		if value, ok := last[key]; ok {
			out[key] = value
		}
	}
	return out
}

func claudeMessageContent(message map[string]any) (string, []string) {
	content, _ := message["content"].([]any)
	texts := make([]string, 0)
	tools := make([]string, 0)
	for _, raw := range content {
		item, _ := raw.(map[string]any)
		switch mapString(item, "type") {
		case "text":
			if text := mapString(item, "text"); text != "" {
				texts = append(texts, text)
			}
		case "tool_use":
			if name := mapString(item, "name"); name != "" {
				tools = append(tools, name)
			}
		}
	}
	return strings.Join(texts, "\n"), tools
}

func claudeToolResults(message map[string]any) []string {
	content, _ := message["content"].([]any)
	results := make([]string, 0)
	for _, raw := range content {
		item, _ := raw.(map[string]any)
		if mapString(item, "type") == "tool_result" {
			id := mapString(item, "tool_use_id")
			if id == "" {
				id = "tool_result"
			}
			results = append(results, id)
		}
	}
	return results
}

func claudeUsageSummary(message map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"duration_ms", "duration_api_ms", "num_turns", "total_cost_usd", "terminal_reason", "stop_reason"} {
		if value, ok := message[key]; ok {
			out[key] = value
		}
	}
	if usage, ok := message["usage"].(map[string]any); ok {
		summary := map[string]any{}
		for _, key := range []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "service_tier"} {
			if value, exists := usage[key]; exists {
				summary[key] = value
			}
		}
		out["tokens"] = summary
	}
	return out
}

func newClaudeUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

func newClaudeOpaque(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}
