package core

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/registry"
	"github.com/isguang2024/fast-spider/internal/hub/store"
	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	"github.com/isguang2024/fast-spider/internal/security"
)

const (
	bootstrapTTL   = 30 * time.Minute
	enrollmentTTL  = 10 * time.Minute
	deviceTokenTTL = 24 * time.Hour
	maxClockSkew   = 5 * time.Minute
)

type Config struct {
	DataDir string
	Version string
}

type Service struct {
	store          *store.Store
	registry       *registry.Registry
	hubPublic      ed25519.PublicKey
	hubPrivate     ed25519.PrivateKey
	hubFingerprint string
	dataDir        string
	version        string
	now            func() time.Time
}

type BootstrapResult struct {
	OwnerID    string `json:"ownerId"`
	OwnerToken string `json:"ownerToken"`
}

type EnrollmentTokenResult struct {
	EnrollmentToken string    `json:"enrollmentToken"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

type EnrollRequest struct {
	EnrollmentToken string `json:"enrollmentToken"`
	IdempotencyKey  string `json:"idempotencyKey"`
	DisplayName     string `json:"displayName"`
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	NodeVersion     string `json:"nodeVersion"`
	PublicKey       string `json:"publicKey"`
}

type EnrollResult struct {
	MachineID      string `json:"machineId"`
	CredentialID   string `json:"credentialId"`
	HubPublicKey   string `json:"hubPublicKey"`
	HubFingerprint string `json:"hubFingerprint"`
	AlreadyDone    bool   `json:"alreadyDone"`
}

type DeviceTokenRequest struct {
	MachineID string `json:"machineId"`
	Nonce     string `json:"nonce"`
	Timestamp string `json:"timestamp"`
	Signature string `json:"signature"`
}

type DeviceTokenResult struct {
	DeviceToken string    `json:"deviceToken"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type CapabilityCallError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *CapabilityCallError) Error() string { return e.Code + ": " + e.Message }

type MachineView struct {
	MachineID     string                            `json:"machineId"`
	DisplayName   string                            `json:"displayName"`
	Status        string                            `json:"status"`
	Online        bool                              `json:"online"`
	RuntimeStatus string                            `json:"runtimeStatus,omitempty"`
	OS            string                            `json:"os"`
	Arch          string                            `json:"arch"`
	NodeVersion   string                            `json:"nodeVersion"`
	Generation    int64                             `json:"generation"`
	LastSeenAt    *time.Time                        `json:"lastSeenAt,omitempty"`
	Capabilities  []protocolv1.CapabilityDescriptor `json:"capabilities,omitempty"`
}

func New(st *store.Store, reg *registry.Registry, cfg Config) (*Service, error) {
	keyPath := filepath.Join(cfg.DataDir, "secrets", "hub-ed25519.key")
	pub, priv, err := security.LoadOrCreateEd25519(keyPath)
	if err != nil {
		return nil, err
	}
	return &Service{
		store:          st,
		registry:       reg,
		hubPublic:      pub,
		hubPrivate:     priv,
		hubFingerprint: security.Fingerprint(pub),
		dataDir:        cfg.DataDir,
		version:        cfg.Version,
		now:            time.Now,
	}, nil
}

func (s *Service) HubPublicKey() string              { return security.EncodePublicKey(s.hubPublic) }
func (s *Service) HubFingerprint() string            { return s.hubFingerprint }
func (s *Service) HubPrivateKey() ed25519.PrivateKey { return s.hubPrivate }
func (s *Service) Version() string                   { return s.version }
func (s *Service) Registry() *registry.Registry      { return s.registry }
func (s *Service) Store() *store.Store               { return s.store }
func (s *Service) DataDir() string                   { return s.dataDir }

