package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var (
	ErrAdminPasswordRequired = errors.New("admin password initialization required")
	ErrAdminRotationRequired = errors.New("admin password rotation required")
)

type AdminAccountRecord struct {
	ID              string
	Username        string
	PasswordHash    string
	PasswordVersion int
	CreatedAt       time.Time
}

type AdminSessionRecord struct {
	ID         string
	AdminID    string
	Username   string
	CSRFToken  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

func (s *Store) HasAdminAccount(ctx context.Context, username string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_accounts WHERE username = ?", username).Scan(&count)
	return count > 0, err
}

func (s *Store) CreateAdminAccountIfAbsent(ctx context.Context, id, username, passwordHash string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO admin_accounts(id, username, password_hash, password_version, created_at, updated_at)
			VALUES(?,?,?,1,?,?) ON CONFLICT(username) DO NOTHING`, id, username, passwordHash, now.Unix(), now.Unix())
	return err
}

func (s *Store) AdminAccountByUsername(ctx context.Context, username string) (AdminAccountRecord, error) {
	var rec AdminAccountRecord
	var created int64
	err := s.db.QueryRowContext(ctx,
		"SELECT id, username, password_hash, password_version, created_at FROM admin_accounts WHERE username = ?",
		username,
	).Scan(&rec.ID, &rec.Username, &rec.PasswordHash, &rec.PasswordVersion, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminAccountRecord{}, ErrNotFound
	}
	if err != nil {
		return AdminAccountRecord{}, err
	}
	rec.CreatedAt = time.Unix(created, 0).UTC()
	return rec, nil
}

// RotateAdminPassword updates an account that predates explicit password
// initialization and revokes every session issued before the rotation.
func (s *Store) RotateAdminPassword(ctx context.Context, adminID, passwordHash string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE admin_accounts
		SET password_hash = ?, password_version = 1, updated_at = ?
		WHERE id = ? AND password_version = 0`, passwordHash, now.Unix(), adminID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE admin_sessions SET revoked_at = ?
		WHERE admin_id = ? AND revoked_at IS NULL`, now.Unix(), adminID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateAdminSession(ctx context.Context, rec AdminSessionRecord, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO admin_sessions(id, admin_id, token_hash, csrf_token, created_at, last_seen_at, expires_at)
		VALUES(?,?,?,?,?,?,?)`, rec.ID, rec.AdminID, tokenHash, rec.CSRFToken, rec.CreatedAt.Unix(), rec.LastSeenAt.Unix(), rec.ExpiresAt.Unix())
	return err
}

func (s *Store) AuthenticateAdminSession(ctx context.Context, tokenHash string, now time.Time) (AdminSessionRecord, error) {
	var rec AdminSessionRecord
	var created, lastSeen, expires int64
	var revoked sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT s.id, s.admin_id, a.username, s.csrf_token, s.created_at, s.last_seen_at, s.expires_at, s.revoked_at
		FROM admin_sessions s JOIN admin_accounts a ON a.id = s.admin_id
		WHERE s.token_hash = ?`, tokenHash).Scan(&rec.ID, &rec.AdminID, &rec.Username, &rec.CSRFToken, &created, &lastSeen, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminSessionRecord{}, ErrUnauthorized
	}
	if err != nil {
		return AdminSessionRecord{}, err
	}
	if revoked.Valid {
		return AdminSessionRecord{}, ErrRevoked
	}
	if expires <= now.Unix() {
		return AdminSessionRecord{}, ErrExpired
	}
	if now.Unix()-lastSeen >= 60 {
		_, _ = s.db.ExecContext(ctx, "UPDATE admin_sessions SET last_seen_at = ? WHERE id = ? AND revoked_at IS NULL", now.Unix(), rec.ID)
		lastSeen = now.Unix()
	}
	rec.CreatedAt = time.Unix(created, 0).UTC()
	rec.LastSeenAt = time.Unix(lastSeen, 0).UTC()
	rec.ExpiresAt = time.Unix(expires, 0).UTC()
	return rec, nil
}

func (s *Store) RevokeAdminSession(ctx context.Context, sessionID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, "UPDATE admin_sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", now.Unix(), sessionID)
	return err
}

func (s *Store) CreateOwnerAccount(ctx context.Context, ownerID, username, displayName, passwordHash string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO owners(id, display_name, username, password_hash, created_at) VALUES(?,?,?,?,?)",
		ownerID, displayName, username, passwordHash, now.Unix())
	return err
}

func (s *Store) ListOwnerAccounts(ctx context.Context) ([]OwnerAccountRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(username, ''), display_name, created_at
		FROM owners ORDER BY created_at DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []OwnerAccountRecord
	for rows.Next() {
		var account OwnerAccountRecord
		var created int64
		if err := rows.Scan(&account.OwnerID, &account.Username, &account.DisplayName, &created); err != nil {
			return nil, err
		}
		account.CreatedAt = time.Unix(created, 0).UTC()
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return accounts, nil
}

func (s *Store) ClearBootstrapTokens(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM bootstrap_tokens")
	return err
}
