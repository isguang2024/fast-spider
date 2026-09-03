CREATE TABLE result_records (
    result_id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners(id),
    machine_id TEXT NOT NULL REFERENCES machines(id),
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('open','ready','aborted','failed')),
    manifest_json TEXT,
    revision INTEGER NOT NULL DEFAULT 1,
    error_code TEXT,
    error_message TEXT,
    created_at INTEGER NOT NULL,
    committed_at INTEGER,
    expires_at INTEGER NOT NULL,
    UNIQUE(owner_id, idempotency_key)
);

CREATE INDEX idx_result_records_owner_created ON result_records(owner_id, created_at DESC);
CREATE INDEX idx_result_records_expires ON result_records(expires_at);

CREATE TABLE result_pages (
    result_id TEXT NOT NULL REFERENCES result_records(result_id) ON DELETE CASCADE,
    page_no INTEGER NOT NULL,
    owner_id TEXT NOT NULL REFERENCES owners(id),
    artifact_id TEXT NOT NULL REFERENCES artifacts(id),
    created_at INTEGER NOT NULL,
    PRIMARY KEY(result_id, page_no)
);

CREATE INDEX idx_result_pages_owner ON result_pages(owner_id, result_id);
CREATE INDEX idx_result_pages_artifact ON result_pages(artifact_id);
