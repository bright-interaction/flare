-- +goose Up
-- Sessions whose auth_epoch predates this instant are rejected. Bumped on a
-- password reset so an attacker's already-active session is revoked when the
-- legitimate owner recovers the account (the reset-token invalidation alone
-- does not kill a session established before the reset).
ALTER TABLE users ADD COLUMN sessions_valid_from timestamptz NOT NULL DEFAULT now();

-- +goose Down
ALTER TABLE users DROP COLUMN sessions_valid_from;
