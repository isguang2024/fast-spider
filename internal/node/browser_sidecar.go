package node

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
	"strings"
	"sync"
	"time"

	"github.com/isguang2024/fast-spider/internal/security"
)

const (
	browserSidecarProtocolVersion = "1.1"
	maxBrowserSidecarLineBytes    = 2 << 20
	browserSidecarCloseTimeout    = 5 * time.Second
	maxBrowserAvailabilityHolders = 128
	browserAvailabilityHolderTTL  = time.Minute
)

var (
	ErrBrowserUnavailable = errors.New("browser sidecar unavailable")
	ErrBrowserSidecarLost = errors.New("browser sidecar lost")
)

type browserSidecarError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type BrowserAvailabilityError struct {
	ReasonCode string
	Err        error
}

func (e *BrowserAvailabilityError) Error() string { return e.Err.Error() }
func (e *BrowserAvailabilityError) Unwrap() error { return e.Err }

type browserAvailabilityStatus struct {
	State      string `json:"state"`
	ReasonCode string `json:"reasonCode"`
	CheckedAt  string `json:"checkedAt"`
	CacheHit   bool   `json:"cacheHit"`
	TotalMs    int64  `json:"totalMs"`
}

type browserAvailabilityCache struct {
	status    browserAvailabilityStatus
	err       error
	expiresAt time.Time
}

type browserAvailabilityHolder struct {
	mu    sync.Mutex
	cache browserAvailabilityCache
}

type browserAvailabilityHolderEntry struct {
	holder   *browserAvailabilityHolder
	lastUsed time.Time
}

type browserAvailabilityHolderRegistry struct {
	mu      sync.Mutex
	entries map[string]browserAvailabilityHolderEntry
}

// NodeUI builds a lightweight local capability client on every diagnostics
// refresh. Keep availability probes keyed by the resolved component directory
// so those clients share the same 30s/5s cache without sharing browser sessions.
var browserAvailabilityHolders = browserAvailabilityHolderRegistry{entries: make(map[string]browserAvailabilityHolderEntry)}

func browserAvailabilityFor(dir string) *browserAvailabilityHolder {
	return browserAvailabilityForAt(dir, time.Now())
}

func browserAvailabilityForAt(dir string, now time.Time) *browserAvailabilityHolder {
	key := filepath.Clean(strings.TrimSpace(dir))
	if key == "." || key == "" {
		key = "<not-configured>"
	}
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	registry := &browserAvailabilityHolders
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for existingKey, entry := range registry.entries {
		if !entry.lastUsed.IsZero() && now.Sub(entry.lastUsed) >= browserAvailabilityHolderTTL {
			delete(registry.entries, existingKey)
		}
	}
	if entry, ok := registry.entries[key]; ok {
		entry.lastUsed = now
		registry.entries[key] = entry
		return entry.holder
	}
	if len(registry.entries) >= maxBrowserAvailabilityHolders {
		var oldestKey string
		var oldest time.Time
		for existingKey, entry := range registry.entries {
			if oldestKey == "" || entry.lastUsed.Before(oldest) {
				oldestKey = existingKey
				oldest = entry.lastUsed
			}
		}
		delete(registry.entries, oldestKey)
	}
	holder := &browserAvailabilityHolder{}
	registry.entries[key] = browserAvailabilityHolderEntry{holder: holder, lastUsed: now}
	return holder
}

type browserCallTiming struct {
	StartupMs   int64
	OperationMs int64
	ColdStart   bool
}

type browserSidecarResponse struct {
	ID     string               `json:"id"`
	OK     bool                 `json:"ok"`
	Result map[string]any       `json:"result,omitempty"`
	Error  *browserSidecarError `json:"error,omitempty"`
}

