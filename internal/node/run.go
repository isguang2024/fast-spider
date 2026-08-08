package node

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"runtime"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/isguang2024/fast-spider/internal/security"
)

func (c *Client) Run(ctx context.Context) error {
	go c.jobs.StartMaintenance(ctx)
	if c.browser != nil {
		go c.browser.StartMaintenance(ctx)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := c.jobs.CancelAll(shutdownCtx); err != nil {
			c.cfg.Logger.Error("node job shutdown incomplete", "error", err)
		}
		if c.browser != nil {
			if err := c.browser.Close(shutdownCtx); err != nil {
				c.cfg.Logger.Error("browser shutdown incomplete", "error", err)
			}
		}
	}()
	state, err := c.State()
	if err != nil {
		return err
	}
	normalizedHubURL, err := c.normalizeHubURL(state.HubURL)
	if err != nil {
		return fmt.Errorf("validate enrolled hub URL: %w", err)
	}
	state.HubURL = normalizedHubURL

	backoff := 500 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := c.runSession(ctx, state)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.cfg.Logger.Warn("node connection ended", "machineId", state.MachineID, "error", err)

		delay := jitter(backoff, 0.20)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func (c *Client) runSession(ctx context.Context, state State) error {
	tokenCtx, cancelToken := context.WithTimeout(ctx, 15*time.Second)
	token, err := c.issueDeviceToken(tokenCtx, state)
	cancelToken()
	if err != nil {
		return fmt.Errorf("issue device token: %w", err)
	}

	endpoint, err := websocketURL(state.HubURL, state.MachineID)
	if err != nil {
		return err
	}
	header := make(http.Header)
	header.Set("Authorization", "Bearer "+token.DeviceToken)
	header.Set("User-Agent", "fast-spider-node/"+c.cfg.Version)
	dialCtx, cancelDial := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.Dial(dialCtx, endpoint, &websocket.DialOptions{
		HTTPHeader:      header,
		CompressionMode: websocket.CompressionDisabled,
	})
	cancelDial()
	if err != nil {
		return fmt.Errorf("connect hub websocket: %w", err)
	}
	conn.SetReadLimit(1 << 20)
	defer conn.CloseNow()

	var serverHello protocolv1.ServerHello
	helloCtx, cancelHello := context.WithTimeout(ctx, 10*time.Second)
	err = wsjson.Read(helloCtx, conn, &serverHello)
	cancelHello()
	if err != nil {
		return fmt.Errorf("read server hello: %w", err)
	}
	if err := c.verifyServerHello(state, serverHello); err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "hub identity verification failed")
		return err
	}

	clientHello := protocolv1.ClientHello{
		MessageType:        protocolv1.MessageClientHello,
		MachineId:          state.MachineID,
		ProtocolVersions:   []string{protocolv1.ProtocolVersion},
		ChallengeSignature: security.EncodeSignature(ed25519.Sign(c.privateKey, protocolv1.DeviceChallengePayload(state.MachineID, serverHello.Challenge))),
		Capabilities:       c.Capabilities(),
		NodeVersion:        c.cfg.Version,
		Os:                 runtime.GOOS,
		Arch:               runtime.GOARCH,
		Timestamp:          protocolv1.Timestamp(time.Now()),
	}
	writeCtx, cancelWrite := context.WithTimeout(ctx, 10*time.Second)
	err = c.writeJSON(writeCtx, conn, clientHello)
	cancelWrite()
	if err != nil {
		return fmt.Errorf("write client hello: %w", err)
	}

	var established protocolv1.SessionEstablished
	establishedCtx, cancelEstablished := context.WithTimeout(ctx, 10*time.Second)
	err = wsjson.Read(establishedCtx, conn, &established)
	cancelEstablished()
	if err != nil {
		return fmt.Errorf("read session established: %w", err)
	}
	if established.MessageType != protocolv1.MessageSessionEstablished || established.ProtocolVersion != protocolv1.ProtocolVersion || established.Generation < 1 {
		return fmt.Errorf("hub returned invalid session establishment")
	}
	heartbeatInterval := time.Duration(established.HeartbeatSeconds) * time.Second
	if heartbeatInterval < 5*time.Second || heartbeatInterval > 5*time.Minute {
		return fmt.Errorf("hub heartbeat interval outside allowed range")
	}

	readyCtx, cancelReady := context.WithTimeout(ctx, 10*time.Second)
	err = c.writeJSON(readyCtx, conn, protocolv1.NodeReady{
		MessageType: protocolv1.MessageNodeReady,
		Status:      "ready",
		Timestamp:   protocolv1.Timestamp(time.Now()),
	})
	cancelReady()
	if err != nil {
		return fmt.Errorf("send node ready: %w", err)
	}

	c.cfg.Logger.Info("node connected", "machineId", state.MachineID, "generation", established.Generation)
	return c.heartbeatLoop(ctx, conn, heartbeatInterval)
}

