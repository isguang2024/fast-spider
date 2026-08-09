ALTER TABLE owners ADD COLUMN username TEXT;
ALTER TABLE owners ADD COLUMN password_hash TEXT;
CREATE UNIQUE INDEX owners_username_idx ON owners(username) WHERE username IS NOT NULL;

ALTER TABLE oauth_clients ADD COLUMN grant_types_json TEXT NOT NULL DEFAULT '["authorization_code","refresh_token"]';
ALTER TABLE oauth_clients ADD COLUMN response_types_json TEXT NOT NULL DEFAULT '["code"]';
ALTER TABLE oauth_clients ADD COLUMN scope TEXT NOT NULL DEFAULT 'fast-spider';

CREATE TABLE web_sessions (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    csrf_token TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    revoked_at INTEGER
);
CREATE INDEX web_sessions_owner_expiry_idx ON web_sessions(owner_id, expires_at, revoked_at);

CREATE TABLE oauth_authorizations (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners(id) ON DELETE CASCADE,
    client_id TEXT NOT NULL REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
    scopes_json TEXT NOT NULL,
    resource TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    last_used_at INTEGER,
    expires_at INTEGER NOT NULL,
    revoked_at INTEGER
);
CREATE INDEX oauth_authorizations_owner_idx ON oauth_authorizations(owner_id, revoked_at, expires_at);

ALTER TABLE oauth_access_tokens ADD COLUMN authorization_id TEXT;
ALTER TABLE oauth_refresh_tokens ADD COLUMN authorization_id TEXT;
CREATE INDEX oauth_access_tokens_authorization_idx ON oauth_access_tokens(authorization_id);
CREATE INDEX oauth_refresh_tokens_authorization_idx ON oauth_refresh_tokens(authorization_id);

ALTER TABLE owner_api_tokens ADD COLUMN label TEXT;
ALTER TABLE owner_api_tokens ADD COLUMN last_used_at INTEGER;
