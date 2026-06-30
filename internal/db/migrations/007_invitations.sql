-- +goose Up
-- Pending team invitations. Only the SHA-256 hash of the invite token is
-- stored; the plaintext lives only in the emailed/copied accept link. One
-- pending invite per (org, email) - re-inviting replaces the token.
CREATE TABLE invitations (
    id          TEXT PRIMARY KEY,
    org_id      TEXT NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    email       TEXT NOT NULL,
    role        TEXT NOT NULL DEFAULT 'member',
    token_hash  TEXT NOT NULL UNIQUE,
    invited_by  TEXT REFERENCES users (id) ON DELETE SET NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    accepted_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, email)
);
CREATE INDEX idx_invitations_org ON invitations (org_id);

-- +goose Down
DROP TABLE IF EXISTS invitations;
