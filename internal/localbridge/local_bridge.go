package localbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/isguang2024/fast-spider/internal/security"
)

const (
	maxLocalBridgeMessageBytes = 2 << 20
	localBridgeRequestTimeout  = 10 * time.Minute
	maxLocalBridgeConnections  = 8
)

var ErrUnavailable = errors.New("local bridge unavailable")

type localBridgeServer struct {
	handler Handler
	logger  *slog.Logger
	sem     chan struct{}
}

type Handler func(context.Context, protocolv1.CapabilityRequest) protocolv1.CapabilityResponse

func Run(ctx context.Context, dataDir string, handler Handler) error {
	server := &localBridgeServer{
		handler: handler,
		logger:  slog.Default(),
		sem:     make(chan struct{}, maxLocalBridgeConnections),
	}
	return runLocalBridgeServer(ctx, dataDir, server.serveConnection)
}

func (s *localBridgeServer) serveConnection(parent context.Context, conn io.ReadWriteCloser) {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-parent.Done():
		_ = conn.Close()
		return
	}
	defer conn.Close()

	connectionID, err := security.RandomOpaque("lconn_")
	if err != nil {
		return
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), maxLocalBridgeMessageBytes+1)
	writer := bufio.NewWriterSize(conn, 64*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		response := s.handleLine(parent, connectionID, line)
		if err := writeLocalBridgeResponse(writer, response); err != nil {
			return
		}
	}
	if err := scanner.Err(); err != nil && parent.Err() == nil {
		s.logger.Debug("local bridge connection ended", "connectionId", connectionID, "error", err)
	}
}

func (s *localBridgeServer) handleLine(parent context.Context, connectionID string, raw []byte) protocolv1.CapabilityResponse {
	now := time.Now().UTC()
	fallback := protocolv1.CapabilityResponse{
		MessageType: protocolv1.MessageCapabilityResponse,
		Timestamp:   protocolv1.Timestamp(now),
	}
	if len(raw) == 0 || len(raw) > maxLocalBridgeMessageBytes {
		fallback.Error = protocolError("INVALID_REQUEST", "local bridge request exceeds the allowed size", false)
		return fallback
	}
	var req protocolv1.CapabilityRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		fallback.Error = protocolError("INVALID_REQUEST", "invalid local bridge request", false)
		return fallback
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		fallback.Error = protocolError("INVALID_REQUEST", "local bridge request must contain one JSON object", false)
		return fallback
	}
	if req.MessageType != "" && req.MessageType != protocolv1.MessageCapabilityRequest {
		fallback.Error = protocolError("INVALID_REQUEST", "unsupported local bridge message type", false)
		return fallback
	}
	if strings.TrimSpace(req.RequestId) == "" {
		requestID, err := security.RandomOpaque("lreq_")
		if err != nil {
			fallback.Error = protocolError("INTERNAL", "failed to allocate local request ID", true)
			return fallback
		}
		req.RequestId = requestID
	}
	fallback.RequestId = req.RequestId
	req.MessageType = protocolv1.MessageCapabilityRequest
	if req.Timestamp == "" {
		req.Timestamp = protocolv1.Timestamp(now)
	}
	if req.Deadline == "" {
		req.Deadline = protocolv1.Timestamp(now.Add(localBridgeRequestTimeout))
	}
	if req.Params == nil {
		req.Params = map[string]any{}
	}

	callCtx, cancel := context.WithTimeout(parent, localBridgeRequestTimeout)
	defer cancel()
	if s.handler == nil {
		fallback.Error = protocolError("INTERNAL", "local bridge handler is unavailable", true)
		return fallback
	}
	response := s.handler(callCtx, req)
	s.logger.Debug("local bridge capability call",
		"connectionId", connectionID,
		"requestId", req.RequestId,
		"workspaceId", req.WorkspaceId,
		"capability", req.Capability,
		"action", req.Action,
		"ok", response.Error == nil,
	)
	return response
}

func writeLocalBridgeResponse(writer *bufio.Writer, response protocolv1.CapabilityResponse) error {
	raw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if len(raw) > maxLocalBridgeMessageBytes {
		return fmt.Errorf("local bridge response exceeds limit")
	}
	if _, err := writer.Write(raw); err != nil {
		return err
	}
	if err := writer.WriteByte('\n'); err != nil {
		return err
	}
	return writer.Flush()
}

func Call(ctx context.Context, dataDir string, request protocolv1.CapabilityRequest) (protocolv1.CapabilityResponse, error) {
	conn, err := dialLocalBridge(ctx, dataDir)
	if err != nil {
		return protocolv1.CapabilityResponse{}, err
	}
	defer conn.Close()
	callDone := make(chan struct{})
	defer close(callDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-callDone:
		}
	}()
	if request.MessageType == "" {
		request.MessageType = protocolv1.MessageCapabilityRequest
	}
	if request.Params == nil {
		request.Params = map[string]any{}
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return protocolv1.CapabilityResponse{}, err
	}
	if len(raw) > maxLocalBridgeMessageBytes {
		return protocolv1.CapabilityResponse{}, fmt.Errorf("local bridge request exceeds limit")
	}
	if _, err := conn.Write(append(raw, '\n')); err != nil {
		return protocolv1.CapabilityResponse{}, err
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), maxLocalBridgeMessageBytes+1)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return protocolv1.CapabilityResponse{}, err
		}
		return protocolv1.CapabilityResponse{}, io.EOF
	}
	var response protocolv1.CapabilityResponse
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
		return protocolv1.CapabilityResponse{}, fmt.Errorf("decode local bridge response: %w", err)
	}
	return response, nil
}

func protocolError(code, message string, retryable bool) *protocolv1.ProtocolError {
	return &protocolv1.ProtocolError{Code: code, Message: message, Retryable: retryable}
}
