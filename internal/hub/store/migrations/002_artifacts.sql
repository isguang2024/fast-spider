CREATE TABLE artifacts (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners(id),
    machine_id TEXT NOT NULL REFERENCES machines(id),
    workspace_id TEXT,
    job_id TEXT,
    logical_name TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    storage_key TEXT,
    status TEXT NOT NULL CHECK(status IN ('uploading','complete','aborted')),
    created_at INTEGER NOT NULL,
    completed_at INTEGER,
    expires_at INTEGER NOT NULL
);

CREATE INDEX idx_artifacts_owner_created ON artifacts(owner_id, created_at DESC);
CREATE INDEX idx_artifacts_expires ON artifacts(expires_at);

CREATE TABLE artifact_uploads (
    id TEXT PRIMARY KEY,
    artifact_id TEXT NOT NULL UNIQUE REFERENCES artifacts(id) ON DELETE CASCADE,
    machine_id TEXT NOT NULL REFERENCES machines(id),
    expected_size INTEGER NOT NULL,
    expected_sha256 TEXT NOT NULL,
    received_size INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL CHECK(status IN ('active','complete','aborted')),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

CREATE INDEX idx_artifact_uploads_expires ON artifact_uploads(expires_at);
