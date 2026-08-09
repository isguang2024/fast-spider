package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type OAuthClientRecord struct {
	ClientID      string
	ClientName    string
	RedirectURIs  []string
	GrantTypes    []string
	ResponseTypes []string
	Scope         string
	CreatedAt     time.Time
}

type OAuthAuthorizationRecord struct {
	AuthorizationID string
	OwnerID         string
	ClientID        string
	ClientName      string
	Scopes          []string
	Resource        string
	CreatedAt       time.Time
	LastUsedAt      *time.Time
	ExpiresAt       time.Time
	RevokedAt       *time.Time
}

type OAuthTokenRecord struct {
	AuthorizationID string
	OwnerID         string
	ClientID        string
	Scopes          []string
	Resource        string
	ExpiresAt       time.Time
}

func (s *Store) RegisterOAuthClient(ctx context.Context, rec OAuthClientRecord) error {
	redirectsJSON, err := json.Marshal(rec.RedirectURIs)
	if err != nil {
		return err
	}
	grantsJSON, err := json.Marshal(rec.GrantTypes)
	if err != nil {
		return err
	}
	responsesJSON, err := json.Marshal(rec.ResponseTypes)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO oauth_clients(
		client_id, client_name, redirect_uris_json, grant_types_json, response_types_json, scope, created_at
	) VALUES(?,?,?,?,?,?,?)`,
		rec.ClientID, rec.ClientName, string(redirectsJSON), string(grantsJSON), string(responsesJSON), rec.Scope, rec.CreatedAt.Unix())
	return err
}

func (s *Store) ListOAuthClients(ctx context.Context) ([]OAuthClientRecord, error) {
	return s.queryOAuthClients(ctx, `SELECT
		client_id, client_name, redirect_uris_json, grant_types_json, response_types_json, scope, created_at
	FROM oauth_clients
	ORDER BY created_at DESC, client_id`)
}

func (s *Store) ListOAuthClientsForOwner(ctx context.Context, ownerID string) ([]OAuthClientRecord, error) {
	return s.queryOAuthClients(ctx, `SELECT
		c.client_id, c.client_name, c.redirect_uris_json, c.grant_types_json, c.response_types_json, c.scope, c.created_at
	FROM oauth_clients c
	WHERE EXISTS (
		SELECT 1 FROM oauth_authorizations owner_auth
		WHERE owner_auth.client_id = c.client_id AND owner_auth.owner_id = ?
	)
	ORDER BY c.created_at DESC, c.client_id`, ownerID)
}

func (s *Store) queryOAuthClients(ctx context.Context, query string, args ...any) ([]OAuthClientRecord, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []OAuthClientRecord
	for rows.Next() {
		var rec OAuthClientRecord
		var redirectsJSON, grantsJSON, responsesJSON string
		var created int64
		if err := rows.Scan(
			&rec.ClientID,
			&rec.ClientName,
			&redirectsJSON,
			&grantsJSON,
			&responsesJSON,
			&rec.Scope,
			&created,
		); err != nil {
			return nil, err
		}
		if err := decodeOAuthClientJSON(&rec, redirectsJSON, grantsJSON, responsesJSON); err != nil {
			return nil, err
		}
		rec.CreatedAt = time.Unix(created, 0).UTC()
		records = append(records, rec)
	}
	return records, rows.Err()
}

func (s *Store) DeleteOAuthClient(ctx context.Context, clientID string) error {
	return s.deleteOAuthClient(ctx, "DELETE FROM oauth_clients WHERE client_id = ?", clientID)
}

func (s *Store) DeleteOAuthClientForOwner(ctx context.Context, ownerID, clientID string) error {
	return s.deleteOAuthClient(ctx, `DELETE FROM oauth_clients
		WHERE client_id = ?
		AND EXISTS (
			SELECT 1 FROM oauth_authorizations owner_auth
			WHERE owner_auth.client_id = oauth_clients.client_id AND owner_auth.owner_id = ?
		)`, clientID, ownerID)
}

func (s *Store) deleteOAuthClient(ctx context.Context, query string, args ...any) error {
	result, err := s.db.ExecContext(ctx, query, args...)
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

func (s *Store) GetOAuthClient(ctx context.Context, clientID string) (OAuthClientRecord, error) {
	var rec OAuthClientRecord
	var redirectsJSON, grantsJSON, responsesJSON string
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT
		client_id, client_name, redirect_uris_json, grant_types_json, response_types_json, scope, created_at
	FROM oauth_clients WHERE client_id = ?`, clientID).Scan(
		&rec.ClientID,
		&rec.ClientName,
		&redirectsJSON,
		&grantsJSON,
		&responsesJSON,
		&rec.Scope,
		&created,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthClientRecord{}, ErrNotFound
	}
	if err != nil {
		return OAuthClientRecord{}, err
	}
	if err := decodeOAuthClientJSON(&rec, redirectsJSON, grantsJSON, responsesJSON); err != nil {
		return OAuthClientRecord{}, err
	}
	rec.CreatedAt = time.Unix(created, 0).UTC()
	return rec, nil
}

