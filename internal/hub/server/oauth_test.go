package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const oauthTestPublicBaseURL = "https://hub.example/fast-spider"

type oauthTestFixture struct {
	httpServer      *httptest.Server
	webSessionToken string
	csrfToken       string
	store           *store.Store
	ownerID         string
}

type oauthClientResponse struct {
	ClientID     string   `json:"client_id"`
	RedirectURIs []string `json:"redirect_uris"`
	GrantTypes   []string `json:"grant_types"`
	Scope        string   `json:"scope"`
}

type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

func newOAuthTestFixture(t *testing.T) oauthTestFixture {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	service, err := core.New(st, registry.New(), core.Config{DataDir: dataDir, Version: "oauth-test"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapToken, err := service.EnsureBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	account, err := service.BootstrapAccount(ctx, bootstrapToken, "oauth-test", "OAuth Test Owner", "oauth-test-password", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	webSession, err := service.CreateWebSession(ctx, account.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	hub := server.New(service, server.Config{
		PublicBaseURL:      oauthTestPublicBaseURL + "/",
		OAuthRedirectHosts: []string{"chatgpt.com", "localhost", "127.0.0.1"},
	})
	httpServer := httptest.NewServer(hub.Handler())
	t.Cleanup(httpServer.Close)
	return oauthTestFixture{
		httpServer:      httpServer,
		webSessionToken: webSession.Token,
		csrfToken:       webSession.CSRFToken,
		store:           st,
		ownerID:         account.OwnerID,
	}
}

func TestOAuthAuthorizationPKCETokenRotationAndMCP(t *testing.T) {
	fixture := newOAuthTestFixture(t)
	client := newOAuthTestHTTPClient(fixture.httpServer.URL, fixture.webSessionToken)
	ctx := context.Background()
	resource := oauthTestPublicBaseURL + "/mcp"

	metadataStatus, metadataHeaders, metadataBody := oauthTestRequest(t, client, http.MethodGet, fixture.httpServer.URL+"/.well-known/oauth-protected-resource", nil)
	if metadataStatus != http.StatusOK {
		t.Fatalf("protected-resource metadata status=%d body=%s", metadataStatus, metadataBody)
	}
	if metadataHeaders.Get("Cache-Control") != "no-store" {
		t.Fatalf("protected-resource metadata cache header=%q", metadataHeaders.Get("Cache-Control"))
	}
	var protectedResource struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	decodeOAuthJSON(t, metadataBody, &protectedResource)
	if protectedResource.Resource != resource || len(protectedResource.AuthorizationServers) != 1 || protectedResource.AuthorizationServers[0] != oauthTestPublicBaseURL {
		t.Fatalf("protected-resource metadata=%s", metadataBody)
	}
	wildcardResourceStatus, wildcardResourceHeaders, wildcardResourceBody := oauthTestRequest(t, client, http.MethodGet, fixture.httpServer.URL+"/.well-known/oauth-protected-resource/fast-spider/mcp", nil)
	if wildcardResourceStatus != http.StatusOK || wildcardResourceHeaders.Get("Cache-Control") != "no-store" {
		t.Fatalf("path-insertion protected-resource status=%d cache=%q body=%s", wildcardResourceStatus, wildcardResourceHeaders.Get("Cache-Control"), wildcardResourceBody)
	}
	var wildcardResource struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	decodeOAuthJSON(t, wildcardResourceBody, &wildcardResource)
	if wildcardResource.Resource != resource || len(wildcardResource.AuthorizationServers) != 1 || wildcardResource.AuthorizationServers[0] != oauthTestPublicBaseURL {
		t.Fatalf("path-insertion protected-resource metadata=%s", wildcardResourceBody)
	}

	authorizationMetadataStatus, _, authorizationMetadataBody := oauthTestRequest(t, client, http.MethodGet, fixture.httpServer.URL+"/.well-known/oauth-authorization-server", nil)
	if authorizationMetadataStatus != http.StatusOK {
		t.Fatalf("authorization-server metadata status=%d body=%s", authorizationMetadataStatus, authorizationMetadataBody)
	}
	var authorizationMetadata map[string]any
	decodeOAuthJSON(t, authorizationMetadataBody, &authorizationMetadata)
	for key, want := range map[string]string{
		"issuer":                 oauthTestPublicBaseURL,
		"authorization_endpoint": oauthTestPublicBaseURL + "/oauth/authorize",
		"token_endpoint":         oauthTestPublicBaseURL + "/oauth/token",
		"registration_endpoint":  oauthTestPublicBaseURL + "/oauth/register",
	} {
		if authorizationMetadata[key] != want {
			t.Fatalf("authorization metadata %s=%v want=%s", key, authorizationMetadata[key], want)
		}
	}
	wildcardAuthorizationStatus, wildcardAuthorizationHeaders, wildcardAuthorizationBody := oauthTestRequest(t, client, http.MethodGet, fixture.httpServer.URL+"/.well-known/oauth-authorization-server/fast-spider", nil)
	if wildcardAuthorizationStatus != http.StatusOK || wildcardAuthorizationHeaders.Get("Cache-Control") != "no-store" {
		t.Fatalf("path-insertion authorization-server status=%d cache=%q body=%s", wildcardAuthorizationStatus, wildcardAuthorizationHeaders.Get("Cache-Control"), wildcardAuthorizationBody)
	}
	var wildcardAuthorization map[string]any
	decodeOAuthJSON(t, wildcardAuthorizationBody, &wildcardAuthorization)
	for key, want := range map[string]string{
		"issuer":                 oauthTestPublicBaseURL,
		"authorization_endpoint": oauthTestPublicBaseURL + "/oauth/authorize",
		"token_endpoint":         oauthTestPublicBaseURL + "/oauth/token",
		"registration_endpoint":  oauthTestPublicBaseURL + "/oauth/register",
	} {
		if wildcardAuthorization[key] != want {
			t.Fatalf("path-insertion authorization metadata %s=%v want=%s", key, wildcardAuthorization[key], want)
		}
	}

	redirectURI := "https://chatgpt.com/oauth/callback"
	clientID, registration := registerOAuthClient(t, client, fixture.httpServer.URL+"/oauth/register", []string{redirectURI, "http://127.0.0.1:1455/callback"}, "")
	if len(registration.RedirectURIs) != 2 || registration.RedirectURIs[0] != redirectURI {
		t.Fatalf("registered redirect URIs=%v", registration.RedirectURIs)
	}
	subsetStatus, _, subsetBody := registerOAuthClientRawWithMetadata(t, client, fixture.httpServer.URL+"/oauth/register", []string{redirectURI}, "", []string{"authorization_code"}, []string{"code"})
	if subsetStatus != http.StatusCreated {
		t.Fatalf("supported DCR subset status=%d body=%s", subsetStatus, subsetBody)
	}
	var subsetRegistration oauthClientResponse
	decodeOAuthJSON(t, subsetBody, &subsetRegistration)
	if subsetRegistration.ClientID == "" {
		t.Fatalf("supported DCR subset omitted client_id: %s", subsetBody)
	}
	if len(subsetRegistration.GrantTypes) != 1 || subsetRegistration.GrantTypes[0] != "authorization_code" {
		t.Fatalf("DCR subset was expanded unexpectedly: %s", subsetBody)
	}
	if subsetRegistration.Scope != "fast-spider" {
		t.Fatalf("unexpected default DCR scope: %s", subsetBody)
	}
	for _, testCase := range []struct {
		name          string
		grantTypes    []string
		responseTypes []string
	}{
		{name: "unknown grant", grantTypes: []string{"authorization_code", "device_code"}, responseTypes: []string{"code"}},
		{name: "unknown response", grantTypes: []string{"authorization_code"}, responseTypes: []string{"code", "token"}},
	} {
		t.Run("registration_"+testCase.name, func(t *testing.T) {
			status, _, body := registerOAuthClientRawWithMetadata(t, client, fixture.httpServer.URL+"/oauth/register", []string{redirectURI}, "", testCase.grantTypes, testCase.responseTypes)
			if status != http.StatusBadRequest {
				t.Fatalf("registration status=%d body=%s", status, body)
			}
			assertOAuthError(t, body, "invalid_client_metadata", true)
		})
	}

	for _, testCase := range []struct {
		name        string
		redirectURI string
		scope       string
		expected    string
	}{
		{name: "untrusted host", redirectURI: "https://evil.example/callback", expected: "invalid_redirect_uri"},
		{name: "lookalike host", redirectURI: "https://chatgpt.com.evil.example/callback", expected: "invalid_redirect_uri"},
		{name: "insecure chatgpt", redirectURI: "http://chatgpt.com/callback", expected: "invalid_redirect_uri"},
		{name: "fragment", redirectURI: "https://chatgpt.com/callback#fragment", expected: "invalid_redirect_uri"},
		{name: "unsupported scope", redirectURI: redirectURI, scope: "openid", expected: "invalid_client_metadata"},
	} {
		t.Run("registration_"+testCase.name, func(t *testing.T) {
			status, _, body := registerOAuthClientRaw(t, client, fixture.httpServer.URL+"/oauth/register", []string{testCase.redirectURI}, testCase.scope)
			if status != http.StatusBadRequest {
				t.Fatalf("registration status=%d body=%s", status, body)
			}
			assertOAuthError(t, body, testCase.expected, true)
		})
	}

	verifier := strings.Repeat("v", 43)
	authorizeValues := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {oauthPKCEChallenge(verifier)},
		"code_challenge_method": {"S256"},
		"scope":                 {"fast-spider"},
		"resource":              {resource},
		"state":                 {"oauth-state-1"},
	}

	subsetVerifier := strings.Repeat("s", 43)
	subsetValues := cloneOAuthValues(authorizeValues)
	subsetValues.Set("client_id", subsetRegistration.ClientID)
	subsetValues.Set("code_challenge", oauthPKCEChallenge(subsetVerifier))
	subsetValues.Set("state", "oauth-state-subset")
	subsetCode := obtainOAuthCode(t, client, fixture.httpServer.URL+"/oauth/authorize", subsetValues, fixture.csrfToken)
	subsetTokenStatus, _, subsetTokenBody := oauthTestRequest(t, client, http.MethodPost, fixture.httpServer.URL+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {subsetRegistration.ClientID},
		"code":          {subsetCode},
		"redirect_uri":  {redirectURI},
		"resource":      {resource},
		"code_verifier": {subsetVerifier},
	})
	if subsetTokenStatus != http.StatusOK {
		t.Fatalf("subset authorization_code exchange status=%d body=%s", subsetTokenStatus, subsetTokenBody)
	}
	var subsetTokens oauthTokenResponse
	decodeOAuthJSON(t, subsetTokenBody, &subsetTokens)
	if subsetTokens.AccessToken == "" || subsetTokens.RefreshToken != "" {
		t.Fatalf("authorization-code-only client received unexpected token response: %s", subsetTokenBody)
	}
	subsetRefreshStatus, _, subsetRefreshBody := oauthTestRequest(t, client, http.MethodPost, fixture.httpServer.URL+"/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {subsetRegistration.ClientID},
		"refresh_token": {"not-issued"},
	})
	if subsetRefreshStatus != http.StatusBadRequest {
		t.Fatalf("authorization-code-only client refresh status=%d body=%s", subsetRefreshStatus, subsetRefreshBody)
	}
	assertOAuthError(t, subsetRefreshBody, "unauthorized_client", true)
	getStatus, getHeaders, getBody := oauthTestRequest(t, client, http.MethodGet, fixture.httpServer.URL+"/oauth/authorize?"+authorizeValues.Encode(), nil)
	if getStatus != http.StatusOK || !strings.Contains(string(getBody), "<form method=\"post\" action=\"/fast-spider/oauth/authorize\">") || !strings.Contains(string(getBody), "允许连接") {
		t.Fatalf("authorization GET status=%d body=%s", getStatus, getBody)
	}
	if csp := getHeaders.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' https://chatgpt.com;") {
		t.Fatalf("authorization GET CSP does not allow validated callback origin: %q", csp)
	}
	if strings.Contains(string(getBody), "Owner Token") {
		t.Fatal("authorization GET exposed retired Owner Token UI")
	}

	badCSRF := cloneOAuthValues(authorizeValues)
	badCSRF.Set("decision", "approve")
	badCSRF.Set("csrf_token", "not-the-session-csrf-token")
	badCSRFStatus, badCSRFHeaders, badCSRFBody := oauthTestRequest(t, client, http.MethodPost, fixture.httpServer.URL+"/oauth/authorize", badCSRF)
	if badCSRFStatus != http.StatusForbidden || badCSRFHeaders.Get("Location") != "" {
		t.Fatalf("bad CSRF authorization status=%d location=%q body=%s", badCSRFStatus, badCSRFHeaders.Get("Location"), badCSRFBody)
	}

	for _, testCase := range []struct {
		name   string
		change func(url.Values)
	}{
		{name: "wrong resource", change: func(values url.Values) { values.Set("resource", oauthTestPublicBaseURL+"/other") }},
		{name: "unregistered redirect", change: func(values url.Values) { values.Set("redirect_uri", "https://chatgpt.com/other") }},
		{name: "unsupported scope", change: func(values url.Values) { values.Set("scope", "openid") }},
		{name: "plain PKCE", change: func(values url.Values) { values.Set("code_challenge_method", "plain") }},
	} {
		t.Run("authorization_"+testCase.name, func(t *testing.T) {
			values := cloneOAuthValues(authorizeValues)
			testCase.change(values)
			status, _, body := oauthTestRequest(t, client, http.MethodGet, fixture.httpServer.URL+"/oauth/authorize?"+values.Encode(), nil)
			if status != http.StatusBadRequest {
				t.Fatalf("authorization status=%d body=%s", status, body)
			}
			assertOAuthError(t, body, "invalid_request", true)
		})
	}

	code := obtainOAuthCode(t, client, fixture.httpServer.URL+"/oauth/authorize", authorizeValues, fixture.csrfToken)
	tokenStatus, _, tokenBody := oauthTestRequest(t, client, http.MethodPost, fixture.httpServer.URL+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"resource":      {resource},
		"code_verifier": {verifier},
	})
	if tokenStatus != http.StatusOK {
		t.Fatalf("authorization_code exchange status=%d body=%s", tokenStatus, tokenBody)
	}
	var firstTokens oauthTokenResponse
	decodeOAuthJSON(t, tokenBody, &firstTokens)
	if firstTokens.AccessToken == "" || firstTokens.RefreshToken == "" || firstTokens.TokenType != "Bearer" || firstTokens.Scope != "fast-spider" || firstTokens.ExpiresIn <= 0 {
		t.Fatalf("authorization_code token response=%s", tokenBody)
	}

	badPKCEValues := cloneOAuthValues(authorizeValues)
	badPKCEValues.Set("state", "oauth-state-bad-pkce")
	badPKCECode := obtainOAuthCode(t, client, fixture.httpServer.URL+"/oauth/authorize", badPKCEValues, fixture.csrfToken)
	badPKCEStatus, _, badPKCEBody := oauthTestRequest(t, client, http.MethodPost, fixture.httpServer.URL+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {badPKCECode},
		"redirect_uri":  {redirectURI},
		"resource":      {resource},
		"code_verifier": {strings.Repeat("w", 43)},
	})
	if badPKCEStatus != http.StatusBadRequest {
		t.Fatalf("bad PKCE status=%d body=%s", badPKCEStatus, badPKCEBody)
	}
	assertOAuthError(t, badPKCEBody, "invalid_grant", true)

	wrongResourceValues := cloneOAuthValues(authorizeValues)
	wrongResourceValues.Set("state", "oauth-state-wrong-resource")
	wrongResourceCode := obtainOAuthCode(t, client, fixture.httpServer.URL+"/oauth/authorize", wrongResourceValues, fixture.csrfToken)
	wrongResourceStatus, _, wrongResourceBody := oauthTestRequest(t, client, http.MethodPost, fixture.httpServer.URL+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {wrongResourceCode},
		"redirect_uri":  {redirectURI},
		"resource":      {oauthTestPublicBaseURL + "/wrong-resource"},
		"code_verifier": {verifier},
	})
	if wrongResourceStatus != http.StatusBadRequest {
		t.Fatalf("wrong resource token status=%d body=%s", wrongResourceStatus, wrongResourceBody)
	}
	assertOAuthError(t, wrongResourceBody, "invalid_grant", true)

	refreshStatus, _, refreshBody := oauthTestRequest(t, client, http.MethodPost, fixture.httpServer.URL+"/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {firstTokens.RefreshToken},
		"resource":      {resource},
	})
	if refreshStatus != http.StatusOK {
		t.Fatalf("refresh exchange status=%d body=%s", refreshStatus, refreshBody)
	}
	var rotatedTokens oauthTokenResponse
	decodeOAuthJSON(t, refreshBody, &rotatedTokens)
	if rotatedTokens.AccessToken == firstTokens.AccessToken || rotatedTokens.RefreshToken == firstTokens.RefreshToken {
		t.Fatalf("refresh rotation reused token material: first=%s rotated=%s", tokenSummary(firstTokens), tokenSummary(rotatedTokens))
	}
	oldRefreshStatus, _, oldRefreshBody := oauthTestRequest(t, client, http.MethodPost, fixture.httpServer.URL+"/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {firstTokens.RefreshToken},
		"resource":      {resource},
	})
	if oldRefreshStatus != http.StatusBadRequest {
		t.Fatalf("reused refresh token status=%d body=%s", oldRefreshStatus, oldRefreshBody)
	}
	assertOAuthError(t, oldRefreshBody, "invalid_grant", true)

	unauthorizedStatus, unauthorizedHeaders, _ := oauthTestRequest(t, client, http.MethodGet, fixture.httpServer.URL+"/mcp", nil)
	if unauthorizedStatus != http.StatusUnauthorized {
		t.Fatalf("unauthorized MCP status=%d", unauthorizedStatus)
	}
	wwwAuthenticate := unauthorizedHeaders.Get("WWW-Authenticate")
	if !strings.Contains(wwwAuthenticate, "Bearer") || !strings.Contains(wwwAuthenticate, "resource_metadata=\""+oauthTestPublicBaseURL+"/.well-known/oauth-protected-resource\"") {
		t.Fatalf("MCP WWW-Authenticate=%q", wwwAuthenticate)
	}

	oauthSession := connectOAuthMCP(t, ctx, fixture.httpServer.URL+"/mcp", rotatedTokens.AccessToken)
	oauthTools, err := oauthSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("OAuth access token tools/list: %v", err)
	}
	assertMCPToolAnnotations(t, oauthTools.Tools)

	proxySession := connectOAuthMCPWithHost(t, ctx, fixture.httpServer.URL+"/mcp", rotatedTokens.AccessToken, "hub.example")
	proxyTools, err := proxySession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("reverse-proxy Host tools/list: %v", err)
	}
	assertMCPToolAnnotations(t, proxyTools.Tools)
}

