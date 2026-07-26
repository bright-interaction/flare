-- name: CreateUser :one
INSERT INTO users (id, org_id, email, password_hash, role)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- ciguard:allow-unscoped the SSO callback resolves the account from the IdP's issuer+subject, which IS the identity assertion; the org is then checked against the one that started the flow
-- name: GetUserBySSOIdentity :one
-- Primary SSO lookup. Resolving by email first was wrong: `sub` is the IdP's
-- stable identifier and the email is not, so an IdP-side email change made the
-- email lookup miss, JIT-provision a second account, and then collide on the
-- (sso_issuer, sso_subject) unique index, locking the user out for good.
SELECT * FROM users
WHERE sso_issuer = $1 AND sso_subject = $2 AND sso_subject <> '';

-- name: UpdateUserEmail :exec
-- Keeps the local email in step with the IdP after a rename.
UPDATE users SET email = $2 WHERE id = $1;

-- name: LinkUserSSOIdentity :execrows
-- Binds an account to the IdP identity that just authenticated it. Only ever
-- fills an EMPTY binding: an account already bound to an issuer+subject can
-- never be silently rebound to a different IdP identity (which would let anyone
-- who can reconfigure SSO take over an existing, higher-privileged account).
UPDATE users SET sso_issuer = $2, sso_subject = $3
WHERE id = $1 AND sso_subject = '';

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: BumpUserSessionsValidFrom :exec
-- Invalidates every session established before now (called on password reset).
UPDATE users SET sessions_valid_from = now() WHERE id = $1;

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2 WHERE id = $1;

-- name: CreatePasswordResetToken :exec
INSERT INTO password_reset_tokens (id, user_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4);

-- ciguard:allow-unscoped reset flow resolves the user from the secret token_hash; not an org-filtered read
-- name: GetPasswordResetToken :one
SELECT * FROM password_reset_tokens
WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now();

-- name: MarkPasswordResetTokenUsed :exec
UPDATE password_reset_tokens SET used_at = now() WHERE id = $1;

-- ciguard:allow-unscoped invalidates all of one user's outstanding reset tokens on a completed reset
-- name: InvalidatePasswordResetTokensForUser :exec
UPDATE password_reset_tokens SET used_at = now() WHERE user_id = $1 AND used_at IS NULL;

-- name: ListUsersByOrg :many
SELECT id, email, role, created_at FROM users WHERE org_id = $1 ORDER BY created_at ASC;

-- name: GetUserInOrg :one
SELECT * FROM users WHERE id = $1 AND org_id = $2;

-- name: UpdateUserRole :execrows
UPDATE users SET role = $3 WHERE id = $1 AND org_id = $2;

-- name: DeleteUser :execrows
DELETE FROM users WHERE id = $1 AND org_id = $2;

-- name: CountOwnersByOrg :one
SELECT count(*) FROM users WHERE org_id = $1 AND role = 'owner';