type BrowserSidecar struct {
	dir    string
	logger *slog.Logger

	startMu     sync.Mutex
	writeMu     sync.Mutex
	mu          sync.Mutex
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	processDone chan struct{}
	closeInput  func(io.WriteCloser) error
	killTree    func(*exec.Cmd) error
	pending     map[string]chan browserSidecarResponse
	ready       bool
	starting    bool
	startDone   chan struct{}
	startErr    error
	closed      bool
	closing     bool
	closeDone   chan struct{}
	closeErr    error
}

func ResolveBrowserSidecarDir(explicit string) string {
	for _, candidate := range []string{strings.TrimSpace(explicit), strings.TrimSpace(os.Getenv("FAST_SPIDER_BROWSER_SIDECAR_DIR"))} {
		if candidate == "" {
			continue
		}
		absolute, err := filepath.Abs(candidate)
		if err == nil {
			return absolute
		}
	}
	if executable, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(executable), "sidecar", "browser")
		if _, statErr := os.Stat(filepath.Join(candidate, "package.json")); statErr == nil {
			return candidate
		}
	}
	if absolute, err := filepath.Abs(filepath.Join("sidecar", "browser")); err == nil {
		if _, statErr := os.Stat(filepath.Join(absolute, "package.json")); statErr == nil {
			return absolute
		}
	}
	return ""
}

func NewBrowserSidecar(dir string, logger *slog.Logger) *BrowserSidecar {
	if logger == nil {
		logger = slog.Default()
	}
	return &BrowserSidecar{dir: ResolveBrowserSidecarDir(dir), logger: logger, pending: make(map[string]chan browserSidecarResponse)}
}

func (s *BrowserSidecar) nodeExecutable() (string, error) {
	if s.dir != "" {
		candidates := []string{filepath.Join(s.dir, "node")}
		if runtime.GOOS == "windows" {
			candidates = []string{filepath.Join(s.dir, "node.exe")}
		} else {
			candidates = append(candidates, filepath.Join(s.dir, "bin", "node"))
		}
		for _, candidate := range candidates {
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				return candidate, nil
			}
		}
	}
	path, err := exec.LookPath("node")
	if err != nil {
		return "", fmt.Errorf("%w: node executable not found", ErrBrowserUnavailable)
	}
	return path, nil
}

func browserSidecarEnvironment(dir string) []string {
	env := safeShellEnvironment()
	browserRoot := filepath.Join(dir, "browsers")
	if info, err := os.Stat(browserRoot); err == nil && info.IsDir() {
		env = append(env, "PLAYWRIGHT_BROWSERS_PATH="+browserRoot)
	}
	return env
}

func (s *BrowserSidecar) Available() error {
	_, err := s.AvailabilityStatus()
	return err
}

func (s *BrowserSidecar) AvailabilityStatus() (browserAvailabilityStatus, error) {
	started := time.Now()
	s.mu.Lock()
	running := s.cmd != nil && s.ready && !s.closed
	starting := s.starting
	startDone := s.startDone
	s.mu.Unlock()
	if running {
		return browserAvailabilityStatus{State: "ready", ReasonCode: "ready", CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), CacheHit: true}, nil
	}
	if starting && startDone != nil {
		select {
		case <-startDone:
		case <-time.After(5 * time.Second):
			return browserAvailabilityStatus{State: "blocked", ReasonCode: "probe_timeout", CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), TotalMs: time.Since(started).Milliseconds()}, &BrowserAvailabilityError{ReasonCode: "probe_timeout", Err: fmt.Errorf("%w: browser sidecar startup is still in progress", ErrBrowserUnavailable)}
		}
		s.mu.Lock()
		running = s.cmd != nil && s.ready && !s.closed
		startErr := s.startErr
		s.mu.Unlock()
		if running {
			return browserAvailabilityStatus{State: "ready", ReasonCode: "ready", CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), CacheHit: true, TotalMs: time.Since(started).Milliseconds()}, nil
		}
		if startErr != nil {
			return browserAvailabilityStatus{State: "blocked", ReasonCode: "sidecar_start_failed", CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), TotalMs: time.Since(started).Milliseconds()}, startErr
		}
	}
	holder := browserAvailabilityFor(s.dir)
	holder.mu.Lock()
	defer holder.mu.Unlock()
	now := time.Now().UTC()
	if now.Before(holder.cache.expiresAt) {
		status := holder.cache.status
		status.CacheHit = true
		return status, holder.cache.err
	}
	err := s.probeAvailability()
	status := browserAvailabilityStatus{State: "ready", ReasonCode: "ready", CheckedAt: now.Format(time.RFC3339Nano), TotalMs: time.Since(started).Milliseconds()}
	ttl := 30 * time.Second
	if err != nil {
		status.State = "blocked"
		status.ReasonCode = "sidecar_start_failed"
		var availabilityErr *BrowserAvailabilityError
		if errors.As(err, &availabilityErr) {
			status.ReasonCode = availabilityErr.ReasonCode
		}
		ttl = 5 * time.Second
	}
	holder.cache = browserAvailabilityCache{status: status, err: err, expiresAt: now.Add(ttl)}
	return status, err
}

