package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type OwnerAccountRecord struct {
	OwnerID      string
	Username     string
	DisplayName  string
	PasswordHash string
	CreatedAt    time.Time
}

func (s *Store) HasOwnerAccount(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM owners WHERE username IS NOT NULL AND username <> '' AND password_hash IS NOT NULL AND password_hash <> ''",
	).Scan(&count); err != nil {
		return false, fmt.Errorf("count owner accounts: %w", err)
	}
	return count > 0, nil
}

type WebSessionRecord struct {
	ID          string
	OwnerID     string
	Username    string
	DisplayName string
	CSRFToken   string
	CreatedAt   time.Time
	LastSeenAt  time.Time
	ExpiresAt   time.Time
}

type ConnectionTokenRecord struct {
	ID         string
	OwnerID    string
	Label      string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
}

func (s *Store) BootstrapOwnerAccount(
	ctx context.Context,
	bootstrapHash, proposedOwnerID, username, displayName, passwordHash string,
	now time.Time,
) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var tokenID string
	var expires int64
	var consumed sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		"SELECT id, expires_at, consumed_at FROM bootstrap_tokens WHERE token_hash = ?",
		bootstrapHash,
	).Scan(&tokenID, &expires, &consumed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrUnauthorized
		}
		return "", err
	}
	if consumed.Valid {
		return "", ErrConsumed
	}
	if expires <= now.Unix() {
		return "", ErrExpired
	}

	var ownerCount int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM owners").Scan(&ownerCount); err != nil {
		return "", err
	}
	if ownerCount != 0 {
		return "", ErrConflict
	}
	ownerID := proposedOwnerID
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO owners(id, display_name, username, password_hash, created_at) VALUES(?,?,?,?,?)",
		ownerID, displayName, username, passwordHash, now.Unix(),
	); err != nil {
		return "", err
	}

	result, err := tx.ExecContext(ctx,
		"UPDATE bootstrap_tokens SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL",
		now.Unix(), tokenID,
	)
	if err != nil {
		return "", err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return "", err
		}
		return "", ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return ownerID, nil
}

func (s *Store) OwnerAccountByUsername(ctx context.Context, username string) (OwnerAccountRecord, error) {
	return s.ownerAccount(ctx,
		"SELECT id, username, display_name, password_hash, created_at FROM owners WHERE username = ?",
		username,
	)
}

func (s *Store) OwnerAccountByID(ctx context.Context, ownerID string) (OwnerAccountRecord, error) {
	return s.ownerAccount(ctx,
		"SELECT id, username, display_name, password_hash, created_at FROM owners WHERE id = ?",
		ownerID,
	)
}

func (s *Store) ownerAccount(ctx context.Context, query, value string) (OwnerAccountRecord, error) {
	var rec OwnerAccountRecord
	var username, passwordHash sql.NullString
	var created int64
	err := s.db.QueryRowContext(ctx, query, value).Scan(
		&rec.OwnerID,
		&username,
		&rec.DisplayName,
		&passwordHash,
		&created,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return OwnerAccountRecord{}, ErrNotFound
	}
	if err != nil {
		return OwnerAccountRecord{}, err
	}
	if !username.Valid || !passwordHash.Valid || username.String == "" || passwordHash.String == "" {
		return OwnerAccountRecord{}, ErrUnauthorized
	}
	rec.Username = username.String
	rec.PasswordHash = passwordHash.String
	rec.CreatedAt = time.Unix(created, 0).UTC()
	return rec, nil
}

func (s *Store) ChangeOwnerPassword(ctx context.Context, ownerID, passwordHash, keepSessionID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE owners SET password_hash = ? WHERE id = ?", passwordHash, ownerID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE web_sessions
		SET revoked_at = ?
		WHERE owner_id = ? AND id <> ? AND revoked_at IS NULL`, now.Unix(), ownerID, keepSessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateConnectionToken(ctx context.Context, rec ConnectionTokenRecord, tokenHash string) error {
	var expires any
	if rec.ExpiresAt != nil {
		expires = rec.ExpiresAt.Unix()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO connection_tokens(
		id, owner_id, token_hash, label, created_at, expires_at
	) VALUES(?,?,?,?,?,?)`,
		rec.ID,
		rec.OwnerID,
		tokenHash,
		nullString(rec.Label),
		rec.CreatedAt.Unix(),
		expires,
	)
	return err
}

