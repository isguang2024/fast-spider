package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

const maxControlMessageBytes = 1 << 20

type Config struct {
	ListenAddr         string
	AllowedHosts       []string
	PublicBaseURL      string
	OAuthRedirectHosts []string
	Logger             *slog.Logger
}

type Server struct {
	service            *core.Service
	config             Config
	oauth              *oauthState
	oauthRegistrations *oauthRegistrationGuard
	loginLimiter       *loginFailureLimiter
	directLimiter      *directRateLimiter
	presentations      *presentationStore
	mcpDiagnostics     *mcpDiagnosticsStore
	http               *http.Server
}

type apiError struct {
	Error protocolv1.ProtocolError `json:"error"`
}

func New(service *core.Service, cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	startedAt := time.Now().UTC()
	s := &Server{
		service:            service,
		config:             cfg,
		oauth:              newOAuthState(),
		oauthRegistrations: newOAuthRegistrationGuard(),
		loginLimiter:       newLoginFailureLimiter(),
		directLimiter:      newDirectRateLimiter(),
		presentations:      newPresentationStore(presentationTempRoot(service.DataDir())),
		mcpDiagnostics:     newMCPDiagnosticsStore(service.Version(), startedAt),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleWebRoot)
	mux.HandleFunc("GET /setup", s.handleSetup)
	mux.HandleFunc("POST /setup", s.handleSetup)
	mux.HandleFunc("GET /login", s.handleLogin)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.webSessionOnly(s.handleLogout))
	mux.HandleFunc("GET /app", s.webSessionOnly(s.handleApp))
	mux.HandleFunc("GET /app/api/mcp-diagnostics", s.webSessionJSONOnly(s.handleAppMCPDiagnostics))
	mux.HandleFunc("POST /app/machines/{machineId}/note", s.webSessionOnly(s.handleAppMachineNote))
	mux.HandleFunc("POST /app/machines/{machineId}/revoke", s.webSessionOnly(s.handleAppMachineRevoke))
	mux.HandleFunc("POST /app/machines/{machineId}/delete", s.webSessionOnly(s.handleAppMachineDelete))
	mux.HandleFunc("POST /app/oauth/authorizations/{authorizationId}/revoke", s.webSessionOnly(s.handleAppAuthorizationRevoke))
	mux.HandleFunc("POST /app/oauth/authorizations/{authorizationId}/delete", s.webSessionOnly(s.handleAppAuthorizationDelete))
	mux.HandleFunc("POST /app/oauth/clients/{clientId}/delete", s.webSessionOnly(s.handleAppClientDelete))
	mux.HandleFunc("POST /app/account/password", s.webSessionOnly(s.handleAppPasswordChange))
	mux.HandleFunc("POST /app/tokens", s.webSessionOnly(s.handleAppTokenCreate))
	mux.HandleFunc("POST /app/tokens/{tokenId}/revoke", s.webSessionOnly(s.handleAppTokenRevoke))
	mux.HandleFunc("POST /app/tokens/{tokenId}/delete", s.webSessionOnly(s.handleAppTokenDelete))
	mux.HandleFunc("POST /app/direct-keys", s.webSessionOnly(s.handleAppDirectKeyCreate))
	mux.HandleFunc("POST /app/direct-keys/{keyId}/revoke", s.webSessionOnly(s.handleAppDirectKeyRevoke))
	mux.HandleFunc("POST /app/direct-keys/{keyId}/delete", s.webSessionOnly(s.handleAppDirectKeyDelete))
	mux.HandleFunc("GET /assets/{file}", s.handleWebAsset)
	mux.HandleFunc("GET /livez", s.handleLive)
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.HandleFunc("POST /api/v1/machines/register", s.connectionTokenOnly(s.handleMachineRegister))
	mux.HandleFunc("POST /api/v1/device/token", s.handleDeviceToken)
	mux.HandleFunc("GET /api/v1/node/releases/{platform}/latest", s.handleNodeReleaseManifest)
	mux.HandleFunc("GET /api/v1/node/releases/{platform}/download", s.handleNodeReleaseDownload)
	mux.HandleFunc("GET /api/v1/node/components/{componentId}/{platform}/latest", s.handleComponentReleaseManifest)
	mux.HandleFunc("GET /api/v1/node/components/{componentId}/{platform}/download", s.handleComponentReleaseDownload)
	mux.HandleFunc("GET /api/v1/presentations/{presentationId}", s.handlePresentationDownload)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.handleOAuthProtectedResource)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/{resourcePath...}", s.handleOAuthProtectedResource)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.handleOAuthAuthorizationServer)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server/{issuerPath...}", s.handleOAuthAuthorizationServer)
	mux.HandleFunc("POST /oauth/register", s.handleOAuthRegister)
	mux.HandleFunc("GET /oauth/authorize", s.handleOAuthAuthorize)
	mux.HandleFunc("POST /oauth/authorize", s.handleOAuthAuthorize)
	mux.HandleFunc("POST /oauth/token", oauthPostOnly(s.handleOAuthToken))
	mux.HandleFunc("POST /oauth/revoke", oauthPostOnly(s.handleOAuthRevoke))
	mux.HandleFunc("GET /direct/v1/tools", s.directAccessOnly(s.handleDirectTools))
	mux.HandleFunc("POST /direct/v1/call", s.directAccessOnly(s.handleDirectCall))
	mux.Handle("/mcp", s.newMCPHandler())
	mux.HandleFunc("GET /node/v1/connect", s.handleNodeConnect)
	mux.HandleFunc("POST /node/v1/presentations", s.handlePresentationUpload)
	mux.HandleFunc("POST /node/v1/artifacts", s.handleArtifactCreate)
	mux.HandleFunc("GET /node/v1/artifacts/{uploadId}", s.handleArtifactUploadStatus)
	mux.HandleFunc("PUT /node/v1/artifacts/{uploadId}/chunk", s.handleArtifactChunk)
	mux.HandleFunc("POST /node/v1/artifacts/{uploadId}/complete", s.handleArtifactComplete)
	mux.HandleFunc("DELETE /node/v1/artifacts/{uploadId}", s.handleArtifactAbort)
	s.http = &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           s.securityHeaders(s.hostGuard(mux)),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       75 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	return s
}

