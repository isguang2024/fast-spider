package adminclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/core"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
)

const maxResponseBytes = 2 << 20

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

type APIError struct {
	Error protocolv1.ProtocolError `json:"error"`
}

type BootstrapResponse struct {
	OwnerID    string `json:"ownerId"`
	OwnerToken string `json:"ownerToken"`
}

type EnrollmentResponse struct {
	EnrollmentToken string    `json:"enrollmentToken"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

type MachineListResponse struct {
	Machines []core.MachineView `json:"machines"`
}

func New(rawURL, token string, allowInsecure bool) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("parse hub URL: %w", err)
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("hub URL must be an absolute URL without credentials, query or fragment")
	}
	if parsed.Scheme != "https" && !(allowInsecure && parsed.Scheme == "http") {
		return nil, fmt.Errorf("hub URL must use https; use --allow-insecure only for local development")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return &Client{
		baseURL: strings.TrimRight(parsed.String(), "/"),
		token:   token,
		http:    &http.Client{Timeout: 20 * time.Second},
	}, nil
}

func (c *Client) Bootstrap(ctx context.Context, bootstrapToken, displayName string) (BootstrapResponse, error) {
	var out BootstrapResponse
	err := c.request(ctx, http.MethodPost, "/api/v1/bootstrap", map[string]any{
		"bootstrapToken": bootstrapToken,
		"displayName":    displayName,
	}, &out, false)
	return out, err
}

func (c *Client) CreateEnrollment(ctx context.Context, expectedName, expectedOS string) (EnrollmentResponse, error) {
	var out EnrollmentResponse
	err := c.request(ctx, http.MethodPost, "/api/v1/enrollment-tokens", map[string]any{
		"expectedName": expectedName,
		"expectedOs":   expectedOS,
	}, &out, true)
	return out, err
}

func (c *Client) ListMachines(ctx context.Context) ([]core.MachineView, error) {
	var out MachineListResponse
	if err := c.request(ctx, http.MethodGet, "/api/v1/machines", nil, &out, true); err != nil {
		return nil, err
	}
	return out.Machines, nil
}

func (c *Client) GetMachine(ctx context.Context, machineID string) (core.MachineView, error) {
	var out core.MachineView
	err := c.request(ctx, http.MethodGet, "/api/v1/machines/"+url.PathEscape(machineID), nil, &out, true)
	return out, err
}

func (c *Client) RevokeMachine(ctx context.Context, machineID string) error {
	return c.request(ctx, http.MethodPost, "/api/v1/machines/"+url.PathEscape(machineID)+"/revoke", map[string]any{}, nil, true)
}

func (c *Client) request(ctx context.Context, method, path string, payload, output any, authenticated bool) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		if c.token == "" {
			return fmt.Errorf("owner token is required")
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxResponseBytes {
		return fmt.Errorf("hub response exceeds limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr APIError
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error.Code != "" {
			return fmt.Errorf("hub %s: %s", apiErr.Error.Code, apiErr.Error.Message)
		}
		return fmt.Errorf("hub returned HTTP %d", resp.StatusCode)
	}
	if output == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, output); err != nil {
		return fmt.Errorf("decode hub response: %w", err)
	}
	return nil
}
