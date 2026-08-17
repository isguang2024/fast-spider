package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/store"
	"github.com/isguang2024/fast-spider/internal/security"
)

const webSessionTTL = 30 * 24 * time.Hour

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)

type OwnerAccountView struct {
	OwnerID     string    `json:"ownerId"`
	Username    string    `json:"username"`
	DisplayName string    `json:"displayName"`
	CreatedAt   time.Time `json:"createdAt"`
}

type ConnectionTokenView struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
}

type ConnectionTokenResult struct {
	Token  string              `json:"token"`
	Record ConnectionTokenView `json:"record"`
}

const (
	DirectScopeFilesWrite    = "files.write"
	DirectScopeShell         = "shell"
	DirectScopeJobs          = "jobs"
	DirectScopeGit           = "git"
	DirectScopeBrowser       = "browser"
	DirectScopeAI            = "ai"
	DirectScopeContextWrite  = "context.write"
	DirectScopeArtifactWrite = "artifacts.write"
)

var directAccessScopes = map[string]struct{}{
	DirectScopeFilesWrite: {}, DirectScopeShell: {}, DirectScopeJobs: {}, DirectScopeGit: {},
	DirectScopeBrowser: {}, DirectScopeAI: {}, DirectScopeContextWrite: {}, DirectScopeArtifactWrite: {},
}

type DirectAccessKeyView struct {
	ID                 string     `json:"id"`
	TokenHint          string     `json:"tokenHint"`
	Label              string     `json:"label"`
	Scopes             []string   `json:"scopes"`
	MachineID          string     `json:"machineId,omitempty"`
	RateLimitPerMinute int        `json:"rateLimitPerMinute"`
	CreatedAt          time.Time  `json:"createdAt"`
	LastUsedAt         *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt          time.Time  `json:"expiresAt"`
	RevokedAt          *time.Time `json:"revokedAt,omitempty"`
}

type DirectAccessKeyResult struct {
	Token  string              `json:"token"`
	Record DirectAccessKeyView `json:"record"`
}

type WebSessionResult struct {
	Token     string
	CSRFToken string
	Record    store.WebSessionRecord
}

func (s *Service) HasOwner(ctx context.Context) (bool, error) {
	return s.store.HasOwner(ctx)
}

func (s *Service) HasOwnerAccount(ctx context.Context) (bool, error) {
	return s.store.HasOwnerAccount(ctx)
}

func (s *Service) BootstrapAccount(
	ctx context.Context,
	bootstrapToken, username, displayName, password, remoteAddr string,
) (OwnerAccountView, error) {
	username, err := normalizeUsername(username)
	if err != nil {
		return OwnerAccountView{}, err
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = username
	}
	if len([]byte(displayName)) > 128 || len(bootstrapToken) > 256 {
		return OwnerAccountView{}, store.ErrConflict
	}
	passwordHash, err := security.HashPassword(password)
	if err != nil {
		return OwnerAccountView{}, store.ErrConflict
	}
	proposedOwnerID, err := security.RandomOpaque("usr_")
	if err != nil {
		return OwnerAccountView{}, err
	}
	now := s.now().UTC()
	ownerID, err := s.store.BootstrapOwnerAccount(
		ctx,
		security.HashToken(bootstrapToken),
		proposedOwnerID,
		username,
		displayName,
		passwordHash,
		now,
	)
	if err != nil {
		return OwnerAccountView{}, err
	}
	_ = os.Remove(filepath.Join(s.dataDir, "bootstrap-token"))
	_ = s.audit(ctx, store.AuditEntry{
		OwnerID: ownerID, ActorType: "bootstrap", ActorID: ownerID,
		Action: "owner.account.setup", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now,
	})
	record, err := s.store.OwnerAccountByID(ctx, ownerID)
	if err != nil {
		return OwnerAccountView{}, err
	}
	return OwnerAccountView{
		OwnerID:     record.OwnerID,
		Username:    record.Username,
		DisplayName: record.DisplayName,
		CreatedAt:   record.CreatedAt,
	}, nil
}