func decodeOAuthClientJSON(rec *OAuthClientRecord, redirectsJSON, grantsJSON, responsesJSON string) error {
	if err := json.Unmarshal([]byte(redirectsJSON), &rec.RedirectURIs); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(grantsJSON), &rec.GrantTypes); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(responsesJSON), &rec.ResponseTypes); err != nil {
		return err
	}
	return nil
}

func (s *Store) CreateOAuthAuthorization(ctx context.Context, rec OAuthAuthorizationRecord) error {
	scopesJSON, err := json.Marshal(rec.Scopes)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO oauth_authorizations(
		id, owner_id, client_id, scopes_json, resource, created_at, expires_at
	) VALUES(?,?,?,?,?,?,?)`,
		rec.AuthorizationID,
		rec.OwnerID,
		rec.ClientID,
		string(scopesJSON),
		rec.Resource,
		rec.CreatedAt.Unix(),
		rec.ExpiresAt.Unix(),
	)
	return err
}

func (s *Store) ListOAuthAuthorizations(ctx context.Context, ownerID string) ([]OAuthAuthorizationRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		a.id, a.owner_id, a.client_id, c.client_name, a.scopes_json, a.resource,
		a.created_at, a.last_used_at, a.expires_at, a.revoked_at
	FROM oauth_authorizations a
	JOIN oauth_clients c ON c.client_id = a.client_id
	WHERE a.owner_id = ?
	ORDER BY a.created_at DESC, a.id`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []OAuthAuthorizationRecord
	for rows.Next() {
		var rec OAuthAuthorizationRecord
		var scopesJSON string
		var created, expires int64
		var lastUsed, revoked sql.NullInt64
		if err := rows.Scan(
			&rec.AuthorizationID,
			&rec.OwnerID,
			&rec.ClientID,
			&rec.ClientName,
			&scopesJSON,
			&rec.Resource,
			&created,
			&lastUsed,
			&expires,
			&revoked,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(scopesJSON), &rec.Scopes); err != nil {
			return nil, err
		}
		rec.CreatedAt = time.Unix(created, 0).UTC()
		rec.ExpiresAt = time.Unix(expires, 0).UTC()
		if lastUsed.Valid {
			value := time.Unix(lastUsed.Int64, 0).UTC()
			rec.LastUsedAt = &value
		}
		if revoked.Valid {
			value := time.Unix(revoked.Int64, 0).UTC()
			rec.RevokedAt = &value
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

func (s *Store) TouchOAuthAuthorization(ctx context.Context, authorizationID string, now time.Time) error {
	if authorizationID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE oauth_authorizations
		SET last_used_at = ?
		WHERE id = ? AND revoked_at IS NULL
		  AND (last_used_at IS NULL OR last_used_at <= ?)`,
		now.Unix(), authorizationID, now.Add(-5*time.Minute).Unix(),
	)
	return err
}

func (s *Store) RevokeOAuthAuthorization(ctx context.Context, ownerID, authorizationID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx,
		"UPDATE oauth_authorizations SET revoked_at = ? WHERE id = ? AND owner_id = ? AND revoked_at IS NULL",
		now.Unix(), authorizationID, ownerID,
	)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM oauth_access_tokens WHERE authorization_id = ?", authorizationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM oauth_refresh_tokens WHERE authorization_id = ?", authorizationID); err != nil {
		return err
	}
	return tx.Commit()
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
	if rec.AuthorizationID == "" {
		return ErrUnauthorized
	}
	authorizationExpires := accessExpires
	if refreshHash != "" {
		authorizationExpires = refreshExpires
	}
	result, err := tx.ExecContext(ctx, `UPDATE oauth_authorizations
		SET last_used_at = ?, expires_at = ?
		WHERE id = ? AND owner_id = ? AND client_id = ? AND revoked_at IS NULL`,
		now.Unix(), authorizationExpires.Unix(), rec.AuthorizationID, rec.OwnerID, rec.ClientID,
	)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return err
		}
		return ErrUnauthorized
	}
	if consumedRefreshHash != "" {
		result, err := tx.ExecContext(ctx, `DELETE FROM oauth_refresh_tokens
			WHERE token_hash = ? AND client_id = ? AND owner_id = ? AND expires_at > ?
			  AND authorization_id = ?`,
			consumedRefreshHash, rec.ClientID, rec.OwnerID, now.Unix(), rec.AuthorizationID)
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
		token_hash, owner_id, client_id, scopes_json, resource, created_at, expires_at, authorization_id
	) VALUES(?,?,?,?,?,?,?,?)`, accessHash, rec.OwnerID, rec.ClientID, string(scopesJSON), rec.Resource, now.Unix(), accessExpires.Unix(), rec.AuthorizationID); err != nil {
		return err
	}
	if refreshHash != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_refresh_tokens(
			token_hash, owner_id, client_id, scopes_json, resource, created_at, expires_at, authorization_id
		) VALUES(?,?,?,?,?,?,?,?)`, refreshHash, rec.OwnerID, rec.ClientID, string(scopesJSON), rec.Resource, now.Unix(), refreshExpires.Unix(), rec.AuthorizationID); err != nil {
			return err
		}
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
	query := "SELECT authorization_id, owner_id, client_id, scopes_json, resource, expires_at FROM " + table + " WHERE token_hash = ? AND authorization_id IS NOT NULL AND authorization_id <> ''"
	var rec OAuthTokenRecord
	var scopesJSON string
	var expires int64
	err := s.db.QueryRowContext(ctx, query, tokenHash).Scan(&rec.AuthorizationID, &rec.OwnerID, &rec.ClientID, &scopesJSON, &rec.Resource, &expires)
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

func (s *Store) RevokeOAuthToken(ctx context.Context, tokenHash string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var authorizationID sql.NullString
	var ownerID string
	err = tx.QueryRowContext(ctx, `SELECT authorization_id, owner_id FROM oauth_access_tokens WHERE token_hash = ?
		UNION ALL
		SELECT authorization_id, owner_id FROM oauth_refresh_tokens WHERE token_hash = ?
		LIMIT 1`, tokenHash, tokenHash).Scan(&authorizationID, &ownerID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && authorizationID.Valid && authorizationID.String != "" {
		if _, err := tx.ExecContext(ctx,
			"UPDATE oauth_authorizations SET revoked_at = ? WHERE id = ? AND owner_id = ? AND revoked_at IS NULL",
			now.Unix(), authorizationID.String, ownerID,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM oauth_access_tokens WHERE authorization_id = ?", authorizationID.String); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM oauth_refresh_tokens WHERE authorization_id = ?", authorizationID.String); err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM oauth_access_tokens WHERE token_hash = ?", tokenHash); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM oauth_refresh_tokens WHERE token_hash = ?", tokenHash); err != nil {
		return err
	}
	return tx.Commit()
}
