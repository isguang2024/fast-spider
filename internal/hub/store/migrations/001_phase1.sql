CREATE TABLE owners (
    id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE owner_api_tokens (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER,
    revoked_at INTEGER
);

CREATE TABLE bootstrap_tokens (
    id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    consumed_at INTEGER
);

CREATE TABLE enrollment_tokens (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    max_attempts INTEGER NOT NULL CHECK(max_attempts > 0),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK(attempt_count >= 0),
    consumed_at INTEGER,
    expected_name TEXT,
    expected_os TEXT,
    idempotency_key TEXT,
    result_machine_id TEXT
);

CREATE TABLE machines (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners(id) ON DELETE CASCADE,
    display_name TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('active','revoked','disabled')),
    os TEXT NOT NULL,
    arch TEXT NOT NULL,
    node_version TEXT NOT NULL,
    capability_digest TEXT,
    last_seen_at INTEGER,
    last_connection_generation INTEGER NOT NULL DEFAULT 0 CHECK(last_connection_generation >= 0),
    revoked_at INTEGER,
    revision INTEGER NOT NULL DEFAULT 1 CHECK(revision > 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE device_credentials (
    id TEXT PRIMARY KEY,
    machine_id TEXT NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    public_key TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('active','overlap','revoked','expired')),
    issued_at INTEGER NOT NULL,
    expires_at INTEGER,
    revoked_at INTEGER,
    UNIQUE(machine_id, fingerprint)
);

CREATE TABLE device_access_tokens (
    id TEXT PRIMARY KEY,
    credential_id TEXT NOT NULL REFERENCES device_credentials(id) ON DELETE CASCADE,
    machine_id TEXT NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    issued_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    revoked_at INTEGER
);

CREATE TABLE device_nonces (
    machine_id TEXT NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    nonce_hash TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY(machine_id, nonce_hash)
);

CREATE TABLE machine_capabilities (
    machine_id TEXT NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    capability_id TEXT NOT NULL,
    version TEXT NOT NULL,
    actions_json TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(machine_id, capability_id)
);

CREATE TABLE audit_entries (
    id TEXT PRIMARY KEY,
    owner_id TEXT,
    machine_id TEXT,
    actor_type TEXT NOT NULL,
    actor_id TEXT,
    action TEXT NOT NULL,
    result TEXT NOT NULL,
    remote_addr TEXT,
    detail_json TEXT,
    created_at INTEGER NOT NULL
);

CREATE INDEX idx_enrollment_expiry ON enrollment_tokens(expires_at, consumed_at);
CREATE INDEX idx_machine_owner_status ON machines(owner_id, status);
CREATE INDEX idx_device_token_machine_expiry ON device_access_tokens(machine_id, expires_at, revoked_at);
CREATE INDEX idx_audit_created ON audit_entries(created_at);