func (s *Service) LoginAccount(ctx context.Context, username, password, remoteAddr string) (OwnerAccountView, error) {
	username, err := normalizeUsername(username)
	if err != nil {
		return OwnerAccountView{}, store.ErrUnauthorized
	}
	record, err := s.store.OwnerAccountByUsername(ctx, username)
	if err != nil || !security.VerifyPassword(record.PasswordHash, password) {
		_ = s.audit(ctx, store.AuditEntry{
			ActorType: "web", ActorID: username, Action: "owner.login", Result: "rejected",
			RemoteAddr: remoteAddr, CreatedAt: s.now().UTC(),
		})
		return OwnerAccountView{}, store.ErrUnauthorized
	}
	now := s.now().UTC()
	_ = s.audit(ctx, store.AuditEntry{
		OwnerID: record.OwnerID, ActorType: "web", ActorID: record.OwnerID,
		Action: "owner.login", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now,
	})
	return OwnerAccountView{
		OwnerID: record.OwnerID, Username: record.Username,
		DisplayName: record.DisplayName, CreatedAt: record.CreatedAt,
	}, nil
}

func (s *Service) ChangeOwnerPassword(
	ctx context.Context,
	ownerID, currentPassword, newPassword, keepSessionID, remoteAddr string,
) error {
	record, err := s.store.OwnerAccountByID(ctx, ownerID)
	if err != nil || !security.VerifyPassword(record.PasswordHash, currentPassword) {
		_ = s.audit(ctx, store.AuditEntry{
			OwnerID: ownerID, ActorType: "web", ActorID: ownerID,
			Action: "owner.password.change", Result: "rejected", RemoteAddr: remoteAddr, CreatedAt: s.now().UTC(),
		})
		return store.ErrUnauthorized
	}
	passwordHash, err := security.HashPassword(newPassword)
	if err != nil {
		return store.ErrConflict
	}
	now := s.now().UTC()
	if err := s.store.ChangeOwnerPassword(ctx, ownerID, passwordHash, keepSessionID, now); err != nil {
		return err
	}
	_ = s.audit(ctx, store.AuditEntry{
		OwnerID: ownerID, ActorType: "web", ActorID: ownerID,
		Action: "owner.password.change", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now,
	})
	return nil
}

func (s *Service) ListConnectionTokens(ctx context.Context, ownerID string) ([]ConnectionTokenView, error) {
	records, err := s.store.ListConnectionTokens(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	views := make([]ConnectionTokenView, 0, len(records))
	for _, record := range records {
		views = append(views, ConnectionTokenView{
			ID:         record.ID,
			Label:      record.Label,
			CreatedAt:  record.CreatedAt,
			LastUsedAt: record.LastUsedAt,
			ExpiresAt:  record.ExpiresAt,
			RevokedAt:  record.RevokedAt,
		})
	}
	return views, nil
}

func (s *Service) CreateConnectionToken(
	ctx context.Context,
	ownerID, label string,
	ttl time.Duration,
	remoteAddr string,
) (ConnectionTokenResult, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "Connection token"
	}
	if len([]byte(label)) > 80 || ttl < 0 || ttl > 5*365*24*time.Hour {
		return ConnectionTokenResult{}, store.ErrConflict
	}
	existing, err := s.store.ListConnectionTokens(ctx, ownerID)
	if err != nil {
		return ConnectionTokenResult{}, err
	}
	now := s.now().UTC()
	active := 0
	for _, record := range existing {
		if record.RevokedAt == nil && (record.ExpiresAt == nil || record.ExpiresAt.After(now)) {
			active++
		}
	}
	if active >= 64 {
		return ConnectionTokenResult{}, store.ErrConflict
	}
	id, err := security.RandomOpaque("tok_")
	if err != nil {
		return ConnectionTokenResult{}, err
	}
	token, err := security.RandomOpaque("ctk_")
	if err != nil {
		return ConnectionTokenResult{}, err
	}
	record := store.ConnectionTokenRecord{ID: id, OwnerID: ownerID, Label: label, CreatedAt: now}
	if ttl > 0 {
		expires := now.Add(ttl)
		record.ExpiresAt = &expires
	}
	if err := s.store.CreateConnectionToken(ctx, record, security.HashToken(token)); err != nil {
		return ConnectionTokenResult{}, err
	}
	_ = s.audit(ctx, store.AuditEntry{
		OwnerID: ownerID, ActorType: "web", ActorID: ownerID,
		Action: "connection.token.create", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now,
		Detail: map[string]any{"tokenId": id, "label": label},
	})
	return ConnectionTokenResult{Token: token, Record: ConnectionTokenView{
		ID:        record.ID,
		Label:     record.Label,
		CreatedAt: record.CreatedAt,
		ExpiresAt: record.ExpiresAt,
	}}, nil
}

