package server

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/store"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/isguang2024/fast-spider/internal/security"
)

func (s *Server) handleNodeConnect(w http.ResponseWriter, r *http.Request) {
	session, err := s.service.AuthenticateDevice(r.Context(), bearerToken(r.Header.Get("Authorization")))
	if err != nil {
		writeError(w, err)
		return
	}
	if requested := r.URL.Query().Get("machine_id"); requested == "" || requested != session.MachineID {
		writeError(w, store.ErrUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		s.config.Logger.Warn("node websocket accept failed", "machineId", session.MachineID, "error", err)
		return
	}
	conn.SetReadLimit(maxControlMessageBytes)
	defer conn.CloseNow()
	// coder/websocket takes over the HTTP connection after Accept. Do not use
	// the request context for the WebSocket lifetime; it may be canceled by
	// net/http independently of the upgraded connection.
	wsCtx, cancelWS := context.WithCancel(context.Background())
	defer cancelWS()

	challenge, err := security.RandomOpaque("chl_")
	if err != nil {
		return
	}
	now := time.Now().UTC()
	serverSignature := ed25519.Sign(s.service.HubPrivateKey(), protocolv1.ServerChallengePayload(session.MachineID, challenge))
	serverHello := protocolv1.ServerHello{
		MessageType: protocolv1.MessageServerHello,
		ProtocolVersions: []string{protocolv1.ProtocolVersion},
		Challenge: challenge,
		HubPublicKey: s.service.HubPublicKey(),
		HubFingerprint: s.service.HubFingerprint(),
		ServerSignature: security.EncodeSignature(serverSignature),
		Timestamp: protocolv1.Timestamp(now),
	}
	writeCtx, cancel := context.WithTimeout(wsCtx, 10*time.Second)
	err = wsjson.Write(writeCtx, conn, serverHello)
	cancel()
	if err != nil {
		return
	}

	var clientHello protocolv1.ClientHello
	readCtx, cancel := context.WithTimeout(wsCtx, 10*time.Second)
	err = wsjson.Read(readCtx, conn, &clientHello)
	cancel()
	if err != nil || s.verifyClientHello(session.MachineID, session.PublicKey, challenge, clientHello) != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "invalid client hello")
		return
	}

	generation, err := s.service.Store().NextGeneration(wsCtx, session.MachineID, time.Now().UTC())
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "machine inactive")
		return
	}
	connectionID, err := security.RandomOpaque("conn_")
	if err != nil {
		return
	}
	registered := registry.NewConnection(session.MachineID, connectionID, generation, time.Now().UTC(), conn)
	replaced, accepted := s.service.Registry().Register(registered)
	if !accepted {
		_ = conn.Close(websocket.StatusPolicyViolation, "stale connection generation")
		return
	}
	defer s.service.Registry().Remove(session.MachineID, generation)
	closeReplaced(replaced)

	s.service.Registry().SetCapabilities(session.MachineID, generation, clientHello.Capabilities)
	if err := s.service.Store().ReplaceCapabilities(wsCtx, session.MachineID, clientHello.Capabilities, time.Now().UTC()); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "capability persistence failed")
		return
	}
	if err := s.service.Store().TouchMachine(wsCtx, session.MachineID, clientHello.Os, clientHello.Arch, clientHello.NodeVersion, capabilityDigest(clientHello.Capabilities), time.Now().UTC()); err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "machine inactive")
		return
	}

	if err := registered.WriteJSON(wsCtx, protocolv1.SessionEstablished{
		MessageType: protocolv1.MessageSessionEstablished,
		ConnectionId: connectionID,
		Generation: generation,
		ProtocolVersion: protocolv1.ProtocolVersion,
		HeartbeatSeconds: 30,
		Timestamp: protocolv1.Timestamp(time.Now()),
	}); err != nil {
		return
	}

	s.config.Logger.Info("node connected", "machineId", session.MachineID, "generation", generation, "remote", remoteIP(r))
	s.nodeReadLoop(wsCtx, registered, clientHello)
	s.config.Logger.Info("node disconnected", "machineId", session.MachineID, "generation", generation)
}

