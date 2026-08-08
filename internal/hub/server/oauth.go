package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/store"
	"github.com/isguang2024/fast-spider/internal/security"
	"github.com/modelcontextprotocol/go-sdk/auth"
)

const (
	oauthScope           = "fast-spider"
	oauthCodeTTL         = 5 * time.Minute
	oauthAccessTokenTTL  = time.Hour
	oauthRefreshTokenTTL = 30 * 24 * time.Hour
)

var defaultOAuthRedirectHosts = []string{"chatgpt.com", "localhost", "127.0.0.1", "::1"}

type oauthAuthorizationCode struct {
	ClientID      string
	OwnerID       string
	RedirectURI   string
	CodeChallenge string
	Scope         string
	Resource      string
	ExpiresAt     time.Time
}

type oauthState struct {
	mu    sync.Mutex
	codes map[string]oauthAuthorizationCode
}

type oauthClientRegistrationRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scope                   string   `json:"scope"`
}

type oauthClientRegistrationResponse struct {
	ClientID                string   `json:"client_id"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	ClientName              string   `json:"client_name,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scope                   string   `json:"scope"`
}

type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

type oauthErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

func newOAuthState() *oauthState {
	return &oauthState{codes: make(map[string]oauthAuthorizationCode)}
}

func (s *Server) oauthBaseURL(r *http.Request) (*url.URL, error) {
	if raw := strings.TrimSpace(s.config.PublicBaseURL); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("invalid public base URL")
		}
		u.Path = strings.TrimRight(u.Path, "/")
		return u, nil
	}
	scheme := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
	if scheme != "http" && scheme != "https" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return &url.URL{Scheme: scheme, Host: r.Host}, nil
}

func oauthURL(base *url.URL, suffix string) string {
	u := *base
	u.Path = strings.TrimRight(base.Path, "/") + suffix
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func (s *Server) oauthResourceURL(r *http.Request) (string, error) {
	base, err := s.oauthBaseURL(r)
	if err != nil {
		return "", err
	}
	return oauthURL(base, "/mcp"), nil
}

func (s *Server) oauthResourceMetadataURL(r *http.Request) (string, error) {
	base, err := s.oauthBaseURL(r)
	if err != nil {
		return "", err
	}
	return oauthURL(base, "/.well-known/oauth-protected-resource"), nil
}

func (s *Server) handleOAuthProtectedResource(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	base, err := s.oauthBaseURL(r)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	resource := oauthURL(base, "/mcp")
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 resource,
		"authorization_servers":    []string{strings.TrimRight(base.String(), "/")},
		"scopes_supported":         []string{oauthScope},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Fast Spider",
	})
}

func (s *Server) handleOAuthAuthorizationServer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	base, err := s.oauthBaseURL(r)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	issuer := strings.TrimRight(base.String(), "/")
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                oauthURL(base, "/oauth/authorize"),
		"token_endpoint":                        oauthURL(base, "/oauth/token"),
		"registration_endpoint":                 oauthURL(base, "/oauth/register"),
		"revocation_endpoint":                   oauthURL(base, "/oauth/revoke"),
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{oauthScope},
	})
}

func (s *Server) handleOAuthRegister(w http.ResponseWriter, r *http.Request) {
	var req oauthClientRegistrationRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxControlMessageBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "invalid client registration request")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 8 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "at least one redirect URI is required")
		return
	}
	if req.TokenEndpointAuthMethod != "" && req.TokenEndpointAuthMethod != "none" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "only public PKCE clients are supported")
		return
	}
	if !allOAuthValuesSupported(req.GrantTypes, "authorization_code", "refresh_token") {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported grant type")
		return
	}
	if !allOAuthValuesSupported(req.ResponseTypes, "code") {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported response type")
		return
	}
	if req.Scope != "" && !scopeAllowed(req.Scope) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported scope")
		return
	}
	for _, raw := range req.RedirectURIs {
		if !validOAuthRedirectURI(raw, s.config.OAuthRedirectHosts) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect URI is not allowed")
			return
		}
	}
	clientID, err := security.RandomOpaque("mcpcli_")
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to create client")
		return
	}
	name := strings.TrimSpace(req.ClientName)
	if name == "" {
		name = "MCP client"
	}
	if len(name) > 128 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "client name is too long")
		return
	}
	now := time.Now().UTC()
	if err := s.service.Store().RegisterOAuthClient(r.Context(), store.OAuthClientRecord{
		ClientID: clientID, ClientName: name, RedirectURIs: req.RedirectURIs, CreatedAt: now,
	}); err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to register client")
		return
	}
	writeJSON(w, http.StatusCreated, oauthClientRegistrationResponse{
		ClientID: clientID, ClientIDIssuedAt: now.Unix(), ClientName: name, RedirectURIs: req.RedirectURIs,
		TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code", "refresh_token"},
		ResponseTypes: []string{"code"}, Scope: oauthScope,
	})
}