func (s *Service) RevokeConnectionToken(ctx context.Context, ownerID, tokenID, remoteAddr string) error {
	now := s.now().UTC()
	if err := s.store.RevokeConnectionToken(ctx, ownerID, tokenID, now); err != nil {
		return err
	}
	_ = s.audit(ctx, store.AuditEntry{
		OwnerID: ownerID, ActorType: "web", ActorID: ownerID,
		Action: "connection.token.revoke", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now,
		Detail: map[string]any{"tokenId": tokenID},
	})
	return nil
}

func (s *Service) DeleteConnectionToken(ctx context.Context, ownerID, tokenID, remoteAddr string) error {
	now := s.now().UTC()
	if err := s.store.DeleteConnectionToken(ctx, ownerID, tokenID, now); err != nil {
		return err
	}
	_ = s.audit(ctx, store.AuditEntry{
		OwnerID: ownerID, ActorType: "web", ActorID: ownerID,
		Action: "connection.token.delete", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now,
		Detail: map[string]any{"tokenId": tokenID},
	})
	return nil
}

func (s *Service) ListDirectAccessKeys(ctx context.Context, ownerID string) ([]DirectAccessKeyView, error) {
	records, err := s.store.ListDirectAccessKeys(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	views := make([]DirectAccessKeyView, 0, len(records))
	for _, record := range records {
		views = append(views, directAccessKeyView(record))
	}
	return views, nil
}

func (s *Service) CreateDirectAccessKey(
	ctx context.Context,
	ownerID, label, machineID string,
	scopes []string,
	ttl time.Duration,
	rateLimitPerMinute int,
	remoteAddr string,
) (DirectAccessKeyResult, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "临时直连密钥"
	}
	machineID = strings.TrimSpace(machineID)
	if len([]byte(label)) > 80 || ttl <= 0 || ttl > 7*24*time.Hour {
		return DirectAccessKeyResult{}, store.ErrConflict
	}
	if rateLimitPerMinute == 0 {
		rateLimitPerMinute = 120
	}
	if rateLimitPerMinute < 1 || rateLimitPerMinute > 600 {
		return DirectAccessKeyResult{}, store.ErrConflict
	}
	normalizedScopes, err := normalizeDirectAccessScopes(scopes)
	if err != nil {
		return DirectAccessKeyResult{}, err
	}
	if len(normalizedScopes) > 0 && ttl > 24*time.Hour {
		return DirectAccessKeyResult{}, store.ErrConflict
	}
	if machineID != "" {
		machine, err := s.GetMachine(ctx, ownerID, machineID)
		if err != nil || machine.Status != "active" {
			return DirectAccessKeyResult{}, store.ErrConflict
		}
	}
	existing, err := s.store.ListDirectAccessKeys(ctx, ownerID)
	if err != nil {
		return DirectAccessKeyResult{}, err
	}
	now := s.now().UTC()
	active := 0
	for _, record := range existing {
		if record.RevokedAt == nil && record.ExpiresAt.After(now) {
			active++
		}
	}
	if active >= 64 {
		return DirectAccessKeyResult{}, store.ErrConflict
	}
	id, err := security.RandomOpaque("dak_")
	if err != nil {
		return DirectAccessKeyResult{}, err
	}
	token, err := security.RandomOpaque("fsp_tmp_")
	if err != nil {
		return DirectAccessKeyResult{}, err
	}
	record := store.DirectAccessKeyRecord{
		ID: id, OwnerID: ownerID, TokenHint: directAccessTokenHint(token), Label: label,
		Scopes: normalizedScopes, MachineID: machineID, RateLimitPerMinute: rateLimitPerMinute,
		CreatedAt: now, ExpiresAt: now.Add(ttl),
	}
	if err := s.store.CreateDirectAccessKey(ctx, record, security.HashToken(token)); err != nil {
		return DirectAccessKeyResult{}, err
	}
	_ = s.audit(ctx, store.AuditEntry{
		OwnerID: ownerID, MachineID: machineID, ActorType: "web", ActorID: ownerID,
		Action: "direct.key.create", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now,
		Detail: map[string]any{"keyId": id, "label": label, "scopes": normalizedScopes, "rateLimitPerMinute": rateLimitPerMinute},
	})
	return DirectAccessKeyResult{Token: token, Record: directAccessKeyView(record)}, nil
}

func (s *Service) AuthenticateDirectAccessKey(ctx context.Context, bearer string) (store.DirectAccessKeyRecord, error) {
	if !strings.HasPrefix(bearer, "fsp_tmp_") || len(bearer) > 256 {
		return store.DirectAccessKeyRecord{}, store.ErrUnauthorized
	}
	return s.store.AuthenticateDirectAccessKey(ctx, security.HashToken(bearer), s.now().UTC())
}