func (s *BrowserSidecar) probeAvailability() error {
	if s.dir == "" {
		return &BrowserAvailabilityError{ReasonCode: "not_configured", Err: fmt.Errorf("%w: sidecar directory is not configured", ErrBrowserUnavailable)}
	}
	nodePath, err := s.nodeExecutable()
	if err != nil {
		return &BrowserAvailabilityError{ReasonCode: "node_runtime_missing", Err: err}
	}
	for _, required := range []string{"package.json", "index.mjs", filepath.Join("node_modules", "playwright", "package.json")} {
		if info, err := os.Stat(filepath.Join(s.dir, required)); err != nil || (required == "index.mjs" && !info.Mode().IsRegular()) {
			return &BrowserAvailabilityError{ReasonCode: "sidecar_files_missing", Err: fmt.Errorf("%w: browser sidecar files are incomplete", ErrBrowserUnavailable)}
		}
	}
	packageRaw, err := os.ReadFile(filepath.Join(s.dir, "package.json"))
	if err != nil {
		return &BrowserAvailabilityError{ReasonCode: "sidecar_files_missing", Err: fmt.Errorf("%w: browser sidecar package metadata is unavailable", ErrBrowserUnavailable)}
	}
	var packageMetadata struct {
		FastSpider struct {
			SidecarProtocol string `json:"sidecarProtocol"`
		} `json:"fastSpider"`
	}
	if json.Unmarshal(packageRaw, &packageMetadata) != nil || packageMetadata.FastSpider.SidecarProtocol != browserSidecarProtocolVersion {
		return &BrowserAvailabilityError{ReasonCode: "protocol_mismatch", Err: fmt.Errorf("%w: browser sidecar protocol is incompatible", ErrBrowserUnavailable)}
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	probe := exec.CommandContext(probeCtx, nodePath, "--input-type=module", "-e", `import fs from 'node:fs'; import { chromium } from 'playwright'; process.exit(fs.existsSync(chromium.executablePath()) ? 0 : 2)`)
	probe.Dir = s.dir
	probe.Env = browserSidecarEnvironment(s.dir)
	if err := probe.Run(); err != nil {
		reason := "chromium_missing"
		if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			reason = "probe_timeout"
		}
		return &BrowserAvailabilityError{ReasonCode: reason, Err: fmt.Errorf("%w: managed Chromium probe failed", ErrBrowserUnavailable)}
	}
	return nil
}

func (s *BrowserSidecar) Call(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	result, _, err := s.CallWithTiming(ctx, action, params)
	return result, err
}

