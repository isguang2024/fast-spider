package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	oauthMaxPendingCodes = 1024
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
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope"`
}

type oauthErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

func newOAuthState() *oauthState {
	return &oauthState{codes: make(map[string]oauthAuthorizationCode)}
}

func (o *oauthState) pruneExpiredLocked(now time.Time) {
	for code, record := range o.codes {
		if !record.ExpiresAt.After(now) {
			delete(o.codes, code)
		}
	}
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
	parsed, err := url.Parse(suffix)
	if err == nil && strings.HasPrefix(parsed.Path, "/") {
		u.Path = strings.TrimRight(base.Path, "/") + parsed.Path
		u.RawQuery = parsed.RawQuery
	} else {
		u.Path = strings.TrimRight(base.Path, "/") + suffix
		u.RawQuery = ""
	}
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
	grantTypes := normalizeOAuthValues(req.GrantTypes, []string{"authorization_code", "refresh_token"})
	if !allOAuthValuesSupported(grantTypes, "authorization_code", "refresh_token") || !slices.Contains(grantTypes, "authorization_code") {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported grant type")
		return
	}
	responseTypes := normalizeOAuthValues(req.ResponseTypes, []string{"code"})
	if !allOAuthValuesSupported(responseTypes, "code") || !slices.Contains(responseTypes, "code") {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported response type")
		return
	}
	clientScope := strings.TrimSpace(req.Scope)
	if clientScope == "" {
		clientScope = oauthScope
	}
	if !scopeAllowed(clientScope) {
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
		ClientID:      clientID,
		ClientName:    name,
		RedirectURIs:  req.RedirectURIs,
		GrantTypes:    grantTypes,
		ResponseTypes: responseTypes,
		Scope:         clientScope,
		CreatedAt:     now,
	}); err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to register client")
		return
	}
	writeJSON(w, http.StatusCreated, oauthClientRegistrationResponse{
		ClientID:                clientID,
		ClientIDIssuedAt:        now.Unix(),
		ClientName:              name,
		RedirectURIs:            req.RedirectURIs,
		TokenEndpointAuthMethod: "none",
		GrantTypes:              grantTypes,
		ResponseTypes:           responseTypes,
		Scope:                   clientScope,
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
	session, err := s.currentWebSession(r)
	if err != nil {
		returnTo := s.publicURL(r, "/oauth/authorize") + "?" + oauthAuthorizationQuery(values).Encode()
		loginURL := s.publicURL(r, "/login") + "?return_to=" + url.QueryEscape(returnTo)
		http.Redirect(w, r, loginURL, http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodGet {
		s.renderOAuthAuthorizePage(w, r, session, client, values, "")
		return
	}
	if !s.verifyCSRF(w, r, session.CSRFToken) {
		return
	}
	if r.PostForm.Get("decision") == "deny" {
		redirectOAuthAuthorizationError(w, r, values, "access_denied")
		return
	}
	code, err := security.RandomOpaque("code_")
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to create authorization code")
		return
	}
	now := time.Now().UTC()
	record := oauthAuthorizationCode{
		ClientID: client.ClientID, OwnerID: session.OwnerID, RedirectURI: values.Get("redirect_uri"),
		CodeChallenge: values.Get("code_challenge"), Scope: scope, Resource: resource,
		ExpiresAt: now.Add(oauthCodeTTL),
	}
	s.oauth.mu.Lock()
	s.oauth.pruneExpiredLocked(now)
	if len(s.oauth.codes) >= oauthMaxPendingCodes {
		s.oauth.mu.Unlock()
		writeOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "too many pending authorization requests")
		return
	}
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

func oauthAuthorizationQuery(values url.Values) url.Values {
	result := make(url.Values)
	for _, name := range []string{
		"response_type", "client_id", "redirect_uri", "code_challenge",
		"code_challenge_method", "scope", "state", "resource",
	} {
		if value := values.Get(name); value != "" {
			result.Set(name, value)
		}
	}
	return result
}

func redirectOAuthAuthorizationError(w http.ResponseWriter, r *http.Request, values url.Values, code string) {
	redirect, err := url.Parse(values.Get("redirect_uri"))
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid redirect URI")
		return
	}
	query := redirect.Query()
	query.Set("error", code)
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
	if !slices.Contains(client.ResponseTypes, "code") {
		return store.OAuthClientRecord{}, "", "", fmt.Errorf("client did not register the code response type")
	}
	if !slices.Contains(client.RedirectURIs, values.Get("redirect_uri")) {
		return store.OAuthClientRecord{}, "", "", fmt.Errorf("redirect_uri is not registered")
	}
	if values.Get("code_challenge_method") != "S256" || values.Get("code_challenge") == "" {
		return store.OAuthClientRecord{}, "", "", fmt.Errorf("PKCE S256 is required")
	}
	scope := strings.TrimSpace(values.Get("scope"))
	if scope == "" {
		scope = client.Scope
	}
	if !scopeAllowed(scope) || !scopeSubset(strings.Fields(scope), strings.Fields(client.Scope)) {
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
	grantType := r.PostForm.Get("grant_type")
	if !slices.Contains(client.GrantTypes, grantType) {
		writeOAuthError(w, http.StatusBadRequest, "unauthorized_client", "client did not register this grant type")
		return
	}
	switch grantType {
	case "authorization_code":
		s.handleOAuthAuthorizationCodeExchange(w, r, client)
	case "refresh_token":
		s.handleOAuthRefreshExchange(w, r, client)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "unsupported grant type")
	}
}

func (s *Server) handleOAuthAuthorizationCodeExchange(w http.ResponseWriter, r *http.Request, client store.OAuthClientRecord) {
	code := r.PostForm.Get("code")
	now := time.Now().UTC()
	s.oauth.mu.Lock()
	s.oauth.pruneExpiredLocked(now)
	record, ok := s.oauth.codes[code]
	failure := ""
	if !ok || record.ClientID != client.ClientID {
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
	s.issueOAuthTokens(w, r, record.OwnerID, client, record.Scope, record.Resource, "", "")
}

func (s *Server) handleOAuthRefreshExchange(w http.ResponseWriter, r *http.Request, client store.OAuthClientRecord) {
	refresh := r.PostForm.Get("refresh_token")
	record, err := s.service.Store().GetOAuthRefreshToken(r.Context(), security.HashToken(refresh), time.Now().UTC())
	if err != nil || record.ClientID != client.ClientID {
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
	s.issueOAuthTokens(w, r, record.OwnerID, client, scope, record.Resource, record.AuthorizationID, security.HashToken(refresh))
}

func (s *Server) issueOAuthTokens(w http.ResponseWriter, r *http.Request, ownerID string, client store.OAuthClientRecord, scope, resource, authorizationID, consumedRefreshHash string) {
	access, err := security.RandomOpaque("oat_")
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to issue access token")
		return
	}
	now := time.Now().UTC()
	accessExpires := now.Add(oauthAccessTokenTTL)
	refreshExpires := now.Add(oauthRefreshTokenTTL)
	refresh := ""
	refreshHash := ""
	if slices.Contains(client.GrantTypes, "refresh_token") {
		refresh, err = security.RandomOpaque("ort_")
		if err != nil {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to issue refresh token")
			return
		}
		refreshHash = security.HashToken(refresh)
	}
	authorizationExpires := accessExpires
	if refresh != "" {
		authorizationExpires = refreshExpires
	}
	scopes := strings.Fields(scope)
	createdAuthorization := false
	if authorizationID == "" {
		authorizationID, err = security.RandomOpaque("authz_")
		if err != nil {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to create authorization")
			return
		}
		if err := s.service.Store().CreateOAuthAuthorization(r.Context(), store.OAuthAuthorizationRecord{
			AuthorizationID: authorizationID,
			OwnerID:         ownerID,
			ClientID:        client.ClientID,
			Scopes:          scopes,
			Resource:        resource,
			CreatedAt:       now,
			ExpiresAt:       authorizationExpires,
		}); err != nil {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to persist authorization")
			return
		}
		createdAuthorization = true
	}
	if err := s.service.Store().SaveOAuthTokenPair(r.Context(), security.HashToken(access), refreshHash, store.OAuthTokenRecord{
		AuthorizationID: authorizationID,
		OwnerID:         ownerID,
		ClientID:        client.ClientID,
		Scopes:          scopes,
		Resource:        resource,
	}, accessExpires, refreshExpires, consumedRefreshHash, now); err != nil {
		if createdAuthorization {
			_ = s.service.Store().RevokeOAuthAuthorization(r.Context(), ownerID, authorizationID, now)
		}
		if consumedRefreshHash != "" && errors.Is(err, store.ErrUnauthorized) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "invalid refresh token")
		} else {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to persist OAuth token")
		}
		return
	}
	writeJSON(w, http.StatusOK, oauthTokenResponse{
		AccessToken:  access,
		TokenType:    "Bearer",
		ExpiresIn:    int64(oauthAccessTokenTTL.Seconds()),
		RefreshToken: refresh,
		Scope:        strings.Join(scopes, " "),
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
		_ = s.service.Store().RevokeOAuthToken(r.Context(), security.HashToken(token), time.Now().UTC())
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) mcpTokenVerifier(ctx context.Context, token string, req *http.Request) (*auth.TokenInfo, error) {
	now := time.Now().UTC()
	record, err := s.service.Store().AuthenticateOAuthAccessToken(ctx, security.HashToken(token), now)
	if err != nil || !slices.Contains(record.Scopes, oauthScope) {
		return nil, fmt.Errorf("%w: invalid bearer token", auth.ErrInvalidToken)
	}
	resource, err := s.oauthResourceURL(req)
	if err != nil || record.Resource != resource {
		return nil, fmt.Errorf("%w: OAuth resource mismatch", auth.ErrInvalidToken)
	}
	_ = s.service.Store().TouchOAuthAuthorization(ctx, record.AuthorizationID, now)
	s.mcpDiagnostics.recordAuthenticatedRequest(record.OwnerID, normalizeMCPClientName(req.UserAgent()), now)
	return &auth.TokenInfo{Scopes: record.Scopes, Expiration: record.ExpiresAt, UserID: record.OwnerID, Extra: map[string]any{"clientId": record.ClientID, "resource": record.Resource, "authorizationId": record.AuthorizationID}}, nil
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

func normalizeOAuthValues(values, defaults []string) []string {
	if len(values) == 0 {
		return append([]string(nil), defaults...)
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result
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
