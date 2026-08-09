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

	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/server"
	"github.com/isguang2024/fast-spider/internal/hub/store"
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

func TestLegacyOwnerAddsWebAccountWithoutIdentityReset(t *testing.T) {
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
	legacyBootstrap, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	legacyOwner, err := service.BootstrapOwner(ctx, legacyBootstrap, "Legacy Owner", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	setupToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if setupToken == "" || setupToken == legacyBootstrap {
		t.Fatal("legacy owner account setup token was not rotated")
	}

	hub := server.New(service, server.Config{})
	httpServer := httptest.NewServer(hub.Handler())
	defer httpServer.Close()
	client := newWebTestClient(t)
	status, headers, _ := webTestRequest(t, client, http.MethodGet, httpServer.URL+"/", nil)
	if status != http.StatusFound || !strings.HasSuffix(headers.Get("Location"), "/setup") {
		t.Fatalf("legacy owner root status=%d location=%q", status, headers.Get("Location"))
	}
	status, headers, body := webTestRequest(t, client, http.MethodPost, httpServer.URL+"/setup", url.Values{
		"bootstrap_token":  {setupToken},
		"username":         {"legacy-owner"},
		"display_name":     {"Legacy Owner"},
		"password":         {"correct horse battery staple"},
		"password_confirm": {"correct horse battery staple"},
	})
	if status != http.StatusSeeOther || !strings.HasSuffix(headers.Get("Location"), "/app") {
		t.Fatalf("legacy account setup status=%d location=%q body=%s", status, headers.Get("Location"), body)
	}
	account, err := st.OwnerAccountByUsername(ctx, "legacy-owner")
	if err != nil {
		t.Fatal(err)
	}
	if account.OwnerID != legacyOwner.OwnerID {
		t.Fatalf("owner identity changed: got %s want %s", account.OwnerID, legacyOwner.OwnerID)
	}
	if ownerID, err := service.AuthenticateOwner(ctx, legacyOwner.OwnerToken); err != nil || ownerID != legacyOwner.OwnerID {
		t.Fatalf("legacy owner token no longer authenticates: owner=%s err=%v", ownerID, err)
	}
	if account, err := service.LoginAccount(ctx, "legacy-owner", "correct horse battery staple", "127.0.0.1"); err != nil || account.OwnerID != legacyOwner.OwnerID {
		t.Fatalf("legacy owner web login failed: account=%+v err=%v", account, err)
	}
	tokens, err := service.ListOwnerAPITokens(ctx, legacyOwner.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].ID == "" {
		t.Fatalf("legacy Owner API token was not preserved: %+v", tokens)
	}
}

func TestWebPersonalAccessTokenIsOneTimeAndCSRFProtected(t *testing.T) {
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
	account, err := service.BootstrapAccount(ctx, bootstrapToken, "pat-owner", "PAT Owner", "correct horse battery staple", "127.0.0.1")
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
	if status != http.StatusOK || !strings.Contains(string(dashboard), "访问令牌") {
		t.Fatalf("PAT dashboard status=%d body=%s", status, dashboard)
	}
	status, _, body := webTestRequest(t, client, http.MethodPost, httpServer.URL+"/app/tokens", url.Values{
		"csrf_token":   {"wrong-csrf"},
		"label":        {"rejected"},
		"expires_days": {"90"},
	})
	if status != http.StatusForbidden {
		t.Fatalf("bad PAT CSRF status=%d body=%s", status, body)
	}
	if tokens, err := service.ListOwnerAPITokens(ctx, account.OwnerID); err != nil || len(tokens) != 0 {
		t.Fatalf("bad CSRF created PATs=%+v err=%v", tokens, err)
	}

	status, _, body = webTestRequest(t, client, http.MethodPost, httpServer.URL+"/app/tokens", url.Values{
		"csrf_token":   {session.CSRFToken},
		"label":        {"maintenance"},
		"expires_days": {"90"},
	})
	if status != http.StatusOK {
		t.Fatalf("PAT create status=%d body=%s", status, body)
	}
	secret := webCreatedToken(t, body)
	if strings.Count(string(body), secret) != 1 {
		t.Fatalf("PAT secret displayed %d times, want once: %s", strings.Count(string(body), secret), body)
	}
	status, _, dashboard = webTestRequest(t, client, http.MethodGet, httpServer.URL+"/app", nil)
	if status != http.StatusOK || strings.Contains(string(dashboard), secret) {
		t.Fatalf("dashboard leaked PAT secret: status=%d body=%s", status, dashboard)
	}
}

func TestOwnerPATHTTPAuthenticationAndRevocation(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service, err := core.New(st, registry.New(), core.Config{DataDir: dataDir, Version: "owner-pat-http-test"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	account, err := service.BootstrapAccount(ctx, bootstrapToken, "pat-http-owner", "PAT HTTP Owner", "correct horse battery staple", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	pat, err := service.CreateOwnerAPIToken(ctx, account.OwnerID, "http-test", 0, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	hub := server.New(service, server.Config{})
	httpServer := httptest.NewServer(hub.Handler())
	defer httpServer.Close()

	requestMachines := func() (int, []byte) {
		req, err := http.NewRequest(http.MethodGet, httpServer.URL+"/api/v1/machines", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+pat.Token)
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, body
	}
	if status, body := requestMachines(); status != http.StatusOK {
		t.Fatalf("owner PAT GET /api/v1/machines status=%d body=%s", status, body)
	}
	if err := service.RevokeOwnerAPIToken(ctx, account.OwnerID, pat.Record.ID, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if status, body := requestMachines(); status != http.StatusConflict || !strings.Contains(string(body), "REVOKED") {
		t.Fatalf("revoked owner PAT status=%d body=%s, want 409/REVOKED", status, body)
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
