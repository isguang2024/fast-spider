package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	"github.com/isguang2024/fast-spider/internal/hub/store"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

const maxInlineArtifactRead = 128 << 10

func (s *Server) handleArtifactCreate(w http.ResponseWriter, r *http.Request) {
	session, err := s.service.AuthenticateDevice(r.Context(), bearerToken(r.Header.Get("Authorization")))
	if err != nil {
		writeError(w, err)
		return
	}
	var req core.ArtifactCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.service.CreateArtifactUpload(r.Context(), session, req)
	if err != nil {
		writeArtifactCoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) handleArtifactUploadStatus(w http.ResponseWriter, r *http.Request) {
	session, err := s.service.AuthenticateDevice(r.Context(), bearerToken(r.Header.Get("Authorization")))
	if err != nil {
		writeError(w, err)
		return
	}
	upload, err := s.service.Store().GetArtifactUpload(r.Context(), session.MachineID, r.PathValue("uploadId"), time.Now().UTC())
	if err != nil {
		writeArtifactCoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"uploadId": upload.ID, "artifactId": upload.ArtifactID, "receivedBytes": upload.ReceivedSize,
		"expectedBytes": upload.ExpectedSize, "expiresAt": upload.ExpiresAt,
	})
}

func (s *Server) handleArtifactChunk(w http.ResponseWriter, r *http.Request) {
	session, err := s.service.AuthenticateDevice(r.Context(), bearerToken(r.Header.Get("Authorization")))
	if err != nil {
		writeError(w, err)
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		writeArtifactError(w, http.StatusConflict, "OFFSET_CONFLICT", "invalid artifact upload offset", false)
		return
	}
	chunk, err := io.ReadAll(io.LimitReader(r.Body, int64(core.MaxArtifactChunkBytes)+1))
	if err != nil {
		writeArtifactError(w, http.StatusBadRequest, "INVALID_CHUNK", "failed to read artifact chunk", true)
		return
	}
	if len(chunk) == 0 || len(chunk) > core.MaxArtifactChunkBytes {
		writeArtifactError(w, http.StatusBadRequest, "INVALID_CHUNK", "artifact chunk is empty or too large", false)
		return
	}
	received, err := s.service.UploadArtifactChunk(r.Context(), session.MachineID, r.PathValue("uploadId"), offset, chunk)
	if err != nil {
		writeArtifactCoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uploadId": r.PathValue("uploadId"), "receivedBytes": received})
}

func (s *Server) handleArtifactComplete(w http.ResponseWriter, r *http.Request) {
	session, err := s.service.AuthenticateDevice(r.Context(), bearerToken(r.Header.Get("Authorization")))
	if err != nil {
		writeError(w, err)
		return
	}
	artifact, err := s.service.CompleteArtifactUpload(r.Context(), session, r.PathValue("uploadId"))
	if err != nil {
		writeArtifactCoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, artifact)
}

func (s *Server) handleArtifactAbort(w http.ResponseWriter, r *http.Request) {
	session, err := s.service.AuthenticateDevice(r.Context(), bearerToken(r.Header.Get("Authorization")))
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.service.AbortArtifactUpload(r.Context(), session, r.PathValue("uploadId")); err != nil {
		writeArtifactCoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uploadId": r.PathValue("uploadId"), "status": "aborted"})
}

func (s *Server) handleArtifactMetadata(w http.ResponseWriter, r *http.Request, ownerID string) {
	artifact, err := s.service.GetArtifact(r.Context(), ownerID, r.PathValue("artifactId"))
	if err != nil {
		writeArtifactCoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, artifact)
}

func (s *Server) handleArtifactContent(w http.ResponseWriter, r *http.Request, ownerID string) {
	artifact, file, err := s.service.OpenArtifact(r.Context(), ownerID, r.PathValue("artifactId"))
	if err != nil {
		writeArtifactCoreError(w, err)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", artifact.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.SizeBytes, 10))
	w.Header().Set("ETag", `"`+strings.TrimPrefix(artifact.SHA256, "sha256:")+`"`)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", strings.ReplaceAll(artifact.LogicalName, "\"", "")))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, file)
}

func readArtifactInline(ctx context.Context, service *core.Service, artifact store.ArtifactRecord) (string, bool, error) {
	if artifact.SizeBytes > maxInlineArtifactRead || !isInlineArtifactType(artifact.ContentType) {
		return "", false, nil
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	_, file, err := service.OpenArtifact(ctx, artifact.OwnerID, artifact.ID)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxInlineArtifactRead+1))
	if err != nil {
		return "", false, err
	}
	if len(raw) > maxInlineArtifactRead || !utf8.Valid(raw) {
		return "", false, nil
	}
	return string(raw), true, nil
}

func isInlineArtifactType(contentType string) bool {
	lower := strings.ToLower(contentType)
	return strings.HasPrefix(lower, "text/") || strings.HasPrefix(lower, "application/json") || strings.HasPrefix(lower, "application/xml")
}

func writeArtifactCoreError(w http.ResponseWriter, err error) {
	var capabilityErr *core.CapabilityCallError
	if errors.As(err, &capabilityErr) {
		status := http.StatusConflict
		if capabilityErr.Retryable {
			status = http.StatusServiceUnavailable
		}
		writeArtifactError(w, status, capabilityErr.Code, capabilityErr.Message, capabilityErr.Retryable)
		return
	}
	writeError(w, err)
}

func writeArtifactError(w http.ResponseWriter, status int, code, message string, retryable bool) {
	writeJSON(w, status, apiError{Error: protocolv1.ProtocolError{Code: code, Message: message, Retryable: retryable}})
}
