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

// NodeUI builds a lightweight local capability client on every diagnostics
// refresh. Keep availability probes keyed by the resolved component directory
// so those clients share the same 30s/5s cache without sharing browser sessions.
var browserAvailabilityHolders sync.Map

func browserAvailabilityFor(dir string) *browserAvailabilityHolder {
	key := filepath.Clean(strings.TrimSpace(dir))
	if key == "." || key == "" {
		key = "<not-configured>"
	}
	value, _ := browserAvailabilityHolders.LoadOrStore(key, &browserAvailabilityHolder{})
	return value.(*browserAvailabilityHolder)
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

	startMu   sync.Mutex
	mu        sync.Mutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	pending   map[string]chan browserSidecarResponse
	ready     bool
	starting  bool
	startDone chan struct{}
	startErr  error
	closed    bool
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
	s.mu.Lock()
	if s.stdin != stdin || s.closed {
		s.mu.Unlock()
		s.removePending(requestID)
		return nil, ErrBrowserSidecarLost
	}
	_, writeErr := stdin.Write(append(raw, '\n'))
	s.mu.Unlock()
	if writeErr != nil {
		s.removePending(requestID)
		return nil, fmt.Errorf("write browser sidecar request: %w", writeErr)
	}

	select {
	case <-ctx.Done():
		s.removePending(requestID)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.Close(shutdownCtx)
		cancel()
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

	s.mu.Lock()
	if s.cmd != nil && s.ready && !s.closed {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
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
	s.ready = false
	s.mu.Unlock()
	go s.readResponses(stdout)
	go s.readStderr(stderr)
	go s.waitProcess(cmd)

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

func (s *BrowserSidecar) waitProcess(cmd *exec.Cmd) {
	err := cmd.Wait()
	s.mu.Lock()
	if s.cmd != cmd {
		s.mu.Unlock()
		return
	}
	s.cmd = nil
	s.stdin = nil
	s.ready = false
	pending := s.pending
	s.pending = make(map[string]chan browserSidecarResponse)
	s.mu.Unlock()
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
	cmd := s.cmd
	stdin := s.stdin
	s.closed = true
	s.cmd = nil
	s.stdin = nil
	s.ready = false
	pending := s.pending
	s.pending = make(map[string]chan browserSidecarResponse)
	s.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	for id, ch := range pending {
		ch <- browserSidecarResponse{ID: id, OK: false, Error: &browserSidecarError{Code: "BROWSER_SIDECAR_CLOSED", Message: "browser sidecar closed", Retryable: true}}
	}
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- killProcessTree(cmd) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}
