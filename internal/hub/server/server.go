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
	ListenAddr   string
	AllowedHosts []string
	Logger       *slog.Logger
}

type Server struct {
	service *core.Service
	config  Config
	http    *http.Server
}

type apiError struct {
	Error protocolv1.ProtocolError `json:"error"`
}

type bootstrapRequest struct {
	BootstrapToken string `json:"bootstrapToken"`
	DisplayName    string `json:"displayName"`
}

type enrollmentTokenRequest struct {
	ExpectedName string `json:"expectedName"`
	ExpectedOS   string `json:"expectedOs"`
}

func New(service *core.Service, cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Server{service: service, config: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", s.handleLive)
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.HandleFunc("POST /api/v1/bootstrap", s.handleBootstrap)
	mux.HandleFunc("POST /api/v1/enrollment-tokens", s.ownerOnly(s.handleCreateEnrollment))
	mux.HandleFunc("POST /api/v1/enroll", s.handleEnroll)
	mux.HandleFunc("POST /api/v1/device/token", s.handleDeviceToken)
	mux.HandleFunc("GET /api/v1/machines", s.ownerOnly(s.handleMachineList))
	mux.HandleFunc("GET /api/v1/machines/{machineId}", s.ownerOnly(s.handleMachineGet))
	mux.HandleFunc("POST /api/v1/machines/{machineId}/revoke", s.ownerOnly(s.handleMachineRevoke))
	mux.HandleFunc("GET /api/v1/artifacts/{artifactId}", s.ownerOnly(s.handleArtifactMetadata))
	mux.HandleFunc("GET /api/v1/artifacts/{artifactId}/content", s.ownerOnly(s.handleArtifactContent))
	mux.Handle("/mcp", s.newMCPHandler())
	mux.HandleFunc("GET /node/v1/connect", s.handleNodeConnect)
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

func (s *Server) Handler() http.Handler { return s.http.Handler }
func (s *Server) Serve(listener net.Listener) error { return s.http.Serve(listener) }
func (s *Server) ListenAndServe() error { return s.http.ListenAndServe() }

func (s *Server) Shutdown(ctx context.Context) error {
	for _, conn := range s.service.Registry().List() {
		s.service.Registry().CloseMachine(ctx, conn.MachineID, "HUB_SHUTDOWN", "hub is shutting down")
	}
	return s.http.Shutdown(ctx)
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

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	var req bootstrapRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.service.BootstrapOwner(r.Context(), req.BootstrapToken, req.DisplayName, remoteIP(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) handleCreateEnrollment(w http.ResponseWriter, r *http.Request, ownerID string) {
	var req enrollmentTokenRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.service.CreateEnrollmentToken(r.Context(), ownerID, req.ExpectedName, req.ExpectedOS, remoteIP(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	var req core.EnrollRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.service.Enroll(r.Context(), req, remoteIP(r))
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

func (s *Server) handleMachineList(w http.ResponseWriter, r *http.Request, ownerID string) {
	machines, err := s.service.ListMachines(r.Context(), ownerID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"machines": machines})
}

func (s *Server) handleMachineGet(w http.ResponseWriter, r *http.Request, ownerID string) {
	machine, err := s.service.GetMachine(r.Context(), ownerID, r.PathValue("machineId"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, machine)
}

func (s *Server) handleMachineRevoke(w http.ResponseWriter, r *http.Request, ownerID string) {
	if err := s.service.RevokeMachine(r.Context(), ownerID, r.PathValue("machineId"), remoteIP(r)); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked"})
}

func (s *Server) ownerOnly(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ownerID, err := s.authenticateOwnerRequest(r)
		if err != nil {
			writeError(w, err)
			return
		}
		next(w, r, ownerID)
	}
}

func (s *Server) authenticateOwnerRequest(r *http.Request) (string, error) {
	return s.service.AuthenticateOwner(r.Context(), bearerToken(r.Header.Get("Authorization")))
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
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
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
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
