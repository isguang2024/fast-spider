CREATE TABLE direct_access_keys (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    token_hint TEXT NOT NULL,
    label TEXT NOT NULL,
    scopes_json TEXT NOT NULL,
    machine_id TEXT,
    rate_limit_per_minute INTEGER NOT NULL DEFAULT 120 CHECK(rate_limit_per_minute BETWEEN 1 AND 600),
    created_at INTEGER NOT NULL,
    last_used_at INTEGER,
    expires_at INTEGER NOT NULL,
    revoked_at INTEGER,
    deleted_at INTEGER
);

CREATE INDEX direct_access_keys_owner_status_idx
    ON direct_access_keys(owner_id, deleted_at, revoked_at, expires_at);
