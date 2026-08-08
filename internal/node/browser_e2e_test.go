package node

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestClientCapabilitiesOmitBrowserWhenChromiumMissing(t *testing.T) {
	sidecarDir := t.TempDir()
	playwrightDir := filepath.Join(sidecarDir, "node_modules", "playwright")
	if err := os.MkdirAll(playwrightDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"package.json": `{"name":"test-browser-sidecar","type":"module"}`,
		"index.mjs":    "export {};\n",
		filepath.Join("node_modules", "playwright", "package.json"): `{"name":"playwright","type":"module","exports":"./index.mjs"}`,
		filepath.Join("node_modules", "playwright", "index.mjs"):    "export const chromium = { executablePath: () => '/definitely-missing-fast-spider-chromium' };\n",
	} {
		path := filepath.Join(sidecarDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	client, err := New(Config{DataDir: t.TempDir(), Version: "test", BrowserSidecarDir: sidecarDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.browser.Available(); !errors.Is(err, ErrBrowserUnavailable) {
		t.Fatalf("missing Chromium probe error=%v", err)
	}
	for _, capability := range client.Capabilities() {
		if capability.CapabilityId == protocolv1.BrowserCapability.CapabilityId {
			t.Fatal("Client advertised browser without a Chromium executable")
		}
	}
}

func TestBrowserRealSidecarE2E(t *testing.T) {
	if os.Getenv("FAST_SPIDER_BROWSER_E2E") != "1" {
		t.Skip("set FAST_SPIDER_BROWSER_E2E=1 to run the real Chromium/Sidecar E2E")
	}

	sidecarDir := realBrowserSidecarDir(t)
	if err := NewBrowserSidecar(sidecarDir, nil).Available(); err != nil {
		t.Fatalf("real browser sidecar unavailable: %v", err)
	}

	dataDir := t.TempDir()
	workspaceRoot := t.TempDir()
	otherRoot := t.TempDir()
	store := NewWorkspaceStore(dataDir)
	workspace, err := store.Add(workspaceRoot, "browser-e2e")
	if err != nil {
		t.Fatal(err)
	}
	otherWorkspace, err := store.Add(otherRoot, "browser-e2e-other")
	if err != nil {
		t.Fatal(err)
	}
	hangStarted := make(chan struct{}, 1)
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, `<!doctype html>
<html><head><title>Fast Spider E2E</title></head><body>
<label for="name">Name</label><input id="name">
<button id="submit" type="button">Apply</button>
<label for="ws-url">WebSocket URL</label><input id="ws-url">
<button id="probe" type="button">Probe WebSocket</button>
<output id="status" aria-live="polite">idle</output>
<script>
const name = document.querySelector('#name');
const status = document.querySelector('#status');
document.querySelector('#submit').addEventListener('click', () => { status.textContent = 'clicked: ' + name.value; });
document.querySelector('#probe').addEventListener('click', () => {
  try {
    const socket = new WebSocket(document.querySelector('#ws-url').value);
    socket.onerror = () => { status.textContent = 'websocket error'; };
  } catch (error) { status.textContent = 'websocket error'; }
});
</script></body></html>`)
		case "/hang":
			select {
			case hangStarted <- struct{}{}:
			default:
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
		}
	}))
	defer pageServer.Close()

	unauthorizedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "unauthorized")
	}))
	defer unauthorizedServer.Close()
	lateOriginServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, "<!doctype html><html><body>late origin</body></html>")
	}))
	defer lateOriginServer.Close()

	if _, err := store.AuthorizeBrowserOrigin(context.Background(), workspace.WorkspaceID, pageServer.URL); err != nil {
		t.Fatal(err)
	}
	if publicURL, err := store.ValidateBrowserURL(context.Background(), workspace.WorkspaceID, "https://8.8.8.8/public"); err != nil || publicURL != "https://8.8.8.8/public" {
		t.Fatalf("public origin should not need a whitelist: url=%q err=%v", publicURL, err)
	}

	client, err := New(Config{DataDir: dataDir, Version: "browser-e2e", BrowserSidecarDir: sidecarDir})
	if err != nil {
		t.Fatal(err)
	}
	manager := client.browser
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	defer func() { _ = manager.Close(context.Background()) }()

	hasBrowser := false
	for _, capability := range client.Capabilities() {
		if capability.CapabilityId == protocolv1.BrowserCapability.CapabilityId {
			hasBrowser = true
		}
	}
	if !hasBrowser {
		t.Fatal("Client did not advertise browser with managed Chromium installed")
	}

	call := func(callCtx context.Context, action string, params map[string]any) (map[string]any, error) {
		return client.browserControl(callCtx, workspace.WorkspaceID, action, params)
	}
	launch, err := call(ctx, "launch", map[string]any{"headless": true})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	sessionID, ok := launch["browserSessionId"].(string)
	if !ok || sessionID == "" {
		t.Fatalf("launch returned invalid session: %+v", launch)
	}

	if _, err := manager.Execute(ctx, otherWorkspace.WorkspaceID, "pages.list", map[string]any{"browserSessionId": sessionID}); !isBrowserActionCode(err, "BROWSER_SESSION_NOT_FOUND") {
		t.Fatalf("cross-workspace browser session was not rejected: %v", err)
	}
	if _, err := call(ctx, "page.open", map[string]any{"browserSessionId": sessionID, "url": unauthorizedServer.URL + "/"}); !errors.Is(err, ErrBrowserOriginDenied) {
		t.Fatalf("unauthorized origin error=%v", err)
	}

	opened, err := call(ctx, "page.open", map[string]any{
		"browserSessionId": sessionID,
		"url":              pageServer.URL + "/",
		"waitUntil":        "domcontentloaded",
	})
	if err != nil {
		t.Fatalf("page.open: %v", err)
	}
	pageID, ok := opened["pageId"].(string)
	if !ok || pageID == "" {
		t.Fatalf("page.open returned invalid page: %+v", opened)
	}
	pageParams := func(extra map[string]any) map[string]any {
		params := map[string]any{"browserSessionId": sessionID, "pageId": pageID}
		for key, value := range extra {
			params[key] = value
		}
		return params
	}
	if err := store.SetPermissions(workspace.WorkspaceID, []string{WorkspacePermissionRead}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Execute(ctx, workspace.WorkspaceID, "pages.list", map[string]any{"browserSessionId": sessionID}); err != nil {
		t.Fatalf("browser session closed after ordinary workspace revision change: %v", err)
	}
	if err := store.RevokeBrowserOrigin(workspace.WorkspaceID, pageServer.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := call(ctx, "page.navigate", pageParams(map[string]any{"url": pageServer.URL + "/", "waitUntil": "domcontentloaded"})); !errors.Is(err, ErrBrowserOriginDenied) {
		t.Fatalf("page.navigate did not recheck revoked origin: %v", err)
	}
	if _, err := store.AuthorizeBrowserOrigin(context.Background(), workspace.WorkspaceID, pageServer.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := call(ctx, "page.navigate", pageParams(map[string]any{"url": pageServer.URL + "/", "waitUntil": "domcontentloaded"})); err != nil {
		t.Fatalf("page.navigate rejected reauthorized origin: %v", err)
	}
	if _, err := store.AuthorizeBrowserOrigin(context.Background(), workspace.WorkspaceID, lateOriginServer.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := call(ctx, "page.open", map[string]any{"browserSessionId": sessionID, "url": lateOriginServer.URL + "/", "waitUntil": "domcontentloaded"}); !isBrowserActionCode(err, "BROWSER_NETWORK_DENIED") {
		t.Fatalf("launch snapshot unexpectedly hot-synced newly authorized origin: %v", err)
	}
	if _, err := call(ctx, "close", map[string]any{"browserSessionId": sessionID}); err != nil {
		t.Fatalf("close before refreshing allowed origin snapshot: %v", err)
	}
	relaunch, err := call(ctx, "launch", map[string]any{"headless": true})
	if err != nil {
		t.Fatalf("relaunch after origin change: %v", err)
	}
	newSessionID, ok := relaunch["browserSessionId"].(string)
	if !ok || newSessionID == "" || newSessionID == sessionID {
		t.Fatalf("relaunch returned invalid or reused session: old=%q result=%+v", sessionID, relaunch)
	}
	sessionID = newSessionID
	opened, err = call(ctx, "page.open", map[string]any{"browserSessionId": sessionID, "url": lateOriginServer.URL + "/", "waitUntil": "domcontentloaded"})
	if err != nil {
		t.Fatalf("new launch did not use latest authorized origin snapshot: %v", err)
	}
	pageID, ok = opened["pageId"].(string)
	if !ok || pageID == "" {
		t.Fatalf("new launch returned invalid late-origin page: %+v", opened)
	}
	if _, err := call(ctx, "page.navigate", pageParams(map[string]any{"url": pageServer.URL + "/", "waitUntil": "domcontentloaded"})); err != nil {
		t.Fatalf("new launch could not navigate to original origin: %v", err)
	}
	if _, err := call(ctx, "type", pageParams(map[string]any{
		"locator": map[string]any{"css": "#name"},
		"text":    "Fast Spider",
	})); err != nil {
		t.Fatalf("type: %v", err)
	}
	if _, err := call(ctx, "click", pageParams(map[string]any{"locator": map[string]any{"css": "#submit"}})); err != nil {
		t.Fatalf("click: %v", err)
	}
	if _, err := call(ctx, "wait", pageParams(map[string]any{
		"locator": map[string]any{"text": "clicked: Fast Spider", "exact": true},
		"state":   "visible",
	})); err != nil {
		t.Fatalf("wait after click: %v", err)
	}
	snapshot, err := call(ctx, "snapshot", pageParams(nil))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	ariaSnapshot, _ := snapshot["ariaSnapshot"].(string)
	if !strings.Contains(ariaSnapshot, "Fast Spider") || !strings.Contains(ariaSnapshot, "clicked") {
		t.Fatalf("snapshot did not contain typed/clicked state: %q", ariaSnapshot)
	}

	unauthorizedWS := strings.Replace(unauthorizedServer.URL, "http://", "ws://", 1) + "/socket"
	if _, err := call(ctx, "type", pageParams(map[string]any{
		"locator": map[string]any{"css": "#ws-url"},
		"text":    unauthorizedWS,
	})); err != nil {
		t.Fatalf("type unauthorized websocket URL: %v", err)
	}
	if _, err := call(ctx, "click", pageParams(map[string]any{"locator": map[string]any{"css": "#probe"}})); err != nil {
		t.Fatalf("click websocket probe: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		events, eventErr := call(ctx, "events", map[string]any{"browserSessionId": sessionID, "cursor": 0})
		if eventErr != nil {
			t.Fatalf("events: %v", eventErr)
		}
		if browserEventsContain(events, "websocket_blocked") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("events did not contain websocket_blocked: %+v", events)
		}
		time.Sleep(100 * time.Millisecond)
	}

	var consumedPath string
	screenshot, err := manager.ExecuteScreenshot(ctx, workspace.WorkspaceID, pageParams(map[string]any{"format": "png"}), func(path, logicalName, contentType string) (map[string]any, error) {
		consumedPath = path
		screenshotRoot := filepath.Clean(filepath.Join(dataDir, "browser", "sessions", sessionID, "screenshots"))
		if !pathWithin(screenshotRoot, filepath.Clean(path)) {
			return nil, fmt.Errorf("screenshot path escaped managed directory: path=%s root=%s", path, screenshotRoot)
		}
		info, statErr := os.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() < 1 {
			return nil, fmt.Errorf("invalid consumed screenshot path=%q err=%v", path, statErr)
		}
		if logicalName == "" || contentType != "image/png" {
			return nil, fmt.Errorf("invalid screenshot metadata name=%q contentType=%q", logicalName, contentType)
		}
		return map[string]any{"artifactId": "art_browser_e2e"}, nil
	})
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if _, leaked := screenshot["path"]; leaked {
		t.Fatalf("screenshot result leaked local path: %+v", screenshot)
	}
	if screenshot["artifactId"] != "art_browser_e2e" || consumedPath == "" {
		t.Fatalf("screenshot did not return consumed artifact: %+v", screenshot)
	}
	if _, err := os.Stat(consumedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed screenshot was not cleaned up, stat error=%v", err)
	}

	cancelCtx, cancelAction := context.WithCancel(context.Background())
	navigateErr := make(chan error, 1)
	go func() {
		_, navigateErrValue := manager.Execute(cancelCtx, workspace.WorkspaceID, "page.navigate", pageParams(map[string]any{
			"url":       pageServer.URL + "/hang",
			"waitUntil": "load",
			"timeoutMs": 30000,
		}))
		navigateErr <- navigateErrValue
	}()
	select {
	case <-hangStarted:
	case <-time.After(10 * time.Second):
		cancelAction()
		t.Fatalf("page.navigate did not reach the hanging local page")
	}
	cancelAction()
	select {
	case navigateErrValue := <-navigateErr:
		if !errors.Is(navigateErrValue, context.Canceled) {
			t.Fatalf("canceled page.navigate error=%v", navigateErrValue)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("canceled page.navigate did not return")
	}

	deadline = time.Now().Add(5 * time.Second)
	for {
		manager.mu.Lock()
		sessionCleared := manager.session == nil
		manager.mu.Unlock()
		manager.sidecar.mu.Lock()
		sidecarStopped := manager.sidecar.cmd == nil
		manager.sidecar.mu.Unlock()
		if sessionCleared && sidecarStopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("context cancellation did not clear session/stop sidecar: sessionCleared=%v sidecarStopped=%v", sessionCleared, sidecarStopped)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := manager.Execute(context.Background(), workspace.WorkspaceID, "pages.list", map[string]any{"browserSessionId": sessionID}); !isBrowserActionCode(err, "BROWSER_SESSION_NOT_FOUND") {
		t.Fatalf("cleared session remained usable after context cancellation: %v", err)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sidecarDir, "%SystemDrive%")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Browser E2E left Windows input-method directory under sidecar: stat error=%v", err)
	}
}

func realBrowserSidecarDir(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate browser E2E test source")
	}
	sidecarDir, err := filepath.Abs(filepath.Join(filepath.Dir(sourceFile), "..", "..", "sidecar", "browser"))
	if err != nil {
		t.Fatal(err)
	}
	return sidecarDir
}

func isBrowserActionCode(err error, want string) bool {
	var actionErr *BrowserActionError
	return errors.As(err, &actionErr) && actionErr.Code == want
}

func browserEventsContain(result map[string]any, want string) bool {
	events, ok := result["events"].([]any)
	if !ok {
		return false
	}
	for _, raw := range events {
		event, ok := raw.(map[string]any)
		if ok && event["type"] == want {
			return true
		}
	}
	return false
}
