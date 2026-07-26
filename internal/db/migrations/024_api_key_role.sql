-- +goose Up
-- Give API keys their own role instead of hardcoding every key to "member".
--
-- requireAuth stamped ctxRole = "member" for ANY valid API key, so a key handed
-- out for a read-only integration (dashboards, an MCP reader, a status page)
-- silently carried the full member write surface: DELETE /projects/{id}, which
-- erases a project and all its telemetry, plus create/delete of API keys
-- themselves. There was no way to issue a narrower key, and no way to tell from
-- the key what it could do.
--
-- Default 'member' so every existing key keeps exactly the authority it has
-- today: this migration must not silently downgrade a key a deploy pipeline is
-- using. New keys can be created as 'viewer'.
--
-- 'owner' and 'admin' are deliberately excluded by the CHECK: those roles gate
-- team management, SSO configuration and workspace erasure, which are
-- interactive, human-only actions. A leaked long-lived token must never be able
-- to reconfigure who can log in.
ALTER TABLE api_keys
    ADD COLUMN role TEXT NOT NULL DEFAULT 'member'
    CONSTRAINT api_keys_role_check CHECK (role IN ('viewer', 'member'));

-- +goose Down
ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_role_check;
ALTER TABLE api_keys DROP COLUMN IF EXISTS role;