func (s *BrowserSidecar) CallWithTiming(ctx context.Context, action string, params map[string]any) (map[string]any, browserCallTiming, error) {
	s.mu.Lock()
	coldStart := s.cmd == nil || !s.ready || s.closed
	s.mu.Unlock()
	startupStarted := time.Now()
	if err := s.ensureStarted(ctx); err != nil {
		return nil, browserCallTiming{StartupMs: time.Since(startupStarted).Milliseconds(), ColdStart: coldStart}, err
	}
	timing := browserCallTiming{StartupMs: time.Since(startupStarted).Milliseconds(), ColdStart: coldStart}
	operationStarted := time.Now()
	result, err := s.callStarted(ctx, action, params)
	timing.OperationMs = time.Since(operationStarted).Milliseconds()
	return result, timing, err
}

func (s *BrowserSidecar) callStarted(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	requestID, err := security.RandomOpaque("breq_")
	if err != nil {
		return nil, err
	}
	responseCh := make(chan browserSidecarResponse, 1)
	s.mu.Lock()
	if s.closed || s.cmd == nil || s.stdin == nil {
		s.mu.Unlock()
		return nil, ErrBrowserSidecarLost
	}
	s.pending[requestID] = responseCh
	stdin := s.stdin
	s.mu.Unlock()

	request := map[string]any{"id": requestID, "action": action, "params": params}
	raw, err := json.Marshal(request)
	if err != nil {
		s.removePending(requestID)
		return nil, err
	}
	if len(raw) > 256<<10 {
		s.removePending(requestID)
		return nil, fmt.Errorf("browser sidecar request exceeds limit")
	}
	writeDone := make(chan error, 1)
	go func() {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		if err := ctx.Err(); err != nil {
			writeDone <- err
			return
		}
		s.mu.Lock()
		current := s.stdin == stdin && !s.closed
		s.mu.Unlock()
		if !current {
			writeDone <- ErrBrowserSidecarLost
			return
		}
		frame := append(raw, '\n')
		for len(frame) > 0 {
			n, err := stdin.Write(frame)
			if err != nil {
				writeDone <- err
				return
			}
			if n <= 0 {
				writeDone <- io.ErrShortWrite
				return
			}
			frame = frame[n:]
		}
		writeDone <- nil
	}()
	select {
	case <-ctx.Done():
		s.removePending(requestID)
		_ = s.Close(ctx)
		return nil, ctx.Err()
	case writeErr := <-writeDone:
		if ctxErr := ctx.Err(); ctxErr != nil {
			s.removePending(requestID)
			_ = s.Close(ctx)
			return nil, ctxErr
		}
		if writeErr == nil {
			break
		}
		s.removePending(requestID)
		// A failed write may have emitted only part of a JSON frame. Retire the
		// process so a later request cannot continue on a corrupted stream.
		_ = s.Close(context.Background())
		return nil, fmt.Errorf("write browser sidecar request: %w", writeErr)
	}

	select {
	case <-ctx.Done():
		s.removePending(requestID)
		_ = s.Close(ctx)
		return nil, ctx.Err()
	case response := <-responseCh:
		if !response.OK {
			if response.Error == nil {
				return nil, fmt.Errorf("browser sidecar returned an invalid error response")
			}
			return nil, &BrowserActionError{Code: response.Error.Code, Message: response.Error.Message, Retryable: response.Error.Retryable}
		}
		return response.Result, nil
	}
}

