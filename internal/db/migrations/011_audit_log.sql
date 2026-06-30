-- +goose Up
-- Append-only audit trail of sensitive actions (who did what, when).
-- actor_user_id is nullable (NULL for API-key actions) and has NO FK to users:
-- the trail must survive the actor being removed.
CREATE TABLE audit_log (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    actor_user_id TEXT,
    action        TEXT NOT NULL,
    target        TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_org ON audit_log (org_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS audit_log;
