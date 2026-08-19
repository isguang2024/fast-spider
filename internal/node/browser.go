package node

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/isguang2024/fast-spider/internal/browserext"
	"github.com/isguang2024/fast-spider/internal/security"
)

const (
	browserSessionIdleTTL       = 10 * time.Minute
	browserSessionOrphanTTL     = time.Hour
	maxBrowserSessionScan       = 256
	maxBrowserSessionCleanup    = 32
	browserOpaqueIDEncodedBytes = 32
	maxBrowserExtensions        = 4
)

type BrowserActionError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *BrowserActionError) Error() string {
	if e == nil {
		return "browser action failed"
	}
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

type browserSessionRecord struct {
	BrowserSessionID string
	SessionDir       string
	ScreenshotDir    string
	CreatedAt        time.Time
	LastUsedAt       time.Time
}

type BrowserManager struct {
	dataDir string
	sidecar *BrowserSidecar
	logger  *slog.Logger

	actionMu  sync.Mutex
	mu        sync.Mutex
	session   *browserSessionRecord
	launching bool
}

func NewBrowserManager(dataDir, sidecarDir string, logger *slog.Logger) *BrowserManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &BrowserManager{
		dataDir: dataDir,
		sidecar: NewBrowserSidecar(sidecarDir, logger),
		logger:  logger,
	}
}

func (m *BrowserManager) Available() error {
	return m.sidecar.Available()
}

func (m *BrowserManager) Execute(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	started := time.Now()
	if action == "readiness" {
		status, _ := m.sidecar.AvailabilityStatus()
		return map[string]any{"state": status.State, "ready": status.State == "ready", "reasonCode": status.ReasonCode, "checkedAt": status.CheckedAt, "cacheHit": status.CacheHit, "timing": map[string]any{"totalMs": status.TotalMs}}, nil
	}
	queueStarted := time.Now()
	m.actionMu.Lock()
	defer m.actionMu.Unlock()
	queueMs := time.Since(queueStarted).Milliseconds()
	result, err := m.executeLocked(ctx, action, params)
	if result != nil {
		timing, _ := result["timing"].(map[string]any)
		if timing == nil {
			timing = map[string]any{}
		}
		timing["queueMs"] = queueMs
		timing["totalMs"] = time.Since(started).Milliseconds()
		result["timing"] = timing
	}
	return result, err
}

func (m *BrowserManager) executeLocked(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	if action == "launch" {
		return m.launch(ctx, params)
	}
	if action == "extensions.list" {
		return m.listExtensions()
	}
	switch action {
	case "close", "page.open", "page.navigate", "page.close", "pages.list", "click", "type", "press", "wait", "batch", "snapshot", "screenshot", "events":
		return m.executeSessionAction(ctx, action, params)
	default:
		return nil, &BrowserActionError{Code: "INVALID_REQUEST", Message: "unsupported browser action", Retryable: false}
	}
}

func (m *BrowserManager) launch(ctx context.Context, params map[string]any) (map[string]any, error) {
	m.mu.Lock()
	if m.session != nil || m.launching {
		m.mu.Unlock()
		return nil, &BrowserActionError{Code: "BROWSER_BUSY", Message: "a browser session is already active", Retryable: true}
	}
	m.launching = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.launching = false
		m.mu.Unlock()
	}()

	browserSessionID, err := security.RandomOpaque("brs_")
	if err != nil {
		return nil, err
	}
	sessionDir := filepath.Join(m.dataDir, "browser", "sessions", browserSessionID)
	screenshotDir := filepath.Join(sessionDir, "screenshots")
	if err := os.MkdirAll(screenshotDir, 0o700); err != nil {
		return nil, err
	}

	launchParams := make(map[string]any)
	launchParams["browserSessionId"] = browserSessionID
	launchParams["engine"] = stringParam(params, "engine", "chromium")
	launchParams["headless"] = boolParam(params, "headless", true)
	if viewport, ok := params["viewport"].(map[string]any); ok {
		launchParams["viewport"] = viewport
	}
	launchParams["screenshotDir"] = screenshotDir
	extensionPaths, extensionIDs, err := m.extensionPaths(params)
	if err != nil {
		_ = os.RemoveAll(sessionDir)
		return nil, err
	}
	if len(extensionPaths) > 0 {
		if boolParam(params, "headless", true) {
			_ = os.RemoveAll(sessionDir)
			return nil, &BrowserActionError{Code: "BROWSER_EXTENSION_REQUIRES_HEADED", Message: "browser extensions require a headed managed browser; set headed=true", Retryable: false}
		}
		userDataDir, profileErr := m.browserExtensionProfileDir()
		if profileErr != nil {
			_ = os.RemoveAll(sessionDir)
			return nil, profileErr
		}
		launchParams["extensionPaths"] = extensionPaths
		launchParams["userDataDir"] = userDataDir
	}

	result, sidecarTiming, err := m.sidecar.CallWithTiming(ctx, "launch", launchParams)
	if err != nil {
		_ = os.RemoveAll(sessionDir)
		return nil, err
	}
	if len(extensionIDs) > 0 {
		result["extensionIds"] = extensionIDs
	}
	now := time.Now().UTC()
	m.mu.Lock()
	m.session = &browserSessionRecord{
		BrowserSessionID: browserSessionID,
		SessionDir:       sessionDir,
		ScreenshotDir:    screenshotDir,
		CreatedAt:        now,
		LastUsedAt:       now,
	}
	m.mu.Unlock()
	attachBrowserSidecarTiming(result, sidecarTiming)
	return result, nil
}

