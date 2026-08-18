package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/isguang2024/fast-spider/internal/hub/store"
	"github.com/isguang2024/fast-spider/internal/security"
)

const (
	DefaultAdminUsername = "admin"
	DefaultAdminPassword = "AA@@123456"
	adminSessionTTL      = 12 * time.Hour
)

type AdminAccountView struct {
	ID        string
	Username  string
	CreatedAt time.Time
}

type AdminSessionResult struct {
	Token  string
	Record store.AdminSessionRecord
}

func (s *Service) EnsureAdminAccount(ctx context.Context) error {
	exists, err := s.store.HasAdminAccount(ctx, DefaultAdminUsername)
	if err != nil || exists {
		return err
	}
	hash, err := security.HashPassword(DefaultAdminPassword)
	if err != nil {
		return err
	}
	id, err := security.RandomOpaque("adm_")
	if err != nil {
		return err
	}
	return s.store.CreateAdminAccountIfAbsent(ctx, id, DefaultAdminUsername, hash, s.now().UTC())
}

func (s *Service) LoginAdmin(ctx context.Context, username, password, remoteAddr string) (AdminAccountView, error) {
	username = strings.TrimSpace(username)
	record, err := s.store.AdminAccountByUsername(ctx, username)
	if err != nil || !security.VerifyPassword(record.PasswordHash, password) {
		_ = s.audit(ctx, store.AuditEntry{ActorType: "admin", ActorID: username, Action: "admin.login", Result: "rejected", RemoteAddr: remoteAddr, CreatedAt: s.now().UTC()})
		return AdminAccountView{}, store.ErrUnauthorized
	}
	now := s.now().UTC()
	_ = s.audit(ctx, store.AuditEntry{ActorType: "admin", ActorID: record.ID, Action: "admin.login", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now})
	return AdminAccountView{ID: record.ID, Username: record.Username, CreatedAt: record.CreatedAt}, nil
}

func (s *Service) CreateAdminSession(ctx context.Context, adminID string) (AdminSessionResult, error) {
	sessionID, err := security.RandomOpaque("ads_")
	if err != nil {
		return AdminSessionResult{}, err
	}
	token, err := security.RandomOpaque("adst_")
	if err != nil {
		return AdminSessionResult{}, err
	}
	csrf, err := security.RandomOpaque("adcsrf_")
	if err != nil {
		return AdminSessionResult{}, err
	}
	now := s.now().UTC()
	record := store.AdminSessionRecord{ID: sessionID, AdminID: adminID, CSRFToken: csrf, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(adminSessionTTL)}
	if err := s.store.CreateAdminSession(ctx, record, security.HashToken(token)); err != nil {
		return AdminSessionResult{}, err
	}
	return AdminSessionResult{Token: token, Record: record}, nil
}

func (s *Service) AuthenticateAdminSession(ctx context.Context, token string) (store.AdminSessionRecord, error) {
	if strings.TrimSpace(token) == "" {
		return store.AdminSessionRecord{}, store.ErrUnauthorized
	}
	return s.store.AuthenticateAdminSession(ctx, security.HashToken(token), s.now().UTC())
}

func (s *Service) RevokeAdminSession(ctx context.Context, sessionID string) error {
	return s.store.RevokeAdminSession(ctx, sessionID, s.now().UTC())
}

func (s *Service) CreateUser(ctx context.Context, adminID, username, displayName, password, remoteAddr string) (OwnerAccountView, error) {
	username, err := normalizeUsername(username)
	if err != nil {
		return OwnerAccountView{}, err
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = username
	}
	if len([]byte(displayName)) > 128 {
		return OwnerAccountView{}, store.ErrConflict
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return OwnerAccountView{}, err
	}
	ownerID, err := security.RandomOpaque("usr_")
	if err != nil {
		return OwnerAccountView{}, err
	}
	now := s.now().UTC()
	if err := s.store.CreateOwnerAccount(ctx, ownerID, username, displayName, hash, now); err != nil {
		return OwnerAccountView{}, store.ErrConflict
	}
	_ = s.store.ClearBootstrapTokens(ctx)
	_ = os.Remove(filepath.Join(s.dataDir, "bootstrap-token"))
	_ = s.audit(ctx, store.AuditEntry{OwnerID: ownerID, ActorType: "admin", ActorID: adminID, Action: "admin.user.create", Result: "success", RemoteAddr: remoteAddr, CreatedAt: now})
	return OwnerAccountView{OwnerID: ownerID, Username: username, DisplayName: displayName, CreatedAt: now}, nil
}

func (s *Service) ListUsers(ctx context.Context) ([]OwnerAccountView, error) {
	records, err := s.store.ListOwnerAccounts(ctx)
	if err != nil {
		return nil, err
	}
	users := make([]OwnerAccountView, 0, len(records))
	for _, record := range records {
		users = append(users, OwnerAccountView{
			OwnerID:     record.OwnerID,
			Username:    record.Username,
			DisplayName: record.DisplayName,
			CreatedAt:   record.CreatedAt,
		})
	}
	return users, nil
}
