package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
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

	"github.com/isguang2024/fast-spider/internal/operationlog"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/isguang2024/fast-spider/internal/security"
)

var ErrNotRegistered = errors.New("node is not registered")

const maxHTTPResponseBytes = 1 << 20

type ConnectionStatus struct {
	State string
	Error string
}

type Config struct {
	DataDir       string
	Version       string
	DisplayName   string
	AllowInsecure bool
	// ProjectRoot enables the default open-source project boundary. An empty
	// value preserves the original machine mode and its OS-account boundary.
	ProjectRoot       string
	BrowserSidecarDir string
	Agent             AgentController
	// AgentCallerOwned keeps the injected Agent under the caller's ownership.
	// When false, Client.Run closes Agent before it returns.
	AgentCallerOwned bool
	Logger           *slog.Logger
	OperationLog     *operationlog.Store
	ConnectionStatus func(ConnectionStatus)
	ReleaseNotice    func(*Client, string, string)
}

type Client struct {
	cfg            Config
	http           *http.Client
	publicKey      ed25519.PublicKey
	privateKey     ed25519.PrivateKey
	windowTokenKey [32]byte
	statePath      string
	writeMu        sync.Mutex
	activityMu     sync.Mutex
	releaseDrain   bool
	jobs           *JobManager
	browser        *BrowserManager
	agent          AgentController
	requestSem     chan struct{}
	screenshotSem  chan struct{}
	operationLog   *operationlog.Store
	projectPolicy  *projectPolicy
}

type machineRegistrationResponse struct {
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
	projectPolicy, err := newProjectPolicy(cfg.ProjectRoot)
	if err != nil {
		return nil, err
	}
	keyPath := filepath.Join(cfg.DataDir, "secrets", "node-ed25519.key")
	pub, priv, err := security.LoadOrCreateEd25519(keyPath)
	if err != nil {
		return nil, err
	}
	client := &Client{
		cfg:            cfg,
		http:           &http.Client{Timeout: 20 * time.Second},
		publicKey:      pub,
		privateKey:     priv,
		windowTokenKey: windowTokenKey(priv),
		statePath:      filepath.Join(cfg.DataDir, "state.json"),
		jobs:           NewJobManager(cfg.DataDir),
		requestSem:     make(chan struct{}, 8),
		screenshotSem:  make(chan struct{}, 1),
		agent:          cfg.Agent,
		operationLog:   cfg.OperationLog,
		projectPolicy:  projectPolicy,
	}
	client.browser = NewBrowserManager(cfg.DataDir, cfg.BrowserSidecarDir, cfg.Logger)
	if setter, ok := cfg.Agent.(interface{ SetCloudResultPublisher(any) }); ok {
		setter.SetCloudResultPublisher(client)
	}
	return client, nil
}

// ProjectRoot returns the resolved project boundary, or an empty string when
// this Node is running in the original machine mode.
func (c *Client) ProjectRoot() string {
	if c.projectPolicy == nil {
		return ""
	}
	return c.projectPolicy.root
}

func (c *Client) State() (State, error) { return LoadState(c.statePath) }

func (c *Client) reportConnectionStatus(state string, err error) {
	if c.cfg.ConnectionStatus == nil {
		return
	}
	status := ConnectionStatus{State: state}
	if err != nil {
		status.Error = err.Error()
	}
	c.cfg.ConnectionStatus(status)
}

func (c *Client) Capabilities() []protocolv1.CapabilityDescriptor {
	out := make([]protocolv1.CapabilityDescriptor, len(protocolv1.NodeCapabilities), len(protocolv1.NodeCapabilities)+2)
	copy(out, protocolv1.NodeCapabilities)
	out = append(out, protocolv1.ScreenshotCapabilityForOS(runtime.GOOS))
	if c.browser != nil {
		out = append(out, protocolv1.BrowserCapability)
	}
	return out
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
		"nonce":     nonce,
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
