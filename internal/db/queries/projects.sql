-- name: CreateProject :one
INSERT INTO projects (id, org_id, name, slug, platform, public_key, dsn_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListProjectsByOrg :many
SELECT * FROM projects
WHERE org_id = $1
ORDER BY created_at DESC;

-- ciguard:allow-unscoped ingest path keyed by the unguessable public_key (the DSN secret)
-- name: GetProjectByPublicKey :one
SELECT * FROM projects WHERE public_key = $1;

-- name: GetProjectByID :one
SELECT * FROM projects
WHERE id = $1
  AND (sqlc.arg(org_scope)::text = '' OR org_id = sqlc.arg(org_scope));

-- name: GetProjectBySlug :one
SELECT * FROM projects WHERE org_id = $1 AND slug = $2;

-- ciguard:allow-unscoped /go/{dsnID} redirect resolves numeric dsn_id; target page requires a session
-- name: GetProjectByDsnID :one
SELECT * FROM projects WHERE dsn_id = $1;