func (s *Service) EnsureBootstrap(ctx context.Context) (string, error) {
	hasOwner, err := s.store.HasOwner(ctx)
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.dataDir, "bootstrap-token")
	if hasOwner {
		_ = os.Remove(path)
		return "", nil
	}
	token, err := security.RandomOpaque("bsp_")
	if err != nil {
		return "", err
	}
	id, err := security.RandomOpaque("bst_")
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	if _, err := s.store.EnsureBootstrapToken(ctx, id, security.HashToken(token), now, now.Add(bootstrapTTL)); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := atomicWritePrivate(path, []byte(token+"\n")); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) BootstrapOwner(ctx context.Context, bootstrapToken, displayName, remoteAddr string) (BootstrapResult, error) {
	displayName = strings.TrimSpace(displayName)
	if len(displayName) < 1 || len(displayName) > 128 || len(bootstrapToken) > 256 {
		return BootstrapResult{}, store.ErrUnauthorized
	}
	ownerID, err := security.RandomOpaque("usr_")
	if err != nil {
		return BootstrapResult{}, err
	}
	apiTokenID, err := security.RandomOpaque("tok_")
	if err != nil {
		return BootstrapResult{}, err
	}
	apiToken, err := security.RandomOpaque("own_")
	if err != nil {
		return BootstrapResult{}, err
	}
	now := s.now().UTC()
	if err := s.store.BootstrapOwner(ctx, security.HashToken(bootstrapToken), ownerID, displayName, apiTokenID, security.HashToken(apiToken), now); err != nil {
		return BootstrapResult{}, err
	}
	_ = os.Remove(filepath.Join(s.dataDir, "bootstrap-token"))
	_ = s.audit(ctx, store.AuditEntry{OwnerID: ownerID, ActorType: "bootstrap", ActorID: ownerID, Action: "owner.bootstrap", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now})
	return BootstrapResult{OwnerID: ownerID, OwnerToken: apiToken}, nil
}

func (s *Service) AuthenticateOwner(ctx context.Context, bearer string) (string, error) {
	if !strings.HasPrefix(bearer, "own_") || len(bearer) > 256 {
		return "", store.ErrUnauthorized
	}
	return s.store.AuthenticateOwnerToken(ctx, security.HashToken(bearer), s.now().UTC())
}

func (s *Service) CreateEnrollmentToken(ctx context.Context, ownerID, expectedName, expectedOS, remoteAddr string) (EnrollmentTokenResult, error) {
	if len(expectedName) > 128 || len(expectedOS) > 64 {
		return EnrollmentTokenResult{}, store.ErrConflict
	}
	token, err := security.RandomOpaque("enr_")
	if err != nil {
		return EnrollmentTokenResult{}, err
	}
	id, err := security.RandomOpaque("enrrec_")
	if err != nil {
		return EnrollmentTokenResult{}, err
	}
	now := s.now().UTC()
	expires := now.Add(enrollmentTTL)
	if err := s.store.CreateEnrollmentToken(ctx, id, ownerID, security.HashToken(token), strings.TrimSpace(expectedName), strings.TrimSpace(expectedOS), now, expires, 5); err != nil {
		return EnrollmentTokenResult{}, err
	}
	_ = s.audit(ctx, store.AuditEntry{OwnerID: ownerID, ActorType: "owner", ActorID: ownerID, Action: "enrollment.create", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now})
	return EnrollmentTokenResult{EnrollmentToken: token, ExpiresAt: expires}, nil
}

func (s *Service) Enroll(ctx context.Context, req EnrollRequest, remoteAddr string) (EnrollResult, error) {
	if err := validateEnroll(req); err != nil {
		return EnrollResult{}, err
	}
	pub, err := security.DecodePublicKey(req.PublicKey)
	if err != nil {
		return EnrollResult{}, store.ErrUnauthorized
	}
	machineID, err := security.RandomOpaque("mach_")
	if err != nil {
		return EnrollResult{}, err
	}
	credentialID, err := security.RandomOpaque("cred_")
	if err != nil {
		return EnrollResult{}, err
	}
	now := s.now().UTC()
	result, err := s.store.Enroll(ctx, store.EnrollmentInput{
		TokenHash: security.HashToken(req.EnrollmentToken), IdempotencyKey: req.IdempotencyKey,
		MachineID: machineID, CredentialID: credentialID, DisplayName: strings.TrimSpace(req.DisplayName),
		OS: strings.TrimSpace(req.OS), Arch: strings.TrimSpace(req.Arch), NodeVersion: strings.TrimSpace(req.NodeVersion),
		PublicKey: security.EncodePublicKey(pub), Fingerprint: security.Fingerprint(pub), Now: now,
	})
	if err != nil {
		return EnrollResult{}, err
	}
	_ = s.audit(ctx, store.AuditEntry{OwnerID: result.OwnerID, MachineID: result.MachineID, ActorType: "node", ActorID: result.MachineID, Action: "node.enroll", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now})
	return EnrollResult{
		MachineID: result.MachineID, CredentialID: result.CredentialID,
		HubPublicKey: s.HubPublicKey(), HubFingerprint: s.HubFingerprint(), AlreadyDone: result.AlreadyDone,
	}, nil
}