func (s *Store) ListConnectionTokens(ctx context.Context, ownerID string) ([]ConnectionTokenRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, owner_id, label, created_at, last_used_at, expires_at, revoked_at
	FROM connection_tokens
	WHERE owner_id = ? AND deleted_at IS NULL
	ORDER BY created_at DESC, id`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []ConnectionTokenRecord
	for rows.Next() {
		var rec ConnectionTokenRecord
		var label sql.NullString
		var created int64
		var lastUsed, expires, revoked sql.NullInt64
		if err := rows.Scan(&rec.ID, &rec.OwnerID, &label, &created, &lastUsed, &expires, &revoked); err != nil {
			return nil, err
		}
		rec.Label = label.String
		rec.CreatedAt = time.Unix(created, 0).UTC()
		if lastUsed.Valid {
			value := time.Unix(lastUsed.Int64, 0).UTC()
			rec.LastUsedAt = &value
		}
		if expires.Valid {
			value := time.Unix(expires.Int64, 0).UTC()
			rec.ExpiresAt = &value
		}
		if revoked.Valid {
			value := time.Unix(revoked.Int64, 0).UTC()
			rec.RevokedAt = &value
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

func (s *Store) RevokeConnectionToken(ctx context.Context, ownerID, tokenID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx,
		"UPDATE connection_tokens SET revoked_at = ? WHERE id = ? AND owner_id = ? AND revoked_at IS NULL AND deleted_at IS NULL",
		now.Unix(), tokenID, ownerID,
	)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteConnectionToken(ctx context.Context, ownerID, tokenID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE connection_tokens SET deleted_at = ?
		WHERE id = ? AND owner_id = ? AND deleted_at IS NULL
		AND (revoked_at IS NOT NULL OR (expires_at IS NOT NULL AND expires_at <= ?))`,
		now.Unix(), tokenID, ownerID, now.Unix(),
	)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return ErrConflict
	}
	return nil
}

func (s *Store) CreateWebSession(ctx context.Context, rec WebSessionRecord, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO web_sessions(
		id, owner_id, token_hash, csrf_token, created_at, last_seen_at, expires_at
	) VALUES(?,?,?,?,?,?,?)`,
		rec.ID,
		rec.OwnerID,
		tokenHash,
		rec.CSRFToken,
		rec.CreatedAt.Unix(),
		rec.LastSeenAt.Unix(),
		rec.ExpiresAt.Unix(),
	)
	return err
}

func (s *Store) AuthenticateWebSession(ctx context.Context, tokenHash string, now time.Time) (WebSessionRecord, error) {
	var rec WebSessionRecord
	var created, lastSeen, expires int64
	var revoked sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT
		ws.id, ws.owner_id, o.username, o.display_name, ws.csrf_token,
		ws.created_at, ws.last_seen_at, ws.expires_at, ws.revoked_at
	FROM web_sessions ws
	JOIN owners o ON o.id = ws.owner_id
	WHERE ws.token_hash = ?`, tokenHash).Scan(
		&rec.ID,
		&rec.OwnerID,
		&rec.Username,
		&rec.DisplayName,
		&rec.CSRFToken,
		&created,
		&lastSeen,
		&expires,
		&revoked,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return WebSessionRecord{}, ErrUnauthorized
	}
	if err != nil {
		return WebSessionRecord{}, err
	}
	if revoked.Valid {
		return WebSessionRecord{}, ErrRevoked
	}
	if expires <= now.Unix() {
		return WebSessionRecord{}, ErrExpired
	}
	rec.CreatedAt = time.Unix(created, 0).UTC()
	rec.LastSeenAt = time.Unix(lastSeen, 0).UTC()
	rec.ExpiresAt = time.Unix(expires, 0).UTC()
	return rec, nil
}

func (s *Store) TouchWebSession(ctx context.Context, sessionID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE web_sessions SET last_seen_at = ? WHERE id = ? AND revoked_at IS NULL AND expires_at > ?",
		now.Unix(), sessionID, now.Unix(),
	)
	return err
}

func (s *Store) RevokeWebSession(ctx context.Context, sessionID, ownerID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx,
		"UPDATE web_sessions SET revoked_at = ? WHERE id = ? AND owner_id = ? AND revoked_at IS NULL",
		now.Unix(), sessionID, ownerID,
	)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteExpiredWebSessions(ctx context.Context, now time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		"DELETE FROM web_sessions WHERE expires_at <= ? OR revoked_at IS NOT NULL",
		now.Unix(),
	); err != nil {
		return fmt.Errorf("delete expired web sessions: %w", err)
	}
	return nil
}