func (s *Server) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, maxControlMessageBytes)
		if err := r.ParseForm(); err != nil {
			writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid form")
			return
		}
	}
	values := r.URL.Query()
	if r.Method == http.MethodPost {
		values = r.PostForm
	}
	client, resource, scope, err := s.validateOAuthAuthorizationRequest(r.Context(), r, values)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if r.Method == http.MethodGet {
		writeOAuthAuthorizeForm(w, client.ClientName, values, "")
		return
	}
	ownerToken := r.PostForm.Get("owner_token")
	ownerID, err := s.service.AuthenticateOwner(r.Context(), ownerToken)
	if err != nil {
		writeOAuthAuthorizeForm(w, client.ClientName, values, "Owner Token was not accepted.")
		return
	}
	code, err := security.RandomOpaque("code_")
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to create authorization code")
		return
	}
	record := oauthAuthorizationCode{
		ClientID: client.ClientID, OwnerID: ownerID, RedirectURI: values.Get("redirect_uri"),
		CodeChallenge: values.Get("code_challenge"), Scope: scope, Resource: resource,
		ExpiresAt: time.Now().UTC().Add(oauthCodeTTL),
	}
	s.oauth.mu.Lock()
	s.oauth.codes[code] = record
	s.oauth.mu.Unlock()
	redirect, _ := url.Parse(record.RedirectURI)
	query := redirect.Query()
	query.Set("code", code)
	if state := values.Get("state"); state != "" {
		query.Set("state", state)
	}
	redirect.RawQuery = query.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (s *Server) validateOAuthAuthorizationRequest(ctx context.Context, r *http.Request, values url.Values) (store.OAuthClientRecord, string, string, error) {
	if values.Get("response_type") != "code" {
		return store.OAuthClientRecord{}, "", "", fmt.Errorf("response_type must be code")
	}
	client, err := s.service.Store().GetOAuthClient(ctx, values.Get("client_id"))
	if err != nil {
		return store.OAuthClientRecord{}, "", "", fmt.Errorf("unknown client")
	}
	if !slices.Contains(client.RedirectURIs, values.Get("redirect_uri")) {
		return store.OAuthClientRecord{}, "", "", fmt.Errorf("redirect_uri is not registered")
	}
	if values.Get("code_challenge_method") != "S256" || values.Get("code_challenge") == "" {
		return store.OAuthClientRecord{}, "", "", fmt.Errorf("PKCE S256 is required")
	}
	scope := strings.TrimSpace(values.Get("scope"))
	if scope == "" {
		scope = oauthScope
	}
	if !scopeAllowed(scope) {
		return store.OAuthClientRecord{}, "", "", fmt.Errorf("unsupported scope")
	}
	resource, err := s.oauthResourceURL(r)
	if err != nil {
		return store.OAuthClientRecord{}, "", "", err
	}
	if requested := strings.TrimSpace(values.Get("resource")); requested != "" && requested != resource {
		return store.OAuthClientRecord{}, "", "", fmt.Errorf("invalid OAuth resource")
	}
	return client, resource, scope, nil
}

func (s *Server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxControlMessageBytes)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid form")
		return
	}
	clientID := strings.TrimSpace(r.PostForm.Get("client_id"))
	client, err := s.service.Store().GetOAuthClient(r.Context(), clientID)
	if err != nil {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "unknown client")
		return
	}
	_ = client
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.handleOAuthAuthorizationCodeExchange(w, r, clientID)
	case "refresh_token":
		s.handleOAuthRefreshExchange(w, r, clientID)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "unsupported grant type")
	}
}

func (s *Server) handleOAuthAuthorizationCodeExchange(w http.ResponseWriter, r *http.Request, clientID string) {
	code := r.PostForm.Get("code")
	s.oauth.mu.Lock()
	record, ok := s.oauth.codes[code]
	failure := ""
	if !ok || record.ExpiresAt.Before(time.Now().UTC()) || record.ClientID != clientID {
		failure = "invalid authorization code"
	} else if redirectURI := r.PostForm.Get("redirect_uri"); redirectURI != "" && redirectURI != record.RedirectURI {
		failure = "redirect_uri does not match"
	} else if resource := r.PostForm.Get("resource"); resource != "" && resource != record.Resource {
		failure = "resource does not match"
	} else if !verifyPKCE(record.CodeChallenge, r.PostForm.Get("code_verifier")) {
		failure = "PKCE verification failed"
	} else {
		delete(s.oauth.codes, code)
	}
	s.oauth.mu.Unlock()
	if failure != "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", failure)
		return
	}
	s.issueOAuthTokens(w, r, record.OwnerID, clientID, record.Scope, record.Resource, "")
}

func (s *Server) handleOAuthRefreshExchange(w http.ResponseWriter, r *http.Request, clientID string) {
	refresh := r.PostForm.Get("refresh_token")
	record, err := s.service.Store().GetOAuthRefreshToken(r.Context(), security.HashToken(refresh), time.Now().UTC())
	if err != nil || record.ClientID != clientID {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "invalid refresh token")
		return
	}
	if resource := r.PostForm.Get("resource"); resource != "" && resource != record.Resource {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "resource does not match")
		return
	}
	scope := strings.TrimSpace(r.PostForm.Get("scope"))
	if scope == "" {
		scope = strings.Join(record.Scopes, " ")
	}
	if !scopeAllowed(scope) || !scopeSubset(strings.Fields(scope), record.Scopes) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_scope", "refresh token cannot grant requested scope")
		return
	}
	s.issueOAuthTokens(w, r, record.OwnerID, clientID, scope, record.Resource, security.HashToken(refresh))
}