func (s *Service) IssueDeviceToken(ctx context.Context, req DeviceTokenRequest, remoteAddr string) (DeviceTokenResult, error) {
	if len(req.MachineID) > 128 || len(req.Nonce) < 16 || len(req.Nonce) > 256 || len(req.Signature) > 256 {
		return DeviceTokenResult{}, store.ErrUnauthorized
	}
	ts, err := time.Parse(time.RFC3339Nano, req.Timestamp)
	if err != nil {
		return DeviceTokenResult{}, store.ErrUnauthorized
	}
	now := s.now().UTC()
	if delta := now.Sub(ts); delta > maxClockSkew || delta < -maxClockSkew {
		return DeviceTokenResult{}, store.ErrExpired
	}
	identity, err := s.store.GetDeviceIdentity(ctx, req.MachineID)
	if err != nil {
		return DeviceTokenResult{}, err
	}
	pub, err := security.DecodePublicKey(identity.PublicKey)
	if err != nil {
		return DeviceTokenResult{}, store.ErrUnauthorized
	}
	sig, err := security.DecodeSignature(req.Signature)
	if err != nil || !ed25519.Verify(pub, protocolv1.DeviceTokenPayload(req.MachineID, req.Nonce, req.Timestamp), sig) {
		return DeviceTokenResult{}, store.ErrUnauthorized
	}
	token, err := security.RandomOpaque("dev_")
	if err != nil {
		return DeviceTokenResult{}, err
	}
	tokenID, err := security.RandomOpaque("dtok_")
	if err != nil {
		return DeviceTokenResult{}, err
	}
	nonceSum := sha256.Sum256([]byte(req.Nonce))
	expires := now.Add(deviceTokenTTL)
	if err := s.store.IssueDeviceToken(ctx, req.MachineID, identity.CredentialID, hex.EncodeToString(nonceSum[:]), tokenID, security.HashToken(token), now, expires); err != nil {
		return DeviceTokenResult{}, err
	}
	_ = s.audit(ctx, store.AuditEntry{OwnerID: identity.Machine.OwnerID, MachineID: req.MachineID, ActorType: "node", ActorID: req.MachineID, Action: "device_token.issue", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now})
	return DeviceTokenResult{DeviceToken: token, ExpiresAt: expires}, nil
}

func (s *Service) AuthenticateDevice(ctx context.Context, bearer string) (store.DeviceSession, error) {
	if !strings.HasPrefix(bearer, "dev_") || len(bearer) > 256 {
		return store.DeviceSession{}, store.ErrUnauthorized
	}
	return s.store.AuthenticateDeviceToken(ctx, security.HashToken(bearer), s.now().UTC())
}

func (s *Service) ListMachines(ctx context.Context, ownerID string) ([]MachineView, error) {
	records, err := s.store.ListMachines(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	views := make([]MachineView, 0, len(records))
	for _, rec := range records {
		view, err := s.machineView(ctx, rec)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Online != views[j].Online {
			return views[i].Online
		}
		return views[i].DisplayName < views[j].DisplayName
	})
	return views, nil
}

func (s *Service) GetMachine(ctx context.Context, ownerID, machineID string) (MachineView, error) {
	rec, err := s.store.GetMachine(ctx, ownerID, machineID)
	if err != nil {
		return MachineView{}, err
	}
	return s.machineView(ctx, rec)
}

func (s *Service) machineView(ctx context.Context, rec store.MachineRecord) (MachineView, error) {
	view := MachineView{
		MachineID: rec.ID, DisplayName: rec.DisplayName, Status: rec.Status,
		OS: rec.OS, Arch: rec.Arch, NodeVersion: rec.NodeVersion,
		Generation: rec.LastConnectionGeneration, LastSeenAt: rec.LastSeenAt,
	}
	if snap, ok := s.registry.Get(rec.ID); ok && rec.Status == "active" {
		view.Online = true
		view.RuntimeStatus = snap.Status
		view.Generation = snap.Generation
		last := snap.LastSeenAt.UTC()
		view.LastSeenAt = &last
		view.Capabilities = snap.Capabilities
		return view, nil
	}
	caps, err := s.store.Capabilities(ctx, rec.ID)
	if err != nil {
		return MachineView{}, err
	}
	view.Capabilities = caps
	return view, nil
}

func (s *Service) RevokeMachine(ctx context.Context, ownerID, machineID, remoteAddr string) error {
	now := s.now().UTC()
	if err := s.store.RevokeMachine(ctx, ownerID, machineID, now); err != nil {
		return err
	}
	s.registry.CloseMachine(ctx, machineID, "MACHINE_REVOKED", "machine was revoked by owner")
	_ = s.audit(ctx, store.AuditEntry{OwnerID: ownerID, MachineID: machineID, ActorType: "owner", ActorID: ownerID, Action: "machine.revoke", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now})
	return nil
}