func (s *BrowserSidecar) ensureStarted(ctx context.Context) error {
	s.startMu.Lock()
	defer s.startMu.Unlock()

	for {
		s.mu.Lock()
		closing := s.closing
		closeDone := s.closeDone
		closeErr := s.closeErr
		if !closing && s.cmd != nil && s.ready && !s.closed {
			s.mu.Unlock()
			return nil
		}
		s.mu.Unlock()
		if !closing {
			if closeErr != nil {
				return fmt.Errorf("%w: previous browser sidecar cleanup failed: %v", ErrBrowserSidecarLost, closeErr)
			}
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-closeDone:
		}
	}
	if err := s.Available(); err != nil {
		return err
	}
	s.beginStart()
	failStart := func(err error) error {
		s.finishStart(err)
		return err
	}
	nodePath, err := s.nodeExecutable()
	if err != nil {
		return failStart(err)
	}
	cmd := exec.Command(nodePath, filepath.Join(s.dir, "index.mjs"))
	cmd.Dir = s.dir
	cmd.Env = browserSidecarEnvironment(s.dir)
	configureProcessTree(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return failStart(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return failStart(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return failStart(err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return failStart(fmt.Errorf("start browser sidecar: %w", err))
	}

	s.mu.Lock()
	if s.cmd != nil && !s.closed {
		s.mu.Unlock()
		_ = stdin.Close()
		_ = killProcessTree(cmd)
		return nil
	}
	s.cmd = cmd
	s.stdin = stdin
	s.processDone = make(chan struct{})
	s.ready = false
	processDone := s.processDone
	s.mu.Unlock()
	go s.readResponses(stdout)
	go s.readStderr(stderr)
	go s.waitProcess(cmd, processDone)

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := s.callStarted(probeCtx, "runtime.status", map[string]any{})
	if err != nil {
		_ = s.Close(context.Background())
		startErr := fmt.Errorf("browser sidecar handshake failed: %w", err)
		s.finishStart(startErr)
		return startErr
	}
	if version, _ := result["protocolVersion"].(string); version != browserSidecarProtocolVersion {
		_ = s.Close(context.Background())
		startErr := fmt.Errorf("browser sidecar protocol mismatch: %q", version)
		s.finishStart(startErr)
		return startErr
	}
	s.mu.Lock()
	if s.cmd == cmd && !s.closed {
		s.ready = true
	}
	s.mu.Unlock()
	s.finishStart(nil)
	return nil
}

func (s *BrowserSidecar) beginStart() {
	s.mu.Lock()
	s.starting = true
	s.startDone = make(chan struct{})
	s.startErr = nil
	s.closed = false
	s.closeErr = nil
	s.mu.Unlock()
}

func (s *BrowserSidecar) finishStart(err error) {
	if err != nil {
		now := time.Now().UTC()
		availabilityErr := &BrowserAvailabilityError{ReasonCode: "sidecar_start_failed", Err: err}
		holder := browserAvailabilityFor(s.dir)
		holder.mu.Lock()
		holder.cache = browserAvailabilityCache{
			status: browserAvailabilityStatus{State: "blocked", ReasonCode: "sidecar_start_failed", CheckedAt: now.Format(time.RFC3339Nano)},
			err:    availabilityErr, expiresAt: now.Add(5 * time.Second),
		}
		holder.mu.Unlock()
	}
	s.mu.Lock()
	done := s.startDone
	s.starting = false
	s.startDone = nil
	s.startErr = err
	s.mu.Unlock()
	if done != nil {
		close(done)
	}
}

func (s *BrowserSidecar) readResponses(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxBrowserSidecarLineBytes)
	for scanner.Scan() {
		var response browserSidecarResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil || response.ID == "" {
			s.logger.Warn("browser sidecar emitted invalid response", "error", err)
			continue
		}
		s.mu.Lock()
		ch := s.pending[response.ID]
		if ch != nil {
			delete(s.pending, response.ID)
		}
		s.mu.Unlock()
		if ch != nil {
			ch <- response
		}
	}
	if err := scanner.Err(); err != nil {
		s.logger.Warn("browser sidecar stdout ended", "error", err)
	}
}

func (s *BrowserSidecar) readStderr(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 8<<10), 64<<10)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			s.logger.Debug("browser sidecar", "message", truncateBrowserLog(line, 2048))
		}
	}
}

