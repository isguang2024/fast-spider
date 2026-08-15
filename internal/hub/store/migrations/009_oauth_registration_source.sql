ALTER TABLE oauth_clients ADD COLUMN registration_source_hash TEXT;
CREATE INDEX oauth_clients_registration_source_idx ON oauth_clients(registration_source_hash, created_at);
