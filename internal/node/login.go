package node

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	nodeLoginTimeout = 5 * time.Minute
	nodeOAuthScope   = "fast-spider:device-connect"
)

type LoginOptions struct {
	HubURL             string
	DisplayName        string
	OpenBrowser        bool
	AuthorizationReady func(string)
}

type oauthRegistrationResponse struct {
	ClientID string `json:"client_id"`
}

type oauthLoginTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
}

type enrollmentTokenResponse struct {
	EnrollmentToken string    `json:"enrollmentToken"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

type oauthLoginCallback struct {
	Code  string
	Error string
}

func (c *Client) Login(ctx context.Context, options LoginOptions) (State, error) {
	hubURL, err := c.normalizeHubURL(options.HubURL)
	if err != nil {
		return State{}, err
	}
	if state, stateErr := c.State(); stateErr == nil && state.MachineID != "" && state.HubURL == hubURL {
		probeCtx, probeCancel := context.WithTimeout(ctx, 10*time.Second)
		_, probeErr := c.issueDeviceToken(probeCtx, state)
		probeCancel()
		if probeErr == nil {
			return State{}, errors.New("node is already connected to this Hub; use run to reconnect")
		}
		var hubErr *HubAPIError
		if !errors.As(probeErr, &hubErr) || (hubErr.Code != "REVOKED" && hubErr.Code != "NOT_FOUND" && hubErr.Code != "UNAUTHORIZED") {
			return State{}, fmt.Errorf("verify existing node enrollment before login: %w", probeErr)
		}
	}
	displayName := strings.TrimSpace(options.DisplayName)
	if displayName == "" {
		return State{}, errors.New("display name is required")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return State{}, fmt.Errorf("start OAuth callback listener: %w", err)
	}
	defer listener.Close()
	callbackURL := "http://" + listener.Addr().String() + "/oauth/callback"

	var registration oauthRegistrationResponse
	if err := c.postJSON(ctx, hubURL+"/oauth/register", "", map[string]any{
		"client_name":                "Fast Spider Node - " + displayName,
		"redirect_uris":              []string{callbackURL},
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
		"scope":                      nodeOAuthScope,
	}, &registration); err != nil {
		return State{}, fmt.Errorf("register OAuth client: %w", err)
	}
	if registration.ClientID == "" {
		return State{}, errors.New("hub returned an empty OAuth client ID")
	}

	verifier, err := randomURLSafe(32)
	if err != nil {
		return State{}, err
	}
	stateValue, err := randomURLSafe(24)
	if err != nil {
		return State{}, err
	}
	challengeSum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeSum[:])
	resource := hubURL + "/api/v1/enrollment-tokens"
	authorizeURL, err := url.Parse(hubURL + "/oauth/authorize")
	if err != nil {
		return State{}, err
	}
	query := authorizeURL.Query()
	query.Set("response_type", "code")
	query.Set("client_id", registration.ClientID)
	query.Set("redirect_uri", callbackURL)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("scope", nodeOAuthScope)
	query.Set("resource", resource)
	query.Set("state", stateValue)
	authorizeURL.RawQuery = query.Encode()

	callbackCh := make(chan oauthLoginCallback, 1)
	callbackServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/oauth/callback" {
				http.NotFound(w, r)
				return
			}
			if r.URL.Query().Get("state") != stateValue {
				http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
				return
			}
			result := oauthLoginCallback{Code: r.URL.Query().Get("code"), Error: r.URL.Query().Get("error")}
			select {
			case callbackCh <- result:
			default:
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
			if result.Error != "" || result.Code == "" {
				_, _ = io.WriteString(w, `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Fast Spider 登录已取消</title><body style="font-family:system-ui;padding:48px;max-width:560px;margin:auto"><h1>登录未完成</h1><p>授权已取消或没有返回授权码。可以关闭此页面并重新运行 Node 登录。</p></body></html>`)
				return
			}
			_, _ = io.WriteString(w, `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Fast Spider 授权完成</title><body style="font-family:system-ui;padding:48px;max-width:560px;margin:auto"><h1>授权已返回 Node</h1><p>Node 正在完成设备登记和连接。可以关闭此页面。</p></body></html>`)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveDone := make(chan error, 1)
	go func() {
		err := callbackServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveDone <- err
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = callbackServer.Shutdown(shutdownCtx)
	}()

	if options.AuthorizationReady != nil {
		options.AuthorizationReady(authorizeURL.String())
	}
	if options.OpenBrowser {
		if err := openSystemBrowser(authorizeURL.String()); err != nil {
			return State{}, fmt.Errorf("open system browser: %w; rerun with --no-browser", err)
		}
	}

	loginCtx, cancel := context.WithTimeout(ctx, nodeLoginTimeout)
	defer cancel()
	var callback oauthLoginCallback
	select {
	case callback = <-callbackCh:
	case err := <-serveDone:
		if err == nil {
			err = errors.New("OAuth callback listener stopped")
		}
		return State{}, err
	case <-loginCtx.Done():
		return State{}, fmt.Errorf("OAuth login timed out: %w", loginCtx.Err())
	}
	if callback.Error != "" {
		return State{}, fmt.Errorf("OAuth authorization failed: %s", callback.Error)
	}
	if callback.Code == "" {
		return State{}, errors.New("OAuth callback did not include an authorization code")
	}

	var tokens oauthLoginTokenResponse
	if err := c.postForm(loginCtx, hubURL+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {registration.ClientID},
		"code":          {callback.Code},
		"redirect_uri":  {callbackURL},
		"resource":      {resource},
		"code_verifier": {verifier},
	}, &tokens); err != nil {
		return State{}, fmt.Errorf("exchange OAuth authorization code: %w", err)
	}
	if tokens.AccessToken == "" || !strings.EqualFold(tokens.TokenType, "Bearer") {
		return State{}, errors.New("hub returned an invalid OAuth token response")
	}
	defer func() {
		revokeCtx, revokeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer revokeCancel()
		_ = c.postForm(revokeCtx, hubURL+"/oauth/revoke", url.Values{
			"token": {tokens.AccessToken},
		}, nil)
	}()

	var enrollment enrollmentTokenResponse
	if err := c.postJSON(loginCtx, hubURL+"/api/v1/enrollment-tokens", tokens.AccessToken, map[string]any{
		"expectedName": displayName,
		"expectedOs":   runtime.GOOS,
	}, &enrollment); err != nil {
		return State{}, fmt.Errorf("create device enrollment: %w", err)
	}
	if enrollment.EnrollmentToken == "" {
		return State{}, errors.New("hub returned an empty enrollment token")
	}
	return c.Enroll(loginCtx, hubURL, enrollment.EnrollmentToken, displayName)
}

func (c *Client) postForm(ctx context.Context, endpoint string, values url.Values, output any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "fast-spider-node/"+c.cfg.Version)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponseBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxHTTPResponseBytes {
		return errors.New("hub response exceeds limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var oauthError struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		if json.Unmarshal(body, &oauthError) == nil && oauthError.Error != "" {
			return fmt.Errorf("hub OAuth %s: %s", oauthError.Error, oauthError.ErrorDescription)
		}
		return fmt.Errorf("hub HTTP status %d", resp.StatusCode)
	}
	if output == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, output); err != nil {
		return fmt.Errorf("decode hub response: %w", err)
	}
	return nil
}

func randomURLSafe(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate OAuth random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func openSystemBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		command = exec.Command("open", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
}
