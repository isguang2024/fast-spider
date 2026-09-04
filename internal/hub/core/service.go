package core

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	deviceTokenTTL = 24 * time.Hour
	maxClockSkew   = 5 * time.Minute
)

type Config struct {
	DataDir    string
	ReleaseDir string
	Version    string
}

type Service struct {
	store          *store.Store
	registry       *registry.Registry
	hubPublic      ed25519.PublicKey
	hubPrivate     ed25519.PrivateKey
	hubFingerprint string
	dataDir        string
	releaseDir     string
	version        string
	now            func() time.Time
	artifactLocks  artifactLifecycleLocks
	artifactRemove func(string) error
}

type MachineRegistrationRequest struct {
	DisplayName string `json:"displayName"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	NodeVersion string `json:"nodeVersion"`
	PublicKey   string `json:"publicKey"`
}

type MachineRegistrationResult struct {
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
	Details   map[string]any
}

func (e *CapabilityCallError) Error() string {
	message := e.Code + ": " + e.Message
	if len(e.Details) > 0 {
		raw, _ := json.Marshal(e.Details)
		message += " " + string(raw)
	}
	return message
}

type MachineView struct {
	MachineID            string                            `json:"machineId"`
	DisplayName          string                            `json:"displayName"`
	AdminNote            string                            `json:"adminNote,omitempty"`
	Status               string                            `json:"status"`
	Online               bool                              `json:"online"`
	RuntimeStatus        string                            `json:"runtimeStatus,omitempty"`
	OS                   string                            `json:"os"`
	Arch                 string                            `json:"arch"`
	NodeVersion          string                            `json:"nodeVersion"`
	Generation           int64                             `json:"generation"`
	LastSeenAt           *time.Time                        `json:"lastSeenAt,omitempty"`
	RegistrationMode     string                            `json:"registrationMode"`
	ConfigurationScope   string                            `json:"configurationScope"`
	RuntimeCredential    string                            `json:"runtimeCredential"`
	ConnectionTokenSaved bool                              `json:"connectionTokenSaved"`
	Capabilities         []protocolv1.CapabilityDescriptor `json:"capabilities,omitempty"`
}

func New(st *store.Store, reg *registry.Registry, cfg Config) (*Service, error) {
	keyPath := filepath.Join(cfg.DataDir, "secrets", "hub-ed25519.key")
	pub, priv, err := security.LoadOrCreateEd25519(keyPath)
	if err != nil {
		return nil, err
	}
	releaseDir := strings.TrimSpace(cfg.ReleaseDir)
	if releaseDir == "" {
		releaseDir = cfg.DataDir + "-releases"
	}
	return &Service{
		store:          st,
		registry:       reg,
		hubPublic:      pub,
		hubPrivate:     priv,
		hubFingerprint: security.Fingerprint(pub),
		dataDir:        cfg.DataDir,
		releaseDir:     releaseDir,
		version:        cfg.Version,
		now:            time.Now,
		artifactRemove: os.Remove,
	}, nil
}

func (s *Service) HubPublicKey() string              { return security.EncodePublicKey(s.hubPublic) }
func (s *Service) HubFingerprint() string            { return s.hubFingerprint }
func (s *Service) HubPrivateKey() ed25519.PrivateKey { return s.hubPrivate }
func (s *Service) Version() string                   { return s.version }
func (s *Service) Registry() *registry.Registry      { return s.registry }
func (s *Service) Store() *store.Store               { return s.store }
func (s *Service) DataDir() string                   { return s.dataDir }
func (s *Service) ReleaseDir() string                { return s.releaseDir }

func (s *Service) EnsureBootstrap(ctx context.Context) (string, error) {
	hasOwnerAccount, err := s.store.HasOwnerAccount(ctx)
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.dataDir, "bootstrap-token")
	if hasOwnerAccount {
		_ = os.Remove(path)
		return "", nil
	}
	hasOwner, err := s.store.HasOwner(ctx)
	if err != nil {
		return "", err
	}
	if hasOwner {
		return "", errors.New("owner exists without complete web credentials; manual data repair is required")
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

func (s *Service) AuthenticateConnectionToken(ctx context.Context, bearer string) (string, error) {
	if !strings.HasPrefix(bearer, "ctk_") || len(bearer) > 256 {
		return "", store.ErrUnauthorized
	}
	return s.store.AuthenticateConnectionToken(ctx, security.HashToken(bearer), s.now().UTC())
}

func (s *Service) RegisterMachine(ctx context.Context, ownerID string, req MachineRegistrationRequest, remoteAddr string) (MachineRegistrationResult, error) {
	if err := validateMachineRegistration(req); err != nil {
		return MachineRegistrationResult{}, err
	}
	pub, err := security.DecodePublicKey(req.PublicKey)
	if err != nil {
		return MachineRegistrationResult{}, store.ErrUnauthorized
	}
	machineID, err := security.RandomOpaque("mach_")
	if err != nil {
		return MachineRegistrationResult{}, err
	}
	credentialID, err := security.RandomOpaque("cred_")
	if err != nil {
		return MachineRegistrationResult{}, err
	}
	now := s.now().UTC()
	result, err := s.store.RegisterMachine(ctx, store.MachineRegistrationInput{
		MachineID: machineID, CredentialID: credentialID, OwnerID: ownerID,
		DisplayName: strings.TrimSpace(req.DisplayName), OS: strings.TrimSpace(req.OS),
		Arch: strings.TrimSpace(req.Arch), NodeVersion: strings.TrimSpace(req.NodeVersion),
		PublicKey: security.EncodePublicKey(pub), Fingerprint: security.Fingerprint(pub), Now: now,
	})
	if err != nil {
		return MachineRegistrationResult{}, err
	}
	_ = s.audit(ctx, store.AuditEntry{OwnerID: result.OwnerID, MachineID: result.MachineID, ActorType: "node", ActorID: result.MachineID, Action: "node.register", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now})
	return MachineRegistrationResult{
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
	capabilities, err := s.store.CapabilitiesByOwner(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	views := make([]MachineView, 0, len(records))
	for _, rec := range records {
		views = append(views, s.machineViewWithCapabilities(rec, capabilities[rec.ID]))
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Online != views[j].Online {
			return views[i].Online
		}
		return views[i].DisplayName < views[j].DisplayName
	})
	return views, nil
}

func (s *Service) ListMachinesPage(ctx context.Context, ownerID string, offset, limit int, includeCapabilities bool) ([]MachineView, bool, error) {
	if offset < 0 || limit < 1 || limit > 50 {
		return nil, false, fmt.Errorf("invalid machine page offset or limit")
	}
	records, err := s.store.ListMachinesPage(ctx, ownerID, offset, limit+1)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	views := make([]MachineView, 0, len(records))
	for _, rec := range records {
		var view MachineView
		if includeCapabilities {
			view, err = s.machineView(ctx, rec)
			if err != nil {
				return nil, false, err
			}
		} else {
			view = s.machineViewWithCapabilities(rec, nil)
			view.Capabilities = nil
		}
		views = append(views, view)
	}
	return views, hasMore, nil
}

func (s *Service) GetMachine(ctx context.Context, ownerID, machineID string) (MachineView, error) {
	rec, err := s.store.GetMachine(ctx, ownerID, machineID)
	if err != nil {
		return MachineView{}, err
	}
	return s.machineView(ctx, rec)
}

func (s *Service) machineView(ctx context.Context, rec store.MachineRecord) (MachineView, error) {
	caps, err := s.store.Capabilities(ctx, rec.ID)
	if err != nil {
		return MachineView{}, err
	}
	return s.machineViewWithCapabilities(rec, caps), nil
}

func (s *Service) machineViewWithCapabilities(rec store.MachineRecord, capabilities []protocolv1.CapabilityDescriptor) MachineView {
	view := MachineView{
		MachineID: rec.ID, DisplayName: rec.DisplayName, AdminNote: rec.AdminNote, Status: rec.Status,
		OS: rec.OS, Arch: rec.Arch, NodeVersion: rec.NodeVersion,
		Generation: rec.LastConnectionGeneration, LastSeenAt: rec.LastSeenAt,
		RegistrationMode: "connection_token", ConfigurationScope: "local_node",
		RuntimeCredential: "device_key", ConnectionTokenSaved: false,
		Capabilities: capabilities,
	}
	if snap, ok := s.registry.Get(rec.ID); ok && rec.Status == "active" {
		view.Online = true
		view.RuntimeStatus = snap.Status
		view.Generation = snap.Generation
		last := snap.LastSeenAt.UTC()
		view.LastSeenAt = &last
		view.Capabilities = snap.Capabilities
	}
	return view
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

func (s *Service) UpdateMachineAdminNote(ctx context.Context, ownerID, machineID, adminNote, remoteAddr string) error {
	adminNote = strings.TrimSpace(adminNote)
	if len([]byte(adminNote)) > 128 {
		return store.ErrConflict
	}
	now := s.now().UTC()
	if err := s.store.UpdateMachineAdminNote(ctx, ownerID, machineID, adminNote, now); err != nil {
		return err
	}
	_ = s.audit(ctx, store.AuditEntry{OwnerID: ownerID, MachineID: machineID, ActorType: "owner", ActorID: ownerID, Action: "machine.admin_note.update", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now})
	return nil
}

func (s *Service) DeleteMachine(ctx context.Context, ownerID, machineID, remoteAddr string) error {
	now := s.now().UTC()
	if err := s.store.DeleteMachine(ctx, ownerID, machineID, now); err != nil {
		return err
	}
	_ = s.audit(ctx, store.AuditEntry{OwnerID: ownerID, MachineID: machineID, ActorType: "owner", ActorID: ownerID, Action: "machine.delete", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now})
	return nil
}

func (s *Service) CapabilityCatalog() []protocolv1.CapabilityDescriptor {
	out := make([]protocolv1.CapabilityDescriptor, len(protocolv1.NodeCapabilities), len(protocolv1.NodeCapabilities)+2)
	copy(out, protocolv1.NodeCapabilities)
	out = append(out, protocolv1.ScreenshotCapability, protocolv1.BrowserCapability)
	return out
}

func (s *Service) CallCapability(ctx context.Context, ownerID, machineID, capability, action string, params any) (map[string]any, error) {
	started := time.Now()
	record, err := s.store.GetMachine(ctx, ownerID, machineID)
	if err != nil {
		return nil, capabilityCallTransportError(err, capability, action)
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
	traceID, err := security.RandomOpaque("tr_")
	if err != nil {
		return nil, err
	}
	operationDeadline, responseDeadline := capabilityCallDeadlines(s.now().UTC(), ctx, capability, action)
	callCtx, cancel := context.WithDeadline(ctx, responseDeadline)
	defer cancel()
	dispatchStarted := time.Now()
	response, err := s.registry.Call(callCtx, machineID, protocolv1.CapabilityRequest{
		MessageType: protocolv1.MessageCapabilityRequest,
		RequestId:   requestID,
		TraceId:     traceID,
		Capability:  capability,
		Action:      action,
		Params:      normalized,
		Deadline:    protocolv1.Timestamp(operationDeadline),
		Timestamp:   protocolv1.Timestamp(s.now()),
	})
	nodeReturnedAt := time.Now()
	if err != nil {
		return nil, capabilityCallTransportError(err, capability, action)
	}
	if response.Error != nil {
		if shouldAuditCapability(capability, action) {
			_ = s.audit(ctx, store.AuditEntry{OwnerID: ownerID, MachineID: machineID, ActorType: "owner", ActorID: ownerID, Action: capability + "." + action, Result: "rejected", Detail: map[string]any{"errorCode": response.Error.Code}, CreatedAt: s.now().UTC()})
		}
		return nil, &CapabilityCallError{Code: response.Error.Code, Message: response.Error.Message, Retryable: response.Error.Retryable, Details: response.Error.Details}
	}
	if shouldAuditCapability(capability, action) {
		_ = s.audit(ctx, store.AuditEntry{OwnerID: ownerID, MachineID: machineID, ActorType: "owner", ActorID: ownerID, Action: capability + "." + action, Result: "success", CreatedAt: s.now().UTC()})
	}
	if response.Result == nil {
		response.Result = map[string]any{}
	}
	attachCapabilityCallMetadata(response.Result, requestID, traceID)
	timing, _ := response.Result["timing"].(map[string]any)
	if timing == nil {
		timing = map[string]any{}
	}
	timing["hubPreDispatchMs"] = dispatchStarted.Sub(started).Milliseconds()
	timing["nodeRoundTripMs"] = nodeReturnedAt.Sub(dispatchStarted).Milliseconds()
	timing["hubTotalMs"] = time.Since(started).Milliseconds()
	response.Result["timing"] = timing
	return response.Result, nil
}

func attachCapabilityCallMetadata(result map[string]any, requestID, traceID string) {
	jobRequestID, hasJobRequestID := result["requestId"].(string)
	jobTraceID, hasJobTraceID := result["traceId"].(string)
	if hasJobRequestID && strings.TrimSpace(jobRequestID) != "" {
		result["callRequestId"] = requestID
	} else {
		result["requestId"] = requestID
	}
	if hasJobTraceID && strings.TrimSpace(jobTraceID) != "" {
		result["callTraceId"] = traceID
	} else {
		result["traceId"] = traceID
	}
}

func capabilityCallTransportError(err error, capability, action string) error {
	retryable := isRetryableCapability(capability, action)
	switch {
	case errors.Is(err, registry.ErrMachineOffline):
		return &CapabilityCallError{Code: "MACHINE_OFFLINE", Message: "machine is offline", Retryable: true}
	case errors.Is(err, registry.ErrConnectionLost):
		return &CapabilityCallError{Code: "CONNECTION_LOST", Message: "node connection was lost before a response was received", Retryable: retryable}
	case errors.Is(err, context.DeadlineExceeded):
		if capability == "agent.control" && action == "session.create" {
			return &CapabilityCallError{
				Code:      "DEADLINE_EXCEEDED",
				Message:   "session.create response deadline exceeded; retry the same request with the original idempotencyKey to reconcile the stored result",
				Retryable: true,
				Details: map[string]any{
					"mayHaveCreated": true,
					"recovery":       "retry_same_idempotency_key",
				},
			}
		}
		return &CapabilityCallError{Code: "DEADLINE_EXCEEDED", Message: "capability call deadline exceeded", Retryable: retryable}
	default:
		return err
	}
}

func isRetryableCapability(capability, action string) bool {
	switch capability + "/" + action {
	case "machine.status/report",
		"file.read/read",
		"file.write/preview",
		"code.search/search",
		"job.control/watch",
		"git.repository/status", "git.repository/diff", "git.repository/stagedDiff", "git.repository/log", "git.repository/show", "git.repository/branches", "git.repository/currentBranch", "git.repository/worktrees",
		"working.context/get", "working.context/plan.get", "working.context/plan.list", "working.context/markdown.list", "working.context/markdown.read", "working.context/progress.watch",
		"browser.automation/readiness", "browser.automation/pages.list", "browser.automation/snapshot", "browser.automation/events",
		"screenshot.capture/listDisplays", "screenshot.capture/desktop", "screenshot.capture/display", "screenshot.capture/listWindows", "screenshot.capture/window",
		"agent.control/routing.status", "agent.control/providers.list", "agent.control/provider.readiness", "agent.control/models.list", "agent.control/provider.capabilities", "agent.control/projects.list", "agent.control/skills.list", "agent.control/hooks.list", "agent.control/permissions.list", "agent.control/plugins.list", "agent.control/plugins.installed", "agent.control/plugins.get", "agent.control/plugin.skill.read", "agent.control/mcp.status.list", "agent.control/session.list", "agent.control/session.get", "agent.control/session.create", "agent.control/session.watch", "agent.control/session.callback.list", "agent.control/session.result", "agent.control/session.goal.get":
		return true
	default:
		return false
	}
}

func capabilityCallTimeout(capability, action string) time.Duration {
	switch capability + "/" + action {
	case "artifact.store/uploadFile", "artifact.store/uploadJobLog", "artifact.store/publishFile":
		return 10 * time.Minute
	case "git.repository/diff", "git.repository/stagedDiff", "git.repository/show":
		return 5 * time.Minute
	case "browser.automation/launch", "browser.automation/close", "browser.automation/page.open", "browser.automation/page.navigate", "browser.automation/click", "browser.automation/type", "browser.automation/press", "browser.automation/wait", "browser.automation/batch", "browser.automation/snapshot", "browser.automation/screenshot":
		return 2 * time.Minute
	case "screenshot.capture/desktop", "screenshot.capture/display", "screenshot.capture/window":
		return 2 * time.Minute
	case "agent.control/session.create":
		// ChatGPT Cloud owns a bounded 120s SSE stream after sentinel/prepare.
		// Keep those setup phases inside a separate, still-bounded operation budget.
		return 150 * time.Second
	case "agent.control/session.send", "agent.control/session.callback.register", "agent.control/session.callback.arm", "agent.control/session.callback.unregister", "agent.control/session.fork", "agent.control/session.compact", "agent.control/session.rollback", "agent.control/session.goal.set", "agent.control/session.goal.clear", "agent.control/session.settings.update", "agent.control/session.review", "agent.control/session.unarchive", "agent.control/session.delete":
		return 2 * time.Minute
	case "agent.control/session.watch", "agent.control/session.cancel", "agent.control/session.steer", "agent.control/session.respond":
		return 30 * time.Second
	case "job.control/watch":
		return 30 * time.Second
	default:
		return 20 * time.Second
	}
}

func capabilityResponseGrace(capability, action string) time.Duration {
	if capability == "agent.control" && action == "session.create" {
		// The Node may recover a provider-emitted session ID exactly when the
		// operation context expires, persist it, and still need to write the final
		// response. Its WebSocket write is bounded to 15 seconds; retain a small
		// delivery margin rather than racing the same boundary again.
		return 20 * time.Second
	}
	return 0
}

func capabilityCallDeadlines(now time.Time, parent context.Context, capability, action string) (time.Time, time.Time) {
	operationDeadline := now.Add(capabilityCallTimeout(capability, action))
	responseDeadline := operationDeadline.Add(capabilityResponseGrace(capability, action))
	if callerDeadline, ok := parent.Deadline(); ok {
		if callerDeadline.Before(operationDeadline) {
			operationDeadline = callerDeadline
		}
		if callerDeadline.Before(responseDeadline) {
			responseDeadline = callerDeadline
		}
	}
	return operationDeadline, responseDeadline
}

func shouldAuditCapability(capability, action string) bool {
	switch capability + "/" + action {
	case "file.write/edit", "file.write/create", "file.write/replace", "file.write/editMany", "shell.exec/run", "job.control/cancel", "git.repository/add", "git.repository/commit", "git.repository/fetch", "git.repository/pull", "git.repository/push", "git.repository/createWorktree", "git.repository/deleteWorktree", "build.exec/run", "working.context/set", "working.context/clear", "working.context/plan.init", "working.context/plan.sync", "working.context/task.update", "working.context/markdown.append", "browser.automation/launch", "browser.automation/close", "browser.automation/page.open", "browser.automation/page.navigate", "browser.automation/click", "browser.automation/type", "browser.automation/press", "browser.automation/batch", "browser.automation/screenshot", "screenshot.capture/desktop", "screenshot.capture/display", "screenshot.capture/window", "agent.control/session.create", "agent.control/session.send", "agent.control/session.steer", "agent.control/session.respond", "agent.control/session.callback.register", "agent.control/session.callback.arm", "agent.control/session.callback.unregister", "agent.control/session.callback.claim", "agent.control/session.callback.ack", "agent.control/session.cancel", "agent.control/session.rename", "agent.control/session.archive", "agent.control/session.unarchive", "agent.control/session.delete", "agent.control/session.fork", "agent.control/session.compact", "agent.control/session.rollback", "agent.control/session.goal.set", "agent.control/session.goal.clear", "agent.control/session.settings.update", "agent.control/session.review":
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
			if err := s.store.CleanupExpired(ctx, now.UTC()); err != nil {
				slog.Error("hub maintenance cleanup failed", "operation", "expired_metadata", "error", err)
			}
			if err := s.store.CleanupResults(ctx, now.UTC(), 128); err != nil {
				slog.Error("hub maintenance cleanup failed", "operation", "result_pool", "error", err)
			}
			if err := s.cleanupArtifacts(ctx, now.UTC()); err != nil {
				slog.Error("hub maintenance cleanup failed", "operation", "artifact_files", "error", err)
			}
		}
	}
}

func (s *Service) cleanupArtifacts(ctx context.Context, now time.Time) error {
	const cleanupBatch = 128
	s.artifactLocks.maintenance.Lock()
	deletions, err := s.store.CleanupArtifacts(ctx, now, cleanupBatch)
	s.artifactLocks.maintenance.Unlock()
	if err != nil {
		return err
	}
	var completed []store.ArtifactFileDeletion
	var failures []error
	for _, deletion := range deletions {
		done, deletionErr := s.cleanupArtifactDeletion(ctx, deletion, now)
		if deletionErr != nil {
			failures = append(failures, deletionErr)
		}
		if done {
			completed = append(completed, deletion)
		}
	}
	if err := s.store.CompleteArtifactFileDeletions(ctx, completed); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (s *Service) cleanupArtifactDeletion(ctx context.Context, deletion store.ArtifactFileDeletion, now time.Time) (bool, error) {
	var path, lockKey string
	switch deletion.Kind {
	case "upload":
		if !validArtifactUploadID(deletion.PathKey) {
			err := fmt.Errorf("invalid managed artifact upload path key %q", deletion.PathKey)
			return false, errors.Join(err, s.store.FailArtifactFileDeletion(ctx, deletion, err, now))
		}
		path = s.artifactUploadPath(deletion.PathKey)
		lockKey = "upload\x00" + deletion.PathKey
	case "blob":
		var err error
		path, err = s.artifactBlobPath(deletion.PathKey)
		if err != nil {
			return false, errors.Join(err, s.store.FailArtifactFileDeletion(ctx, deletion, err, now))
		}
		lockKey = "blob\x00" + deletion.PathKey
	default:
		err := fmt.Errorf("invalid artifact file deletion kind %q", deletion.Kind)
		return false, errors.Join(err, s.store.FailArtifactFileDeletion(ctx, deletion, err, now))
	}

	unlock := s.lockArtifactKey(lockKey)
	defer unlock()
	if deletion.Kind == "blob" {
		referenced, err := s.store.ArtifactStorageKeyReferenced(ctx, deletion.PathKey)
		if err != nil {
			return false, errors.Join(err, s.store.FailArtifactFileDeletion(ctx, deletion, err, now))
		}
		if referenced {
			// The key may have been reused since an earlier delete attempt.
			// Drop the stale queue entry without touching the live blob.
			return true, nil
		}
	}
	if err := s.removeArtifactPath(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		wrapped := fmt.Errorf("remove managed artifact %s %q: %w", deletion.Kind, deletion.PathKey, err)
		return false, errors.Join(wrapped, s.store.FailArtifactFileDeletion(ctx, deletion, err, now))
	}
	return true, nil
}

func (s *Service) removeArtifactPath(path string) error {
	if s.artifactRemove != nil {
		return s.artifactRemove(path)
	}
	return os.Remove(path)
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

func validateMachineRegistration(req MachineRegistrationRequest) error {
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
		case "MACHINE_OFFLINE", "CONNECTION_LOST":
			return 503
		case "DEADLINE_EXCEEDED", "TIMEOUT":
			return 504
		case "PERMISSION_DENIED":
			return 403
		case "NOT_FOUND", "COLLABORATION_NOT_FOUND", "TASK_NOT_FOUND":
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
