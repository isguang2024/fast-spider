package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/isguang2024/fast-spider/internal/node"
)

const (
	codexRPCLineLimit       = 8 << 20
	codexEventLimit         = 1000
	codexAppServerStopWait  = 5 * time.Second
	codexInterruptRetryWait = 50 * time.Millisecond
	codexInterruptAttempts  = 5
)

var errCodexChatGPTNotAuthenticated = errors.New("Codex app-server is not authenticated with ChatGPT")

type codexRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type codexRPCResponseError struct {
	*ExecutionError
	code int
}

func (e *codexRPCResponseError) Unwrap() error { return e.ExecutionError }

func isDefinitiveCodexRPCRejection(err error) bool {
	var responseErr *codexRPCResponseError
	return errors.As(err, &responseErr) && responseErr.code != -1
}

type codexRPCMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *codexRPCError  `json:"error,omitempty"`
}

type codexPending struct {
	ch         chan codexRPCMessage
	generation uint64
}

// codexProcess is an immutable snapshot of one app-server generation.  Stop
// and reader paths must operate on this snapshot rather than looking up the
// adapter's currently active resources after a replacement has started.
type codexProcess struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	done       chan struct{}
	generation uint64
}

type codexServerRequest struct {
	RawID      json.RawMessage
	RequestID  string
	Method     string
	SessionID  string
	TurnID     string
	Params     map[string]any
	ReceivedAt time.Time
	Responding bool
	generation uint64
}

type codexSessionLock struct {
	mu   sync.Mutex
	refs int
}

type CodexAdapter struct {
	logger       *slog.Logger
	versionCache *ttlCache[versionProbe]
	modelsCache  *ttlCache[map[string]any]
	executable   string
	configErr    *ExecutionError

	startGateMu      sync.Mutex
	startGate        chan struct{}
	lifecycleMu      sync.Mutex
	loadLocksMu      sync.Mutex
	loadLocks        map[string]*codexSessionLock
	mu               sync.Mutex
	rpcWriteMu       sync.Mutex
	cmd              *exec.Cmd
	stdin            io.WriteCloser
	pending          map[int64]codexPending
	nextID           int64
	closed           bool
	quarantined      bool
	processDone      chan struct{}
	generation       uint64
	loaded           map[string]struct{}
	loadedGeneration map[string]uint64

	eventMu              sync.Mutex
	events               []AgentEvent
	nextEvent            int64
	eventNotify          chan struct{}
	activeTurns          map[string]string
	activeTurnGeneration map[string]uint64
	eventObserver        func(AgentEvent)

	serverMu                 sync.Mutex
	serverRequests           map[string]codexServerRequest
	requestOverride          func(context.Context, string, map[string]any) (map[string]any, error)
	stopProcessOverride      func(context.Context, *exec.Cmd) error
	stopTargetOverride       func(context.Context, codexProcess) error
	unloadThreadOverride     func(context.Context, string) error
	unloadBeforeSend         func(codexProcess)
	notificationBeforeCommit func(uint64)
}

func NewCodexAdapter(logger *slog.Logger) *CodexAdapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &CodexAdapter{
		logger:               logger,
		versionCache:         newTTLCache[versionProbe](cliProbeTTL, 1, nil),
		modelsCache:          newTTLCache[map[string]any](modelsTTL, 1, cloneAgentMap),
		pending:              make(map[int64]codexPending),
		loaded:               make(map[string]struct{}),
		loadedGeneration:     make(map[string]uint64),
		eventNotify:          make(chan struct{}),
		activeTurns:          make(map[string]string),
		activeTurnGeneration: make(map[string]uint64),
		serverRequests:       make(map[string]codexServerRequest),
	}
}

func (a *CodexAdapter) SetEventObserver(observer func(AgentEvent)) {
	if a == nil {
		return
	}
	a.eventMu.Lock()
	a.eventObserver = observer
	a.eventMu.Unlock()
}

func (a *CodexAdapter) Availability(ctx context.Context) (string, error) {
	if value, ok := a.versionCache.get("version"); ok {
		return value.version, value.err
	}
	for _, path := range codexExecutableCandidates() {
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		output, err := exec.CommandContext(probeCtx, path, "--version").Output()
		cancel()
		if err != nil || strings.TrimSpace(string(output)) == "" {
			continue
		}
		version := strings.TrimSpace(string(output))
		a.mu.Lock()
		a.executable = path
		a.mu.Unlock()
		a.versionCache.set("version", versionProbe{version: version})
		return version, nil
	}
	err := fmt.Errorf("%w: compatible Codex executable not found", node.ErrAgentProviderUnavailable)
	a.versionCache.set("version", versionProbe{err: err})
	return "", err
}

func codexExecutableCandidates() []string {
	if explicit := strings.TrimSpace(os.Getenv("FAST_SPIDER_CODEX_EXECUTABLE")); explicit != "" {
		if absolute, err := filepath.Abs(explicit); err == nil {
			return []string{absolute}
		}
		return []string{explicit}
	}
	type candidate struct {
		path    string
		modTime time.Time
	}
	var desktop []candidate
	if runtime.GOOS == "windows" {
		localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
		root := ""
		if filepath.IsAbs(localAppData) {
			root = filepath.Join(localAppData, "OpenAI", "Codex", "bin")
		}
		if root != "" {
			if entries, err := os.ReadDir(root); err == nil {
				for _, entry := range entries {
					path := filepath.Join(root, entry.Name(), "codex.exe")
					if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
						desktop = append(desktop, candidate{path: path, modTime: info.ModTime()})
					}
				}
			}
		}
	}
	sort.Slice(desktop, func(i, j int) bool {
		if desktop[i].modTime.Equal(desktop[j].modTime) {
			return desktop[i].path < desktop[j].path
		}
		return desktop[i].modTime.After(desktop[j].modTime)
	})
	paths := make([]string, 0, len(desktop)+1)
	seen := map[string]struct{}{}
	add := func(path string) {
		if path == "" {
			return
		}
		key := strings.ToLower(filepath.Clean(path))
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		paths = append(paths, path)
	}
	for _, item := range desktop {
		add(item.path)
	}
	if path, err := exec.LookPath("codex"); err == nil {
		add(path)
	}
	return paths
}

func codexAppServerEnvironment(base []string) []string {
	environment := make([]string, 0, len(base)+4)
	noProxyValues := make([]string, 0, 4)
	seenNoProxy := make(map[string]struct{})
	addNoProxy := func(raw string) {
		for _, value := range strings.Split(raw, ",") {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			key := strings.ToLower(value)
			if _, exists := seenNoProxy[key]; exists {
				continue
			}
			seenNoProxy[key] = struct{}{}
			noProxyValues = append(noProxyValues, value)
		}
	}
	for _, entry := range base {
		key, value, found := strings.Cut(entry, "=")
		if found && strings.EqualFold(strings.TrimSpace(key), "NO_PROXY") {
			addNoProxy(value)
			continue
		}
		environment = append(environment, entry)
	}
	addNoProxy("127.0.0.1")
	addNoProxy("localhost")
	noProxy := strings.Join(noProxyValues, ",")
	return append(environment,
		"NO_PROXY="+noProxy,
		"no_proxy="+noProxy,
		"LOG_FORMAT=json",
		"RUST_LOG=warn",
	)
}

func codexAppServerCommandArgs() []string {
	return []string{"app-server", "--stdio"}
}

func (a *CodexAdapter) executionMetadata() (string, string) {
	return "codex_app_server", "fast_spider_node"
}

