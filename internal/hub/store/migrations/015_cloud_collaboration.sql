CREATE TABLE cloud_collaborations (
    collaboration_id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners(id),
    machine_id TEXT NOT NULL REFERENCES machines(id),
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('active','paused','needs_attention','closing','completed','canceled')),
    state_json TEXT NOT NULL,
    revision INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE(owner_id, idempotency_key)
);

CREATE INDEX idx_cloud_collaborations_owner_updated
    ON cloud_collaborations(owner_id, updated_at DESC, collaboration_id);
