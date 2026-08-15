CREATE TABLE artifact_file_deletions (
    kind TEXT NOT NULL CHECK(kind IN ('upload','blob')),
    path_key TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    next_attempt_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(kind, path_key)
);

CREATE INDEX idx_artifact_file_deletions_due
    ON artifact_file_deletions(next_attempt_at, kind, path_key);

CREATE INDEX idx_artifacts_storage_key
    ON artifacts(storage_key) WHERE storage_key IS NOT NULL;

CREATE INDEX idx_artifact_uploads_cleanup
    ON artifact_uploads(status, expires_at, id);
