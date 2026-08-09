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
	hangStarted := make(chan struct{}, 1)
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, `<!doctype html><html><body>
<label for="name">Name</label><input id="name"><button id="submit">Apply</button>
<output id="status">idle</output><script>
const name=document.querySelector('#name'); const status=document.querySelector('#status');
document.querySelector('#submit').addEventListener('click',()=>{status.textContent='clicked: '+name.value});
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
		return client.browserControl(callCtx, action, params)
	}
	launch, err := call(ctx, "launch", map[string]any{"headless": true})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	sessionID, _ := launch["browserSessionId"].(string)
	if sessionID == "" {
		t.Fatalf("launch returned invalid session: %+v", launch)
	}

	opened, err := call(ctx, "page.open", map[string]any{"browserSessionId": sessionID, "url": pageServer.URL + "/", "waitUntil": "domcontentloaded"})
	if err != nil {
		t.Fatalf("local/private page.open should be allowed in machine mode: %v", err)
	}
	pageID, _ := opened["pageId"].(string)
	if pageID == "" {
		t.Fatalf("page.open returned invalid page: %+v", opened)
	}
	pageParams := func(extra map[string]any) map[string]any {
		params := map[string]any{"browserSessionId": sessionID, "pageId": pageID}
		for k, v := range extra {
			params[k] = v
		}
		return params
	}
	if _, err := call(ctx, "type", pageParams(map[string]any{"locator": map[string]any{"css": "#name"}, "text": "Fast Spider"})); err != nil {
		t.Fatalf("type: %v", err)
	}
	if _, err := call(ctx, "click", pageParams(map[string]any{"locator": map[string]any{"css": "#submit"}})); err != nil {
		t.Fatalf("click: %v", err)
	}
	if _, err := call(ctx, "wait", pageParams(map[string]any{"locator": map[string]any{"text": "clicked: Fast Spider", "exact": true}, "state": "visible"})); err != nil {
		t.Fatalf("wait: %v", err)
	}
	snapshot, err := call(ctx, "snapshot", pageParams(nil))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	ariaSnapshot, _ := snapshot["ariaSnapshot"].(string)
	if !strings.Contains(ariaSnapshot, "Fast Spider") || !strings.Contains(ariaSnapshot, "clicked") {
		t.Fatalf("snapshot=%q", ariaSnapshot)
	}

	var consumedPath string
	screenshot, err := manager.ExecuteScreenshot(ctx, pageParams(map[string]any{"format": "png"}), func(path, logicalName, contentType string) (map[string]any, error) {
		consumedPath = path
		screenshotRoot := filepath.Clean(filepath.Join(dataDir, "browser", "sessions", sessionID, "screenshots"))
		if !pathWithin(screenshotRoot, filepath.Clean(path)) {
			return nil, fmt.Errorf("screenshot escaped managed directory")
		}
		if info, statErr := os.Stat(path); statErr != nil || !info.Mode().IsRegular() || info.Size() < 1 {
			return nil, fmt.Errorf("invalid screenshot: %v", statErr)
		}
		if logicalName == "" || contentType != "image/png" {
			return nil, fmt.Errorf("invalid screenshot metadata")
		}
		return map[string]any{"artifactId": "art_browser_e2e"}, nil
	})
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if _, leaked := screenshot["path"]; leaked {
		t.Fatalf("screenshot leaked path: %+v", screenshot)
	}
	if screenshot["artifactId"] != "art_browser_e2e" || consumedPath == "" {
		t.Fatalf("screenshot=%+v", screenshot)
	}
	if _, err := os.Stat(consumedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed screenshot not cleaned: %v", err)
	}

	cancelCtx, cancelAction := context.WithCancel(context.Background())
	navigateErr := make(chan error, 1)
	go func() {
		_, err := manager.Execute(cancelCtx, "page.navigate", pageParams(map[string]any{"url": pageServer.URL + "/hang", "waitUntil": "load", "timeoutMs": 30000}))
		navigateErr <- err
	}()
	select {
	case <-hangStarted:
	case <-time.After(10 * time.Second):
		cancelAction()
		t.Fatal("hanging navigation did not start")
	}
	cancelAction()
	select {
	case err := <-navigateErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled navigation error=%v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("canceled navigation did not return")
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
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