func (s *Server) Handler() http.Handler             { return s.http.Handler }
func (s *Server) Serve(listener net.Listener) error { return s.http.Serve(listener) }
func (s *Server) ListenAndServe() error             { return s.http.ListenAndServe() }

func (s *Server) Shutdown(ctx context.Context) error {
	for _, conn := range s.service.Registry().List() {
		s.service.Registry().CloseMachine(ctx, conn.MachineID, "HUB_SHUTDOWN", "hub is shutting down")
	}
	err := s.http.Shutdown(ctx)
	s.presentations.clear()
	return err
}

func (s *Server) handleLive(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "fast-spider-hub", "version": s.service.Version()})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.service.Store().Ping(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (s *Server) handleMachineRegister(w http.ResponseWriter, r *http.Request, ownerID string) {
	var req core.MachineRegistrationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.service.RegisterMachine(r.Context(), ownerID, req, remoteIP(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	var req core.DeviceTokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.service.IssueDeviceToken(r.Context(), req, remoteIP(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) connectionTokenOnly(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ownerID, err := s.authenticateConnectionTokenRequest(r)
		if err != nil {
			writeError(w, err)
			return
		}
		next(w, r, ownerID)
	}
}

func (s *Server) authenticateConnectionTokenRequest(r *http.Request) (string, error) {
	return s.service.AuthenticateConnectionToken(r.Context(), bearerToken(r.Header.Get("Authorization")))
}

func (s *Server) hostGuard(next http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(s.config.AllowedHosts))
	for _, host := range s.config.AllowedHosts {
		allowed[strings.ToLower(strings.TrimSpace(host))] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(allowed) != 0 {
			host := strings.ToLower(r.Host)
			if parsed, _, err := net.SplitHostPort(host); err == nil {
				host = parsed
			}
			if _, ok := allowed[host]; !ok {
				writeJSON(w, http.StatusMisdirectedRequest, apiError{Error: protocolv1.ProtocolError{Code: "HOST_NOT_ALLOWED", Message: "host is not allowed", Retryable: false}})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; script-src 'self'; style-src 'self'")
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxControlMessageBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: protocolv1.ProtocolError{Code: "INVALID_REQUEST", Message: "invalid JSON request", Retryable: false}})
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: protocolv1.ProtocolError{Code: "INVALID_REQUEST", Message: "request must contain exactly one valid JSON value", Retryable: false}})
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, err error) {
	status := core.ErrorStatus(err)
	message := "request failed"
	if core.IsClientError(err) {
		message = core.ErrorCode(err)
	}
	writeJSON(w, status, apiError{Error: protocolv1.ProtocolError{Code: core.ErrorCode(err), Message: message, Retryable: status >= 500}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

func remoteIP(r *http.Request) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	peerIP := net.ParseIP(strings.TrimSpace(peer))
	if peerIP == nil || !peerIP.IsLoopback() {
		return peer
	}
	for _, header := range []string{"CF-Connecting-IP", "X-Real-IP"} {
		if candidate := net.ParseIP(strings.TrimSpace(r.Header.Get(header))); candidate != nil {
			return candidate.String()
		}
	}
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
		if candidate := net.ParseIP(forwarded); candidate != nil {
			return candidate.String()
		}
	}
	return peer
}