func (a *CodexAdapter) ensureStarted(ctx context.Context) error {
	release, err := a.acquireStartGate(ctx)
	if err != nil {
		return err
	}
	defer release()
	for {
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			return fmt.Errorf("%w: adapter is closed", node.ErrAgentProviderUnavailable)
		}
		if a.cmd != nil && a.cmd.Process != nil && a.cmd.ProcessState == nil && !a.quarantined {
			a.mu.Unlock()
			return nil
		}
		previousDone := a.processDone
		a.mu.Unlock()
		if previousDone == nil {
			break
		}
		waitCtx, cancel := context.WithTimeout(ctx, codexAppServerStopWait)
		select {
		case <-waitCtx.Done():
			cancel()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w: quarantined Codex app-server did not exit", node.ErrAgentProviderUnavailable)
		case <-previousDone:
			cancel()
		}
	}

	if _, err := a.Availability(ctx); err != nil {
		return err
	}
	a.mu.Lock()
	path := a.executable
	a.mu.Unlock()
	if path == "" {
		return fmt.Errorf("%w: compatible Codex executable was not resolved", node.ErrAgentProviderUnavailable)
	}
	cmd := exec.Command(path, codexAppServerCommandArgs()...)
	if home, homeErr := os.UserHomeDir(); homeErr == nil && home != "" {
		cmd.Dir = home
	}
	cmd.Env = codexAppServerEnvironment(os.Environ())
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

	done := make(chan struct{})
	generation, published := a.publishStartedProcess(cmd, stdin, done)
	if !published {
		_ = a.stopProcessTarget(context.Background(), codexProcess{cmd: cmd, stdin: stdin})
		return fmt.Errorf("%w: adapter is closed", node.ErrAgentProviderUnavailable)
	}
	go a.stderrLoop(stderr)
	go a.waitLoop(cmd, generation, done)
	go a.readLoop(stdout, generation)

	initCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := a.request(initCtx, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "Codex Desktop",
			"title":   "Fast Spider",
			"version": "0.1.0",
		},
		"capabilities": map[string]any{
			"experimentalApi": true,
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

func (a *CodexAdapter) IsStarted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.closed && a.cmd != nil && a.cmd.Process != nil && a.cmd.ProcessState == nil && a.stdin != nil && !a.quarantined && a.configErr == nil
}

// AuthToken returns the ChatGPT access token the Codex app-server is logged in
// with, or an error if the app-server is not authenticated. The token is the
// source for the chatgpt_cloud backend (desktop-initiated cloud conversations).
func (a *CodexAdapter) AuthToken(ctx context.Context) (string, error) {
	result, err := a.request(ctx, "getAuthStatus", map[string]any{"includeToken": true, "refreshToken": false})
	if err != nil {
		return "", err
	}
	token, _ := result["authToken"].(string)
	if token == "" {
		return "", errCodexChatGPTNotAuthenticated
	}
	return token, nil
}

func (a *CodexAdapter) Close(ctx context.Context) error {
	a.lifecycleMu.Lock()
	a.mu.Lock()
	a.closed = true
	cmd := a.cmd
	a.mu.Unlock()
	a.lifecycleMu.Unlock()
	if cmd == nil {
		return nil
	}
	return a.stopProcess(ctx, cmd)
}

func (a *CodexAdapter) publishStartedProcess(cmd *exec.Cmd, stdin io.WriteCloser, done chan struct{}) (uint64, bool) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return 0, false
	}
	a.generation++
	generation := a.generation
	a.cmd = cmd
	a.stdin = stdin
	a.processDone = done
	a.quarantined = false
	a.configErr = nil
	return generation, true
}

func (a *CodexAdapter) stopProcess(ctx context.Context, cmd *exec.Cmd) error {
	a.mu.Lock()
	if a.cmd != cmd {
		a.mu.Unlock()
		return nil
	}
	target := codexProcess{cmd: cmd, stdin: a.stdin, done: a.processDone, generation: a.generation}
	a.mu.Unlock()
	return a.stopProcessTarget(ctx, target)
}

func (a *CodexAdapter) stopProcessTarget(ctx context.Context, target codexProcess) error {
	if ctx == nil {
		ctx = context.Background()
	}
	stopCtx, cancel := context.WithTimeout(ctx, codexAppServerStopWait)
	defer cancel()
	cmd := target.cmd
	if cmd == nil {
		return nil
	}
	if target.stdin != nil {
		_ = target.stdin.Close()
	}
	if cmd.Process == nil || cmd.ProcessState != nil {
		return nil
	}
	if target.done == nil {
		killDone := make(chan error, 1)
		go func() { killDone <- node.KillProcessTree(cmd) }()
		select {
		case <-killDone:
			return nil
		case <-stopCtx.Done():
			return stopCtx.Err()
		}
	}
	grace := time.NewTimer(time.Second)
	defer grace.Stop()
	select {
	case <-target.done:
		return nil
	case <-stopCtx.Done():
		return stopCtx.Err()
	case <-grace.C:
	}

	// Keep this adapter's single stop budget authoritative even when the
	// platform process-tree command has a longer internal timeout. The command
	// is an immutable snapshot, so a late kill cannot touch a replacement.
	killDone := make(chan error, 1)
	go func() { killDone <- node.KillProcessTree(cmd) }()
	select {
	case <-target.done:
		return nil
	case <-killDone:
	case <-stopCtx.Done():
		return stopCtx.Err()
	}
	select {
	case <-target.done:
		return nil
	case <-stopCtx.Done():
		return stopCtx.Err()
	}
}