func (m *BrowserManager) listExtensions() (map[string]any, error) {
	installed, err := browserext.List(m.dataDir)
	if err != nil {
		return nil, err
	}
	return map[string]any{"extensions": installed}, nil
}

func (m *BrowserManager) extensionPaths(params map[string]any) ([]string, []string, error) {
	raw, present := params["extensionIds"]
	if !present || raw == nil {
		// Installing an extension must not silently change ordinary headless
		// browser launches. Loading is an explicit per-session opt-in through
		// extensionIds.
		return nil, nil, nil
	}

	ids, err := browserExtensionIDs(raw)
	if err != nil {
		return nil, nil, err
	}
	if len(ids) > maxBrowserExtensions {
		return nil, nil, &BrowserActionError{Code: "BROWSER_EXTENSION_LIMIT", Message: "at most four browser extensions may be loaded", Retryable: false}
	}
	paths := make([]string, 0, len(ids))
	for _, id := range ids {
		extension, resolveErr := browserext.Resolve(m.dataDir, id)
		if resolveErr != nil {
			return nil, nil, &BrowserActionError{Code: "BROWSER_EXTENSION_NOT_FOUND", Message: "requested browser extension is not installed", Retryable: false}
		}
		paths = append(paths, extension.Path)
	}
	return paths, ids, nil
}

func (m *BrowserManager) browserExtensionProfileDir() (string, error) {
	profileDir, err := filepath.Abs(filepath.Join(m.dataDir, "browser", "extension-profile"))
	if err != nil {
		return "", &BrowserActionError{Code: "BROWSER_EXTENSION_PROFILE_INVALID", Message: "managed browser extension profile path is invalid", Retryable: false}
	}
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return "", &BrowserActionError{Code: "BROWSER_EXTENSION_PROFILE_INVALID", Message: "managed browser extension profile is unavailable", Retryable: false}
	}
	info, err := os.Lstat(profileDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", &BrowserActionError{Code: "BROWSER_EXTENSION_PROFILE_INVALID", Message: "managed browser extension profile is invalid", Retryable: false}
	}
	return profileDir, nil
}

