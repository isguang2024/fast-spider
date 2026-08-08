package node

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

func TestBrowserOriginPolicyAndWorkspaceSummaries(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want string
	}{
		{raw: "HTTPS://Example.COM", want: "https://example.com:443"},
		{raw: "http://127.0.0.1:8080/", want: "http://127.0.0.1:8080"},
		{raw: "https://[2001:db8::1]", want: "https://[2001:db8::1]:443"},
	} {
		got, _, err := normalizeBrowserOrigin(test.raw)
		if err != nil || got != test.want {
			t.Fatalf("normalizeBrowserOrigin(%q)=%q err=%v want=%q", test.raw, got, err, test.want)
		}
	}
	for _, raw := range []string{
		"ftp://example.com",
		"file:///tmp/page",
		"https://example.com/path",
		"https://example.com?query=1",
		"https://example.com#fragment",
		"https://user:pass@example.com",
	} {
		if _, _, err := normalizeBrowserOrigin(raw); err == nil {
			t.Fatalf("normalizeBrowserOrigin(%q) unexpectedly accepted", raw)
		}
	}

	dataDir := t.TempDir()
	root := t.TempDir()
	store := NewWorkspaceStore(dataDir)
	workspace, err := store.Add(root, "browser-policy")
	if err != nil {
		t.Fatal(err)
	}
	defaultOrigin, err := store.AuthorizeBrowserOrigin(context.Background(), workspace.WorkspaceID, "HTTP://127.0.0.1:8080/")
	if err != nil {
		t.Fatal(err)
	}
	if defaultOrigin.Origin != "http://127.0.0.1:8080" || len(defaultOrigin.PinnedIPs) != 1 || defaultOrigin.PinnedIPs[0] != "127.0.0.1" {
		t.Fatalf("default browser origin=%+v", defaultOrigin)
	}
	if got, err := store.ValidateBrowserURL(context.Background(), workspace.WorkspaceID, "http://8.8.8.8:8080/public?query=1"); err != nil || got != "http://8.8.8.8:8080/public?query=1" {
		t.Fatalf("public URL should not need a whitelist: got=%q err=%v", got, err)
	}
	if _, err := store.ValidateBrowserURL(context.Background(), workspace.WorkspaceID, "http://127.0.0.1:8080/private"); err != nil {
		t.Fatalf("authorized local origin rejected: %v", err)
	}
	reloaded := NewWorkspaceStore(dataDir)
	if _, err := reloaded.ValidateBrowserURL(context.Background(), workspace.WorkspaceID, "http://127.0.0.1:8080/after-reload"); err != nil {
		t.Fatalf("persisted local origin was not reusable: %v", err)
	}

	for _, raw := range []string{"http://localhost:8765", "http://10.0.0.1:8766", "http://192.168.1.1:8767"} {
		if _, err := store.AuthorizeBrowserOrigin(context.Background(), workspace.WorkspaceID, raw); err != nil {
			t.Fatalf("explicit local/private origin %q rejected: %v", raw, err)
		}
	}
	for _, raw := range []string{"http://169.254.1.1:8080", "http://100.100.100.200:8080", "http://[fe80::1]:8080"} {
		if _, err := store.AuthorizeBrowserOrigin(context.Background(), workspace.WorkspaceID, raw); !errors.Is(err, ErrBrowserOriginUnsafe) {
			t.Fatalf("unsafe origin %q error=%v, want ErrBrowserOriginUnsafe", raw, err)
		}
	}

	local, err := store.ListLocal()
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 1 || local[0].Root != workspace.Root {
		t.Fatalf("local workspace summary=%+v", local)
	}
	var foundLocalOrigin bool
	for _, origin := range local[0].BrowserOrigins {
		if origin.Origin == defaultOrigin.Origin {
			foundLocalOrigin = true
		}
	}
	if !foundLocalOrigin {
		t.Fatalf("local summary did not expose authorized origin: %+v", local[0])
	}
	remote, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	rawRemote, err := json.Marshal(remote)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawRemote), workspace.Root) || strings.Contains(string(rawRemote), defaultOrigin.Origin) || strings.Contains(string(rawRemote), "browserOrigins") {
		t.Fatalf("remote workspace summary leaked local browser data: %s", rawRemote)
	}
}

func TestBrowserManagerOriginAndSessionScope(t *testing.T) {
	dataDir := t.TempDir()
	workspaceRoot := t.TempDir()
	otherRoot := t.TempDir()
	store := NewWorkspaceStore(dataDir)
	workspace, err := store.Add(workspaceRoot, "browser-one")
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Add(otherRoot, "browser-two")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewBrowserManager(dataDir, filepath.Join(dataDir, "missing-sidecar"), nil)
	ctx := context.Background()
	if _, err := store.ValidateBrowserURL(ctx, workspace.WorkspaceID, "http://127.0.0.1:8765"); !errors.Is(err, ErrBrowserOriginDenied) {
		t.Fatalf("unauthorized local origin error=%v", err)
	}

	manager.mu.Lock()
	manager.session = &browserSessionRecord{BrowserSessionID: "brs_scope_test", WorkspaceID: workspace.WorkspaceID}
	manager.mu.Unlock()
	for _, test := range []struct {
		name        string
		workspaceID string
		sessionID   string
	}{
		{name: "cross workspace", workspaceID: other.WorkspaceID, sessionID: "brs_scope_test"},
		{name: "wrong session", workspaceID: workspace.WorkspaceID, sessionID: "brs_other"},
	} {
		_, err := manager.Execute(ctx, test.workspaceID, "pages.list", map[string]any{"browserSessionId": test.sessionID})
		var actionErr *BrowserActionError
		if !errors.As(err, &actionErr) || actionErr.Code != "BROWSER_SESSION_NOT_FOUND" {
			t.Fatalf("%s error=%v", test.name, err)
		}
	}
}

func TestBrowserUnavailableCapabilitiesOmitBrowser(t *testing.T) {
	client, err := New(Config{DataDir: t.TempDir(), Version: "test", BrowserSidecarDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.browser.Available(); !errors.Is(err, ErrBrowserUnavailable) {
		t.Fatalf("unavailable sidecar error=%v", err)
	}
	for _, capability := range client.Capabilities() {
		if capability.CapabilityId == protocolv1.BrowserCapability.CapabilityId {
			t.Fatal("unavailable BrowserSidecar was advertised in client capabilities")
		}
	}
}
