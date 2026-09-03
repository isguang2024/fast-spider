package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/isguang2024/fast-spider/internal/hub/store"
)

type resultManifestResponse struct {
	ResultID    string          `json:"resultId"`
	Status      string          `json:"status"`
	Revision    int64           `json:"revision"`
	Manifest    json.RawMessage `json:"manifest,omitempty"`
	CreatedAt   string          `json:"createdAt"`
	CommittedAt string          `json:"committedAt,omitempty"`
	ExpiresAt   string          `json:"expiresAt"`
}

func (s *Server) resultAuthenticate(w http.ResponseWriter, r *http.Request) (store.DeviceSession, bool) {
	session, err := s.service.AuthenticateDevice(r.Context(), bearerToken(r.Header.Get("Authorization")))
	if err != nil {
		writeError(w, err)
		return store.DeviceSession{}, false
	}
	return session, true
}

func (s *Server) handleResultCreate(w http.ResponseWriter, r *http.Request) {
	session, ok := s.resultAuthenticate(w, r)
	if !ok {
		return
	}
	var req core.ResultCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.service.CreateResult(r.Context(), session, req)
	if err != nil {
		writeResultError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) handleResultLookup(w http.ResponseWriter, r *http.Request) {
	session, ok := s.resultAuthenticate(w, r)
	if !ok {
		return
	}
	rec, err := s.service.LookupResult(r.Context(), session.OwnerID, r.URL.Query().Get("idempotencyKey"), r.URL.Query().Get("requestHash"))
	if err != nil {
		writeResultError(w, err)
		return
	}
	if rec.MachineID != session.MachineID {
		writeResultError(w, store.ErrUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, resultManifestResponseFor(rec))
}

func (s *Server) handleResultAttachPage(w http.ResponseWriter, r *http.Request) {
	session, ok := s.resultAuthenticate(w, r)
	if !ok {
		return
	}
	var req core.ResultAttachPageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.service.AttachResultPage(r.Context(), session, r.PathValue("resultId"), req)
	if err != nil {
		writeResultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleResultCommit(w http.ResponseWriter, r *http.Request) {
	session, ok := s.resultAuthenticate(w, r)
	if !ok {
		return
	}
	var req core.ResultCommitRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.service.CommitResult(r.Context(), session, r.PathValue("resultId"), req)
	if err != nil {
		writeResultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleResultManifest(w http.ResponseWriter, r *http.Request) {
	session, ok := s.resultAuthenticate(w, r)
	if !ok {
		return
	}
	rec, err := s.service.GetResultManifest(r.Context(), session.OwnerID, r.PathValue("resultId"))
	if err != nil {
		writeResultError(w, err)
		return
	}
	if rec.MachineID != session.MachineID {
		writeResultError(w, store.ErrUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, resultManifestResponseFor(rec))
}

func (s *Server) handleResultReadPage(w http.ResponseWriter, r *http.Request) {
	session, ok := s.resultAuthenticate(w, r)
	if !ok {
		return
	}
	pageNo, err := strconv.Atoi(r.PathValue("pageNo"))
	if err != nil || pageNo < 0 {
		writeResultError(w, store.ErrConflict)
		return
	}
	page, file, err := s.service.ReadResultPage(r.Context(), session.OwnerID, r.PathValue("resultId"), pageNo)
	if err != nil {
		writeResultError(w, err)
		return
	}
	defer file.Close()
	if page.Artifact.MachineID != session.MachineID {
		writeResultError(w, store.ErrUnauthorized)
		return
	}
	w.Header().Set("Content-Type", page.Artifact.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(page.Artifact.SizeBytes, 10))
	_, _ = io.CopyN(w, file, page.Artifact.SizeBytes)
}

func (s *Server) handleResultAbort(w http.ResponseWriter, r *http.Request) {
	session, ok := s.resultAuthenticate(w, r)
	if !ok {
		return
	}
	revision, err := strconv.ParseInt(r.URL.Query().Get("expectedRevision"), 10, 64)
	if err != nil {
		writeResultError(w, store.ErrConflict)
		return
	}
	result, err := s.service.AbortResult(r.Context(), session, r.PathValue("resultId"), revision)
	if err != nil {
		writeResultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleResultFail(w http.ResponseWriter, r *http.Request) {
	session, ok := s.resultAuthenticate(w, r)
	if !ok {
		return
	}
	var req core.ResultFailRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.service.FailResult(r.Context(), session, r.PathValue("resultId"), req)
	if err != nil {
		writeResultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func resultManifestResponseFor(rec store.ResultRecord) resultManifestResponse {
	response := resultManifestResponse{ResultID: rec.ResultID, Status: rec.Status, Revision: rec.Revision, CreatedAt: rec.CreatedAt.Format(time.RFC3339Nano), ExpiresAt: rec.ExpiresAt.Format(time.RFC3339Nano)}
	if rec.ManifestJSON != "" {
		response.Manifest = json.RawMessage(rec.ManifestJSON)
	}
	if rec.CommittedAt != nil {
		response.CommittedAt = rec.CommittedAt.Format(time.RFC3339Nano)
	}
	return response
}

func writeResultError(w http.ResponseWriter, err error) { writeArtifactCoreError(w, err) }