func (s *Server) issueOAuthTokens(w http.ResponseWriter, r *http.Request, ownerID, clientID, scope, resource, consumedRefreshHash string) {
	access, err := security.RandomOpaque("oat_")
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to issue access token")
		return
	}
	refresh, err := security.RandomOpaque("ort_")
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to issue refresh token")
		return
	}
	now := time.Now().UTC()
	scopes := strings.Fields(scope)
	if err := s.service.Store().SaveOAuthTokenPair(r.Context(), security.HashToken(access), security.HashToken(refresh), store.OAuthTokenRecord{
		OwnerID: ownerID, ClientID: clientID, Scopes: scopes, Resource: resource,
	}, now.Add(oauthAccessTokenTTL), now.Add(oauthRefreshTokenTTL), consumedRefreshHash, now); err != nil {
		if consumedRefreshHash != "" && errors.Is(err, store.ErrUnauthorized) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "invalid refresh token")
		} else {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to persist OAuth token")
		}
		return
	}
	writeJSON(w, http.StatusOK, oauthTokenResponse{
		AccessToken: access, TokenType: "Bearer", ExpiresIn: int64(oauthAccessTokenTTL.Seconds()),
		RefreshToken: refresh, Scope: strings.Join(scopes, " "),
	})
}

func (s *Server) handleOAuthRevoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxControlMessageBytes)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid form")
		return
	}
	token := strings.TrimSpace(r.PostForm.Get("token"))
	if token != "" {
		_ = s.service.Store().RevokeOAuthToken(r.Context(), security.HashToken(token))
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) mcpTokenVerifier(ctx context.Context, token string, req *http.Request) (*auth.TokenInfo, error) {
	if ownerID, err := s.service.AuthenticateOwner(ctx, token); err == nil {
		return &auth.TokenInfo{Scopes: []string{oauthScope}, Expiration: time.Now().UTC().Add(24 * time.Hour), UserID: ownerID}, nil
	}
	record, err := s.service.Store().AuthenticateOAuthAccessToken(ctx, security.HashToken(token), time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("%w: invalid bearer token", auth.ErrInvalidToken)
	}
	resource, err := s.oauthResourceURL(req)
	if err != nil || record.Resource != resource {
		return nil, fmt.Errorf("%w: OAuth resource mismatch", auth.ErrInvalidToken)
	}
	return &auth.TokenInfo{Scopes: record.Scopes, Expiration: record.ExpiresAt, UserID: record.OwnerID, Extra: map[string]any{"clientId": record.ClientID, "resource": record.Resource}}, nil
}

func validOAuthRedirectURI(raw string, allowedHosts []string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.Fragment != "" || u.User != nil || u.Opaque != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	allowed := allowedHosts
	if len(allowed) == 0 {
		allowed = defaultOAuthRedirectHosts
	}
	if !slices.ContainsFunc(allowed, func(item string) bool { return strings.EqualFold(strings.TrimSpace(item), host) }) {
		return false
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return u.Scheme == "http" || u.Scheme == "https"
	}
	return u.Scheme == "https"
}

func allOAuthValuesSupported(values []string, supported ...string) bool {
	for _, value := range values {
		if !slices.Contains(supported, value) {
			return false
		}
	}
	return true
}

func scopeAllowed(scope string) bool {
	fields := strings.Fields(scope)
	return len(fields) == 1 && fields[0] == oauthScope
}

func scopeSubset(requested, existing []string) bool {
	for _, scope := range requested {
		if !slices.Contains(existing, scope) {
			return false
		}
	}
	return true
}

func verifyPKCE(challenge, verifier string) bool {
	if challenge == "" || len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}

func writeOAuthAuthorizeForm(w http.ResponseWriter, clientName string, values url.Values, errorMessage string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	field := func(name string) string {
		return `<input type="hidden" name="` + html.EscapeString(name) + `" value="` + html.EscapeString(values.Get(name)) + `">`
	}
	errorHTML := ""
	if errorMessage != "" {
		errorHTML = `<p><strong>` + html.EscapeString(errorMessage) + `</strong></p>`
	}
	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Connect Fast Spider</title></head><body><main><h1>Connect Fast Spider</h1><p>Approve only if you are connecting your own MCP client to your Fast Spider Hub.</p>%s<p>Client: <strong>%s</strong></p><form method="post">%s%s%s%s%s%s%s%s<label>Owner Token <input name="owner_token" type="password" autocomplete="current-password" required autofocus></label><button type="submit">Authorize Fast Spider</button></form></main></body></html>`,
		errorHTML, html.EscapeString(clientName), field("response_type"), field("client_id"), field("redirect_uri"), field("code_challenge"), field("code_challenge_method"), field("scope"), field("state"), field("resource"))
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, oauthErrorResponse{Error: code, ErrorDescription: description})
}

func oauthFormMethodOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}

func oauthPostOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}
