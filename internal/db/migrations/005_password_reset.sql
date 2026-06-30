-- +goose Up
-- Single-use, time-boxed password reset tokens. Only the SHA-256 hash of the
-- token is stored; the plaintext lives only in the emailed link. No org_id:
-- this is identity-plane data (like users), intentionally outside the ciguard
-- tenant-scoping set, and resolved by the secret token_hash.
CREATE TABLE password_reset_tokens (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_prt_user ON password_reset_tokens (user_id);

-- +goose Down
DROP TABLE IF EXISTS password_reset_tokens;