func truncateBrowserLog(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

func (s *BrowserSidecar) waitProcess(cmd *exec.Cmd, done chan struct{}) {
	err := cmd.Wait()
	s.mu.Lock()
	if s.cmd != cmd {
		s.mu.Unlock()
		close(done)
		return
	}
	s.cmd = nil
	s.stdin = nil
	if s.processDone == done {
		s.processDone = nil
	}
	s.ready = false
	pending := s.pending
	s.pending = make(map[string]chan browserSidecarResponse)
	s.mu.Unlock()
	close(done)
	for id, ch := range pending {
		ch <- browserSidecarResponse{ID: id, OK: false, Error: &browserSidecarError{Code: "BROWSER_SIDECAR_LOST", Message: "browser sidecar process exited", Retryable: true}}
	}
	if err != nil {
		s.logger.Warn("browser sidecar exited", "error", err)
	}
}

func (s *BrowserSidecar) removePending(requestID string) {
	s.mu.Lock()
	delete(s.pending, requestID)
	s.mu.Unlock()
}

func (s *BrowserSidecar) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closing {
		done := s.closeDone
		s.mu.Unlock()
		return s.waitClose(ctx, done)
	}
	if s.cmd == nil && s.stdin == nil {
		s.closed = true
		s.ready = false
		pending := s.pending
		s.pending = make(map[string]chan browserSidecarResponse)
		err := s.closeErr
		s.mu.Unlock()
		s.notifyClosed(pending)
		return err
	}
	cmd := s.cmd
	stdin := s.stdin
	processDone := s.processDone
	s.closed = true
	s.ready = false
	s.closing = true
	s.closeDone = make(chan struct{})
	s.closeErr = nil
	done := s.closeDone
	pending := s.pending
	s.pending = make(map[string]chan browserSidecarResponse)
	s.mu.Unlock()
	s.notifyClosed(pending)
	go s.finishClose(cmd, stdin, processDone, done)
	return s.waitClose(ctx, done)
}

func (s *BrowserSidecar) notifyClosed(pending map[string]chan browserSidecarResponse) {
	for id, ch := range pending {
		ch <- browserSidecarResponse{ID: id, OK: false, Error: &browserSidecarError{Code: "BROWSER_SIDECAR_CLOSED", Message: "browser sidecar closed", Retryable: true}}
	}
}

func (s *BrowserSidecar) finishClose(cmd *exec.Cmd, stdin io.WriteCloser, processDone <-chan struct{}, done chan struct{}) {
	inputDone := make(chan error, 1)
	if stdin != nil {
		go func() {
			if s.closeInput != nil {
				inputDone <- s.closeInput(stdin)
				return
			}
			inputDone <- stdin.Close()
		}()
	} else {
		inputDone <- nil
	}
	killDone := make(chan error, 1)
	if cmd != nil && cmd.Process != nil {
		go func() {
			if s.killTree != nil {
				killDone <- s.killTree(cmd)
				return
			}
			killDone <- killProcessTree(cmd)
		}()
	} else {
		killDone <- nil
	}
	inputErr := <-inputDone
	killErr := <-killDone
	if processDone != nil {
		<-processDone
	}
	closeErr := killErr
	if closeErr == nil {
		closeErr = inputErr
	}
	if processDone != nil && closeErr != nil {
		// Kill/pipe-close operations can race with a process that has already
		// exited. Once Wait has completed, cleanup is definitive and a stale
		// helper error must not permanently block the next sidecar generation.
		if s.logger != nil {
			s.logger.Debug("browser sidecar cleanup completed after helper error", "error", closeErr)
		}
		closeErr = nil
	}

	s.mu.Lock()
	if s.cmd == cmd {
		s.cmd = nil
	}
	if s.stdin == stdin {
		s.stdin = nil
	}
	if s.processDone == processDone {
		s.processDone = nil
	}
	s.ready = false
	s.closing = false
	s.closeErr = closeErr
	close(done)
	s.mu.Unlock()
}

func (s *BrowserSidecar) waitClose(ctx context.Context, done <-chan struct{}) error {
	closeCtx, cancel := context.WithTimeout(ctx, browserSidecarCloseTimeout)
	defer cancel()
	select {
	case <-closeCtx.Done():
		return closeCtx.Err()
	case <-done:
		s.mu.Lock()
		err := s.closeErr
		s.mu.Unlock()
		return err
	}
}