func (a *CodexAdapter) acquireStartGate(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	a.startGateMu.Lock()
	if a.startGate == nil {
		a.startGate = make(chan struct{}, 1)
		a.startGate <- struct{}{}
	}
	gate := a.startGate
	a.startGateMu.Unlock()
	select {
	case <-gate:
		return func() { gate <- struct{}{} }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *CodexAdapter) request(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	if a.requestOverride != nil {
		return a.requestOverride(ctx, method, params)
	}
	if method != "initialize" {
		if err := a.ensureStarted(ctx); err != nil {
			return nil, err
		}
	}
	a.mu.Lock()
	if a.configErr != nil {
		err := a.configErr
		a.mu.Unlock()
		return nil, err
	}
	if a.stdin == nil || a.cmd == nil {
		a.mu.Unlock()
		return nil, node.ErrAgentProviderUnavailable
	}
	a.nextID++
	id := a.nextID
	generation := a.generation
	pending := codexPending{ch: make(chan codexRPCMessage, 1), generation: generation}
	a.pending[id] = pending
	stdin := a.stdin
	a.mu.Unlock()

	message := map[string]any{"id": id, "method": method}
	if params != nil {
		message["params"] = params
	}
	if err := a.writeMessage(stdin, message); err != nil {
		a.removePending(id)
		return nil, err
	}
	select {
	case <-ctx.Done():
		a.removePending(id)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			a.quarantineProcess(generation)
		}
		return nil, ctx.Err()
	case response := <-pending.ch:
		if response.Error != nil {
			a.logger.Debug("Codex app-server request failed", "method", method, "rpcCode", response.Error.Code, "errorClass", classifyExecutionText(response.Error.Message))
			return nil, &codexRPCResponseError{ExecutionError: newExecutionError("codex", method, response.Error.Message), code: response.Error.Code}
		}
		a.mu.Lock()
		configErr := a.configErr
		a.mu.Unlock()
		if configErr != nil {
			return nil, configErr
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

// requestOnTarget sends an RPC only through the immutable process snapshot
// captured by the originating generation. The lifecycle lock is held only
// while validating/capturing pending state; all I/O and waiting happen after
// it is released.
func (a *CodexAdapter) requestOnTarget(ctx context.Context, target codexProcess, method string, params map[string]any) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	a.lifecycleMu.Lock()
	a.mu.Lock()
	if !a.isCurrentGenerationLocked(target.generation) || a.cmd != target.cmd || a.stdin != target.stdin || a.processDone != target.done {
		a.mu.Unlock()
		a.lifecycleMu.Unlock()
		return nil, node.ErrAgentProviderUnavailable
	}
	a.nextID++
	id := a.nextID
	pending := codexPending{ch: make(chan codexRPCMessage, 1), generation: target.generation}
	a.pending[id] = pending
	a.mu.Unlock()
	a.lifecycleMu.Unlock()

	message := map[string]any{"id": id, "method": method}
	if params != nil {
		message["params"] = params
	}
	if err := a.writeMessage(target.stdin, message); err != nil {
		a.removePending(id)
		return nil, err
	}
	select {
	case <-ctx.Done():
		a.removePending(id)
		return nil, ctx.Err()
	case response := <-pending.ch:
		if response.Error != nil {
			return nil, &codexRPCResponseError{ExecutionError: newExecutionError("codex", method, response.Error.Message), code: response.Error.Code}
		}
		if len(response.Result) == 0 || string(response.Result) == "null" {
			return map[string]any{}, nil
		}
		var result map[string]any
		if err := json.Unmarshal(response.Result, &result); err != nil {
			return nil, fmt.Errorf("decode Codex %s result: %w", method, err)
		}
		return result, nil
	}
}

// quarantineProcess fences a timed-out RPC's app-server generation before the
// next request can reuse its stdio pipe. The process is owned by this adapter's
// Node; the desktop app-server is never reachable through this command.
func (a *CodexAdapter) quarantineProcess(generation uint64) {
	a.lifecycleMu.Lock()
	a.mu.Lock()
	if a.closed || !a.isCurrentGenerationLocked(generation) || a.cmd == nil || a.processDone == nil {
		a.mu.Unlock()
		a.lifecycleMu.Unlock()
		return
	}
	a.quarantined = true
	target := codexProcess{cmd: a.cmd, stdin: a.stdin, done: a.processDone, generation: generation}
	stopOverride := a.stopProcessOverride
	stopTargetOverride := a.stopTargetOverride
	a.mu.Unlock()
	a.lifecycleMu.Unlock()

	stopCtx, cancel := context.WithTimeout(context.Background(), codexAppServerStopWait)
	defer cancel()
	if stopTargetOverride != nil {
		stopDone := make(chan error, 1)
		go func() { stopDone <- stopTargetOverride(stopCtx, target) }()
		select {
		case <-stopDone:
		case <-stopCtx.Done():
		}
		return
	}
	if stopOverride != nil {
		stopDone := make(chan error, 1)
		go func() { stopDone <- stopOverride(stopCtx, target.cmd) }()
		select {
		case <-stopDone:
		case <-stopCtx.Done():
		}
		return
	}
	_ = a.stopProcessTarget(stopCtx, target)
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
	return a.writeMessage(stdin, message)
}

func (a *CodexAdapter) writeMessage(stdin io.Writer, value any) error {
	a.rpcWriteMu.Lock()
	defer a.rpcWriteMu.Unlock()
	if stdin == nil {
		return node.ErrAgentProviderUnavailable
	}
	return writeCodexJSONLine(stdin, value)
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

func (a *CodexAdapter) readLoop(reader io.Reader, generation ...uint64) {
	gen := a.currentGeneration()
	if len(generation) > 0 {
		gen = generation[0]
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), codexRPCLineLimit)
	for scanner.Scan() {
		a.handleRPCMessageForGeneration(gen, scanner.Bytes())
	}
	if err := scanner.Err(); err != nil {
		a.logger.Debug("Codex app-server stdout ended", "error", err)
	}
}

func (a *CodexAdapter) handleRPCMessage(raw []byte) {
	a.handleRPCMessageForGeneration(a.currentGeneration(), raw)
}

func (a *CodexAdapter) handleRPCMessageForGeneration(generation uint64, raw []byte) {
	if !a.isCurrentGeneration(generation) {
		return
	}
	var message codexRPCMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		a.logger.Debug("invalid Codex app-server message", "error", err)
		return
	}
	if len(message.ID) > 0 && (len(message.Result) > 0 || message.Error != nil) {
		id, err := codexResponseID(message.ID)
		if err != nil {
			return
		}
		a.mu.Lock()
		pending, ok := a.pending[id]
		if ok && pending.generation == generation {
			delete(a.pending, id)
		}
		a.mu.Unlock()
		if ok && pending.generation == generation {
			pending.ch <- message
		}
		return
	}
	if len(message.ID) > 0 && message.Method != "" {
		a.handleServerRequestForGeneration(generation, message.ID, message.Method, message.Params)
		return
	}
	if message.Method != "" {
		a.handleNotificationForGeneration(generation, message.Method, message.Params)
	}
}

func (a *CodexAdapter) currentGeneration() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.generation
}

func (a *CodexAdapter) isCurrentGeneration(generation uint64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.isCurrentGenerationLocked(generation)
}

func (a *CodexAdapter) isCurrentGenerationLocked(generation uint64) bool {
	return a.generation == generation && !a.quarantined && (generation == 0 || a.cmd != nil)
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

func (a *CodexAdapter) handleServerRequest(id json.RawMessage, method string, rawParams json.RawMessage) {
	a.handleServerRequestForGeneration(a.currentGeneration(), id, method, rawParams)
}

func (a *CodexAdapter) handleServerRequestForGeneration(generation uint64, id json.RawMessage, method string, rawParams json.RawMessage) {
	if !a.isCurrentGeneration(generation) {
		return
	}
	requestID, err := codexRequestIDString(id)
	if err != nil {
		if target, ok := a.captureProcessForGeneration(generation); ok {
			a.replyServerRequestErrorTarget(target, id, -32600, "invalid Codex server request id")
		}
		return
	}
	var params map[string]any
	if len(rawParams) > 0 && string(rawParams) != "null" {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			if target, ok := a.captureProcessForGeneration(generation); ok {
				a.replyServerRequestErrorTarget(target, id, -32602, "invalid Codex server request params")
			}
			return
		}
	}
	if params == nil {
		params = map[string]any{}
	}
	sessionID := mapString(params, "threadId")
	turnID := mapString(params, "turnId")
	requestType, respondable := codexServerRequestType(method)
	if !respondable {
		a.lifecycleMu.Lock()
		target, ok := a.captureProcessForGenerationLocked(generation)
		if !ok {
			a.lifecycleMu.Unlock()
			return
		}
		event := AgentEvent{
			Type:        requestType,
			SessionID:   sessionID,
			TurnID:      turnID,
			RequestID:   requestID,
			RequestType: method,
			State:       "unsupported",
			Detail:      codexServerRequestDetail(method, params),
			Timestamp:   protocolTimestampNow(),
		}
		observer := a.appendEvent(event)
		a.lifecycleMu.Unlock()
		if observer != nil {
			observer(event)
		}
		a.replyServerRequestErrorTarget(target, id, -32601, "Fast Spider bridge does not expose this Codex server request: "+method)
		return
	}

	a.lifecycleMu.Lock()
	a.mu.Lock()
	if !a.isCurrentGenerationLocked(generation) {
		a.mu.Unlock()
		a.lifecycleMu.Unlock()
		return
	}
	target := codexProcess{cmd: a.cmd, stdin: a.stdin, done: a.processDone, generation: generation}
	a.serverMu.Lock()
	if _, duplicate := a.serverRequests[requestID]; duplicate {
		a.serverMu.Unlock()
		a.mu.Unlock()
		a.lifecycleMu.Unlock()
		a.replyServerRequestErrorTarget(target, id, -32600, "duplicate pending Codex server request id")
		return
	}
	if len(a.serverRequests) >= 64 {
		a.serverMu.Unlock()
		a.mu.Unlock()
		a.lifecycleMu.Unlock()
		a.replyServerRequestErrorTarget(target, id, -32000, "too many pending Codex server requests")
		return
	}
	a.serverRequests[requestID] = codexServerRequest{
		RawID:      append(json.RawMessage(nil), id...),
		RequestID:  requestID,
		Method:     method,
		SessionID:  sessionID,
		TurnID:     turnID,
		Params:     params,
		ReceivedAt: time.Now().UTC(),
		generation: generation,
	}
	a.serverMu.Unlock()
	a.mu.Unlock()
	event := AgentEvent{
		Type:        requestType,
		SessionID:   sessionID,
		TurnID:      turnID,
		RequestID:   requestID,
		RequestType: method,
		State:       "pending",
		Detail:      codexServerRequestDetail(method, params),
		Timestamp:   protocolTimestampNow(),
	}
	observer := a.appendEvent(event)
	a.lifecycleMu.Unlock()
	if observer != nil {
		observer(event)
	}
}

