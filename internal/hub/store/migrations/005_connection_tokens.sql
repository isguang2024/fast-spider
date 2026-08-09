-- Remove the retired browser-based Node pairing and legacy token compatibility state.
DELETE FROM owner_api_tokens;

DELETE FROM oauth_access_tokens
WHERE authorization_id IS NULL OR authorization_id = '';

DELETE FROM oauth_refresh_tokens
WHERE authorization_id IS NULL OR authorization_id = '';

DELETE FROM oauth_clients
WHERE scope = 'fast-spider:device-connect';

DROP TABLE IF EXISTS enrollment_tokens;

ALTER TABLE owner_api_tokens RENAME TO connection_tokens;
