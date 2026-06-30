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