func anyRequestIDString(value any) string {
	switch current := value.(type) {
	case string:
		return strings.TrimSpace(current)
	case float64:
		if current == float64(int64(current)) {
			return strconv.FormatInt(int64(current), 10)
		}
	case int64:
		return strconv.FormatInt(current, 10)
	case json.Number:
		return current.String()
	}
	return ""
}

func codexRequestIDString(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			return "", fmt.Errorf("empty request id")
		}
		return text, nil
	}
	var number int64
	if err := json.Unmarshal(raw, &number); err == nil {
		return strconv.FormatInt(number, 10), nil
	}
	return "", fmt.Errorf("invalid request id")
}

func codexServerRequestType(method string) (string, bool) {
	switch method {
	case "item/tool/requestUserInput":
		return "user_input.requested", true
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		return "approval.requested", true
	case "mcpServer/elicitation/request":
		return "mcp_elicitation.requested", true
	case "item/permissions/requestApproval":
		return "permission.requested", false
	default:
		return "server_request.unsupported", false
	}
}

func codexServerRequestDetail(method string, params map[string]any) map[string]any {
	detail := map[string]any{}
	copyKeys := func(keys ...string) {
		for _, key := range keys {
			if value, ok := params[key]; ok {
				detail[key] = value
			}
		}
	}
	switch method {
	case "item/tool/requestUserInput":
		copyKeys("itemId", "questions", "autoResolutionMs")
	case "item/commandExecution/requestApproval":
		copyKeys("itemId", "command", "cwd", "reason")
	case "item/fileChange/requestApproval":
		copyKeys("itemId", "reason", "grantRoot")
	case "item/permissions/requestApproval":
		copyKeys("itemId", "cwd", "reason")
	case "mcpServer/elicitation/request":
		copyKeys("serverName", "mode", "message", "url", "requestedSchema", "elicitationId")
	default:
		copyKeys("itemId")
	}
	return boundedAgentMap(detail, 32<<10)
}

func boundedAgentMap(value map[string]any, maxBytes int) map[string]any {
	if len(value) == 0 {
		return nil
	}
	raw, err := json.Marshal(value)
	if err == nil && len(raw) <= maxBytes {
		return value
	}
	return map[string]any{"truncated": true}
}

func (a *CodexAdapter) replyServerRequestError(id json.RawMessage, code int, message string) {
	a.replyServerRequestErrorForGeneration(a.currentGeneration(), id, code, message)
}

func (a *CodexAdapter) replyServerRequestErrorForGeneration(generation uint64, id json.RawMessage, code int, message string) {
	a.mu.Lock()
	if !a.isCurrentGenerationLocked(generation) {
		a.mu.Unlock()
		return
	}
	target := codexProcess{cmd: a.cmd, stdin: a.stdin, done: a.processDone, generation: generation}
	a.mu.Unlock()
	a.replyServerRequestErrorTarget(target, id, code, message)
}

func (a *CodexAdapter) captureProcessForGeneration(generation uint64) (codexProcess, bool) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	return a.captureProcessForGenerationLocked(generation)
}

func (a *CodexAdapter) captureProcessForGenerationLocked(generation uint64) (codexProcess, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.isCurrentGenerationLocked(generation) {
		return codexProcess{}, false
	}
	return codexProcess{cmd: a.cmd, stdin: a.stdin, done: a.processDone, generation: generation}, true
}

func (a *CodexAdapter) replyServerRequestErrorTarget(target codexProcess, id json.RawMessage, code int, message string) {
	stdin := target.stdin
	if stdin == nil {
		return
	}
	var rawID any
	if err := json.Unmarshal(id, &rawID); err != nil {
		return
	}
	_ = a.writeMessage(stdin, map[string]any{
		"id": rawID,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func (a *CodexAdapter) stderrLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 8*1024), 256*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			a.logger.Debug("Codex app-server stderr", "errorClass", classifyExecutionText(line))
		}
	}
}

func (a *CodexAdapter) waitLoop(cmd *exec.Cmd, generation uint64, done chan struct{}) {
	a.finishProcess(cmd, generation, done, cmd.Wait())
}

func (a *CodexAdapter) finishProcess(cmd *exec.Cmd, generation uint64, done chan struct{}, err error) {
	a.lifecycleMu.Lock()
	a.mu.Lock()
	isCurrent := a.cmd == cmd && a.generation == generation
	if isCurrent {
		a.cmd = nil
		a.stdin = nil
		a.processDone = nil
		a.quarantined = false
		a.configErr = nil
	}
	pending := make([]codexPending, 0)
	for id, item := range a.pending {
		if item.generation == generation {
			pending = append(pending, item)
			delete(a.pending, id)
		}
	}
	if isCurrent {
		a.loaded = make(map[string]struct{})
		a.loadedGeneration = make(map[string]uint64)
	}
	closed := a.closed
	a.mu.Unlock()
	a.serverMu.Lock()
	if isCurrent {
		a.serverRequests = make(map[string]codexServerRequest)
	} else {
		for id, request := range a.serverRequests {
			if request.generation == generation {
				delete(a.serverRequests, id)
			}
		}
	}
	a.serverMu.Unlock()
	a.eventMu.Lock()
	activeTurns := make(map[string]string)
	for sessionID, turnID := range a.activeTurns {
		trackedGeneration := a.activeTurnGeneration[sessionID]
		if isCurrent || trackedGeneration == generation || trackedGeneration == 0 && isCurrent {
			activeTurns[sessionID] = turnID
			delete(a.activeTurns, sessionID)
			delete(a.activeTurnGeneration, sessionID)
		}
	}
	if isCurrent {
		a.activeTurns = make(map[string]string)
		a.activeTurnGeneration = make(map[string]uint64)
	}
	a.eventMu.Unlock()
	type pendingEvent struct {
		event    AgentEvent
		observer func(AgentEvent)
	}
	failedEvents := make([]pendingEvent, 0, len(activeTurns))
	for sessionID, turnID := range activeTurns {
		event := AgentEvent{
			Type:      "turn.failed",
			SessionID: sessionID,
			TurnID:    turnID,
			State:     "failed",
			Text:      publicErrorMessage(ErrorRuntimeUnavailable),
			Detail:    map[string]any{"errorClass": ErrorRuntimeUnavailable},
			Timestamp: protocolTimestampNow(),
		}
		failedEvents = append(failedEvents, pendingEvent{event: event, observer: a.appendEvent(event)})
	}
	a.lifecycleMu.Unlock()
	for _, item := range failedEvents {
		if item.observer != nil {
			item.observer(item.event)
		}
	}
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
	a.handleNotificationForGeneration(a.currentGeneration(), method, raw)
}

