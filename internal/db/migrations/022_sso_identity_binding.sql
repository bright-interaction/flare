-- +goose Up
-- Bind a user to the IdP identity that signed them in.
--
-- Matching an SSO login to an account by EMAIL ALONE means whoever controls the
-- org's IdP config can assert any email and be signed in as that user, including
-- the org owner. Pairing SSO config with owner-only access closes the
-- admin -> owner path; this binding closes the rest: once an account has signed
-- in through a given issuer+subject, only that same issuer+subject can ever
-- match it again, so re-pointing SSO at a different IdP cannot take over
-- existing accounts.
--
-- Empty string = not yet linked. The first SSO sign-in links the account.
ALTER TABLE users ADD COLUMN sso_issuer  TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN sso_subject TEXT NOT NULL DEFAULT '';

-- A given IdP subject maps to at most one account per issuer.
CREATE UNIQUE INDEX idx_users_sso_identity
    ON users (sso_issuer, sso_subject)
    WHERE sso_subject <> '';

-- +goose Down
DROP INDEX IF EXISTS idx_users_sso_identity;
ALTER TABLE users DROP COLUMN IF EXISTS sso_subject;
ALTER TABLE users DROP COLUMN IF EXISTS sso_issuer;