func browserExtensionIDs(value any) ([]string, error) {
	var raw []any
	switch typed := value.(type) {
	case []any:
		raw = typed
	case []string:
		raw = make([]any, 0, len(typed))
		for _, item := range typed {
			raw = append(raw, item)
		}
	default:
		return nil, &BrowserActionError{Code: "INVALID_REQUEST", Message: "extensionIds must be an array", Retryable: false}
	}
	ids := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		id, ok := item.(string)
		id = strings.TrimSpace(id)
		if !ok || id == "" {
			return nil, &BrowserActionError{Code: "INVALID_REQUEST", Message: "extensionIds must contain non-empty strings", Retryable: false}
		}
		if _, exists := seen[id]; exists {
			return nil, &BrowserActionError{Code: "INVALID_REQUEST", Message: "extensionIds must not contain duplicates", Retryable: false}
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func (m *BrowserManager) executeSessionAction(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	browserSessionID, _ := params["browserSessionId"].(string)
	if browserSessionID == "" {
		return nil, &BrowserActionError{Code: "INVALID_REQUEST", Message: "browserSessionId is required", Retryable: false}
	}
	session, err := m.sessionFor(browserSessionID)
	if err != nil {
		return nil, err
	}

	callParams := cloneMap(params)

	result, sidecarTiming, err := m.sidecar.CallWithTiming(ctx, action, callParams)
	if err != nil {
		code := browserErrorCode(err)
		if ctx.Err() != nil || errors.Is(err, ErrBrowserSidecarLost) || errors.Is(err, ErrBrowserUnavailable) || code == "BROWSER_SIDECAR_LOST" || code == "BROWSER_SESSION_NOT_FOUND" {
			m.clearSession(session)
		}
		return nil, err
	}
	if action == "close" {
		m.clearSession(session)
		attachBrowserSidecarTiming(result, sidecarTiming)
		return result, nil
	}
	m.mu.Lock()
	if m.session != nil && m.session.BrowserSessionID == browserSessionID {
		m.session.LastUsedAt = time.Now().UTC()
	}
	m.mu.Unlock()
	attachBrowserSidecarTiming(result, sidecarTiming)
	return result, nil
}

func attachBrowserSidecarTiming(result map[string]any, timing browserCallTiming) {
	if result == nil {
		return
	}
	result["timing"] = map[string]any{"startupMs": timing.StartupMs, "operationMs": timing.OperationMs, "coldStart": timing.ColdStart}
}

func (m *BrowserManager) ExecuteScreenshot(ctx context.Context, params map[string]any, consume func(path, logicalName, contentType string) (map[string]any, error)) (map[string]any, error) {
	started := time.Now()
	queueStarted := time.Now()
	m.actionMu.Lock()
	defer m.actionMu.Unlock()
	queueMs := time.Since(queueStarted).Milliseconds()
	result, err := m.executeLocked(ctx, "screenshot", params)
	if err != nil {
		return nil, err
	}
	browserSessionID, _ := params["browserSessionId"].(string)
	rawPath, _ := result["path"].(string)
	managedPath, err := m.ManagedScreenshotPath(browserSessionID, rawPath)
	if err != nil {
		return nil, err
	}
	defer os.Remove(managedPath)
	logicalName, _ := result["logicalName"].(string)
	contentType, _ := result["contentType"].(string)
	if logicalName == "" || contentType == "" {
		return nil, &BrowserActionError{Code: "SCREENSHOT_INVALID", Message: "sidecar returned incomplete screenshot metadata", Retryable: false}
	}
	artifact, err := consume(managedPath, logicalName, contentType)
	if err != nil {
		return nil, err
	}
	out := cloneMap(result)
	delete(out, "path")
	for key, value := range artifact {
		out[key] = value
	}
	timing, _ := out["timing"].(map[string]any)
	if timing == nil {
		timing = map[string]any{}
	}
	timing["queueMs"] = queueMs
	timing["totalMs"] = time.Since(started).Milliseconds()
	out["timing"] = timing
	return out, nil
}

func (m *BrowserManager) sessionFor(browserSessionID string) (browserSessionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session == nil || m.session.BrowserSessionID != browserSessionID {
		return browserSessionRecord{}, &BrowserActionError{Code: "BROWSER_SESSION_NOT_FOUND", Message: "browser session was not found", Retryable: false}
	}
	return *m.session, nil
}

func (m *BrowserManager) ManagedScreenshotPath(browserSessionID, rawPath string) (string, error) {
	session, err := m.sessionFor(browserSessionID)
	if err != nil {
		return "", err
	}
	if rawPath == "" || !filepath.IsAbs(rawPath) {
		return "", &BrowserActionError{Code: "SCREENSHOT_INVALID", Message: "sidecar returned an invalid screenshot path", Retryable: false}
	}
	rootReal, err := filepath.EvalSymlinks(session.ScreenshotDir)
	if err != nil {
		return "", err
	}
	pathReal, err := filepath.EvalSymlinks(rawPath)
	if err != nil {
		return "", err
	}
	if !pathWithin(rootReal, pathReal) || samePath(rootReal, pathReal) {
		return "", &BrowserActionError{Code: "SCREENSHOT_INVALID", Message: "screenshot path escaped the managed session directory", Retryable: false}
	}
	info, err := os.Stat(pathReal)
	if err != nil || !info.Mode().IsRegular() {
		if err != nil {
			return "", err
		}
		return "", ErrNotRegularFile
	}
	return pathReal, nil
}

func (m *BrowserManager) StartMaintenance(ctx context.Context) {
	m.cleanupOrphanSessions(time.Now().UTC())
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			m.closeIdle(ctx, now.UTC())
			// Continue draining orphaned session directories after startup. The
			// bounded scan/removal keeps each tick cheap while ensuring a large
			// backlog does not become permanent when the process stays alive.
			m.cleanupOrphanSessions(now.UTC())
		}
	}
}

