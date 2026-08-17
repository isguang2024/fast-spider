package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/server"
	"github.com/isguang2024/fast-spider/internal/hub/store"
)

func TestDirectAccessAPIIsIndependentAndScopeBound(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := core.New(st, registry.New(), core.Config{DataDir: dataDir, Version: "direct-api-test"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	account, err := service.BootstrapAccount(ctx, bootstrap, "direct-api-owner", "Direct API Owner", "correct horse battery staple", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	directKey, err := service.CreateDirectAccessKey(ctx, account.OwnerID, "http-ai", "", nil, time.Hour, 20, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	connectionToken, err := service.CreateConnectionToken(ctx, account.OwnerID, "node-only", time.Hour, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	hub := httptest.NewServer(server.New(service, server.Config{}).Handler())
	defer hub.Close()

	resp := directRequest(t, http.MethodGet, hub.URL+"/direct/v1/tools", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated tools status=%d body=%s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()

	resp = directRequest(t, http.MethodGet, hub.URL+"/direct/v1/tools", connectionToken.Token, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("connection token was accepted by Direct API: status=%d body=%s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()

	resp = directRequest(t, http.MethodGet, hub.URL+"/direct/v1/tools", directKey.Token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("direct tools status=%d body=%s", resp.StatusCode, readBody(t, resp))
	}
	var tools struct {
		APIVersion string `json:"apiVersion"`
		Tools      []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tools); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if tools.APIVersion != "v1" || len(tools.Tools) != 17 {
		t.Fatalf("direct tools response version=%q count=%d", tools.APIVersion, len(tools.Tools))
	}
	seen := map[string]bool{}
	for _, tool := range tools.Tools {
		seen[tool.Name] = true
		if len(tool.InputSchema) == 0 {
			t.Fatalf("tool %q has empty input schema", tool.Name)
		}
	}
	for _, name := range []string{"machine_list", "file_read", "file_edit", "shell_run", "browser_control", "ai_control", "working_context"} {
		if !seen[name] {
			t.Fatalf("missing direct tool %q", name)
		}
	}

	resp = directRequest(t, http.MethodPost, hub.URL+"/direct/v1/call", directKey.Token, []byte(`{"tool":"thinking_team","arguments":{"action":"departments.list"}}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("thinking_team direct status=%d body=%s", resp.StatusCode, readBody(t, resp))
	}
	body := readBody(t, resp)
	resp.Body.Close()
	if !strings.Contains(body, `"ok":true`) || !strings.Contains(body, `"departments"`) {
		t.Fatalf("unexpected direct thinking response: %s", body)
	}

	resp = directRequest(t, http.MethodPost, hub.URL+"/direct/v1/call", directKey.Token, []byte(`{"tool":"file_edit","arguments":{}}`))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read-only key file_edit status=%d body=%s", resp.StatusCode, readBody(t, resp))
	}
	body = readBody(t, resp)
	resp.Body.Close()
	if !strings.Contains(body, "DIRECT_SCOPE_REQUIRED") {
		t.Fatalf("missing direct scope error: %s", body)
	}

	resp = directRequest(t, http.MethodPost, hub.URL+"/mcp", directKey.Token, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("direct key was accepted by MCP OAuth endpoint: status=%d body=%s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()

	if err := service.RevokeDirectAccessKey(ctx, account.OwnerID, directKey.Record.ID, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	resp = directRequest(t, http.MethodGet, hub.URL+"/direct/v1/tools", directKey.Token, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked direct key status=%d body=%s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()
}

func TestDirectAccessAPIRateLimit(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := core.New(st, registry.New(), core.Config{DataDir: dataDir, Version: "direct-rate-test"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	account, err := service.BootstrapAccount(ctx, bootstrap, "direct-rate-owner", "Direct Rate Owner", "correct horse battery staple", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	key, err := service.CreateDirectAccessKey(ctx, account.OwnerID, "one-per-minute", "", nil, time.Hour, 1, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	hub := httptest.NewServer(server.New(service, server.Config{}).Handler())
	defer hub.Close()

	resp := directRequest(t, http.MethodGet, hub.URL+"/direct/v1/tools", key.Token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first direct request status=%d body=%s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()
	resp = directRequest(t, http.MethodGet, hub.URL+"/direct/v1/tools", key.Token, nil)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "60" {
		t.Fatalf("second direct request status=%d retry-after=%q body=%s", resp.StatusCode, resp.Header.Get("Retry-After"), readBody(t, resp))
	}
	resp.Body.Close()
}

func directRequest(t *testing.T, method, url, token string, body []byte) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