func (s *Server) verifyClientHello(machineID, encodedPublicKey, challenge string, hello protocolv1.ClientHello) error {
	if hello.MessageType != protocolv1.MessageClientHello || hello.MachineId != machineID {
		return errors.New("invalid client hello identity")
	}
	if !contains(hello.ProtocolVersions, protocolv1.ProtocolVersion) {
		return errors.New("no common protocol version")
	}
	if len(hello.Capabilities) > 128 || len(hello.NodeVersion) > 64 || len(hello.Os) > 64 || len(hello.Arch) > 64 {
		return errors.New("client hello exceeds limits")
	}
	if ts, err := time.Parse(time.RFC3339Nano, hello.Timestamp); err != nil || absDuration(time.Since(ts)) > 5*time.Minute {
		return errors.New("client hello timestamp outside allowed skew")
	}
	pub, err := security.DecodePublicKey(encodedPublicKey)
	if err != nil {
		return err
	}
	sig, err := security.DecodeSignature(hello.ChallengeSignature)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, protocolv1.DeviceChallengePayload(machineID, challenge), sig) {
		return errors.New("challenge signature invalid")
	}
	return nil
}

func (s *Server) nodeReadLoop(ctx context.Context, conn *registry.Connection, hello protocolv1.ClientHello) {
	lastPersist := time.Now().UTC()
	for {
		var raw json.RawMessage
		if err := conn.ReadJSON(ctx, &raw); err != nil {
			return
		}
		messageType, err := protocolv1.MessageType(raw)
		if err != nil {
			_ = conn.Close(websocket.StatusPolicyViolation, "invalid message envelope")
			return
		}
		now := time.Now().UTC()
		switch messageType {
		case protocolv1.MessageNodeReady:
			var ready protocolv1.NodeReady
			if err := json.Unmarshal(raw, &ready); err != nil || !validRuntimeStatus(ready.Status) {
				_ = conn.Close(websocket.StatusPolicyViolation, "invalid node.ready")
				return
			}
			if !s.service.Registry().Touch(conn.MachineID, conn.Generation, ready.Status, now) {
				return
			}
		case protocolv1.MessageHeartbeat:
			var heartbeat protocolv1.Heartbeat
			if err := json.Unmarshal(raw, &heartbeat); err != nil || !validRuntimeStatus(heartbeat.Status) {
				_ = conn.Close(websocket.StatusPolicyViolation, "invalid heartbeat")
				return
			}
			if !s.service.Registry().Touch(conn.MachineID, conn.Generation, heartbeat.Status, now) {
				return
			}
			if now.Sub(lastPersist) >= time.Minute {
				_ = s.service.Store().TouchMachine(ctx, conn.MachineID, hello.Os, hello.Arch, hello.NodeVersion, capabilityDigest(hello.Capabilities), now)
				lastPersist = now
			}
			if err := conn.WriteJSON(ctx, protocolv1.Heartbeat{
				MessageType: protocolv1.MessageHeartbeatAck,
				Sequence: heartbeat.Sequence,
				Status: heartbeat.Status,
				Timestamp: protocolv1.Timestamp(now),
			}); err != nil {
				return
			}
		case protocolv1.MessageCapabilityResponse:
			var response protocolv1.CapabilityResponse
			if err := json.Unmarshal(raw, &response); err != nil || response.RequestId == "" {
				_ = conn.Close(websocket.StatusPolicyViolation, "invalid capability.response")
				return
			}
			if !conn.DeliverResponse(response) {
				s.config.Logger.Warn("unmatched capability response", "machineId", conn.MachineID, "requestId", response.RequestId)
			}
		default:
			_ = conn.Close(websocket.StatusUnsupportedData, "message type not available")
			return
		}
	}
}

func closeReplaced(conn *registry.Connection) {
	if conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = conn.WriteJSON(ctx, protocolv1.ConnectionClose{
		MessageType: protocolv1.MessageConnectionClose,
		Code: "CONNECTION_REPLACED",
		Reason: "a newer device connection became active",
		Timestamp: protocolv1.Timestamp(time.Now()),
	})
	_ = conn.Close(websocket.StatusNormalClosure, "connection replaced")
}

func capabilityDigest(capabilities []protocolv1.CapabilityDescriptor) string {
	raw, _ := json.Marshal(capabilities)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func validRuntimeStatus(status string) bool { return status == "ready" || status == "busy" }

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}
