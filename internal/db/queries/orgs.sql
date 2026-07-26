-- name: CreateOrg :one
INSERT INTO orgs (id, name, slug)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetOrgBySlug :one
SELECT * FROM orgs WHERE slug = $1;

-- name: GetOrgByID :one
SELECT * FROM orgs WHERE id = $1;

-- name: CountOrgs :one
SELECT count(*) FROM orgs;

-- name: DeleteOrg :exec
-- Right-to-erasure: cascades to users, projects (and their telemetry),
-- channels, keys, invites, github_config and audit_log via ON DELETE CASCADE.
DELETE FROM orgs WHERE id = $1;

-- ciguard:allow-unscoped the scs session table is keyed by opaque token with no org column; the caller matches on the encoded user ids it is erasing
-- name: DeleteSessionsForUsers :exec
-- The sessions table is the one store holding tenant data that no foreign key
-- reaches: scs writes an opaque token plus a gob blob, so an org delete used to
-- leave every one of its members' live session rows behind. Matching on the
-- encoded user id inside the payload is the only handle available.
DELETE FROM sessions
WHERE EXISTS (
    SELECT 1 FROM unnest(@user_ids::text[]) AS u(id)
    WHERE position(u.id in encode(data, 'escape')) > 0
);

-- name: GetFirstOrg :one
-- The oldest org, used as the system/owner scope for Flare's own security
-- events (e.g. an ingest-auth rejection that matches no project has no org).
SELECT * FROM orgs ORDER BY created_at ASC LIMIT 1;