func (m *BrowserManager) cleanupOrphanSessions(now time.Time) {
	m.actionMu.Lock()
	defer m.actionMu.Unlock()
	m.mu.Lock()
	activeSessionID := ""
	if m.session != nil {
		activeSessionID = m.session.BrowserSessionID
	}
	m.mu.Unlock()
	removed, err := cleanupOrphanBrowserSessions(m.dataDir, activeSessionID, now)
	if err != nil {
		m.logger.Warn("cleanup orphan browser sessions failed", "error", err)
	}
	if removed > 0 {
		m.logger.Debug("cleaned orphan browser sessions", "count", removed)
	}
}

func cleanupOrphanBrowserSessions(dataDir, activeSessionID string, now time.Time) (int, error) {
	root := filepath.Join(dataDir, "browser", "sessions")
	directory, err := os.Open(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer directory.Close()
	entries, readErr := directory.ReadDir(maxBrowserSessionScan)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	removed := 0
	var cleanupErr error
	for _, entry := range entries {
		if removed >= maxBrowserSessionCleanup {
			break
		}
		name := entry.Name()
		if name == activeSessionID || !validManagedBrowserSessionID(name) {
			continue
		}
		path := filepath.Join(root, name)
		info, statErr := os.Lstat(path)
		if statErr != nil {
			if !errors.Is(statErr, os.ErrNotExist) {
				cleanupErr = errors.Join(cleanupErr, statErr)
			}
			continue
		}
		if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 || now.Sub(info.ModTime()) < browserSessionOrphanTTL {
			continue
		}
		if removeErr := os.RemoveAll(path); removeErr != nil {
			cleanupErr = errors.Join(cleanupErr, removeErr)
			continue
		}
		removed++
	}
	return removed, errors.Join(readErr, cleanupErr)
}

func validManagedBrowserSessionID(value string) bool {
	if len(value) != len("brs_")+browserOpaqueIDEncodedBytes || !strings.HasPrefix(value, "brs_") {
		return false
	}
	for _, character := range value[len("brs_"):] {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func (m *BrowserManager) closeIdle(parent context.Context, now time.Time) {
	m.actionMu.Lock()
	defer m.actionMu.Unlock()
	m.mu.Lock()
	if m.session == nil || now.Sub(m.session.LastUsedAt) < browserSessionIdleTTL {
		m.mu.Unlock()
		return
	}
	session := *m.session
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	_, _ = m.sidecar.Call(ctx, "close", map[string]any{"browserSessionId": session.BrowserSessionID})
	m.clearSession(session)
}

func (m *BrowserManager) Close(ctx context.Context) error {
	m.actionMu.Lock()
	defer m.actionMu.Unlock()
	m.mu.Lock()
	var session *browserSessionRecord
	if m.session != nil {
		copySession := *m.session
		session = &copySession
	}
	m.session = nil
	m.mu.Unlock()
	if session != nil {
		_, _ = m.sidecar.Call(ctx, "close", map[string]any{"browserSessionId": session.BrowserSessionID})
		_ = os.RemoveAll(session.SessionDir)
	}
	return m.sidecar.Close(ctx)
}

func (m *BrowserManager) clearSession(session browserSessionRecord) {
	m.mu.Lock()
	if m.session != nil && m.session.BrowserSessionID == session.BrowserSessionID {
		m.session = nil
	}
	m.mu.Unlock()
	_ = os.RemoveAll(session.SessionDir)
}

func browserErrorCode(err error) string {
	var actionErr *BrowserActionError
	if errors.As(err, &actionErr) {
		return actionErr.Code
	}
	return ""
}

func cloneMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func stringParam(params map[string]any, name, fallback string) string {
	if value, ok := params[name].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func boolParam(params map[string]any, name string, fallback bool) bool {
	if value, ok := params[name].(bool); ok {
		return value
	}
	return fallback
}
