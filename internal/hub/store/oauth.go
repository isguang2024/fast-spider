package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type OAuthClientRecord struct {
	ClientID     string
	ClientName   string
	RedirectURIs []string
	CreatedAt    time.Time
}

type OAuthTokenRecord struct {
	OwnerID   string
	ClientID  string
	Scopes    []string
	Resource  string
	ExpiresAt time.Time
}

func (s *Store) RegisterOAuthClient(ctx context.Context, rec OAuthClientRecord) error {
	raw, err := json.Marshal(rec.RedirectURIs)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		"INSERT INTO oauth_clients(client_id, client_name, redirect_uris_json, created_at) VALUES(?,?,?,?)",
		rec.ClientID, rec.ClientName, string(raw), rec.CreatedAt.Unix())
	return err
}

func (s *Store) GetOAuthClient(ctx context.Context, clientID string) (OAuthClientRecord, error) {
	var rec OAuthClientRecord
	var raw string
	var created int64
	err := s.db.QueryRowContext(ctx,
		"SELECT client_id, client_name, redirect_uris_json, created_at FROM oauth_clients WHERE client_id = ?",
		clientID).Scan(&rec.ClientID, &rec.ClientName, &raw, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthClientRecord{}, ErrNotFound
	}
	if err != nil {
		return OAuthClientRecord{}, err
	}
	if err := json.Unmarshal([]byte(raw), &rec.RedirectURIs); err != nil {
		return OAuthClientRecord{}, err
	}
	rec.CreatedAt = time.Unix(created, 0).UTC()
	return rec, nil
}

func (s *Store) SaveOAuthTokenPair(
	ctx context.Context,
	accessHash, refreshHash string,
	rec OAuthTokenRecord,
	accessExpires, refreshExpires time.Time,
	consumedRefreshHash string,
	now time.Time,
) error {
	scopesJSON, err := json.Marshal(rec.Scopes)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if consumedRefreshHash != "" {
		result, err := tx.ExecContext(ctx,
			"DELETE FROM oauth_refresh_tokens WHERE token_hash = ? AND client_id = ? AND owner_id = ? AND expires_at > ?",
			consumedRefreshHash, rec.ClientID, rec.OwnerID, now.Unix())
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			if err != nil {
				return err
			}
			return ErrUnauthorized
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_access_tokens(
		token_hash, owner_id, client_id, scopes_json, resource, created_at, expires_at
	) VALUES(?,?,?,?,?,?,?)`, accessHash, rec.OwnerID, rec.ClientID, string(scopesJSON), rec.Resource, now.Unix(), accessExpires.Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_refresh_tokens(
		token_hash, owner_id, client_id, scopes_json, resource, created_at, expires_at
	) VALUES(?,?,?,?,?,?,?)`, refreshHash, rec.OwnerID, rec.ClientID, string(scopesJSON), rec.Resource, now.Unix(), refreshExpires.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AuthenticateOAuthAccessToken(ctx context.Context, tokenHash string, now time.Time) (OAuthTokenRecord, error) {
	return s.oauthTokenRecord(ctx, "oauth_access_tokens", tokenHash, now)
}

func (s *Store) GetOAuthRefreshToken(ctx context.Context, tokenHash string, now time.Time) (OAuthTokenRecord, error) {
	return s.oauthTokenRecord(ctx, "oauth_refresh_tokens", tokenHash, now)
}

func (s *Store) oauthTokenRecord(ctx context.Context, table, tokenHash string, now time.Time) (OAuthTokenRecord, error) {
	query := "SELECT owner_id, client_id, scopes_json, resource, expires_at FROM " + table + " WHERE token_hash = ?"
	var rec OAuthTokenRecord
	var scopesJSON string
	var expires int64
	err := s.db.QueryRowContext(ctx, query, tokenHash).Scan(&rec.OwnerID, &rec.ClientID, &scopesJSON, &rec.Resource, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthTokenRecord{}, ErrUnauthorized
	}
	if err != nil {
		return OAuthTokenRecord{}, err
	}
	if expires <= now.Unix() {
		return OAuthTokenRecord{}, ErrExpired
	}
	if err := json.Unmarshal([]byte(scopesJSON), &rec.Scopes); err != nil {
		return OAuthTokenRecord{}, err
	}
	rec.ExpiresAt = time.Unix(expires, 0).UTC()
	return rec, nil
}

func (s *Store) RevokeOAuthToken(ctx context.Context, tokenHash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM oauth_access_tokens WHERE token_hash = ?", tokenHash); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM oauth_refresh_tokens WHERE token_hash = ?", tokenHash); err != nil {
		return err
	}
	return tx.Commit()
}
