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

type browserSidecarResponse struct {
	ID     string               `json:"id"`
	OK     bool                 `json:"ok"`
	Result map[string]any       `json:"result,omitempty"`
	Error  *browserSidecarError `json:"error,omitempty"`
}

type BrowserSidecar struct {
	dir    string
	logger *slog.Logger

	startMu sync.Mutex
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	pending map[string]chan browserSidecarResponse
	closed  bool
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
	if s.dir == "" {
		return fmt.Errorf("%w: sidecar directory is not configured", ErrBrowserUnavailable)
	}
	nodePath, err := s.nodeExecutable()
	if err != nil {
		return err
	}
	for _, required := range []string{"package.json", "index.mjs", filepath.Join("node_modules", "playwright", "package.json")} {
		if info, err := os.Stat(filepath.Join(s.dir, required)); err != nil || (required == "index.mjs" && !info.Mode().IsRegular()) {
			return fmt.Errorf("%w: missing %s", ErrBrowserUnavailable, required)
		}
	}
	packageRaw, err := os.ReadFile(filepath.Join(s.dir, "package.json"))
	if err != nil {
		return fmt.Errorf("%w: browser sidecar package metadata is unavailable", ErrBrowserUnavailable)
	}
	var packageMetadata struct {
		FastSpider struct {
			SidecarProtocol string `json:"sidecarProtocol"`
		} `json:"fastSpider"`
	}
	if json.Unmarshal(packageRaw, &packageMetadata) != nil || packageMetadata.FastSpider.SidecarProtocol != browserSidecarProtocolVersion {
		return fmt.Errorf("%w: browser sidecar protocol %q is incompatible with required %s", ErrBrowserUnavailable, packageMetadata.FastSpider.SidecarProtocol, browserSidecarProtocolVersion)
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	probe := exec.CommandContext(probeCtx, nodePath, "--input-type=module", "-e", `import fs from 'node:fs'; import { chromium } from 'playwright'; process.exit(fs.existsSync(chromium.executablePath()) ? 0 : 2)`)
	probe.Dir = s.dir
	probe.Env = browserSidecarEnvironment(s.dir)
	if err := probe.Run(); err != nil {
		return fmt.Errorf("%w: managed Chromium is not installed", ErrBrowserUnavailable)
	}
	return nil
}

func (s *BrowserSidecar) Call(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	if err := s.ensureStarted(ctx); err != nil {
		return nil, err
	}
	return s.callStarted(ctx, action, params)
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
	if s.cmd != nil && !s.closed {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if err := s.Available(); err != nil {
		return err
	}
	nodePath, err := s.nodeExecutable()
	if err != nil {
		return err
	}
	cmd := exec.Command(nodePath, filepath.Join(s.dir, "index.mjs"))
	cmd.Dir = s.dir
	cmd.Env = browserSidecarEnvironment(s.dir)
	configureProcessTree(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start browser sidecar: %w", err)
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
	s.closed = false
	s.mu.Unlock()
	go s.readResponses(stdout)
	go s.readStderr(stderr)
	go s.waitProcess(cmd)

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := s.callStarted(probeCtx, "runtime.status", map[string]any{})
	if err != nil {
		_ = s.Close(context.Background())
		return fmt.Errorf("browser sidecar handshake failed: %w", err)
	}
	if version, _ := result["protocolVersion"].(string); version != browserSidecarProtocolVersion {
		_ = s.Close(context.Background())
		return fmt.Errorf("browser sidecar protocol mismatch: %q", version)
	}
	return nil
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