func (a *CodexAdapter) handleNotificationForGeneration(generation uint64, method string, raw json.RawMessage) {
	if !a.isCurrentGeneration(generation) {
		return
	}
	if beforeCommit := a.notificationBeforeCommit; beforeCommit != nil {
		beforeCommit(generation)
	}
	a.lifecycleMu.Lock()
	a.mu.Lock()
	if !a.isCurrentGenerationLocked(generation) {
		a.mu.Unlock()
		a.lifecycleMu.Unlock()
		return
	}
	target := codexProcess{cmd: a.cmd, stdin: a.stdin, done: a.processDone, generation: generation}
	a.mu.Unlock()
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
	case "configWarning":
		class := classifyExecutionText(string(raw))
		if class == ErrorConfigInvalid {
			a.mu.Lock()
			if a.generation == generation && (generation == 0 || a.cmd != nil) {
				a.configErr = &ExecutionError{
					Class:     ErrorConfigInvalid,
					Provider:  "codex",
					Operation: "configuration",
					debugText: "Codex reported an incompatible configuration",
				}
			}
			a.mu.Unlock()
		}
		event.Type = "warning"
		event.Text = publicErrorMessage(class)
		event.Detail = map[string]any{"errorClass": class}
	case "turn/started":
		event.Type = "turn.started"
		event.State = "running"
		if sessionID != "" && turnID != "" {
			a.eventMu.Lock()
			if a.activeTurnGeneration == nil {
				a.activeTurnGeneration = make(map[string]uint64)
			}
			if a.activeTurns == nil {
				a.activeTurns = make(map[string]string)
			}
			a.activeTurns[sessionID] = turnID
			a.activeTurnGeneration[sessionID] = generation
			a.eventMu.Unlock()
		}
	case "turn/completed":
		event.Type = "turn.completed"
		event.State = normalizedCodexTurnStatus(mapNestedString(params, "turn", "status"))
		if event.State == "" {
			event.State = "completed"
		}
		if event.State == "canceled" {
			event.Type = "turn.interrupted"
		}
		if sessionID != "" {
			a.eventMu.Lock()
			if trackedGeneration := a.activeTurnGeneration[sessionID]; trackedGeneration == generation || generation == 0 {
				delete(a.activeTurns, sessionID)
				delete(a.activeTurnGeneration, sessionID)
			}
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
	case "serverRequest/resolved":
		event.Type = "request.resolved"
		event.RequestID = anyRequestIDString(params["requestId"])
		if event.RequestID != "" {
			a.serverMu.Lock()
			if request, ok := a.serverRequests[event.RequestID]; ok && (request.generation == generation || generation == 0) {
				delete(a.serverRequests, event.RequestID)
			}
			a.serverMu.Unlock()
		}
	case "warning", "error":
		event.Type = method
		rawText := firstNonEmptyString(
			mapString(params, "message"),
			mapNestedString(params, "error", "message"),
			mapNestedString(params, "error", "msg"),
		)
		class := classifyExecutionText(rawText)
		event.Text = publicErrorMessage(class)
		event.Detail = map[string]any{"errorClass": class}
	}
	observer := a.appendEvent(event)
	shouldUnload := method == "turn/completed" && sessionID != ""
	a.lifecycleMu.Unlock()
	if observer != nil {
		observer(event)
	}
	if shouldUnload {
		targetForUnload := target
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := a.unloadThreadForGeneration(ctx, targetForUnload, sessionID); err != nil {
				a.logger.Debug("unload completed Codex thread", "sessionId", sessionID, "error", err)
			}
		}()
	}
}

func (a *CodexAdapter) recordEvent(event AgentEvent) {
	observer := a.appendEvent(event)
	if observer != nil {
		observer(event)
	}
}

// appendEvent mutates the event stream and returns the observer callback for
// the caller to invoke after releasing any lifecycle locks.
func (a *CodexAdapter) appendEvent(event AgentEvent) func(AgentEvent) {
	a.eventMu.Lock()
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
	observer := a.eventObserver
	a.eventMu.Unlock()
	return observer
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

func (a *CodexAdapter) PendingRequests(sessionID string) []map[string]any {
	a.serverMu.Lock()
	items := make([]codexServerRequest, 0, len(a.serverRequests))
	for _, item := range a.serverRequests {
		if !item.Responding && (sessionID == "" || item.SessionID == sessionID) {
			items = append(items, item)
		}
	}
	a.serverMu.Unlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].ReceivedAt.Equal(items[j].ReceivedAt) {
			return items[i].RequestID < items[j].RequestID
		}
		return items[i].ReceivedAt.Before(items[j].ReceivedAt)
	})
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{
			"requestId":   item.RequestID,
			"requestType": item.Method,
			"turnId":      item.TurnID,
			"receivedAt":  item.ReceivedAt.Format(time.RFC3339Nano),
			"detail":      codexServerRequestDetail(item.Method, item.Params),
		})
	}
	return out
}

func (a *CodexAdapter) ListModels(ctx context.Context) (map[string]any, error) {
	if value, ok := a.modelsCache.get("models"); ok {
		return value, nil
	}
	result, err := a.request(ctx, "model/list", map[string]any{})
	if err == nil {
		a.modelsCache.set("models", result)
	}
	return result, err
}

func (a *CodexAdapter) ProviderCapabilities(ctx context.Context) (map[string]any, error) {
	return a.request(ctx, "modelProvider/capabilities/read", map[string]any{})
}

func (a *CodexAdapter) ListHooks(ctx context.Context, cwd string) (map[string]any, error) {
	params := map[string]any{}
	if strings.TrimSpace(cwd) != "" {
		params["cwds"] = []string{cwd}
	}
	return a.request(ctx, "hooks/list", params)
}

func (a *CodexAdapter) ListPermissionProfiles(ctx context.Context, cwd string, limit int, cursor string) (map[string]any, error) {
	params := map[string]any{}
	if strings.TrimSpace(cwd) != "" {
		params["cwd"] = cwd
	}
	if limit > 0 {
		params["limit"] = limit
	}
	if strings.TrimSpace(cursor) != "" {
		params["cursor"] = cursor
	}
	return a.request(ctx, "permissionProfile/list", params)
}

func (a *CodexAdapter) ListMCPServerStatus(ctx context.Context, sessionID, detail string, limit int, cursor string) (map[string]any, error) {
	params := map[string]any{}
	if strings.TrimSpace(sessionID) != "" {
		params["threadId"] = sessionID
	}
	if strings.TrimSpace(detail) != "" {
		params["detail"] = detail
	}
	if limit > 0 {
		params["limit"] = limit
	}
	if strings.TrimSpace(cursor) != "" {
		params["cursor"] = cursor
	}
	return a.request(ctx, "mcpServerStatus/list", params)
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
	return a.ReadThreadMetadata(ctx, sessionID)
}

func (a *CodexAdapter) ReadThreadMetadata(ctx context.Context, sessionID string) (map[string]any, error) {
	return a.request(ctx, "thread/read", map[string]any{"threadId": sessionID, "includeTurns": false})
}

func isCodexThreadNotMaterialized(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(executionDebugText(err))
	return strings.Contains(message, "not materialized yet") && strings.Contains(message, "includeturns")
}

func isCodexThreadArchived(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(executionDebugText(err))
	return strings.Contains(message, "is archived") && strings.Contains(message, "unarchive")
}

func isCodexThreadWriterConflict(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(executionDebugText(err)), "already has an active writer")
}

func (a *CodexAdapter) StartThread(ctx context.Context, workingDirectory, projectDirectory, model, thinking string) (map[string]any, error) {
	return a.StartThreadWithOptions(ctx, workingDirectory, projectDirectory, model, thinking, false)
}

func (a *CodexAdapter) StartThreadWithOptions(ctx context.Context, workingDirectory, projectDirectory, model, thinking string, ephemeral bool) (map[string]any, error) {
	params := codexThreadStartParamsWithEphemeral(workingDirectory, projectDirectory, model, thinking, ephemeral)
	result, err := a.request(ctx, "thread/start", params)
	if err != nil {
		return nil, err
	}
	if sessionID := mapNestedString(result, "thread", "id"); sessionID != "" {
		a.markThreadLoaded(sessionID)
	}
	return result, nil
}

func (a *CodexAdapter) markThreadLoaded(sessionID string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	a.mu.Lock()
	if a.loaded == nil {
		a.loaded = make(map[string]struct{})
	}
	if a.loadedGeneration == nil {
		a.loadedGeneration = make(map[string]uint64)
	}
	a.loaded[sessionID] = struct{}{}
	a.loadedGeneration[sessionID] = a.generation
	a.mu.Unlock()
}

func (a *CodexAdapter) ensureThreadLoaded(ctx context.Context, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("sessionId is required")
	}
	unlock := a.lockSessionLoad(sessionID)
	defer unlock()
	return a.ensureThreadLoadedLocked(ctx, sessionID)
}

func (a *CodexAdapter) ensureThreadLoadedLocked(ctx context.Context, sessionID string) error {
	a.mu.Lock()
	loaded := a.loadedForGenerationLocked(sessionID, a.generation)
	a.mu.Unlock()
	if loaded {
		return nil
	}
	if _, err := a.request(ctx, "thread/resume", map[string]any{"threadId": sessionID}); err != nil {
		return err
	}
	a.markThreadLoaded(sessionID)
	return nil
}

func (a *CodexAdapter) unloadThread(ctx context.Context, sessionID string) error {
	if override := a.unloadThreadOverride; override != nil {
		return override(ctx, sessionID)
	}
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("sessionId is required")
	}
	unlock := a.lockSessionLoad(sessionID)
	defer unlock()
	return a.unloadThreadLocked(ctx, sessionID)
}

