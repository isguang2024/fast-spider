package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
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

func TestWebSetupLoginAndDashboard(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := core.New(st, registry.New(), core.Config{DataDir: dataDir, Version: "web-test"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hub := server.New(service, server.Config{})
	httpServer := httptest.NewServer(hub.Handler())
	defer httpServer.Close()

	client := newWebTestClient(t)
	status, headers, _ := webTestRequest(t, client, http.MethodGet, httpServer.URL+"/", nil)
	if status != http.StatusFound || !strings.HasSuffix(headers.Get("Location"), "/setup") {
		t.Fatalf("root before setup status=%d location=%q", status, headers.Get("Location"))
	}

	status, headers, body := webTestRequest(t, client, http.MethodPost, httpServer.URL+"/setup", url.Values{
		"bootstrap_token":  {bootstrapToken},
		"username":         {"owner"},
		"display_name":     {"Fast Spider Owner"},
		"password":         {"correct horse battery staple"},
		"password_confirm": {"correct horse battery staple"},
	})
	if status != http.StatusSeeOther || !strings.HasSuffix(headers.Get("Location"), "/app") {
		t.Fatalf("setup status=%d location=%q body=%s", status, headers.Get("Location"), body)
	}

	status, _, body = webTestRequest(t, client, http.MethodGet, httpServer.URL+"/app", nil)
	if status != http.StatusOK || !strings.Contains(string(body), "设备与授权中心") {
		t.Fatalf("dashboard status=%d body=%s", status, body)
	}
	if strings.Contains(string(body), "Owner Token") {
		t.Fatal("dashboard exposed legacy Owner Token UI")
	}
	if completeSetupToken, err := service.EnsureBootstrap(ctx); err != nil {
		t.Fatal(err)
	} else if completeSetupToken != "" {
		t.Fatalf("complete Web account unexpectedly regenerated setup token %q", completeSetupToken)
	}

	freshClient := newWebTestClient(t)
	status, headers, body = webTestRequest(t, freshClient, http.MethodPost, httpServer.URL+"/login", url.Values{
		"username":  {"owner"},
		"password":  {"correct horse battery staple"},
		"return_to": {httpServer.URL + "/app"},
	})
	if status != http.StatusSeeOther || headers.Get("Location") != httpServer.URL+"/app" {
		t.Fatalf("login status=%d location=%q body=%s", status, headers.Get("Location"), body)
	}
	status, _, body = webTestRequest(t, freshClient, http.MethodGet, httpServer.URL+"/app", nil)
	if status != http.StatusOK || !strings.Contains(string(body), "Fast Spider Owner") {
		t.Fatalf("dashboard after login status=%d body=%s", status, body)
	}
}

func TestWebConnectionTokenIsOneTimeAndCSRFProtected(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := core.New(st, registry.New(), core.Config{DataDir: dataDir, Version: "web-pat-test"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	account, err := service.BootstrapAccount(ctx, bootstrapToken, "token-owner", "Token Owner", "correct horse battery staple", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateWebSession(ctx, account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	hub := server.New(service, server.Config{})
	httpServer := httptest.NewServer(hub.Handler())
	defer httpServer.Close()

	client := newWebTestClient(t)
	hubURL, err := url.Parse(httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.Jar.SetCookies(hubURL, []*http.Cookie{{Name: "fast_spider_session", Value: session.Token, Path: "/"}})

	status, _, dashboard := webTestRequest(t, client, http.MethodGet, httpServer.URL+"/app", nil)
	if status != http.StatusOK || !strings.Contains(string(dashboard), "连接令牌") {
		t.Fatalf("connection token dashboard status=%d body=%s", status, dashboard)
	}
	status, _, body := webTestRequest(t, client, http.MethodPost, httpServer.URL+"/app/tokens", url.Values{
		"csrf_token":   {"wrong-csrf"},
		"label":        {"rejected"},
		"expires_days": {"90"},
	})
	if status != http.StatusForbidden {
		t.Fatalf("bad connection token CSRF status=%d body=%s", status, body)
	}
	if tokens, err := service.ListConnectionTokens(ctx, account.OwnerID); err != nil || len(tokens) != 0 {
		t.Fatalf("bad CSRF created connection tokens=%+v err=%v", tokens, err)
	}

	status, _, body = webTestRequest(t, client, http.MethodPost, httpServer.URL+"/app/tokens", url.Values{
		"csrf_token":   {session.CSRFToken},
		"label":        {"maintenance"},
		"expires_days": {"90"},
	})
	if status != http.StatusOK {
		t.Fatalf("connection token create status=%d body=%s", status, body)
	}
	secret := webCreatedToken(t, body)
	if strings.Count(string(body), secret) != 1 {
		t.Fatalf("connection token secret displayed %d times, want once: %s", strings.Count(string(body), secret), body)
	}
	status, _, dashboard = webTestRequest(t, client, http.MethodGet, httpServer.URL+"/app", nil)
	if status != http.StatusOK || strings.Contains(string(dashboard), secret) {
		t.Fatalf("dashboard leaked connection token secret: status=%d body=%s", status, dashboard)
	}
}

func TestConnectionTokenRegistersNodeButCannotUseMCPOrAdminREST(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := core.New(st, registry.New(), core.Config{DataDir: dataDir, Version: "connection-token-http-test"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	account, err := service.BootstrapAccount(ctx, bootstrapToken, "token-http-owner", "Token HTTP Owner", "correct horse battery staple", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	connectionToken, err := service.CreateConnectionToken(ctx, account.OwnerID, "node-connect", 0, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	hub := server.New(service, server.Config{})
	httpServer := httptest.NewServer(hub.Handler())
	defer httpServer.Close()

	nodeClient, err := node.New(node.Config{DataDir: t.TempDir(), Version: "connection-token-test", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	state, err := nodeClient.Connect(ctx, httpServer.URL, connectionToken.Token, "Connected Node")
	if err != nil || state.MachineID == "" {
		t.Fatalf("connection token register state=%+v err=%v", state, err)
	}

	for _, target := range []string{"/api/v1/machines", "/mcp"} {
		req, err := http.NewRequest(http.MethodGet, httpServer.URL+target, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+connectionToken.Token)
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("connection token unexpectedly authorized %s", target)
		}
	}

	if err := service.RevokeConnectionToken(ctx, account.OwnerID, connectionToken.Record.ID, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	secondClient, err := node.New(node.Config{DataDir: t.TempDir(), Version: "connection-token-test", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secondClient.Connect(ctx, httpServer.URL, connectionToken.Token, "Denied Node"); err == nil {
		t.Fatal("revoked connection token unexpectedly registered another node")
	}
}

func TestWebPasswordChangeProtectsSessionsAndPreservesOAuth(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := core.New(st, registry.New(), core.Config{DataDir: dataDir, Version: "web-password-test"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	account, err := service.BootstrapAccount(ctx, bootstrapToken, "password-owner", "Password Owner", "old-password-123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	currentSession, err := service.CreateWebSession(ctx, account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	otherSession, err := service.CreateWebSession(ctx, account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	hub := server.New(service, server.Config{
		PublicBaseURL:      oauthTestPublicBaseURL + "/",
		OAuthRedirectHosts: []string{"chatgpt.com", "localhost", "127.0.0.1"},
	})
	httpServer := httptest.NewServer(hub.Handler())
	defer httpServer.Close()

	currentClient := newWebTestClient(t)
	hubURL, err := url.Parse(httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	currentClient.Jar.SetCookies(hubURL, []*http.Cookie{{Name: "fast_spider_session", Value: currentSession.Token, Path: "/"}})
	otherClient := newWebTestClient(t)
	otherClient.Jar.SetCookies(hubURL, []*http.Cookie{{Name: "fast_spider_session", Value: otherSession.Token, Path: "/"}})

	redirectURI := "https://chatgpt.com/password-callback"
	clientID, _ := registerOAuthClient(t, currentClient, httpServer.URL+"/oauth/register", []string{redirectURI}, "")
	resource := oauthTestPublicBaseURL + "/mcp"
	verifier := strings.Repeat("p", 43)
	authorizeValues := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {oauthPKCEChallenge(verifier)},
		"code_challenge_method": {"S256"},
		"scope":                 {"fast-spider"},
		"resource":              {resource},
		"state":                 {"password-change-oauth"},
	}
	code := obtainOAuthCode(t, currentClient, httpServer.URL+"/oauth/authorize", authorizeValues, currentSession.CSRFToken)
	status, _, body := oauthTestRequest(t, currentClient, http.MethodPost, httpServer.URL+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"resource":      {resource},
		"code_verifier": {verifier},
	})
	if status != http.StatusOK {
		t.Fatalf("OAuth setup status=%d body=%s", status, body)
	}
	authorizations, err := st.ListOAuthAuthorizations(ctx, account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(authorizations) != 1 {
		t.Fatalf("OAuth setup authorizations=%+v", authorizations)
	}
	authorizationID := authorizations[0].AuthorizationID

	passwordForm := url.Values{
		"csrf_token":       {"wrong-csrf"},
		"current_password": {"old-password-123"},
		"new_password":     {"new-password-456"},
		"password_confirm": {"new-password-456"},
	}
	status, _, body = webTestRequest(t, currentClient, http.MethodPost, httpServer.URL+"/app/account/password", passwordForm)
	if status != http.StatusForbidden {
		t.Fatalf("bad password-change CSRF status=%d body=%s", status, body)
	}

	passwordForm.Set("csrf_token", currentSession.CSRFToken)
	passwordForm.Set("current_password", "wrong-current-password")
	status, headers, body := webTestRequest(t, currentClient, http.MethodPost, httpServer.URL+"/app/account/password", passwordForm)
	if status != http.StatusSeeOther || headers.Get("Location") != oauthTestPublicBaseURL+"/app?error=password-invalid" {
		t.Fatalf("wrong current password status=%d location=%q body=%s", status, headers.Get("Location"), body)
	}
	status, headers, body = webTestRequest(t, currentClient, http.MethodGet, httpServer.URL+"/app?error=password-invalid", nil)
	if status != http.StatusOK || !strings.Contains(string(body), "当前密码不正确") || headers.Get("Location") != "" {
		t.Fatalf("wrong current password error UX status=%d location=%q body=%s", status, headers.Get("Location"), body)
	}
	if _, err := service.AuthenticateWebSession(ctx, currentSession.Token); err != nil {
		t.Fatalf("current session was invalidated by rejected password change: %v", err)
	}
	if _, err := service.AuthenticateWebSession(ctx, otherSession.Token); err != nil {
		t.Fatalf("other session was invalidated by rejected password change: %v", err)
	}

	passwordForm.Set("current_password", "old-password-123")
	status, headers, body = webTestRequest(t, currentClient, http.MethodPost, httpServer.URL+"/app/account/password", passwordForm)
	if status != http.StatusSeeOther || headers.Get("Location") != oauthTestPublicBaseURL+"/app?notice=password-changed" {
		t.Fatalf("password change status=%d location=%q body=%s", status, headers.Get("Location"), body)
	}
	if _, err := service.AuthenticateWebSession(ctx, currentSession.Token); err != nil {
		t.Fatalf("current session was not preserved after password change: %v", err)
	}
	if _, err := service.AuthenticateWebSession(ctx, otherSession.Token); err == nil {
		t.Fatal("other Web session remained valid after password change")
	}
	status, _, body = webTestRequest(t, currentClient, http.MethodGet, httpServer.URL+"/app", nil)
	if status != http.StatusOK || !strings.Contains(string(body), "账户安全") {
		t.Fatalf("current session dashboard after password change status=%d body=%s", status, body)
	}
	status, headers, body = webTestRequest(t, otherClient, http.MethodGet, httpServer.URL+"/app", nil)
	if status != http.StatusSeeOther || !strings.Contains(headers.Get("Location"), "/login?") {
		t.Fatalf("other session dashboard status=%d location=%q body=%s", status, headers.Get("Location"), body)
	}

	oldLoginClient := newWebTestClient(t)
	status, _, body = webTestRequest(t, oldLoginClient, http.MethodPost, httpServer.URL+"/login", url.Values{
		"username": {"password-owner"},
		"password": {"old-password-123"},
	})
	if status != http.StatusOK || !strings.Contains(string(body), "用户名或密码不正确") {
		t.Fatalf("old password login status=%d body=%s", status, body)
	}
	newLoginClient := newWebTestClient(t)
	status, headers, body = webTestRequest(t, newLoginClient, http.MethodPost, httpServer.URL+"/login", url.Values{
		"username": {"password-owner"},
		"password": {"new-password-456"},
	})
	if status != http.StatusSeeOther || headers.Get("Location") != oauthTestPublicBaseURL+"/app" {
		t.Fatalf("new password login status=%d location=%q body=%s", status, headers.Get("Location"), body)
	}

	authorizations, err = st.ListOAuthAuthorizations(ctx, account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(authorizations) != 1 || authorizations[0].AuthorizationID != authorizationID || authorizations[0].RevokedAt != nil {
		t.Fatalf("OAuth authorization changed during password change: %+v", authorizations)
	}
}

func TestWebPathPrefixUsesPublicRedirectCookieAndAssets(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := core.New(st, registry.New(), core.Config{DataDir: dataDir, Version: "web-prefix-test"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hub := server.New(service, server.Config{PublicBaseURL: "https://sharedservices.tibbs.app/fast-spider/"})
	httpServer := httptest.NewServer(hub.Handler())
	defer httpServer.Close()
	client := newWebTestClient(t)

	status, headers, _ := webTestRequest(t, client, http.MethodGet, httpServer.URL+"/", nil)
	if status != http.StatusFound || headers.Get("Location") != "https://sharedservices.tibbs.app/fast-spider/setup" {
		t.Fatalf("path-prefix root status=%d location=%q", status, headers.Get("Location"))
	}
	status, _, body := webTestRequest(t, client, http.MethodGet, httpServer.URL+"/setup", nil)
	if status != http.StatusOK || !strings.Contains(string(body), "/fast-spider/assets/app.css") || !strings.Contains(string(body), "/fast-spider/assets/setup.js") {
		t.Fatalf("path-prefix setup status=%d body=%s", status, body)
	}
	for _, asset := range []string{"app.css", "setup.js"} {
		status, headers, body := webTestRequest(t, client, http.MethodGet, httpServer.URL+"/assets/"+asset, nil)
		if status != http.StatusOK || len(body) == 0 {
			t.Fatalf("asset %s status=%d body=%s", asset, status, body)
		}
		if headers.Get("Cache-Control") != "no-cache" {
			t.Fatalf("asset %s Cache-Control=%q, want no-cache", asset, headers.Get("Cache-Control"))
		}
	}
	status, headers, body = webTestRequest(t, client, http.MethodPost, httpServer.URL+"/setup", url.Values{
		"bootstrap_token":  {bootstrapToken},
		"username":         {"prefix-owner"},
		"display_name":     {"Prefix Owner"},
		"password":         {"correct horse battery staple"},
		"password_confirm": {"correct horse battery staple"},
	})
	if status != http.StatusSeeOther || headers.Get("Location") != "https://sharedservices.tibbs.app/fast-spider/app" {
		t.Fatalf("path-prefix setup status=%d location=%q body=%s", status, headers.Get("Location"), body)
	}
	cookie, err := http.ParseSetCookie(headers.Get("Set-Cookie"))
	if err != nil {
		t.Fatal(err)
	}
	if cookie.Path != "/fast-spider" || !cookie.Secure || !cookie.HttpOnly {
		t.Fatalf("path-prefix session cookie=%+v", cookie)
	}
}

func TestWebRevokedObjectsCanBeDeleted(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := core.New(st, registry.New(), core.Config{DataDir: dataDir, Version: "web-delete-test"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	account, err := service.BootstrapAccount(ctx, bootstrapToken, "delete-owner", "Delete Owner", "correct horse battery staple", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateWebSession(ctx, account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	connectionToken, err := service.CreateConnectionToken(ctx, account.OwnerID, "delete-me-token", time.Hour, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	hub := server.New(service, server.Config{})
	httpServer := httptest.NewServer(hub.Handler())
	defer httpServer.Close()

	nodeClient, err := node.New(node.Config{DataDir: t.TempDir(), Version: "web-delete-node", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	state, err := nodeClient.Connect(ctx, httpServer.URL, connectionToken.Token, "delete-me-machine")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	if err := st.RegisterOAuthClient(ctx, store.OAuthClientRecord{
		ClientID: "mcpcli_delete_test", ClientName: "Delete OAuth Client", RedirectURIs: []string{"http://127.0.0.1/callback"},
		GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}, Scope: "fast-spider", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateOAuthAuthorization(ctx, store.OAuthAuthorizationRecord{
		AuthorizationID: "authz_delete_test", OwnerID: account.OwnerID, ClientID: "mcpcli_delete_test", ClientName: "Delete OAuth Client",
		Scopes: []string{"fast-spider"}, Resource: httpServer.URL + "/mcp", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	client := newWebTestClient(t)
	hubURL, err := url.Parse(httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.Jar.SetCookies(hubURL, []*http.Cookie{{Name: "fast_spider_session", Value: session.Token, Path: "/"}})

	status, _, body := webTestRequest(t, client, http.MethodPost, httpServer.URL+"/app/machines/"+state.MachineID+"/delete", url.Values{"csrf_token": {session.CSRFToken}})
	if status != http.StatusBadRequest {
		t.Fatalf("active machine delete status=%d body=%s", status, body)
	}
	status, _, body = webTestRequest(t, client, http.MethodPost, httpServer.URL+"/app/machines/"+state.MachineID+"/revoke", url.Values{"csrf_token": {session.CSRFToken}})
	if status != http.StatusSeeOther {
		t.Fatalf("machine revoke status=%d body=%s", status, body)
	}
	status, _, dashboard := webTestRequest(t, client, http.MethodGet, httpServer.URL+"/app", nil)
	if status != http.StatusOK || !strings.Contains(string(dashboard), "删除设备") || !strings.Contains(string(dashboard), state.MachineID) {
		t.Fatalf("revoked machine delete action missing: status=%d body=%s", status, dashboard)
	}
	status, _, body = webTestRequest(t, client, http.MethodPost, httpServer.URL+"/app/machines/"+state.MachineID+"/delete", url.Values{"csrf_token": {session.CSRFToken}})
	if status != http.StatusSeeOther {
		t.Fatalf("machine delete status=%d body=%s", status, body)
	}
	machines, err := service.ListMachines(ctx, account.OwnerID)
	if err != nil || len(machines) != 0 {
		t.Fatalf("deleted machine still listed: machines=%+v err=%v", machines, err)
	}

	status, _, body = webTestRequest(t, client, http.MethodPost, httpServer.URL+"/app/tokens/"+connectionToken.Record.ID+"/delete", url.Values{"csrf_token": {session.CSRFToken}})
	if status != http.StatusBadRequest {
		t.Fatalf("active token delete status=%d body=%s", status, body)
	}
	status, _, body = webTestRequest(t, client, http.MethodPost, httpServer.URL+"/app/tokens/"+connectionToken.Record.ID+"/revoke", url.Values{"csrf_token": {session.CSRFToken}})
	if status != http.StatusSeeOther {
		t.Fatalf("token revoke status=%d body=%s", status, body)
	}
	status, _, dashboard = webTestRequest(t, client, http.MethodGet, httpServer.URL+"/app", nil)
	if status != http.StatusOK || !strings.Contains(string(dashboard), "删除令牌") || !strings.Contains(string(dashboard), "delete-me-token") {
		t.Fatalf("revoked token delete action missing: status=%d body=%s", status, dashboard)
	}
	status, _, body = webTestRequest(t, client, http.MethodPost, httpServer.URL+"/app/tokens/"+connectionToken.Record.ID+"/delete", url.Values{"csrf_token": {session.CSRFToken}})
	if status != http.StatusSeeOther {
		t.Fatalf("token delete status=%d body=%s", status, body)
	}
	tokens, err := service.ListConnectionTokens(ctx, account.OwnerID)
	if err != nil || len(tokens) != 0 {
		t.Fatalf("deleted token still listed: tokens=%+v err=%v", tokens, err)
	}

	status, _, body = webTestRequest(t, client, http.MethodPost, httpServer.URL+"/app/oauth/authorizations/authz_delete_test/delete", url.Values{"csrf_token": {session.CSRFToken}})
	if status != http.StatusBadRequest {
		t.Fatalf("active authorization delete status=%d body=%s", status, body)
	}
	status, _, body = webTestRequest(t, client, http.MethodPost, httpServer.URL+"/app/oauth/authorizations/authz_delete_test/revoke", url.Values{"csrf_token": {session.CSRFToken}})
	if status != http.StatusSeeOther {
		t.Fatalf("authorization revoke status=%d body=%s", status, body)
	}
	status, _, dashboard = webTestRequest(t, client, http.MethodGet, httpServer.URL+"/app", nil)
	if status != http.StatusOK || !strings.Contains(string(dashboard), "删除授权") || !strings.Contains(string(dashboard), "Delete OAuth Client") {
		t.Fatalf("revoked authorization delete action missing: status=%d body=%s", status, dashboard)
	}
	status, _, body = webTestRequest(t, client, http.MethodPost, httpServer.URL+"/app/oauth/authorizations/authz_delete_test/delete", url.Values{"csrf_token": {session.CSRFToken}})
	if status != http.StatusSeeOther {
		t.Fatalf("authorization delete status=%d body=%s", status, body)
	}
	authorizations, err := st.ListOAuthAuthorizations(ctx, account.OwnerID)
	if err != nil || len(authorizations) != 0 {
		t.Fatalf("deleted authorization still listed: authorizations=%+v err=%v", authorizations, err)
	}
	clients, err := st.ListOAuthClientsForOwner(ctx, account.OwnerID)
	if err != nil || len(clients) != 0 {
		t.Fatalf("OAuth client with only deleted authorization still listed: clients=%+v err=%v", clients, err)
	}
}

func webCreatedToken(t *testing.T, body []byte) string {
	t.Helper()
	const prefix = `<textarea id="created-token" rows="4" readonly spellcheck="false">`
	start := strings.Index(string(body), prefix)
	if start < 0 {
		t.Fatalf("created token textarea missing: %s", body)
	}
	start += len(prefix)
	end := strings.Index(string(body[start:]), "</textarea>")
	if end < 0 {
		t.Fatalf("created token textarea is not closed: %s", body)
	}
	return string(body[start : start+end])
}

func newWebTestClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func webTestRequest(t *testing.T, client *http.Client, method, endpoint string, form url.Values) (int, http.Header, []byte) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, raw
}
