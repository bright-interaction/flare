-- +goose Up
-- One GitHub integration per org: a token + default repo (owner/name) used to
-- open a GitHub issue from a Flare issue on demand. github_url records the
-- created issue's URL on the Flare issue so the link is shown thereafter.
CREATE TABLE github_config (
    org_id     TEXT PRIMARY KEY REFERENCES orgs (id) ON DELETE CASCADE,
    repo       TEXT NOT NULL,
    token      TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE issues ADD COLUMN github_url TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE issues DROP COLUMN IF EXISTS github_url;
DROP TABLE IF EXISTS github_config;