func (s *Service) CapabilityCatalog() []protocolv1.CapabilityDescriptor {
	out := make([]protocolv1.CapabilityDescriptor, len(protocolv1.NodeCapabilities), len(protocolv1.NodeCapabilities)+2)
	copy(out, protocolv1.NodeCapabilities)
	out = append(out, protocolv1.ScreenshotCapability, protocolv1.BrowserCapability)
	return out
}

func (s *Service) CallCapability(ctx context.Context, ownerID, machineID, workspaceID, capability, action string, params any) (map[string]any, error) {
	record, err := s.store.GetMachine(ctx, ownerID, machineID)
	if err != nil {
		return nil, err
	}
	if record.Status != "active" {
		return nil, &CapabilityCallError{Code: "MACHINE_INACTIVE", Message: "machine is not active", Retryable: false}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var normalized map[string]any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return nil, err
	}
	requestID, err := security.RandomOpaque("req_")
	if err != nil {
		return nil, err
	}
	deadline := s.now().UTC().Add(capabilityCallTimeout(capability, action))
	if current, ok := ctx.Deadline(); ok && current.Before(deadline) {
		deadline = current
	}
	callCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	response, err := s.registry.Call(callCtx, machineID, protocolv1.CapabilityRequest{
		MessageType: protocolv1.MessageCapabilityRequest,
		RequestId:   requestID,
		Capability:  capability,
		Action:      action,
		WorkspaceId: workspaceID,
		Params:      normalized,
		Deadline:    protocolv1.Timestamp(deadline),
		Timestamp:   protocolv1.Timestamp(s.now()),
	})
	if err != nil {
		if errors.Is(err, registry.ErrMachineOffline) {
			return nil, &CapabilityCallError{Code: "MACHINE_OFFLINE", Message: "machine is offline", Retryable: true}
		}
		return nil, err
	}
	if response.Error != nil {
		if shouldAuditCapability(capability, action) {
			_ = s.audit(ctx, store.AuditEntry{OwnerID: ownerID, MachineID: machineID, ActorType: "owner", ActorID: ownerID, Action: capability + "." + action, Result: "rejected", Detail: map[string]any{"workspaceId": workspaceID, "errorCode": response.Error.Code}, CreatedAt: s.now().UTC()})
		}
		return nil, &CapabilityCallError{Code: response.Error.Code, Message: response.Error.Message, Retryable: response.Error.Retryable}
	}
	if shouldAuditCapability(capability, action) {
		_ = s.audit(ctx, store.AuditEntry{OwnerID: ownerID, MachineID: machineID, ActorType: "owner", ActorID: ownerID, Action: capability + "." + action, Result: "success", Detail: map[string]any{"workspaceId": workspaceID}, CreatedAt: s.now().UTC()})
	}
	return response.Result, nil
}

func capabilityCallTimeout(capability, action string) time.Duration {
	switch capability + "/" + action {
	case "artifact.store/uploadFile", "artifact.store/uploadJobLog":
		return 10 * time.Minute
	case "git.repository/diff", "git.repository/stagedDiff", "git.repository/show":
		return 5 * time.Minute
	case "browser.automation/launch", "browser.automation/close", "browser.automation/page.open", "browser.automation/page.navigate", "browser.automation/click", "browser.automation/type", "browser.automation/press", "browser.automation/wait", "browser.automation/snapshot", "browser.automation/screenshot":
		return 2 * time.Minute
	case "screenshot.capture/desktop", "screenshot.capture/display", "screenshot.capture/window":
		return 2 * time.Minute
	case "agent.control/session.create", "agent.control/session.send":
		return 2 * time.Minute
	case "agent.control/session.watch", "agent.control/session.cancel":
		return 30 * time.Second
	case "job.control/watch":
		return 30 * time.Second
	default:
		return 20 * time.Second
	}
}

func shouldAuditCapability(capability, action string) bool {
	switch capability + "/" + action {
	case "file.write/edit", "shell.exec/run", "job.control/cancel", "git.repository/add", "git.repository/commit", "git.repository/fetch", "git.repository/pull", "git.repository/push", "git.repository/createWorktree", "git.repository/deleteWorktree", "build.profile/run", "browser.automation/launch", "browser.automation/close", "browser.automation/page.open", "browser.automation/page.navigate", "browser.automation/click", "browser.automation/type", "browser.automation/press", "browser.automation/screenshot", "screenshot.capture/desktop", "screenshot.capture/display", "screenshot.capture/window", "agent.control/session.create", "agent.control/session.send", "agent.control/session.cancel", "agent.control/session.rename", "agent.control/session.archive":
		return true
	default:
		return false
	}
}