func (a *CodexAdapter) unloadThreadForGeneration(ctx context.Context, target codexProcess, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("sessionId is required")
	}
	unlock := a.lockSessionLoad(sessionID)
	defer unlock()
	if a.unloadBeforeSend != nil {
		a.unloadBeforeSend(target)
	}
	a.lifecycleMu.Lock()
	a.mu.Lock()
	valid := a.isCurrentGenerationLocked(target.generation) && a.cmd == target.cmd && a.stdin == target.stdin && a.processDone == target.done && a.loadedForGenerationLocked(sessionID, target.generation)
	a.mu.Unlock()
	a.lifecycleMu.Unlock()
	if !valid {
		return nil
	}
	if a.ActiveTurn(sessionID) != "" {
		return nil
	}
	if override := a.unloadThreadOverride; override != nil {
		if err := override(ctx, sessionID); err != nil {
			return err
		}
	} else if override := a.requestOverride; override != nil {
		if _, err := override(ctx, "thread/unsubscribe", map[string]any{"threadId": sessionID}); err != nil && !isCodexThreadAlreadyUnsubscribed(err) {
			return err
		}
	} else if _, err := a.requestOnTarget(ctx, target, "thread/unsubscribe", map[string]any{"threadId": sessionID}); err != nil && !isCodexThreadAlreadyUnsubscribed(err) {
		return err
	}
	a.lifecycleMu.Lock()
	a.mu.Lock()
	if a.generation == target.generation && a.cmd == target.cmd && a.loadedForGenerationLocked(sessionID, target.generation) {
		delete(a.loaded, sessionID)
		delete(a.loadedGeneration, sessionID)
	}
	a.mu.Unlock()
	a.lifecycleMu.Unlock()
	return nil
}

func (a *CodexAdapter) unloadThreadLocked(ctx context.Context, sessionID string) error {
	if a.ActiveTurn(sessionID) != "" {
		return nil
	}
	a.mu.Lock()
	targetGeneration := a.generation
	loaded := a.loadedForGenerationLocked(sessionID, targetGeneration)
	a.mu.Unlock()
	if !loaded {
		return nil
	}
	_, err := a.request(ctx, "thread/unsubscribe", map[string]any{"threadId": sessionID})
	if err != nil && !isCodexThreadAlreadyUnsubscribed(err) {
		return err
	}
	a.mu.Lock()
	delete(a.loaded, sessionID)
	delete(a.loadedGeneration, sessionID)
	a.mu.Unlock()
	return nil
}

func (a *CodexAdapter) loadedForGenerationLocked(sessionID string, generation uint64) bool {
	if _, loaded := a.loaded[sessionID]; !loaded {
		return false
	}
	loadedGeneration, tracked := a.loadedGeneration[sessionID]
	return loadedGeneration == generation || !tracked && generation == 0
}

func isCodexThreadAlreadyUnsubscribed(err error) bool {
	if err == nil || isAgentSessionNotFound(err) {
		return true
	}
	message := strings.ToLower(executionDebugText(err))
	return containsAny(message, "not subscribed", "already unsubscribed")
}

func codexThreadStartParams(workingDirectory, projectDirectory, model, thinking string) map[string]any {
	return codexThreadStartParamsWithEphemeral(workingDirectory, projectDirectory, model, thinking, false)
}

func codexThreadStartParamsWithEphemeral(workingDirectory, projectDirectory, model, thinking string, ephemeral bool) map[string]any {
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
		"ephemeral":             ephemeral,
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

type codexTurnOptions struct {
	WorkingDirectory string
	Model            string
	Effort           string
	Summary          string
	Personality      string
	ServiceTier      string
	OutputSchema     map[string]any
}

func (a *CodexAdapter) StartTurn(ctx context.Context, sessionID, prompt, workingDirectory, model, thinking string) (map[string]any, error) {
	return a.StartTurnWithInputs(ctx, sessionID, buildAgentTurnInputs(prompt, nil, nil, nil, nil, workingDirectory), workingDirectory, model, thinking, nil)
}

func buildAgentTurnInputs(prompt string, skills []agentSkillInput, images, localImages []string, mentions []agentMentionInput, workingDirectory string) []map[string]any {
	return buildAgentTurnInputsWithDetail(prompt, skills, images, localImages, mentions, "")
}

func buildAgentTurnInputsWithDetail(prompt string, skills []agentSkillInput, images, localImages []string, mentions []agentMentionInput, imageDetail string) []map[string]any {
	inputs := make([]map[string]any, 0, 1+len(skills)+len(images)+len(localImages)+len(mentions))
	if strings.TrimSpace(prompt) != "" {
		inputs = append(inputs, map[string]any{"type": "text", "text": prompt, "text_elements": []any{}})
	}
	for _, skill := range skills {
		if strings.TrimSpace(skill.Name) != "" && strings.TrimSpace(skill.Path) != "" {
			inputs = append(inputs, map[string]any{"type": "skill", "name": skill.Name, "path": skill.Path})
		}
	}
	for _, image := range images {
		if strings.TrimSpace(image) != "" {
			entry := map[string]any{"type": "image", "url": image}
			if strings.TrimSpace(imageDetail) != "" {
				entry["detail"] = imageDetail
			}
			inputs = append(inputs, entry)
		}
	}
	for _, image := range localImages {
		if strings.TrimSpace(image) != "" {
			entry := map[string]any{"type": "localImage", "path": image}
			if strings.TrimSpace(imageDetail) != "" {
				entry["detail"] = imageDetail
			}
			inputs = append(inputs, entry)
		}
	}
	for _, mention := range mentions {
		if strings.TrimSpace(mention.Name) != "" && strings.TrimSpace(mention.Path) != "" {
			inputs = append(inputs, map[string]any{"type": "mention", "name": mention.Name, "path": mention.Path})
		}
	}
	return inputs
}

func (a *CodexAdapter) StartTurnWithInputs(ctx context.Context, sessionID string, inputs []map[string]any, workingDirectory, model, thinking string, outputSchema map[string]any) (map[string]any, error) {
	return a.StartTurnWithOptions(ctx, sessionID, inputs, codexTurnOptions{
		WorkingDirectory: workingDirectory,
		Model:            model,
		Effort:           thinking,
		OutputSchema:     outputSchema,
	})
}

func (a *CodexAdapter) StartTurnWithOptions(ctx context.Context, sessionID string, inputs []map[string]any, options codexTurnOptions) (map[string]any, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("at least one valid turn input is required")
	}
	if len(inputs) > 64 {
		return nil, fmt.Errorf("turn input count exceeds 64")
	}
	params := map[string]any{
		"threadId": sessionID,
		"input":    inputs,
	}
	if options.OutputSchema != nil {
		if err := validateOutputSchema(options.OutputSchema); err != nil {
			return nil, err
		}
		params["outputSchema"] = options.OutputSchema
	}
	for key, value := range map[string]string{
		"cwd":         options.WorkingDirectory,
		"model":       options.Model,
		"effort":      options.Effort,
		"summary":     options.Summary,
		"personality": options.Personality,
		"serviceTier": options.ServiceTier,
	} {
		if strings.TrimSpace(value) != "" {
			params[key] = value
		}
	}
	unlock := a.lockSessionLoad(sessionID)
	defer unlock()
	if active := a.ActiveTurn(sessionID); active != "" {
		return nil, node.ErrAgentSessionBusy
	}
	if err := a.ensureThreadLoadedLocked(ctx, sessionID); err != nil {
		return nil, err
	}
	result, err := a.request(ctx, "turn/start", params)
	if err != nil {
		return nil, err
	}
	turnID := mapNestedString(result, "turn", "id")
	if turnID != "" {
		generation := a.currentGeneration()
		a.lifecycleMu.Lock()
		defer a.lifecycleMu.Unlock()
		a.mu.Lock()
		if !a.isCurrentGenerationLocked(generation) {
			a.mu.Unlock()
			return nil, node.ErrAgentProviderUnavailable
		}
		a.mu.Unlock()
		a.eventMu.Lock()
		if a.activeTurnGeneration == nil {
			a.activeTurnGeneration = make(map[string]uint64)
		}
		a.activeTurns[sessionID] = turnID
		a.activeTurnGeneration[sessionID] = generation
		a.eventMu.Unlock()
	}
	return result, nil
}

