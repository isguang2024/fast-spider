package node_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/server"
	"github.com/isguang2024/fast-spider/internal/hub/store"
	"github.com/isguang2024/fast-spider/internal/node"
)

func TestNodeLoginUsesBrowserOAuthAndInternalEnrollment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hubDataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(hubDataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := core.New(st, registry.New(), core.Config{DataDir: hubDataDir, Version: "node-login-test"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	account, err := service.BootstrapAccount(
		ctx,
		bootstrapToken,
		"owner",
		"Fast Spider Owner",
		"correct horse battery staple",
		"127.0.0.1",
	)
	if err != nil {
		t.Fatal(err)
	}
	webSession, err := service.CreateWebSession(ctx, account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}

	hub := server.New(service, server.Config{})
	httpServer := httptest.NewServer(hub.Handler())
	defer httpServer.Close()

	nodeClient, err := node.New(node.Config{
		DataDir: t.TempDir(), Version: "node-login-test", AllowInsecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	authorizationResult := make(chan error, 1)
	state, err := nodeClient.Login(ctx, node.LoginOptions{
		HubURL:      httpServer.URL,
		DisplayName: "Windows Dev",
		OpenBrowser: false,
		AuthorizationReady: func(authorizeURL string) {
			go func() {
				authorizationResult <- approveNodeOAuth(authorizeURL, httpServer.URL, webSession.Token, webSession.CSRFToken)
			}()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-authorizationResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if state.MachineID == "" || state.HubURL != httpServer.URL {
		t.Fatalf("unexpected node state: %+v", state)
	}

	machines, err := service.ListMachines(ctx, account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(machines) != 1 || machines[0].MachineID != state.MachineID || machines[0].DisplayName != "Windows Dev" {
		t.Fatalf("unexpected machines: %+v", machines)
	}
	authorizations, err := st.ListOAuthAuthorizations(ctx, account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(authorizations) != 1 || authorizations[0].RevokedAt == nil {
		t.Fatalf("temporary Node OAuth authorization was not revoked: %+v", authorizations)
	}
	if len(authorizations[0].Scopes) != 1 || authorizations[0].Scopes[0] != "fast-spider:device-connect" {
		t.Fatalf("unexpected Node OAuth scope: %+v", authorizations[0].Scopes)
	}
	clients, err := st.ListOAuthClients(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 1 || len(clients[0].GrantTypes) != 1 || clients[0].GrantTypes[0] != "authorization_code" {
		t.Fatalf("Node client requested unexpected grants: %+v", clients)
	}
	dashboardJar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	hubURL, err := url.Parse(httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	dashboardJar.SetCookies(hubURL, []*http.Cookie{{Name: "fast_spider_session", Value: webSession.Token, Path: "/"}})
	dashboardClient := &http.Client{Jar: dashboardJar, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := dashboardClient.Get(httpServer.URL + "/app")
	if err != nil {
		t.Fatal(err)
	}
	dashboardBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Node dashboard status=%d body=%s", resp.StatusCode, dashboardBody)
	}
	if strings.Contains(string(dashboardBody), clients[0].ClientName) || strings.Contains(string(dashboardBody), "fast-spider:device-connect") {
		t.Fatalf("device-connect OAuth client was exposed in owner dashboard: %s", dashboardBody)
	}
}

func TestNodeReloginAfterRevokedMachinePreservesWorkspaceRegistry(t *testing.T) {
	fixture := newNodeLoginFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	nodeDataDir := t.TempDir()
	workspaceRoot := t.TempDir()
	workspace, err := node.NewWorkspaceStore(nodeDataDir).Add(workspaceRoot, "persistent workspace")
	if err != nil {
		t.Fatal(err)
	}
	workspacePath := filepath.Join(nodeDataDir, "workspaces.json")
	before, err := os.ReadFile(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	nodeClient, err := node.New(node.Config{DataDir: nodeDataDir, Version: "node-relogin-test", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	first, err := loginNodeWithApproval(ctx, nodeClient, fixture.httpServer.URL, fixture.webSession, "Repaired Node")
	if err != nil {
		t.Fatal(err)
	}
	if first.MachineID == "" {
		t.Fatal("first Node login returned empty machine ID")
	}
	if err := fixture.service.RevokeMachine(ctx, fixture.account.OwnerID, first.MachineID, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	second, err := loginNodeWithApproval(ctx, nodeClient, fixture.httpServer.URL, fixture.webSession, "Repaired Node Again")
	if err != nil {
		t.Fatal(err)
	}
	if second.MachineID == "" || second.MachineID == first.MachineID {
		t.Fatalf("re-login did not create a new machine ID: first=%q second=%q", first.MachineID, second.MachineID)
	}
	after, err := os.ReadFile(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("workspaces.json changed during revoked-machine re-login")
	}
	retained, err := node.NewWorkspaceStore(nodeDataDir).Lookup(workspace.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if retained.WorkspaceID != workspace.WorkspaceID || retained.Root != workspace.Root {
		t.Fatalf("opaque workspace identity was not retained: before=%+v after=%+v", workspace, retained)
	}
	machines, err := fixture.service.ListMachines(ctx, fixture.account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(machines) != 2 {
		t.Fatalf("machine history after re-login=%+v, want old revoked plus new machine", machines)
	}
	authorizations, err := fixture.st.ListOAuthAuthorizations(ctx, fixture.account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(authorizations) != 2 {
		t.Fatalf("OAuth authorization count after re-login=%d, want 2", len(authorizations))
	}
}

func loginNodeWithApproval(ctx context.Context, client *node.Client, hubURL string, session core.WebSessionResult, displayName string) (node.State, error) {
	authorizationResult := make(chan error, 1)
	state, err := client.Login(ctx, node.LoginOptions{
		HubURL: hubURL, DisplayName: displayName, OpenBrowser: false,
		AuthorizationReady: func(authorizeURL string) {
			go func() {
				authorizationResult <- approveNodeOAuth(authorizeURL, hubURL, session.Token, session.CSRFToken)
			}()
		},
	})
	if err != nil {
		return node.State{}, err
	}
	select {
	case err := <-authorizationResult:
		if err != nil {
			return node.State{}, err
		}
		return state, nil
	case <-ctx.Done():
		return node.State{}, ctx.Err()
	}
}

func TestNodeLoginDenyDoesNotCreateMachine(t *testing.T) {
	fixture := newNodeLoginFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	nodeClient, err := node.New(node.Config{DataDir: t.TempDir(), Version: "node-deny-test", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	decisionResult := make(chan error, 1)
	_, err = nodeClient.Login(ctx, node.LoginOptions{
		HubURL:      fixture.httpServer.URL,
		DisplayName: "Denied Node",
		OpenBrowser: false,
		AuthorizationReady: func(authorizeURL string) {
			go func() {
				decisionResult <- denyNodeOAuth(authorizeURL, fixture.httpServer.URL, fixture.webSession.Token, fixture.webSession.CSRFToken)
			}()
		},
	})
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("denied Node login error=%v, want access_denied", err)
	}
	if decisionErr := <-decisionResult; decisionErr != nil {
		t.Fatal(decisionErr)
	}
	machines, err := fixture.service.ListMachines(ctx, fixture.account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(machines) != 0 {
		t.Fatalf("denied Node login created machines: %+v", machines)
	}
	authorizations, err := fixture.st.ListOAuthAuthorizations(ctx, fixture.account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(authorizations) != 0 {
		t.Fatalf("denied Node login created authorizations: %+v", authorizations)
	}
}

func TestNodeLoginCancellationDoesNotCreateMachine(t *testing.T) {
	fixture := newNodeLoginFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	nodeClient, err := node.New(node.Config{DataDir: t.TempDir(), Version: "node-cancel-test", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = nodeClient.Login(ctx, node.LoginOptions{
		HubURL:      fixture.httpServer.URL,
		DisplayName: "Canceled Node",
		OpenBrowser: false,
		AuthorizationReady: func(string) {
			cancel()
		},
	})
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("canceled Node login error=%v, want context canceled", err)
	}
	machines, err := fixture.service.ListMachines(context.Background(), fixture.account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(machines) != 0 {
		t.Fatalf("canceled Node login created machines: %+v", machines)
	}
	authorizations, err := fixture.st.ListOAuthAuthorizations(context.Background(), fixture.account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(authorizations) != 0 {
		t.Fatalf("canceled Node login created authorizations: %+v", authorizations)
	}
}

type nodeLoginFixture struct {
	st         *store.Store
	service    *core.Service
	account    core.OwnerAccountView
	webSession core.WebSessionResult
	httpServer *httptest.Server
}

func newNodeLoginFixture(t *testing.T) nodeLoginFixture {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := core.New(st, registry.New(), core.Config{DataDir: dataDir, Version: "node-cancel-fixture"})
	if err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	account, err := service.BootstrapAccount(ctx, bootstrapToken, "node-owner", "Node Owner", "correct horse battery staple", "127.0.0.1")
	if err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	webSession, err := service.CreateWebSession(ctx, account.OwnerID)
	if err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	hub := server.New(service, server.Config{})
	httpServer := httptest.NewServer(hub.Handler())
	t.Cleanup(func() {
		httpServer.Close()
		_ = st.Close()
	})
	return nodeLoginFixture{st: st, service: service, account: account, webSession: webSession, httpServer: httpServer}
}

func denyNodeOAuth(authorizeURL, hubURL, sessionToken, csrfToken string) error {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	hub, err := url.Parse(hubURL)
	if err != nil {
		return err
	}
	jar.SetCookies(hub, []*http.Cookie{{Name: "fast_spider_session", Value: sessionToken, Path: "/"}})
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	authorize, err := url.Parse(authorizeURL)
	if err != nil {
		return err
	}
	form := make(url.Values)
	for key, values := range authorize.Query() {
		form[key] = append([]string(nil), values...)
	}
	form.Set("decision", "deny")
	form.Set("csrf_token", csrfToken)
	req, err := http.NewRequest(http.MethodPost, authorize.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "登录未完成") {
		return fmt.Errorf("OAuth denial callback status=%d body=%s", resp.StatusCode, body)
	}
	return nil
}

func approveNodeOAuth(authorizeURL, hubURL, sessionToken, csrfToken string) error {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	hub, err := url.Parse(hubURL)
	if err != nil {
		return err
	}
	jar.SetCookies(hub, []*http.Cookie{{Name: "fast_spider_session", Value: sessionToken, Path: "/"}})
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	authorize, err := url.Parse(authorizeURL)
	if err != nil {
		return err
	}
	form := make(url.Values)
	for key, values := range authorize.Query() {
		form[key] = append([]string(nil), values...)
	}
	form.Set("decision", "approve")
	form.Set("csrf_token", csrfToken)
	req, err := http.NewRequest(http.MethodPost, authorize.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("OAuth approval status=%d body=%s", resp.StatusCode, body)
	}
	return nil
}