func (s *Service) StartMaintenance(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.registry.CloseStale(ctx, now.Add(-95*time.Second))
			_ = s.store.CleanupExpired(ctx, now.UTC())
			s.cleanupArtifacts(ctx, now.UTC())
		}
	}
}

func (s *Service) cleanupArtifacts(ctx context.Context, now time.Time) {
	uploadIDs, storageKeys, err := s.store.CleanupArtifacts(ctx, now)
	if err != nil {
		return
	}
	for _, uploadID := range uploadIDs {
		_ = os.Remove(filepath.Join(s.dataDir, "artifacts", "uploads", uploadID+".part"))
	}
	blobRoot := filepath.Join(s.dataDir, "artifacts", "blobs")
	for _, storageKey := range storageKeys {
		clean := filepath.Clean(filepath.FromSlash(storageKey))
		if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			continue
		}
		_ = os.Remove(filepath.Join(blobRoot, clean))
	}
}

func (s *Service) audit(ctx context.Context, entry store.AuditEntry) error {
	id, err := security.RandomOpaque("aud_")
	if err != nil {
		return err
	}
	entry.ID = id
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = s.now().UTC()
	}
	return s.store.AppendAudit(ctx, entry)
}

func validateEnroll(req EnrollRequest) error {
	if !strings.HasPrefix(req.EnrollmentToken, "enr_") || len(req.EnrollmentToken) > 256 {
		return store.ErrUnauthorized
	}
	if len(req.IdempotencyKey) < 12 || len(req.IdempotencyKey) > 128 {
		return store.ErrConflict
	}
	if name := strings.TrimSpace(req.DisplayName); len(name) < 1 || len(name) > 128 {
		return store.ErrConflict
	}
	if osName := strings.TrimSpace(req.OS); len(osName) < 1 || len(osName) > 64 {
		return store.ErrConflict
	}
	if arch := strings.TrimSpace(req.Arch); len(arch) < 1 || len(arch) > 64 {
		return store.ErrConflict
	}
	if version := strings.TrimSpace(req.NodeVersion); len(version) < 1 || len(version) > 64 {
		return store.ErrConflict
	}
	return nil
}

func atomicWritePrivate(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func IsClientError(err error) bool {
	var callErr *CapabilityCallError
	return errors.As(err, &callErr) || errors.Is(err, store.ErrUnauthorized) || errors.Is(err, store.ErrExpired) || errors.Is(err, store.ErrConsumed) || errors.Is(err, store.ErrRevoked) || errors.Is(err, store.ErrReplay) || errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound)
}

func ErrorCode(err error) string {
	var callErr *CapabilityCallError
	if errors.As(err, &callErr) {
		return callErr.Code
	}
	switch {
	case errors.Is(err, store.ErrUnauthorized):
		return "UNAUTHORIZED"
	case errors.Is(err, store.ErrExpired):
		return "EXPIRED"
	case errors.Is(err, store.ErrConsumed):
		return "TOKEN_CONSUMED"
	case errors.Is(err, store.ErrRevoked):
		return "REVOKED"
	case errors.Is(err, store.ErrReplay):
		return "REPLAY_DETECTED"
	case errors.Is(err, store.ErrConflict):
		return "CONFLICT"
	case errors.Is(err, store.ErrNotFound):
		return "NOT_FOUND"
	default:
		return "INTERNAL"
	}
}

func ErrorStatus(err error) int {
	var callErr *CapabilityCallError
	if errors.As(err, &callErr) {
		switch callErr.Code {
		case "MACHINE_OFFLINE":
			return 503
		case "DEADLINE_EXCEEDED", "TIMEOUT":
			return 504
		case "PERMISSION_DENIED":
			return 403
		case "NOT_FOUND":
			return 404
		case "INVALID_REQUEST":
			return 400
		default:
			return 409
		}
	}
	switch {
	case errors.Is(err, store.ErrUnauthorized):
		return 401
	case errors.Is(err, store.ErrExpired), errors.Is(err, store.ErrConsumed), errors.Is(err, store.ErrRevoked), errors.Is(err, store.ErrReplay), errors.Is(err, store.ErrConflict):
		return 409
	case errors.Is(err, store.ErrNotFound):
		return 404
	default:
		return 500
	}
}

func (s *Service) String() string {
	return fmt.Sprintf("Fast Spider Hub %s (%s)", s.version, s.hubFingerprint)
}