func (a *CodexAdapter) SteerTurn(ctx context.Context, sessionID, expectedTurnID string, inputs []map[string]any) (map[string]any, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("at least one valid turn input is required")
	}
	if len(inputs) > 64 {
		return nil, fmt.Errorf("turn input count exceeds 64")
	}
	if strings.TrimSpace(expectedTurnID) == "" {
		return nil, fmt.Errorf("expectedTurnId is required")
	}
	if err := a.ensureThreadLoaded(ctx, sessionID); err != nil {
		return nil, err
	}
	return a.request(ctx, "turn/steer", map[string]any{
		"threadId":       sessionID,
		"expectedTurnId": expectedTurnID,
		"input":          inputs,
	})
}

func (a *CodexAdapter) RespondPendingRequest(ctx context.Context, sessionID, requestID string, input agentControlParams) (map[string]any, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, fmt.Errorf("requestId is required")
	}
	a.serverMu.Lock()
	pending, ok := a.serverRequests[requestID]
	if !ok {
		a.serverMu.Unlock()
		return nil, fmt.Errorf("pending Codex request %q was not found", requestID)
	}
	if pending.SessionID != "" && pending.SessionID != sessionID {
		a.serverMu.Unlock()
		return nil, fmt.Errorf("pending Codex request belongs to a different session")
	}
	if pending.Responding {
		a.serverMu.Unlock()
		return nil, fmt.Errorf("pending Codex request %q is already being responded to", requestID)
	}
	pending.Responding = true
	a.serverRequests[requestID] = pending
	a.serverMu.Unlock()
	generation := pending.generation
	responded := false
	defer func() {
		if responded {
			return
		}
		a.serverMu.Lock()
		if current, exists := a.serverRequests[requestID]; exists && current.Responding {
			current.Responding = false
			a.serverRequests[requestID] = current
		}
		a.serverMu.Unlock()
	}()
	result, state, err := codexServerRequestResponse(pending, input)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	if generation != a.generation || (generation != 0 && (a.cmd == nil || a.processDone == nil)) {
		a.mu.Unlock()
		return nil, node.ErrAgentProviderUnavailable
	}
	stdin := a.stdin
	a.mu.Unlock()
	if stdin == nil {
		return nil, node.ErrAgentProviderUnavailable
	}
	var rawID any
	if err := json.Unmarshal(pending.RawID, &rawID); err != nil {
		return nil, fmt.Errorf("decode pending Codex request id: %w", err)
	}
	if err := a.writeMessage(stdin, map[string]any{"id": rawID, "result": result}); err != nil {
		return nil, err
	}
	a.serverMu.Lock()
	delete(a.serverRequests, requestID)
	a.serverMu.Unlock()
	responded = true
	a.recordEvent(AgentEvent{
		Type:        "request.responded",
		SessionID:   sessionID,
		TurnID:      pending.TurnID,
		RequestID:   requestID,
		RequestType: pending.Method,
		State:       state,
		Timestamp:   protocolTimestampNow(),
	})
	return map[string]any{
		"sessionId":   sessionID,
		"turnId":      pending.TurnID,
		"requestId":   requestID,
		"requestType": pending.Method,
		"responded":   true,
		"decision":    state,
	}, nil
}

func codexServerRequestResponse(pending codexServerRequest, input agentControlParams) (map[string]any, string, error) {
	switch pending.Method {
	case "item/tool/requestUserInput":
		if len(input.Answers) == 0 {
			return nil, "", fmt.Errorf("answers are required for requestUserInput")
		}
		questions, _ := pending.Params["questions"].([]any)
		allowed := make(map[string]struct{}, len(questions))
		for _, raw := range questions {
			question, _ := raw.(map[string]any)
			if id := mapString(question, "id"); id != "" {
				allowed[id] = struct{}{}
			}
		}
		answers := make(map[string]any, len(input.Answers))
		for id, values := range input.Answers {
			if _, ok := allowed[id]; !ok {
				return nil, "", fmt.Errorf("answer references unknown question %q", id)
			}
			if len(values) == 0 || len(values) > 16 {
				return nil, "", fmt.Errorf("question %q must contain 1-16 answers", id)
			}
			for _, value := range values {
				if len(value) > 4096 {
					return nil, "", fmt.Errorf("answer for question %q exceeds 4096 characters", id)
				}
			}
			answers[id] = map[string]any{"answers": values}
		}
		return map[string]any{"answers": answers}, "answered", nil
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		decision := strings.TrimSpace(input.Decision)
		if !stringInSet(decision, "accept", "decline", "cancel") {
			return nil, "", fmt.Errorf("decision must be accept, decline, or cancel")
		}
		return map[string]any{"decision": decision}, decision, nil
	case "mcpServer/elicitation/request":
		decision := strings.TrimSpace(input.Decision)
		if !stringInSet(decision, "accept", "decline", "cancel") {
			return nil, "", fmt.Errorf("decision must be accept, decline, or cancel")
		}
		result := map[string]any{"action": decision}
		if decision == "accept" {
			mode := mapString(pending.Params, "mode")
			if mode == "form" && len(input.ResponseContent) == 0 {
				return nil, "", fmt.Errorf("responseContent is required when accepting a form MCP elicitation")
			}
			if len(input.ResponseContent) > 0 {
				if raw, err := json.Marshal(input.ResponseContent); err != nil || len(raw) > 64<<10 {
					return nil, "", fmt.Errorf("responseContent must be valid JSON and at most 65536 bytes")
				}
				result["content"] = input.ResponseContent
			}
		}
		return result, decision, nil
	default:
		return nil, "", fmt.Errorf("Codex request type %q is not respondable", pending.Method)
	}
}