func (c *Client) heartbeatLoop(ctx context.Context, conn *websocket.Conn, interval time.Duration) error {
	ack := make(chan time.Time, 1)
	readErr := make(chan error, 1)
	go func() {
		for {
			var raw json.RawMessage
			if err := wsjson.Read(ctx, conn, &raw); err != nil {
				readErr <- err
				return
			}
			messageType, err := protocolv1.MessageType(raw)
			if err != nil {
				readErr <- err
				return
			}
			switch messageType {
			case protocolv1.MessageHeartbeatAck:
				var heartbeat protocolv1.Heartbeat
				if err := json.Unmarshal(raw, &heartbeat); err != nil {
					readErr <- err
					return
				}
				select {
				case ack <- time.Now():
				default:
				}
			case protocolv1.MessageCapabilityRequest:
				var request protocolv1.CapabilityRequest
				if err := json.Unmarshal(raw, &request); err != nil {
					readErr <- err
					return
				}
				select {
				case c.requestSem <- struct{}{}:
					go func(request protocolv1.CapabilityRequest) {
						defer func() { <-c.requestSem }()
						response := c.handleCapabilityRequest(ctx, request)
						writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
						err := c.writeJSON(writeCtx, conn, response)
						cancel()
						if err != nil {
							select {
							case readErr <- err:
							default:
							}
						}
					}(request)
				default:
					response := protocolv1.CapabilityResponse{
						MessageType: protocolv1.MessageCapabilityResponse,
						RequestId:   request.RequestId,
						Error:       &protocolv1.ProtocolError{Code: "RESOURCE_LIMIT", Message: "too many concurrent capability requests", Retryable: true},
						Timestamp:   protocolv1.Timestamp(time.Now()),
					}
					writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
					err := c.writeJSON(writeCtx, conn, response)
					cancel()
					if err != nil {
						readErr <- err
						return
					}
				}
			case protocolv1.MessageConnectionClose:
				var closed protocolv1.ConnectionClose
				if err := json.Unmarshal(raw, &closed); err != nil {
					readErr <- err
					return
				}
				readErr <- fmt.Errorf("hub closed connection: %s: %s", closed.Code, closed.Reason)
				return
			default:
				readErr <- fmt.Errorf("unexpected phase 1 message type %q", messageType)
				return
			}
		}
	}()

	lastAck := time.Now()
	sequence := int64(0)
	timer := time.NewTimer(jitter(interval, 0.10))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = conn.Close(websocket.StatusNormalClosure, "node stopping")
			return ctx.Err()
		case when := <-ack:
			lastAck = when
		case err := <-readErr:
			return err
		case <-timer.C:
			if time.Since(lastAck) > 3*interval+5*time.Second {
				return errors.New("heartbeat acknowledgements timed out")
			}
			sequence++
			writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.writeJSON(writeCtx, conn, protocolv1.Heartbeat{
				MessageType: protocolv1.MessageHeartbeat,
				Sequence:    sequence,
				Status:      "ready",
				Timestamp:   protocolv1.Timestamp(time.Now()),
			})
			cancel()
			if err != nil {
				return fmt.Errorf("send heartbeat: %w", err)
			}
			timer.Reset(jitter(interval, 0.10))
		}
	}
}

func (c *Client) verifyServerHello(state State, hello protocolv1.ServerHello) error {
	if hello.MessageType != protocolv1.MessageServerHello || !containsProtocol(hello.ProtocolVersions, protocolv1.ProtocolVersion) {
		return errors.New("hub protocol is incompatible")
	}
	if hello.HubPublicKey != state.HubPublicKey || hello.HubFingerprint != state.HubFingerprint {
		return errors.New("hub identity changed; explicit re-enrollment is required")
	}
	publicKey, err := security.DecodePublicKey(hello.HubPublicKey)
	if err != nil {
		return err
	}
	if security.Fingerprint(publicKey) != hello.HubFingerprint {
		return errors.New("hub fingerprint does not match public key")
	}
	signature, err := security.DecodeSignature(hello.ServerSignature)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, protocolv1.ServerChallengePayload(state.MachineID, hello.Challenge), signature) {
		return errors.New("hub challenge signature is invalid")
	}
	if ts, err := time.Parse(time.RFC3339Nano, hello.Timestamp); err != nil || absNodeDuration(time.Since(ts)) > 5*time.Minute {
		return errors.New("hub timestamp outside allowed clock skew")
	}
	return nil
}

func websocketURL(hubURL, machineID string) (string, error) {
	parsed, err := url.Parse(hubURL)
	if err != nil {
		return "", err
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported hub scheme %q", parsed.Scheme)
	}
	parsed.Path = stringsTrimRightSlash(parsed.Path) + "/node/v1/connect"
	query := parsed.Query()
	query.Set("machine_id", machineID)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func containsProtocol(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func stringsTrimRightSlash(value string) string {
	for len(value) > 0 && value[len(value)-1] == '/' {
		value = value[:len(value)-1]
	}
	return value
}

func jitter(base time.Duration, fraction float64) time.Duration {
	if base <= 0 || fraction <= 0 {
		return base
	}
	spread := float64(base) * fraction
	delta := (rand.Float64()*2 - 1) * spread
	value := time.Duration(float64(base) + delta)
	if value < 100*time.Millisecond {
		return 100 * time.Millisecond
	}
	return value
}

func (c *Client) writeJSON(ctx context.Context, conn *websocket.Conn, value any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return wsjson.Write(ctx, conn, value)
}

func absNodeDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}