func TestWebOAuthClientsExcludeOrphanDCR(t *testing.T) {
	fixture := newOAuthTestFixture(t)
	client := newOAuthTestHTTPClient(fixture.httpServer.URL, fixture.webSessionToken)
	redirectURI := "https://chatgpt.com/oauth/callback"
	orphanID, _ := registerOAuthClient(t, client, fixture.httpServer.URL+"/oauth/register", []string{redirectURI}, "")
	authorizedID, _ := registerOAuthClient(t, client, fixture.httpServer.URL+"/oauth/register", []string{redirectURI}, "")
	resource := oauthTestPublicBaseURL + "/mcp"
	verifier := strings.Repeat("o", 43)
	values := url.Values{
		"response_type":         {"code"},
		"client_id":             {authorizedID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {oauthPKCEChallenge(verifier)},
		"code_challenge_method": {"S256"},
		"scope":                 {"fast-spider"},
		"resource":              {resource},
		"state":                 {"owner-client-scope"},
	}
	code := obtainOAuthCode(t, client, fixture.httpServer.URL+"/oauth/authorize", values, fixture.csrfToken)
	status, _, body := oauthTestRequest(t, client, http.MethodPost, fixture.httpServer.URL+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {authorizedID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"resource":      {resource},
		"code_verifier": {verifier},
	})
	if status != http.StatusOK {
		t.Fatalf("authorized client token exchange status=%d body=%s", status, body)
	}

	status, _, body = oauthTestRequest(t, client, http.MethodGet, fixture.httpServer.URL+"/app", nil)
	if status != http.StatusOK {
		t.Fatalf("dashboard status=%d body=%s", status, body)
	}
	if !strings.Contains(string(body), authorizedID) {
		t.Fatalf("dashboard omitted current owner's authorized client %q: %s", authorizedID, body)
	}
	if strings.Contains(string(body), orphanID) {
		t.Fatalf("dashboard exposed orphan DCR client %q: %s", orphanID, body)
	}
}

func TestOAuthRegistrationRejectsOversizedRedirectURI(t *testing.T) {
	fixture := newOAuthTestFixture(t)
	client := newOAuthTestHTTPClient(fixture.httpServer.URL, fixture.webSessionToken)
	redirect := "https://chatgpt.com/" + strings.Repeat("a", 2048)
	status, _, body := registerOAuthClientRaw(t, client, fixture.httpServer.URL+"/oauth/register", []string{redirect}, "")
	if status != http.StatusBadRequest {
		t.Fatalf("oversized redirect status=%d body=%s", status, body)
	}
	assertOAuthError(t, body, "invalid_redirect_uri", true)
}

func TestOAuthRegistrationIgnoresStandardAndExtensionMetadata(t *testing.T) {
	fixture := newOAuthTestFixture(t)
	client := newOAuthTestHTTPClient(fixture.httpServer.URL, fixture.webSessionToken)
	status, _, body := registerOAuthClientJSON(t, client, fixture.httpServer.URL+"/oauth/register", map[string]any{
		"client_name":                "OAuth extension client",
		"redirect_uris":              []string{"https://chatgpt.com/oauth/callback"},
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
		"scope":                      "fast-spider",
		"contacts":                   []string{"ops@example.com"},
		"client_uri":                 "https://client.example/about",
		"software_id":                "example-client",
		"software_version":           "1.2.3",
		"x_vendor_extension":         map[string]any{"mode": "compatible"},
	})
	if status != http.StatusCreated {
		t.Fatalf("DCR extension metadata status=%d body=%s", status, body)
	}
	var response oauthClientResponse
	decodeOAuthJSON(t, body, &response)
	if response.ClientID == "" || len(response.GrantTypes) != 1 || response.GrantTypes[0] != "authorization_code" {
		t.Fatalf("DCR extension metadata response=%s", body)
	}
}

func TestOAuthRegistrationRetainsBodyAndKnownMetadataLimits(t *testing.T) {
	fixture := newOAuthTestFixture(t)
	client := newOAuthTestHTTPClient(fixture.httpServer.URL, fixture.webSessionToken)
	endpoint := fixture.httpServer.URL + "/oauth/register"
	base := map[string]any{
		"client_name":                "OAuth limit client",
		"redirect_uris":              []string{"https://chatgpt.com/oauth/callback"},
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
		"scope":                      "fast-spider",
	}
	base["x_large_extension"] = strings.Repeat("x", 33<<10)
	status, _, body := registerOAuthClientJSON(t, client, endpoint, base)
	if status != http.StatusBadRequest {
		t.Fatalf("oversized DCR body status=%d body=%s", status, body)
	}
	assertOAuthError(t, body, "invalid_client_metadata", true)

	redirects := make([]string, 8)
	for i := range redirects {
		prefix := fmt.Sprintf("https://chatgpt.com/%d/", i)
		redirects[i] = prefix + strings.Repeat("a", 2048-len(prefix))
	}
	delete(base, "x_large_extension")
	base["client_name"] = strings.Repeat("n", 128)
	base["redirect_uris"] = redirects
	status, _, body = registerOAuthClientJSON(t, client, endpoint, base)
	if status != http.StatusBadRequest {
		t.Fatalf("oversized known DCR metadata status=%d body=%s", status, body)
	}
	assertOAuthError(t, body, "invalid_client_metadata", true)
}

func TestOAuthOwnerClientQuotaConsumesAuthorizationCodeAsTerminalInvalidGrant(t *testing.T) {
	fixture := newOAuthTestFixture(t)
	client := newOAuthTestHTTPClient(fixture.httpServer.URL, fixture.webSessionToken)
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 128; i++ {
		clientID := fmt.Sprintf("mcpcli_quota_%03d", i)
		authorizationID := fmt.Sprintf("authz_quota_%03d", i)
		if err := fixture.store.RegisterOAuthClient(context.Background(), store.OAuthClientRecord{
			ClientID: clientID, ClientName: clientID, RedirectURIs: []string{"https://chatgpt.com/oauth/callback"},
			GrantTypes: []string{"authorization_code"}, ResponseTypes: []string{"code"}, Scope: "fast-spider", CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.CreateOAuthAuthorization(context.Background(), store.OAuthAuthorizationRecord{
			AuthorizationID: authorizationID, OwnerID: fixture.ownerID, ClientID: clientID,
			Scopes: []string{"fast-spider"}, Resource: oauthTestPublicBaseURL + "/mcp",
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.RevokeOAuthAuthorization(context.Background(), fixture.ownerID, authorizationID, now); err != nil {
			t.Fatal(err)
		}
	}
	redirectURI := "https://chatgpt.com/oauth/callback"
	clientID, _ := registerOAuthClient(t, client, fixture.httpServer.URL+"/oauth/register", []string{redirectURI}, "")
	verifier := strings.Repeat("q", 43)
	values := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {oauthPKCEChallenge(verifier)},
		"code_challenge_method": {"S256"},
		"scope":                 {"fast-spider"},
		"resource":              {oauthTestPublicBaseURL + "/mcp"},
		"state":                 {"owner-client-quota"},
	}
	code := obtainOAuthCode(t, client, fixture.httpServer.URL+"/oauth/authorize", values, fixture.csrfToken)
	tokenForm := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"resource":      {oauthTestPublicBaseURL + "/mcp"},
		"code_verifier": {verifier},
	}
	for attempt := 1; attempt <= 2; attempt++ {
		status, headers, body := oauthTestRequest(t, client, http.MethodPost, fixture.httpServer.URL+"/oauth/token", tokenForm)
		if status != http.StatusBadRequest || headers.Get("Retry-After") != "" {
			t.Fatalf("quota token attempt %d status=%d retry-after=%q body=%s", attempt, status, headers.Get("Retry-After"), body)
		}
		assertOAuthError(t, body, "invalid_grant", true)
	}
}

func TestOAuthRegistrationRateLimit(t *testing.T) {
	fixture := newOAuthTestFixture(t)
	client := newOAuthTestHTTPClient(fixture.httpServer.URL, fixture.webSessionToken)
	endpoint := fixture.httpServer.URL + "/oauth/register"
	redirect := []string{"https://chatgpt.com/oauth/callback"}
	for attempt := 0; attempt < 30; attempt++ {
		status, _, body := registerOAuthClientRaw(t, client, endpoint, redirect, "")
		if status != http.StatusCreated {
			t.Fatalf("registration %d status=%d body=%s", attempt+1, status, body)
		}
	}
	status, headers, body := registerOAuthClientRaw(t, client, endpoint, redirect, "")
	if status != http.StatusTooManyRequests || headers.Get("Retry-After") == "" {
		t.Fatalf("rate-limited registration status=%d retry-after=%q body=%s", status, headers.Get("Retry-After"), body)
	}
	assertOAuthError(t, body, "temporarily_unavailable", true)
}

func registerOAuthClient(t *testing.T, client *http.Client, endpoint string, redirects []string, scope string) (string, oauthClientResponse) {
	t.Helper()
	status, _, body := registerOAuthClientRaw(t, client, endpoint, redirects, scope)
	if status != http.StatusCreated {
		t.Fatalf("client registration status=%d body=%s", status, body)
	}
	var response oauthClientResponse
	decodeOAuthJSON(t, body, &response)
	if response.ClientID == "" {
		t.Fatalf("client registration omitted client_id: %s", body)
	}
	return response.ClientID, response
}

func registerOAuthClientRaw(t *testing.T, client *http.Client, endpoint string, redirects []string, scope string) (int, http.Header, []byte) {
	return registerOAuthClientRawWithMetadata(t, client, endpoint, redirects, scope, []string{"authorization_code", "refresh_token"}, []string{"code"})
}

func registerOAuthClientRawWithMetadata(t *testing.T, client *http.Client, endpoint string, redirects []string, scope string, grantTypes, responseTypes []string) (int, http.Header, []byte) {
	t.Helper()
	return registerOAuthClientJSON(t, client, endpoint, map[string]any{
		"client_name":                "OAuth test client",
		"redirect_uris":              redirects,
		"token_endpoint_auth_method": "none",
		"grant_types":                grantTypes,
		"response_types":             responseTypes,
		"scope":                      scope,
	})
}

func registerOAuthClientJSON(t *testing.T, client *http.Client, endpoint string, metadata map[string]any) (int, http.Header, []byte) {
	t.Helper()
	body, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
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

func obtainOAuthCode(t *testing.T, client *http.Client, endpoint string, values url.Values, csrfToken string) string {
	t.Helper()
	postValues := cloneOAuthValues(values)
	postValues.Set("decision", "approve")
	postValues.Set("csrf_token", csrfToken)
	status, headers, body := oauthTestRequest(t, client, http.MethodPost, endpoint, postValues)
	if status != http.StatusFound {
		t.Fatalf("authorization POST status=%d location=%q body=%s", status, headers.Get("Location"), body)
	}
	redirect, err := url.Parse(headers.Get("Location"))
	if err != nil {
		t.Fatalf("parse authorization redirect: %v", err)
	}
	code := redirect.Query().Get("code")
	if code == "" || redirect.Query().Get("state") != values.Get("state") {
		t.Fatalf("authorization redirect=%q", headers.Get("Location"))
	}
	return code
}

func oauthTestRequest(t *testing.T, client *http.Client, method, endpoint string, form url.Values) (int, http.Header, []byte) {
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

func newOAuthTestHTTPClient(serverURL, sessionToken string) *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	parsed, err := url.Parse(serverURL)
	if err != nil {
		panic(err)
	}
	jar.SetCookies(parsed, []*http.Cookie{{Name: "fast_spider_session", Value: sessionToken, Path: "/"}})
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func cloneOAuthValues(values url.Values) url.Values {
	clone := make(url.Values, len(values))
	for key, list := range values {
		clone[key] = append([]string(nil), list...)
	}
	return clone
}

func oauthPKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func decodeOAuthJSON(t *testing.T, raw []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode OAuth JSON: %v body=%s", err, raw)
	}
}

func assertOAuthError(t *testing.T, raw []byte, want string, checkCode bool) {
	t.Helper()
	var response struct {
		Error string `json:"error"`
	}
	decodeOAuthJSON(t, raw, &response)
	if checkCode && response.Error != want {
		t.Fatalf("OAuth error=%q want=%q body=%s", response.Error, want, raw)
	}
}

func tokenSummary(tokens oauthTokenResponse) string {
	return "access-present=" + strconvBool(tokens.AccessToken != "") + ",refresh-present=" + strconvBool(tokens.RefreshToken != "")
}

func strconvBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

type oauthBearerTransport struct {
	token string
	base  http.RoundTripper
	host  string
}

func (t oauthBearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	if t.host != "" {
		clone.Host = t.host
	}
	return t.base.RoundTrip(clone)
}

func connectOAuthMCP(t *testing.T, ctx context.Context, endpoint, token string) *mcp.ClientSession {
	return connectOAuthMCPWithHost(t, ctx, endpoint, token, "")
}

func connectOAuthMCPWithHost(t *testing.T, ctx context.Context, endpoint, token, host string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "oauth-test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: endpoint,
		HTTPClient: &http.Client{Transport: oauthBearerTransport{
			token: token,
			base:  http.DefaultTransport,
			host:  host,
		}},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func assertMCPToolAnnotations(t *testing.T, tools []*mcp.Tool) {
	t.Helper()
	want := map[string]struct {
		destructive bool
		openWorld   bool
	}{
		"file_edit":     {destructive: true, openWorld: false},
		"shell_run":     {destructive: true, openWorld: true},
		"git_control":   {destructive: true, openWorld: true},
		"build_control": {destructive: true, openWorld: true},
	}
	seen := make(map[string]bool, len(want))
	for _, tool := range tools {
		expected, ok := want[tool.Name]
		if !ok {
			continue
		}
		seen[tool.Name] = true
		if tool.Annotations == nil {
			t.Errorf("tool %s omitted annotations", tool.Name)
			continue
		}
		if tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %s incorrectly advertises readOnlyHint", tool.Name)
		}
		if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint != expected.destructive {
			t.Errorf("tool %s destructiveHint=%v want=%v", tool.Name, tool.Annotations.DestructiveHint, expected.destructive)
		}
		if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint != expected.openWorld {
			t.Errorf("tool %s openWorldHint=%v want=%v", tool.Name, tool.Annotations.OpenWorldHint, expected.openWorld)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("tools/list omitted %s", name)
		}
	}
}