func validateOutputSchema(schema map[string]any) error {
	raw, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	if len(raw) > 64<<10 {
		return fmt.Errorf("outputSchema exceeds 65536 bytes")
	}
	var walk func(any, int) error
	walk = func(value any, depth int) error {
		if depth > 12 {
			return fmt.Errorf("outputSchema nesting exceeds 12 levels")
		}
		switch v := value.(type) {
		case map[string]any:
			for _, child := range v {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range v {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(schema, 0)
}

func codexRollbackParams(sessionID string, numTurns int) map[string]any {
	return map[string]any{"threadId": sessionID, "numTurns": numTurns}
}

func codexGoalSetParams(sessionID, objective, status string, tokenBudget int64) map[string]any {
	params := map[string]any{"threadId": sessionID}
	if strings.TrimSpace(objective) != "" {
		params["objective"] = objective
	}
	if strings.TrimSpace(status) != "" {
		params["status"] = status
	}
	if tokenBudget != 0 {
		params["tokenBudget"] = tokenBudget
	}
	return params
}

func codexSettingsUpdateParams(sessionID string, input agentControlParams) map[string]any {
	params := map[string]any{"threadId": sessionID}
	for key, value := range map[string]string{
		"model":       input.Model,
		"effort":      input.Effort,
		"cwd":         input.WorkingDirectory,
		"permissions": input.Permissions,
		"personality": input.Personality,
		"serviceTier": input.ServiceTier,
		"summary":     input.Summary,
	} {
		if strings.TrimSpace(value) != "" {
			params[key] = value
		}
	}
	return params
}

func codexReviewStartParams(sessionID string, input agentControlParams) map[string]any {
	delivery := strings.TrimSpace(input.ReviewDelivery)
	if delivery == "" {
		delivery = "inline"
	}
	targetType := strings.TrimSpace(input.ReviewType)
	if targetType == "" {
		targetType = "uncommittedChanges"
	}
	target := map[string]any{"type": targetType}
	switch targetType {
	case "baseBranch":
		target["branch"] = input.ReviewBranch
	case "commit":
		target["sha"] = input.ReviewSHA
		if strings.TrimSpace(input.ReviewTitle) != "" {
			target["title"] = input.ReviewTitle
		}
	case "custom":
		target["instructions"] = input.ReviewInstructions
	}
	return map[string]any{"threadId": sessionID, "delivery": delivery, "target": target}
}

func codexSkillsListParams(cwd string, forceReload bool) map[string]any {
	params := map[string]any{"forceReload": forceReload}
	if strings.TrimSpace(cwd) != "" {
		params["cwds"] = []string{cwd}
	}
	return params
}

func codexPluginListParams(cwd string, marketplaceKinds []string) map[string]any {
	params := map[string]any{}
	if strings.TrimSpace(cwd) != "" {
		params["cwds"] = []string{cwd}
	}
	if len(marketplaceKinds) > 0 {
		params["marketplaceKinds"] = marketplaceKinds
	}
	return params
}

func codexPluginReadParams(pluginName, marketplacePath, remoteMarketplaceName string) map[string]any {
	params := map[string]any{"pluginName": pluginName}
	if strings.TrimSpace(marketplacePath) != "" {
		params["marketplacePath"] = marketplacePath
	}
	if strings.TrimSpace(remoteMarketplaceName) != "" {
		params["remoteMarketplaceName"] = remoteMarketplaceName
	}
	return params
}

func codexPluginSkillReadParams(remoteMarketplaceName, remotePluginID, skillName string) map[string]any {
	return map[string]any{
		"remoteMarketplaceName": remoteMarketplaceName,
		"remotePluginId":        remotePluginID,
		"skillName":             skillName,
	}
}

func (a *CodexAdapter) InterruptTurn(ctx context.Context, sessionID, turnID string) error {
	activeTurnID := a.ActiveTurn(sessionID)
	if activeTurnID == "" {
		return node.ErrAgentSessionNotFound
	}
	if turnID == "" {
		turnID = activeTurnID
	}
	if turnID != activeTurnID {
		return node.ErrAgentSessionNotFound
	}
	params := map[string]any{"threadId": sessionID, "turnId": turnID}
	for attempt := 0; attempt < codexInterruptAttempts; attempt++ {
		_, err := a.request(ctx, "turn/interrupt", params)
		if err == nil {
			return nil
		}
		var responseErr *codexRPCResponseError
		if !errors.As(err, &responseErr) || responseErr.code != -32600 || attempt == codexInterruptAttempts-1 || a.ActiveTurn(sessionID) != turnID {
			return err
		}
		timer := time.NewTimer(codexInterruptRetryWait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func (a *CodexAdapter) RenameThread(ctx context.Context, sessionID, name string) error {
	_, err := a.request(ctx, "thread/name/set", map[string]any{"threadId": sessionID, "name": name})
	return err
}

func (a *CodexAdapter) ArchiveThread(ctx context.Context, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("sessionId is required")
	}
	unlock := a.lockSessionLoad(sessionID)
	defer unlock()
	if err := a.ensureThreadLoadedLocked(ctx, sessionID); err != nil {
		return err
	}
	if _, err := a.request(ctx, "thread/archive", map[string]any{"threadId": sessionID}); err != nil {
		return err
	}
	return a.unloadThreadLocked(ctx, sessionID)
}

func (a *CodexAdapter) UnarchiveThread(ctx context.Context, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("sessionId is required")
	}
	unlock := a.lockSessionLoad(sessionID)
	defer unlock()
	if _, err := a.request(ctx, "thread/unarchive", map[string]any{"threadId": sessionID}); err != nil {
		return err
	}
	a.mu.Lock()
	delete(a.loaded, sessionID)
	delete(a.loadedGeneration, sessionID)
	a.mu.Unlock()
	return a.ensureThreadLoadedLocked(ctx, sessionID)
}
func (a *CodexAdapter) DeleteThread(ctx context.Context, sessionID string) error {
	unlock := a.lockSessionLoad(sessionID)
	defer unlock()
	_, err := a.request(ctx, "thread/delete", map[string]any{"threadId": sessionID})
	if err == nil || isAgentSessionNotFound(err) {
		a.mu.Lock()
		delete(a.loaded, sessionID)
		a.mu.Unlock()
	}
	return err
}

func (a *CodexAdapter) lockSessionLoad(sessionID string) func() {
	a.loadLocksMu.Lock()
	if a.loadLocks == nil {
		a.loadLocks = make(map[string]*codexSessionLock)
	}
	lock := a.loadLocks[sessionID]
	if lock == nil {
		lock = &codexSessionLock{}
		a.loadLocks[sessionID] = lock
	}
	lock.refs++
	a.loadLocksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		a.loadLocksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(a.loadLocks, sessionID)
		}
		a.loadLocksMu.Unlock()
	}
}
func (a *CodexAdapter) ForkThread(ctx context.Context, sessionID, cwd string) (map[string]any, error) {
	p := map[string]any{"threadId": sessionID}
	if cwd != "" {
		p["cwd"] = cwd
	}
	result, err := a.request(ctx, "thread/fork", p)
	if err != nil {
		return nil, err
	}
	if forkedID := mapNestedString(result, "thread", "id"); forkedID != "" {
		a.markThreadLoaded(forkedID)
	}
	return result, nil
}
func (a *CodexAdapter) CompactThread(ctx context.Context, sessionID string) error {
	_, err := a.request(ctx, "thread/compact/start", map[string]any{"threadId": sessionID})
	return err
}
func (a *CodexAdapter) RollbackThread(ctx context.Context, sessionID string, numTurns int) error {
	_, err := a.request(ctx, "thread/rollback", codexRollbackParams(sessionID, numTurns))
	return err
}
func (a *CodexAdapter) GetGoal(ctx context.Context, sessionID string) (map[string]any, error) {
	return a.request(ctx, "thread/goal/get", map[string]any{"threadId": sessionID})
}
func (a *CodexAdapter) SetGoal(ctx context.Context, sessionID, objective, status string, tokenBudget int64) (map[string]any, error) {
	return a.request(ctx, "thread/goal/set", codexGoalSetParams(sessionID, objective, status, tokenBudget))
}
func (a *CodexAdapter) ClearGoal(ctx context.Context, sessionID string) (map[string]any, error) {
	return a.request(ctx, "thread/goal/clear", map[string]any{"threadId": sessionID})
}
func (a *CodexAdapter) UpdateSettings(ctx context.Context, sessionID string, input agentControlParams) (map[string]any, error) {
	return a.request(ctx, "thread/settings/update", codexSettingsUpdateParams(sessionID, input))
}

func (a *CodexAdapter) StartReview(ctx context.Context, sessionID string, input agentControlParams) (map[string]any, error) {
	if err := a.ensureThreadLoaded(ctx, sessionID); err != nil {
		return nil, err
	}
	return a.request(ctx, "review/start", codexReviewStartParams(sessionID, input))
}

func (a *CodexAdapter) ListSkills(ctx context.Context, cwd string, forceReload bool) (map[string]any, error) {
	return a.request(ctx, "skills/list", codexSkillsListParams(cwd, forceReload))
}

func (a *CodexAdapter) ListPlugins(ctx context.Context, cwd string, marketplaceKinds []string) (map[string]any, error) {
	return a.request(ctx, "plugin/list", codexPluginListParams(cwd, marketplaceKinds))
}

func (a *CodexAdapter) ListInstalledPlugins(ctx context.Context, cwd string) (map[string]any, error) {
	params := map[string]any{}
	if strings.TrimSpace(cwd) != "" {
		params["cwds"] = []string{cwd}
	}
	return a.request(ctx, "plugin/installed", params)
}

func (a *CodexAdapter) ReadPlugin(ctx context.Context, pluginName, marketplacePath, remoteMarketplaceName string) (map[string]any, error) {
	return a.request(ctx, "plugin/read", codexPluginReadParams(pluginName, marketplacePath, remoteMarketplaceName))
}

func (a *CodexAdapter) ReadPluginSkill(ctx context.Context, remoteMarketplaceName, remotePluginID, skillName string) (map[string]any, error) {
	return a.request(ctx, "plugin/skill/read", codexPluginSkillReadParams(remoteMarketplaceName, remotePluginID, skillName))
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
