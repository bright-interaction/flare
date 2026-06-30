-- +goose Up
-- Per-org OIDC single sign-on config. The client_secret is admin-set and never
-- returned by the API. default_role is the role a just-in-time provisioned user
-- gets on first SSO login.
CREATE TABLE oidc_config (
    org_id        TEXT PRIMARY KEY REFERENCES orgs (id) ON DELETE CASCADE,
    issuer        TEXT NOT NULL,
    client_id     TEXT NOT NULL,
    client_secret TEXT NOT NULL,
    default_role  TEXT NOT NULL DEFAULT 'member',
    enabled       BOOLEAN NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS oidc_config;
