package node

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestValidateBrowserNavigationURLPolicy(t *testing.T) {
	allowed := []string{
		"https://example.com",
		"http://localhost:8080",
		"http://127.0.0.1:8080",
		"http://[::1]:8080",
		"http://192.168.1.10",
		"http://10.0.0.10",
		"http://172.20.0.10",
		"http://integration-postgres:5432",
		"http://host.docker.internal:8080",
		"http://wsl-development.local:3000",
		"http://lan-hostname",
	}
	for _, raw := range allowed {
		t.Run("allow_"+raw, func(t *testing.T) {
			if err := validateBrowserNavigationURL(raw); err != nil {
				t.Fatalf("URL %q rejected: %v", raw, err)
			}
		})
	}

	rejected := []string{
		"file:///c:/test.txt",
		"javascript:alert(1)",
		"data:text/html,hello",
		"ftp://example.com/file",
		"https://user:password@example.com",
		"https://user@example.com",
		"https://@example.com",
		"https://example.com:bad",
		"http://",
		"not a URL",
		"/relative/path",
	}
	for _, raw := range rejected {
		t.Run("reject_"+raw, func(t *testing.T) {
			err := validateBrowserNavigationURL(raw)
			var browserErr *BrowserActionError
			if !errors.As(err, &browserErr) || browserErr.Code != "BROWSER_NETWORK_DENIED" {
				t.Fatalf("URL %q error=%v", raw, err)
			}
		})
	}
}

func TestBrowserReadinessSanitizerAcceptsOnlyEmptyParams(t *testing.T) {
	params, err := sanitizeBrowserParams("readiness", map[string]any{})
	if err != nil || len(params) != 0 {
		t.Fatalf("readiness params=%v err=%v", params, err)
	}
	if _, err := sanitizeBrowserParams("readiness", map[string]any{"browserSessionId": "brs_unexpected"}); err == nil {
		t.Fatal("readiness accepted a browser session parameter")
	}
	client := NewLocalCapabilityClient(Config{DataDir: t.TempDir(), BrowserSidecarDir: filepath.Join(t.TempDir(), "missing")})
	response := client.HandleLocalCapability(context.Background(), protocolv1.CapabilityRequest{
		RequestId: "req_browser_readiness", Capability: "browser.automation", Action: "readiness", Params: map[string]any{},
	})
	if response.Error != nil || response.Result["ready"] != false {
		t.Fatalf("local readiness response=%#v", response)
	}
}

func TestSanitizeBrowserParamsEnforcesNavigationURLPolicy(t *testing.T) {
	if _, err := sanitizeBrowserParams("page.open", map[string]any{"browserSessionId": "brs_test", "url": "http://integration-postgres:5432"}); err != nil {
		t.Fatalf("integration hostname rejected: %v", err)
	}
	if _, err := sanitizeBrowserParams("page.navigate", map[string]any{"browserSessionId": "brs_test", "pageId": "pg_test", "url": "https://user:password@example.com"}); err == nil {
		t.Fatal("credentialed navigation URL was accepted")
	}
}

func TestBrowserManagerUsesMachineBoundary(t *testing.T) {
	manager := NewBrowserManager(t.TempDir(), t.TempDir(), nil)
	if _, err := manager.Execute(context.Background(), "unsupported", map[string]any{}); err == nil {
		t.Fatal("unsupported browser action unexpectedly succeeded")
	}
}

func TestSanitizeBrowserParamsAllowsRefsAndBoundedBatch(t *testing.T) {
	click, err := sanitizeBrowserParams("click", map[string]any{
		"browserSessionId": "brs_test",
		"pageId":           "pg_test",
		"ref":              "e_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if click["ref"] != "e_1" {
		t.Fatalf("click ref=%v", click["ref"])
	}

	batch, err := sanitizeBrowserParams("batch", map[string]any{
		"browserSessionId": "brs_test",
		"pageId":           "pg_test",
		"steps": []any{
			map[string]any{"action": "type", "ref": "e_1", "text": "Fast Spider"},
			map[string]any{"action": "click", "ref": "e_2"},
		},
		"snapshotAfter": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	steps, ok := batch["steps"].([]any)
	if !ok || len(steps) != 2 {
		t.Fatalf("batch steps=%T %+v", batch["steps"], batch["steps"])
	}
}

func TestSanitizeBrowserParamsRejectsBatchEscape(t *testing.T) {
	_, err := sanitizeBrowserParams("batch", map[string]any{
		"browserSessionId": "brs_test",
		"pageId":           "pg_test",
		"steps": []any{
			map[string]any{"action": "click", "ref": "e_1", "javascript": "alert(1)"},
		},
	})
	if err == nil {
		t.Fatal("batch accepted an unsupported nested parameter")
	}

	_, err = sanitizeBrowserParams("batch", map[string]any{
		"browserSessionId": "brs_test",
		"pageId":           "pg_test",
		"steps": []any{
			map[string]any{"action": "evaluate", "ref": "e_1"},
		},
	})
	if err == nil {
		t.Fatal("batch accepted an unsupported nested action")
	}
}
