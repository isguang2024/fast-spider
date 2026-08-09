package node

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/security"
)

const deviceProbeTimeout = 10 * time.Second

// Connect registers this Node with a Hub by using an Owner-created connection
// token once. The token is never persisted; successful registration stores only
// the device identity needed for later signed WSS connections.
func (c *Client) Connect(ctx context.Context, hubURL, token, displayName string) (State, error) {
	normalized, err := c.normalizeHubURL(hubURL)
	if err != nil {
		return State{}, err
	}
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, "ctk_") || len(token) > 256 {
		return State{}, errors.New("invalid connection token")
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return State{}, errors.New("display name is required")
	}

	if state, stateErr := c.State(); stateErr == nil && state.MachineID != "" && state.HubURL == normalized {
		probeCtx, probeCancel := context.WithTimeout(ctx, deviceProbeTimeout)
		_, probeErr := c.issueDeviceToken(probeCtx, state)
		probeCancel()
		if probeErr == nil {
			return State{}, errors.New("node is already connected to this Hub; use run to reconnect")
		}
		var hubErr *HubAPIError
		if !errors.As(probeErr, &hubErr) || (hubErr.Code != "REVOKED" && hubErr.Code != "NOT_FOUND" && hubErr.Code != "UNAUTHORIZED") {
			return State{}, fmt.Errorf("verify existing node registration before connect: %w", probeErr)
		}
	}

	payload := map[string]any{
		"displayName": displayName,
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
		"nodeVersion": c.cfg.Version,
		"publicKey":   security.EncodePublicKey(c.publicKey),
	}
	var response machineRegistrationResponse
	if err := c.postJSON(ctx, normalized+"/api/v1/machines/register", token, payload, &response); err != nil {
		return State{}, err
	}
	if response.MachineID == "" || response.CredentialID == "" {
		return State{}, errors.New("hub returned an invalid machine registration")
	}
	hubPublic, err := security.DecodePublicKey(response.HubPublicKey)
	if err != nil {
		return State{}, fmt.Errorf("hub returned invalid public key: %w", err)
	}
	if security.Fingerprint(hubPublic) != response.HubFingerprint {
		return State{}, errors.New("hub public key fingerprint mismatch")
	}
	state := State{
		HubURL:         normalized,
		MachineID:      response.MachineID,
		CredentialID:   response.CredentialID,
		HubPublicKey:   response.HubPublicKey,
		HubFingerprint: response.HubFingerprint,
	}
	if err := SaveState(c.statePath, state); err != nil {
		return State{}, fmt.Errorf("save node state: %w", err)
	}
	return state, nil
}
