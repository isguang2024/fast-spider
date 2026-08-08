CREATE TABLE oauth_clients (
    client_id TEXT PRIMARY KEY,
    client_name TEXT NOT NULL,
    redirect_uris_json TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE oauth_access_tokens (
    token_hash TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners(id) ON DELETE CASCADE,
    client_id TEXT NOT NULL REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
    scopes_json TEXT NOT NULL,
    resource TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX oauth_access_tokens_owner_idx ON oauth_access_tokens(owner_id, expires_at);

CREATE TABLE oauth_refresh_tokens (
    token_hash TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners(id) ON DELETE CASCADE,
    client_id TEXT NOT NULL REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
    scopes_json TEXT NOT NULL,
    resource TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX oauth_refresh_tokens_owner_idx ON oauth_refresh_tokens(owner_id, expires_at);
