ALTER TABLE machines ADD COLUMN deleted_at INTEGER;
CREATE INDEX machines_owner_deleted_idx ON machines(owner_id, deleted_at, status);

ALTER TABLE connection_tokens ADD COLUMN deleted_at INTEGER;
CREATE INDEX connection_tokens_owner_deleted_idx ON connection_tokens(owner_id, deleted_at, revoked_at, expires_at);

ALTER TABLE oauth_authorizations ADD COLUMN deleted_at INTEGER;
CREATE INDEX oauth_authorizations_owner_deleted_idx ON oauth_authorizations(owner_id, deleted_at, revoked_at, expires_at);