func (s *Service) RevokeDirectAccessKey(ctx context.Context, ownerID, keyID, remoteAddr string) error {
	now := s.now().UTC()
	if err := s.store.RevokeDirectAccessKey(ctx, ownerID, keyID, now); err != nil {
		return err
	}
	_ = s.audit(ctx, store.AuditEntry{
		OwnerID: ownerID, ActorType: "web", ActorID: ownerID,
		Action: "direct.key.revoke", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now,
		Detail: map[string]any{"keyId": keyID},
	})
	return nil
}

func (s *Service) DeleteDirectAccessKey(ctx context.Context, ownerID, keyID, remoteAddr string) error {
	now := s.now().UTC()
	if err := s.store.DeleteDirectAccessKey(ctx, ownerID, keyID, now); err != nil {
		return err
	}
	_ = s.audit(ctx, store.AuditEntry{
		OwnerID: ownerID, ActorType: "web", ActorID: ownerID,
		Action: "direct.key.delete", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now,
		Detail: map[string]any{"keyId": keyID},
	})
	return nil
}

func DirectAccessKeyHasScope(record store.DirectAccessKeyRecord, scope string) bool {
	for _, current := range record.Scopes {
		if current == scope {
			return true
		}
	}
	return false
}

func directAccessKeyView(record store.DirectAccessKeyRecord) DirectAccessKeyView {
	return DirectAccessKeyView{
		ID: record.ID, TokenHint: record.TokenHint, Label: record.Label, Scopes: append([]string(nil), record.Scopes...),
		MachineID: record.MachineID, RateLimitPerMinute: record.RateLimitPerMinute, CreatedAt: record.CreatedAt,
		LastUsedAt: record.LastUsedAt, ExpiresAt: record.ExpiresAt, RevokedAt: record.RevokedAt,
	}
}

func normalizeDirectAccessScopes(scopes []string) ([]string, error) {
	seen := make(map[string]struct{}, len(scopes))
	result := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, ok := directAccessScopes[scope]; !ok {
			return nil, store.ErrConflict
		}
		if _, duplicate := seen[scope]; duplicate {
			continue
		}
		seen[scope] = struct{}{}
		result = append(result, scope)
	}
	sort.Strings(result)
	return result, nil
}

func directAccessTokenHint(token string) string {
	if len(token) <= 18 {
		return token
	}
	return token[:14] + "…" + token[len(token)-4:]
}

func (s *Service) CreateWebSession(ctx context.Context, ownerID string) (WebSessionResult, error) {
	sessionID, err := security.RandomOpaque("wbs_")
	if err != nil {
		return WebSessionResult{}, err
	}
	token, err := security.RandomOpaque("ses_")
	if err != nil {
		return WebSessionResult{}, err
	}
	csrf, err := security.RandomOpaque("csrf_")
	if err != nil {
		return WebSessionResult{}, err
	}
	now := s.now().UTC()
	record := store.WebSessionRecord{
		ID: sessionID, OwnerID: ownerID, CSRFToken: csrf,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(webSessionTTL),
	}
	if err := s.store.CreateWebSession(ctx, record, security.HashToken(token)); err != nil {
		return WebSessionResult{}, err
	}
	account, err := s.store.OwnerAccountByID(ctx, ownerID)
	if err != nil {
		return WebSessionResult{}, err
	}
	record.Username = account.Username
	record.DisplayName = account.DisplayName
	return WebSessionResult{Token: token, CSRFToken: csrf, Record: record}, nil
}

func (s *Service) AuthenticateWebSession(ctx context.Context, token string) (store.WebSessionRecord, error) {
	if !strings.HasPrefix(token, "ses_") || len(token) > 256 {
		return store.WebSessionRecord{}, store.ErrUnauthorized
	}
	now := s.now().UTC()
	record, err := s.store.AuthenticateWebSession(ctx, security.HashToken(token), now)
	if err != nil {
		return store.WebSessionRecord{}, err
	}
	if now.Sub(record.LastSeenAt) >= 5*time.Minute {
		_ = s.store.TouchWebSession(ctx, record.ID, now)
		record.LastSeenAt = now
	}
	return record, nil
}

func (s *Service) RevokeWebSession(ctx context.Context, sessionID, ownerID string) error {
	return s.store.RevokeWebSession(ctx, sessionID, ownerID, s.now().UTC())
}

func normalizeUsername(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !usernamePattern.MatchString(value) {
		return "", errors.New("username must be 3-64 lowercase letters, numbers, dot, underscore or dash")
	}
	return value, nil
}
