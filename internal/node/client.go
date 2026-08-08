package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/isguang2024/fast-spider/internal/security"
)

var ErrNotEnrolled = errors.New("node is not enrolled")

const maxHTTPResponseBytes = 1 << 20

type Config struct {
	DataDir       string
	Version       string
	AllowInsecure bool
	Logger        *slog.Logger
}

type Client struct {
	cfg        Config
	http       *http.Client
	publicKey  ed25519.PublicKey
	privateKey ed25519.PrivateKey
	statePath  string
	writeMu    sync.Mutex
	jobs       *JobManager
	requestSem chan struct{}
}

type enrollResponse struct {
	MachineID      string `json:"machineId"`
	CredentialID   string `json:"credentialId"`
	HubPublicKey   string `json:"hubPublicKey"`
	HubFingerprint string `json:"hubFingerprint"`
	AlreadyDone    bool   `json:"alreadyDone"`
}

type deviceTokenResponse struct {
	DeviceToken string    `json:"deviceToken"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type protocolAPIError struct {
	Error protocolv1.ProtocolError `json:"error"`
}

type HubAPIError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *HubAPIError) Error() string { return "hub " + e.Code + ": " + e.Message }

func New(cfg Config) (*Client, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	keyPath := filepath.Join(cfg.DataDir, "secrets", "node-ed25519.key")
	pub, priv, err := security.LoadOrCreateEd25519(keyPath)
	if err != nil {
		return nil, err
	}
	return &Client{
		cfg:        cfg,
		http:       &http.Client{Timeout: 20 * time.Second},
		publicKey:  pub,
		privateKey: priv,
		statePath:  filepath.Join(cfg.DataDir, "state.json"),
		jobs:       NewJobManager(cfg.DataDir),
		requestSem: make(chan struct{}, 8),
	}, nil
}

func (c *Client) State() (State, error) { return LoadState(c.statePath) }

func (c *Client) Enroll(ctx context.Context, hubURL, enrollmentToken, displayName string) (State, error) {
	normalized, err := c.normalizeHubURL(hubURL)
	if err != nil {
		return State{}, err
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return State{}, fmt.Errorf("display name is required")
	}
	idempotency := enrollmentIdempotency(enrollmentToken, security.EncodePublicKey(c.publicKey))
	payload := map[string]any{
		"enrollmentToken": enrollmentToken,
		"idempotencyKey": idempotency,
		"displayName": displayName,
		"os": runtime.GOOS,
		"arch": runtime.GOARCH,
		"nodeVersion": c.cfg.Version,
		"publicKey": security.EncodePublicKey(c.publicKey),
	}
	var response enrollResponse
	if err := c.postJSON(ctx, normalized+"/api/v1/enroll", "", payload, &response); err != nil {
		return State{}, err
	}
	hubPublic, err := security.DecodePublicKey(response.HubPublicKey)
	if err != nil {
		return State{}, fmt.Errorf("hub returned invalid public key: %w", err)
	}
	if security.Fingerprint(hubPublic) != response.HubFingerprint {
		return State{}, fmt.Errorf("hub public key fingerprint mismatch")
	}
	state := State{
		HubURL: normalized,
		MachineID: response.MachineID,
		CredentialID: response.CredentialID,
		HubPublicKey: response.HubPublicKey,
		HubFingerprint: response.HubFingerprint,
	}
	if err := SaveState(c.statePath, state); err != nil {
		return State{}, fmt.Errorf("save node state: %w", err)
	}
	return state, nil
}

func (c *Client) issueDeviceToken(ctx context.Context, state State) (deviceTokenResponse, error) {
	nonce, err := security.RandomOpaque("nonce_")
	if err != nil {
		return deviceTokenResponse{}, err
	}
	timestamp := protocolv1.Timestamp(time.Now())
	signature := ed25519.Sign(c.privateKey, protocolv1.DeviceTokenPayload(state.MachineID, nonce, timestamp))
	payload := map[string]any{
		"machineId": state.MachineID,
		"nonce": nonce,
		"timestamp": timestamp,
		"signature": security.EncodeSignature(signature),
	}
	var response deviceTokenResponse
	if err := c.postJSON(ctx, state.HubURL+"/api/v1/device/token", "", payload, &response); err != nil {
		return deviceTokenResponse{}, err
	}
	if response.DeviceToken == "" || !strings.HasPrefix(response.DeviceToken, "dev_") {
		return deviceTokenResponse{}, fmt.Errorf("hub returned invalid device token")
	}
	return response, nil
}

func (c *Client) postJSON(ctx context.Context, endpoint, bearer string, payload, output any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "fast-spider-node/"+c.cfg.Version)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
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
		return fmt.Errorf("hub response exceeds limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr protocolAPIError
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Code != "" {
			return &HubAPIError{Code: apiErr.Error.Code, Message: apiErr.Error.Message, Retryable: apiErr.Error.Retryable}
		}
		return fmt.Errorf("hub HTTP status %d", resp.StatusCode)
	}
	if output == nil {
		return nil
	}
	if err := json.Unmarshal(body, output); err != nil {
		return fmt.Errorf("decode hub response: %w", err)
	}
	return nil
}

func (c *Client) normalizeHubURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("parse hub URL: %w", err)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("hub URL must not contain credentials, query or fragment")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("hub URL requires a host")
	}
	if parsed.Scheme != "https" && !(c.cfg.AllowInsecure && parsed.Scheme == "http") {
		return "", fmt.Errorf("hub URL must use https; http is allowed only with --allow-insecure for local development")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return strings.TrimRight(parsed.String(), "/"), nil
}

func enrollmentIdempotency(token, publicKey string) string {
	sum := sha256.Sum256([]byte("fast-spider-enroll-v1\n" + token + "\n" + publicKey))
	return "idem_" + hex.EncodeToString(sum[:])
}
